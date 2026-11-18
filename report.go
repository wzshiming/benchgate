package benchgate

import (
	"cmp"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/wzshiming/benchgate/sample"
	"github.com/wzshiming/benchgate/verdict"
	"golang.org/x/perf/benchfmt"
)

// reasons say why a package was not measured.
var reasons = map[sample.State]string{
	sample.Identical:   "identical test binaries",
	sample.BaseMissing: "base has no such package",
	sample.BaseFailed:  "base failed to build or run",
}

// outcomes are what can become of a benchmark, in the order the count sentence lists them.
var outcomes = []string{"regressed", "improved", "unchanged", "new", "removed", "not judged"}

// metricWords are what the tables call each judged metric and the words for it going up and down.
var metricWords = map[string]struct{ name, up, down string }{
	"sec/op":    {"time", "slower", "faster"},
	"B/op":      {"memory", "more", "less"},
	"allocs/op": {"allocations", "more", "fewer"},
}

// render is the Markdown report of res, a run of the gate under cfg as o asked for it.
func render(o Options, cfg Config, res result) []byte {
	var b strings.Builder
	confirmed, excluded := res.split()
	counts := make(map[string]map[string]int) // benchmarks by group, then outcome
	total := make(map[string]int)             // benchmarks by outcome
	for _, g := range cfg.Groups {
		counts[g.Name] = tally(g.Name, res.first.verdicts[g.Name].Rows, confirmed)
		for fate, n := range counts[g.Name] {
			total[fate] += n
		}
	}
	headline(&b, total, res.first)
	suspects(&b, cfg, "Regressions", "Over the limit in both measurements.", confirmed, plainChange)
	suspects(&b, cfg, "Not confirmed", "Over the limit in the first measurement only; these do not fail the gate.", excluded,
		func(r verdict.Row) string { return cmp.Or(plain(r, false), "not measured") })
	improvements(&b, cfg, res)
	groups(&b, cfg, res.first, counts)
	if all := comparison(o, cfg, res, confirmed); all != "" {
		fold(&b, "All results", all)
	}
	fold(&b, "How this was measured", howMeasured(o, cfg, res))
	return []byte(b.String())
}

// howMeasured is the Markdown of the How this was measured block.
func howMeasured(o Options, cfg Config, res result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Trees\n\n- Base: %s\n- Head: %s\n\n", o.BaseLabel, o.HeadLabel)
	env := res.first.Env
	fmt.Fprintf(&b, "### Environment\n\n- CPU: %s\n- Cores: %d\n- OS/arch: %s\n- Go: %s\n\n", env.CPU, env.Cores, env.OSArch, env.GoVersion)
	policy(&b, cfg)
	notMeasured(&b, res)
	loadPerRound(&b, res)
	if o.Reproduce != "" {
		fmt.Fprintf(&b, "### Reproduce\n\n%s", o.Reproduce)
	}
	return b.String()
}

// fold writes md as a block folded under summary, set off by the blank lines GitHub needs to render it.
func fold(b *strings.Builder, summary, md string) {
	fmt.Fprintf(b, "<details>\n<summary>%s</summary>\n\n%s\n\n</details>\n\n", summary, strings.TrimRight(md, "\n"))
}

// headline writes the report's conclusion line and count sentence from the benchmarks counted by outcome and the first pass.
func headline(b *strings.Builder, counts map[string]int, first pass) {
	benchmarks := func(n int) string {
		if n == 1 {
			return "1 benchmark"
		}
		return strconv.Itoa(n) + " benchmarks"
	}
	total := 0
	var items []string
	for _, fate := range outcomes {
		if n := counts[fate]; n > 0 {
			total += n
			items = append(items, fmt.Sprintf("%d %s", n, fate))
		}
	}
	switch {
	case counts["regressed"] > 0:
		fmt.Fprintf(b, "# Benchmark gate: FAIL, %s regressed\n\n", benchmarks(counts["regressed"]))
	case total > 0:
		b.WriteString("# Benchmark gate: PASS, no benchmark regressed\n\n")
	default:
		b.WriteString("# Benchmark gate: PASS, nothing to compare\n\n")
	}
	identical := !slices.ContainsFunc(first.Groups, func(g sample.GroupResult) bool {
		return slices.ContainsFunc(g.Packages, func(s sample.Status) bool { return s.State != sample.Identical })
	})
	switch {
	case total > 0:
		fmt.Fprintf(b, "%s: %s.\n\n", benchmarks(total), strings.Join(items, ", "))
	case identical:
		b.WriteString("No benchmark was compared: base and head build identical test binaries.\n\n")
	default:
		b.WriteString("No benchmark was compared: the groups below say why.\n\n")
	}
}

