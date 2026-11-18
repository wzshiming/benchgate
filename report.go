package benchgate

import (
	"cmp"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/wzshiming/benchgate/sample"
	"github.com/wzshiming/benchgate/verdict"
)

// reasons say why a package was not measured.
var reasons = map[sample.State]string{
	sample.Identical:   "identical test binaries",
	sample.BaseMissing: "base has no such package",
	sample.BaseFailed:  "base failed to build or run",
}

// outcomes are what can become of a benchmark, in the order the count sentence lists them.
var outcomes = []string{"regressed", "improved", "unchanged", "new", "removed", "not judged"}

// render is the Markdown report of res, a run of the gate under cfg as o asked for it.
func render(o Options, cfg Config, res result) []byte {
	var b strings.Builder
	confirmed, excluded := res.split()
	total := make(map[string]int) // benchmarks by outcome
	for _, g := range cfg.Groups {
		for fate, n := range tally(g.Name, res.first.verdicts[g.Name].Rows, confirmed) {
			total[fate] += n
		}
	}
	headline(&b, total, res.first)
	b.WriteString("## Confirmed regressions\n\n")
	suspects(&b, cfg, confirmed)
	b.WriteString("## Excluded after re-check\n\n")
	suspects(&b, cfg, excluded)
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

// suspects writes a table of suspects with the numbers of both passes.
func suspects(b *strings.Builder, cfg Config, list []suspect) {
	var rows [][]string
	for _, s := range list {
		recheck := "not measured"
		if s.recheck.Class != "" {
			recheck = numbers(s.recheck) + ", " + string(s.recheck.Class)
		}
		rows = append(rows, []string{s.group, s.first.Pkg, s.first.Name, s.first.Metric,
			percent(cfg.group(s.group).Thresholds[s.first.Metric]), numbers(s.first), recheck})
	}
	table(b, []string{"Group", "Package", "Benchmark", "Metric", "Threshold", "First pass", "Re-check"}, rows)
}

// comparison is every row of the first pass by group, judged groups first, given the suspects the re-check confirmed.
func comparison(o Options, cfg Config, res result, confirmed []suspect) string {
	var b strings.Builder
	for _, reportOnly := range []bool{false, true} {
		for _, g := range cfg.Groups {
			if g.ReportOnly != reportOnly || len(res.first.verdicts[g.Name].Rows) == 0 {
				continue
			}
			kind := "judged"
			if reportOnly {
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
	}
	return b.String()
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

// numbers are a row's medians, change and p-value.
func numbers(r verdict.Row) string {
	return fmt.Sprintf("%s → %s, %s, p=%.3f", value(r.Base.Median, r.Metric), value(r.Head.Median, r.Metric), change(r.Delta), r.P)
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

// table writes a Markdown table of header and rows, or "None." without rows.
func table(b *strings.Builder, header []string, rows [][]string) {
	if len(rows) == 0 {
		b.WriteString("None.\n\n")
		return
	}
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