// tally counts group g's benchmarks in rows by outcome, given the suspects the re-check confirmed.
func tally(g string, rows []verdict.Row, confirmed []suspect) map[string]int {
	byBenchmark := make(map[verdict.Key][]verdict.Row) // keys without a metric
	for _, r := range rows {
		k := verdict.Key{Pkg: r.Pkg, Name: r.Name}
		byBenchmark[k] = append(byBenchmark[k], r)
	}
	counts := make(map[string]int)
	for k, benchRows := range byBenchmark {
		every := func(classes ...verdict.Class) bool {
			return !slices.ContainsFunc(benchRows, func(r verdict.Row) bool { return !slices.Contains(classes, r.Class) })
		}
		fate := "unchanged"
		switch {
		case slices.ContainsFunc(confirmed, func(s suspect) bool { return s.group == g && s.first.Pkg == k.Pkg && s.first.Name == k.Name }):
			fate = "regressed"
		case slices.ContainsFunc(benchRows, func(r verdict.Row) bool { return r.Class == verdict.Improvement }):
			fate = "improved"
		case every(verdict.New):
			fate = "new"
		case every(verdict.Removed):
			fate = "removed"
		case every(verdict.ReportOnly, verdict.TooFewSamples):
			fate = "not judged"
		}
		counts[fate]++
	}
	return counts
}

func policy(b *strings.Builder, cfg Config) {
	identical := "not measured"
	if cfg.MeasureIdentical {
		identical = "measured too"
	}
	fmt.Fprintf(b, "### Policy\n\nSignificance level %v; a row is judged with at least %d samples on each side; identical test binaries are %s.\n\n",
		cfg.Alpha, cfg.MinSamples, identical)
	var rows [][]string
	for _, g := range cfg.Groups {
		thresholds := "report only"
		if !g.ReportOnly {
			var list []string
			for _, m := range judgedMetrics {
				if t, ok := g.Thresholds[m]; ok {
					list = append(list, m+" "+percent(t))
				}
			}
			thresholds = strings.Join(list, ", ")
		}
		rows = append(rows, []string{g.Name, strings.Join(g.Packages, ", "), "`" + g.Bench + "`", cmp.Or(g.Benchtime, "default"), strconv.Itoa(g.Rounds), thresholds})
	}
	table(b, []string{"Group", "Packages", "Benchmarks", "Benchtime", "Rounds", "Thresholds"}, rows)
}

// suspects writes the part heading, intro and a row of each of list, the largest change first; recheck says what the re-check found.
func suspects(b *strings.Builder, cfg Config, heading, intro string, list []suspect, recheck func(verdict.Row) string) {
	var rows [][]string
	for _, s := range slices.SortedStableFunc(slices.Values(list), func(x, y suspect) int { return largestFirst(x.first, y.first) }) {
		rows = append(rows, append(changeCells(s.first), recheck(s.recheck), percent(cfg.group(s.group).Thresholds[s.first.Metric])))
	}
	part(b, heading, intro, []string{"Benchmark", "Metric", "Base", "Head", "Change", "Re-check", "Limit"}, rows)
}

// improvements writes the part of the rows that improved in the first pass, the largest change first.
func improvements(b *strings.Builder, cfg Config, res result) {
	var better []verdict.Row
	for _, g := range cfg.Groups {
		for _, r := range res.first.verdicts[g.Name].Rows {
			if r.Class == verdict.Improvement {
				better = append(better, r)
			}
		}
	}
	var rows [][]string
	for _, r := range slices.SortedStableFunc(slices.Values(better), largestFirst) {
		rows = append(rows, changeCells(r))
	}
	part(b, "Improvements", "Better by more than the limit; measured once.", []string{"Benchmark", "Metric", "Base", "Head", "Change"}, rows)
}

// groups writes the Groups part from the first pass and each group's benchmarks counted by outcome, judged groups first.
func groups(b *strings.Builder, cfg Config, first pass, counts map[string]map[string]int) {
	header := []string{"Group", "Benchmarks"}
	for _, m := range judgedMetrics {
		header = append(header, metricWords[m].name)
	}
	var rows [][]string
	for _, g := range judgedFirst(cfg.Groups) {
		n := 0
		for _, c := range counts[g.Name] {
			n += c
		}
		row := []string{g.Name, strconv.Itoa(n)}
		for _, m := range judgedMetrics {
			overall := ""
			if d, ok := first.verdicts[g.Name].Overall(m); ok {
				overall = change(d)
			}
			row = append(row, overall)
		}
		rows = append(rows, append(row, groupResult(g, first, counts[g.Name]["regressed"])))
	}
	part(b, "Groups", "The time, memory and allocations columns are each group's overall change, the geometric mean over its benchmarks.",
		append(header, "Result"), rows)
}

// groupResult is group g's Result cell, from its packages in the first pass and how many of its benchmarks regressed.
func groupResult(g Group, first pass, regressed int) string {
	var packages []sample.Status
	for _, gr := range first.Groups {
		if gr.Name == g.Name {
			packages = gr.Packages
		}
	}
	var why []string
	unmeasured := 0
	for _, s := range packages {
		if s.State != sample.Measured {
			unmeasured++
			if !slices.Contains(why, reasons[s.State]) {
				why = append(why, reasons[s.State])
			}
		}
	}
	if unmeasured == len(packages) {
		return "not measured: " + strings.Join(why, ", ")
	}
	status := "no regression"
	switch {
	case g.ReportOnly:
		status = "not judged"
	case regressed > 0:
		status = fmt.Sprintf("%d regressed", regressed)
	}
	if unmeasured > 0 {
		status += fmt.Sprintf("; %d of %d packages not measured", unmeasured, len(packages))
	}
	return status
}

// part writes a level-2 heading, intro and a table of header and rows, or nothing without rows.
func part(b *strings.Builder, heading, intro string, header []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n%s\n\n", heading, intro)
	table(b, header, rows)
}

// largestFirst orders rows that changed the same way by the size of their change, the largest first.
func largestFirst(a, b verdict.Row) int {
	return cmp.Compare(math.Abs(b.Delta), math.Abs(a.Delta))
}

// changeCells are a row's Benchmark, Metric, Base, Head and Change cells.
func changeCells(r verdict.Row) []string {
	name := strings.TrimPrefix(r.Name, "Benchmark")
	if _, parts := benchfmt.Name(name).Parts(); len(parts) > 0 && parts[len(parts)-1][0] == '-' {
		name = strings.TrimSuffix(name, string(parts[len(parts)-1])) // a last part starting with - is GOMAXPROCS
	}
	return []string{path.Base(r.Pkg) + " · " + name, metricWords[r.Metric].name,
		value(r.Base.Median, r.Metric), value(r.Head.Median, r.Metric), plainChange(r)}
}

// plainChange is a row's change in plain words, such as 12.3% slower or 4.0× faster.
func plainChange(r verdict.Row) string {
	base, head := r.Base.Median, r.Head.Median
	switch {
	case base == 0:
		return "up from 0"
	case head == 0:
		return "down to 0"
	}
	word, factor := metricWords[r.Metric].up, head/base
	if head < base {
		word, factor = metricWords[r.Metric].down, base/head
	}
	switch {
	case factor < 2:
		return fmt.Sprintf("%.1f%% %s", math.Abs(head/base-1)*100, word)
	case factor < 100:
		return fmt.Sprintf("%.1f× %s", factor, word)
	}
	return fmt.Sprintf("%.0f× %s", factor, word)
}

// comparison is every row of the first pass by group, judged groups first, given the suspects the re-check confirmed.
func comparison(o Options, cfg Config, res result, confirmed []suspect) string {
	var b strings.Builder
	for _, g := range judgedFirst(cfg.Groups) {
		if len(res.first.verdicts[g.Name].Rows) == 0 {
			continue
		}
		kind := "judged"
		if g.ReportOnly {
			kind = "report only"
		}
		fmt.Fprintf(&b, "### %s (%s)\n\n", g.Name, kind)
		if base, head := res.first.files(g.Name); base != "" {
			fmt.Fprintf(&b, "Samples: `%s`, `%s`", o.inOut(base), o.inOut(head))
			if base, head := res.recheck.files(g.Name); base != "" {
				fmt.Fprintf(&b, "; re-check: `%s`, `%s`", o.inOut(base), o.inOut(head))
			}
			b.WriteString(".\n\n")
		}
		var rows [][]string
		for _, r := range res.first.verdicts[g.Name].Rows {
			held := slices.ContainsFunc(confirmed, func(s suspect) bool { return s.group == g.Name && s.first.Key == r.Key })
			rows = append(rows, cells(r, held))
		}
		table(&b, []string{"Package", "Benchmark", "Metric", "Base", "Head", "Delta", "p", "n", "Result"}, rows)
	}
	return b.String()
}

// judgedFirst are groups with the judged ones first, each kind in its order.
func judgedFirst(groups []Group) []Group {
	var judged, reportOnly []Group
	for _, g := range groups {
		if g.ReportOnly {
			reportOnly = append(reportOnly, g)
		} else {
			judged = append(judged, g)
		}
	}
	return append(judged, reportOnly...)
}

// cells are a compared row as the comparison shows it.
func cells(r verdict.Row, confirmed bool) []string {
	delta, p := "", ""
	if r.Class != verdict.New && r.Class != verdict.Removed {
		delta, p = change(r.Delta), fmt.Sprintf("%.3f", r.P)
	}
	return []string{r.Pkg, r.Name, r.Metric, side(r.Base, r.Metric), side(r.Head, r.Metric),
		delta, p, fmt.Sprintf("%d+%d", r.Base.N, r.Head.N), plain(r, confirmed)}
}

// plain is a row's verdict in plain words; confirmed tells whether the re-check found the row regressed too.
func plain(r verdict.Row, confirmed bool) string {
	switch r.Class {
	case verdict.Regression:
		if confirmed {
			return "regression, confirmed"
		}
		return "regression, not confirmed"
	case verdict.Improvement:
		return "improved"
	case verdict.NotSignificant:
		if r.Noisy {
			return "too noisy to tell"
		}
		return "no change (within noise)"
	case verdict.BelowThreshold:
		return "within limit"
	case verdict.TooFewSamples:
		return "too few samples"
	case verdict.New:
		return "new"
	case verdict.Removed:
		return "removed"
	case verdict.ReportOnly:
		return "not judged"
	}
	return ""
}

func notMeasured(b *strings.Builder, res result) {
	var rows [][]string
	for _, p := range []struct {
		name string
		pass pass
	}{{"first", res.first}, {"re-check", res.recheck}} {
		for _, g := range p.pass.Groups {
			for _, s := range g.Packages {
				if s.State != sample.Measured {
					rows = append(rows, []string{p.name, g.Name, s.Package, reasons[s.State]})
				}
			}
		}
	}
	if len(rows) > 0 {
		b.WriteString("### Not measured\n\n")
		table(b, []string{"Pass", "Group", "Package", "Reason"}, rows)
	}
}

func loadPerRound(b *strings.Builder, res result) {
	first, recheck := res.first.Env.Loads, res.recheck.Env.Loads
	var rows [][]string
	for i := range max(len(first), len(recheck)) {
		row := []string{strconv.Itoa(i + 1), "", ""}
		if i < len(first) {
			row[1] = first[i]
		}
		if i < len(recheck) {
			row[2] = recheck[i]
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		b.WriteString("### Load per round\n\nThe 1, 5 and 15 minute load averages before each round.\n\n")
		table(b, []string{"Round", "First pass", "Re-check"}, rows)
	}
}

// side is a side's median and interval, or empty when the row is absent there.
func side(s verdict.Side, metric string) string {
	if s.N == 0 {
		return ""
	}
	return value(s.Median, metric) + " ±" + s.PctRange()
}

// unit is a prefixed unit of a metric.
type unit struct {
	size   float64
	suffix string
}

var (
	timeUnits  = []unit{{1, "s"}, {1e-3, "ms"}, {1e-6, "µs"}, {1e-9, "ns"}}
	byteUnits  = []unit{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}, {1, "B"}}
	countUnits = []unit{{1e9, "G"}, {1e6, "M"}, {1e3, "k"}, {1, ""}}
)

// value is v of metric in the largest unit it fills.
func value(v float64, metric string) string {
	units := countUnits
	switch metric {
	case "sec/op":
		units = timeUnits
	case "B/op":
		units = byteUnits
	}
	u := units[len(units)-1]
	for _, c := range units {
		if math.Abs(v) >= c.size {
			u = c
			break
		}
	}
	return fmt.Sprintf("%.4g%s", v/u.size, u.suffix)
}

// change is a relative change such as +12.34%, or +∞ from a zero base.
func change(d float64) string {
	if math.IsInf(d, 1) {
		return "+∞"
	}
	return fmt.Sprintf("%+.2f%%", d*100)
}

// percent is a threshold such as 0.1 as +10%.
func percent(t float64) string {
	return fmt.Sprintf("+%.4g%%", t*100)
}

// inOut is path relative to the output directory.
func (o Options) inOut(path string) string {
	rel, _ := filepath.Rel(o.Out, path)
	return filepath.ToSlash(rel)
}

// cellEscaper keeps a cell from splitting its row or opening or closing HTML such as a folded block.
var cellEscaper = strings.NewReplacer("|", `\|`, "<", "&lt;")

// table writes a Markdown table of header and rows.
func table(b *strings.Builder, header []string, rows [][]string) {
	line := func(cells []string) {
		escaped := make([]string, len(cells))
		for i, c := range cells {
			escaped[i] = cellEscaper.Replace(c)
		}
		b.WriteString("| " + strings.Join(escaped, " | ") + " |\n")
	}
	line(header)
	b.WriteString(strings.Repeat("| --- ", len(header)) + "|\n")
	for _, r := range rows {
		line(r)
	}
	b.WriteString("\n")
}
