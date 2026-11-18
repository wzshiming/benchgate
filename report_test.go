package benchgate

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/wzshiming/benchgate/sample"
)

// fullConfig judges groups cpu and mem and reports group io, which it lists between them.
const fullConfig = `{
	"alpha": 0.05,
	"minSamples": 10,
	"groups": [
		{"name": "cpu", "packages": ["p", "q", "r", "s"], "bench": "A|B", "benchtime": "100x", "rounds": 10, "thresholds": {"sec/op": 0.1, "B/op": 0.2, "allocs/op": 0}},
		{"name": "io", "packages": ["t"], "bench": ".", "rounds": 5, "reportOnly": true},
		{"name": "mem", "packages": ["u"], "bench": ".", "rounds": 10, "thresholds": {"B/op": 0.1}}
	]
}`

// alternating is a sample file of package example.com/fake with ten results of name, alternately at lo and hi ns/op.
func alternating(name string, lo, hi float64) string {
	var b strings.Builder
	b.WriteString("pkg: example.com/fake\n")
	for i := range 10 {
		fmt.Fprintf(&b, "%s 1000 %g ns/op 0 B/op 0 allocs/op\n", name, []float64{lo, hi}[i%2])
	}
	return b.String()
}

// loads are ten rounds' load averages, told apart by first.
func loads(first int) []string {
	var l []string
	for r := range 10 {
		l = append(l, fmt.Sprintf("%d.%02d 0.50 0.90", first, r+1))
	}
	return l
}

// fullRun is a gate run whose report has something in every section: in group cpu, BenchmarkA-8 regresses
// in both passes and BenchmarkC-8 in the first alone, BenchmarkN-8 is new, BenchmarkR-8 removed and
// BenchmarkZ-8 noisy, and three packages are not measured; group mem regresses in the first pass and its
// base fails in the re-check; report-only group io doubles with three head samples.
type fullRun struct {
	base, head, config string
	outcome
}

func runFull(t *testing.T, edits ...func(*Options)) fullRun {
	t.Helper()
	// BenchmarkF-8 improves, BenchmarkW-8 slows within its limit and BenchmarkT-8 has five head samples.
	cpu := canned{
		base: samples(bench{"BenchmarkA-8", 100, 64, 1}, bench{"BenchmarkC-8", 100, 64, 1}, bench{"BenchmarkR-8", 100, 0, 0}, bench{"BenchmarkZ-8", 100, 0, 0},
			bench{"BenchmarkF-8", 100, 0, 0}, bench{"BenchmarkW-8", 100, 0, 0}, bench{"BenchmarkT-8", 100, 0, 0}),
		head: samples(bench{"BenchmarkA-8", 200, 64, 1}, bench{"BenchmarkC-8", 200, 64, 1}, bench{"BenchmarkN-8", 100, 0, 0},
			bench{"BenchmarkF-8", 50, 0, 0}, bench{"BenchmarkW-8", 105, 0, 0}) + alternating("BenchmarkZ-8", 70, 130) +
			strings.Repeat("BenchmarkT-8 1000 100 ns/op 0 B/op 0 allocs/op\n", 5),
		states: []sample.Status{
			{Package: "p", State: sample.Measured}, {Package: "q", State: sample.Identical},
			{Package: "r", State: sample.BaseMissing}, {Package: "s", State: sample.BaseFailed},
		},
	}
	f := &fake{
		passes: []map[string]canned{
			{
				"cpu": cpu,
				"io":  {base: samples(bench{"BenchmarkD-8", 100, 0, 0}), head: strings.Repeat("pkg: example.com/fake\nBenchmarkD-8 1000 200 ns/op 0 B/op 0 allocs/op\n", 3)},
				"mem": {base: samples(bench{"BenchmarkE-8", 100, 64, 1}), head: samples(bench{"BenchmarkE-8", 100, 128, 1})},
			},
			{
				"cpu": {
					base:   samples(bench{"BenchmarkA-8", 100, 64, 1}, bench{"BenchmarkC-8", 100, 64, 1}),
					head:   samples(bench{"BenchmarkA-8", 210, 64, 1}, bench{"BenchmarkC-8", 100, 64, 1}),
					states: []sample.Status{{Package: "p", State: sample.Measured}},
				},
				"mem": {states: []sample.Status{{Package: "u", State: sample.BaseFailed}}},
			},
		},
		envs: []sample.Environment{
			{CPU: "Fake CPU @ 3.00GHz", Cores: 8, OSArch: "linux/amd64", GoVersion: "go1.27.3", Loads: loads(0)},
			{CPU: "Fake CPU @ 3.00GHz", Cores: 8, OSArch: "linux/amd64", GoVersion: "go1.27.3", Loads: loads(1)},
		},
	}
	r := fullRun{base: t.TempDir(), head: t.TempDir()}
	r.config = filepath.Join(r.head, "benchgate.json")
	writeFile(t, r.config, fullConfig)
	labels := func(o *Options) { o.BaseLabel, o.HeadLabel = "abc1234", "def5678 (merge)" }
	r.outcome = runGate(t, f.run, r.base, r.head, r.config, append([]func(*Options){labels}, edits...)...)
	return r
}

func TestReportHasEverySection(t *testing.T) {
	t.Parallel()
	r := runFull(t)
	wantCode(t, r.outcome, 1)
	report := r.report

	t.Run("conclusion", func(t *testing.T) {
		if lines := strings.SplitN(report, "\n", 4); len(lines) < 3 || lines[2] != "**FAIL**: 1 confirmed regression." {
			t.Errorf("report starts %q, want the title and the conclusion", lines)
		}
	})
	t.Run("trees", func(t *testing.T) {
		if got, want := section(t, folded(t, report, "How this was measured"), "### Trees"), "\n- Base: abc1234\n- Head: def5678 (merge)\n\n"; got != want {
			t.Errorf("trees %q, want %q", got, want)
		}
	})
	t.Run("environment", func(t *testing.T) {
		want := "\n- CPU: Fake CPU @ 3.00GHz\n- Cores: 8\n- OS/arch: linux/amd64\n- Go: go1.27.3\n\n"
		if got := section(t, folded(t, report, "How this was measured"), "### Environment"); got != want {
			t.Errorf("environment %q, want %q", got, want)
		}
	})
	t.Run("policy", func(t *testing.T) {
		policy := section(t, folded(t, report, "How this was measured"), "### Policy")
		if want := "Significance level 0.05; a row is judged with at least 10 samples on each side; identical test binaries are not measured."; !strings.Contains(policy, want) {
			t.Errorf("policy %q does not say %q", policy, want)
		}
		want := [][]string{
			{"cpu", "p, q, r, s", "`A\\|B`", "100x", "10", "sec/op +10%, B/op +20%, allocs/op +0%"},
			{"io", "t", "`.`", "default", "5", "report only"},
			{"mem", "u", "`.`", "default", "10", "B/op +10%"},
		}
		if got := rows(policy); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("policy rows %q, want %q", got, want)
		}
	})
	t.Run("confirmed regressions", func(t *testing.T) {
		want := [][]string{{"cpu", "example.com/fake", "BenchmarkA-8", "sec/op", "+10%",
			"100ns → 200ns, +100.00%, p=0.000", "100ns → 210ns, +110.00%, p=0.000, regression"}}
		if got := rows(section(t, report, "## Confirmed regressions")); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("confirmed regressions %q, want %q", got, want)
		}
	})
	t.Run("excluded after re-check", func(t *testing.T) {
		want := [][]string{
			{"cpu", "example.com/fake", "BenchmarkC-8", "sec/op", "+10%", "100ns → 200ns, +100.00%, p=0.000", "100ns → 100ns, +0.00%, p=1.000, not significant"},
			{"mem", "example.com/fake", "BenchmarkE-8", "B/op", "+10%", "64B → 128B, +100.00%, p=0.000", "not measured"},
		}
		if got := rows(section(t, report, "## Excluded after re-check")); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("excluded after re-check %q, want %q", got, want)
		}
	})
	t.Run("all results", func(t *testing.T) {
		all := folded(t, report, "All results")
		cpu, mem, io := strings.Index(all, "\n### cpu (judged)\n"), strings.Index(all, "\n### mem (judged)\n"), strings.Index(all, "\n### io (report only)\n")
		if cpu < 0 || mem < cpu || io < mem {
			t.Errorf("all results headings at %d, %d, %d; want cpu, mem, then io", cpu, mem, io)
		}
		if cpu := section(t, all, "### cpu (judged)"); !strings.Contains(cpu, "`first/cpu.base.txt`, `first/cpu.head.txt`") || !strings.Contains(cpu, "`recheck/cpu.base.txt`, `recheck/cpu.head.txt`") {
			t.Errorf("cpu results %q do not name the sample files of both passes", cpu)
		}
	})
	t.Run("not measured", func(t *testing.T) {
		want := [][]string{
			{"first", "cpu", "q", "identical test binaries"},
			{"first", "cpu", "r", "base has no such package"},
			{"first", "cpu", "s", "base failed to build or run"},
			{"re-check", "mem", "u", "base failed to build or run"},
		}
		if got := rows(section(t, folded(t, report, "How this was measured"), "### Not measured")); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("not measured %q, want %q", got, want)
		}
	})
	t.Run("load per round", func(t *testing.T) {
		got := rows(section(t, folded(t, report, "How this was measured"), "### Load per round"))
		if len(got) != 10 || !hasRow(got, "1", "0.01 0.50 0.90", "1.01 0.50 0.90") || !hasRow(got, "10", "0.10 0.50 0.90", "1.10 0.50 0.90") {
			t.Errorf("loads %q, want ten rounds of both passes", got)
		}
	})
}

func TestReportNamesLocalPathsOnlyByPlaceholders(t *testing.T) {
	t.Parallel()
	r := runFull(t)
	home, _ := os.UserHomeDir()
	for _, p := range []string{r.base, r.head, r.config, r.out, os.TempDir(), home} {
		if p != "" && strings.Contains(r.report, p) {
			t.Errorf("report holds local path %s", p)
		}
	}
	if m := regexp.MustCompile("(^|[\\s`(])/[^\\s]*").FindAllString(r.report, -1); m != nil {
		t.Errorf("report holds absolute paths %q", m)
	}
}

func TestReportIsWrittenToTheOutputDirectory(t *testing.T) {
	t.Parallel()
	r := runFull(t)
	if got, err := os.ReadFile(filepath.Join(r.out, "report.md")); err != nil || string(got) != r.report {
		t.Errorf("report.md %q (%v), want what the command printed", got, err)
	}
}

func TestNumbersAreShownWithUnitPrefixes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v      float64
		metric string
		want   string
	}{
		{1.5, "sec/op", "1.5s"},
		{2.5e-3, "sec/op", "2.5ms"},
		{1.234e-6, "sec/op", "1.234µs"},
		{100e-9, "sec/op", "100ns"},
		{0, "sec/op", "0ns"},
		{64, "B/op", "64B"},
		{2048, "B/op", "2KiB"},
		{3 << 20, "B/op", "3MiB"},
		{1 << 30, "B/op", "1GiB"},
		{1, "allocs/op", "1"},
		{1500, "allocs/op", "1.5k"},
		{2.5e9, "B/s", "2.5G"},
		{3e6, "cpu-s/GiB", "3M"},
	} {
		if got := value(c.v, c.metric); got != c.want {
			t.Errorf("value(%v, %q) = %q, want %q", c.v, c.metric, got, c.want)
		}
	}
	for d, want := range map[float64]string{1: "+100.00%", -0.5: "-50.00%", 0: "+0.00%", math.Inf(1): "+∞"} {
		if got := change(d); got != want {
			t.Errorf("change(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestIntervalsReadAsBenchstatShowsThem(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("pkg: example.com/fake\n")
	zeroMedian := []int{0, 0, 0, 0, 0, 0, 0, 4, 4, 4}
	for i, zeroBound := range []int{0, 0, 0, 4, 4, 4, 4, 4, 4, 4} {
		fmt.Fprintf(&b, "BenchmarkA-8 1000 100 ns/op %d B/op 0 allocs/op\nBenchmarkB-8 1000 100 ns/op %d B/op 0 allocs/op\n", zeroBound, zeroMedian[i])
	}
	f := &fake{passes: []map[string]canned{{"cpu": {base: b.String(), head: b.String()}}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	cpu := rows(section(t, o.report, "### cpu (judged)"))
	// benchstat prints ± ? where a bound and the median differ in sign.
	for _, cells := range [][]string{
		{"BenchmarkA-8", "sec/op", "100ns ±0%"},
		{"BenchmarkA-8", "B/op", "4B ±?"},
		{"BenchmarkB-8", "B/op", "0B ±?"},
	} {
		if !hasRow(cpu, cells...) {
			t.Errorf("cpu rows %q, want a row with %q", cpu, cells)
		}
	}
}

func TestConclusionLine(t *testing.T) {
	t.Parallel()
	twice := canned{base: samples(steady, another), head: samples(slower, bench{"BenchmarkB-8", 100, 0, 0})}
	for _, c := range []struct {
		name   string
		passes []map[string]canned
		want   string
	}{
		{"nothing measured", []map[string]canned{{}}, "**PASS**: nothing was measured."},
		{"no regression", []map[string]canned{{"cpu": clean}}, "**PASS**: no confirmed regression."},
		{"excluded", []map[string]canned{{"cpu": regressed}, {"cpu": clean}}, "**PASS**: no confirmed regression."},
		{"two confirmed", []map[string]canned{{"cpu": twice}, {"cpu": twice}}, "**FAIL**: 2 confirmed regressions."},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: c.passes}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
			if lines := strings.SplitN(o.report, "\n", 4); len(lines) < 3 || lines[2] != c.want {
				t.Errorf("report starts %q, want the title and %q", lines, c.want)
			}
		})
	}
}

func TestAllResultsKeepsTheComparisonColumns(t *testing.T) {
	t.Parallel()
	all := folded(t, runFull(t).report, "All results")
	// The first eight cells of each row as the Comparison table showed them.
	for _, g := range []struct {
		heading string
		rows    [][]string
	}{
		{"### cpu (judged)", [][]string{
			{"example.com/fake", "BenchmarkA-8", "sec/op", "100ns ±0%", "200ns ±0%", "+100.00%", "0.000", "10+10"},
			{"example.com/fake", "BenchmarkA-8", "B/op", "64B ±0%", "64B ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkA-8", "allocs/op", "1 ±0%", "1 ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkC-8", "sec/op", "100ns ±0%", "200ns ±0%", "+100.00%", "0.000", "10+10"},
			{"example.com/fake", "BenchmarkC-8", "B/op", "64B ±0%", "64B ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkC-8", "allocs/op", "1 ±0%", "1 ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkR-8", "sec/op", "100ns ±0%", "", "", "", "10+0"},
			{"example.com/fake", "BenchmarkR-8", "B/op", "0B ±0%", "", "", "", "10+0"},
			{"example.com/fake", "BenchmarkR-8", "allocs/op", "0 ±0%", "", "", "", "10+0"},
			{"example.com/fake", "BenchmarkZ-8", "sec/op", "100ns ±0%", "100ns ±30%", "-0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkZ-8", "B/op", "0B ±0%", "0B ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkZ-8", "allocs/op", "0 ±0%", "0 ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkF-8", "sec/op", "100ns ±0%", "50ns ±0%", "-50.00%", "0.000", "10+10"},
			{"example.com/fake", "BenchmarkF-8", "B/op", "0B ±0%", "0B ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkF-8", "allocs/op", "0 ±0%", "0 ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkW-8", "sec/op", "100ns ±0%", "105ns ±0%", "+5.00%", "0.000", "10+10"},
			{"example.com/fake", "BenchmarkW-8", "B/op", "0B ±0%", "0B ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkW-8", "allocs/op", "0 ±0%", "0 ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkT-8", "sec/op", "100ns ±0%", "100ns ±∞", "+0.00%", "1.000", "10+5"},
			{"example.com/fake", "BenchmarkT-8", "B/op", "0B ±0%", "0B ±∞", "+0.00%", "1.000", "10+5"},
			{"example.com/fake", "BenchmarkT-8", "allocs/op", "0 ±0%", "0 ±∞", "+0.00%", "1.000", "10+5"},
			{"example.com/fake", "BenchmarkN-8", "sec/op", "", "100ns ±0%", "", "", "0+10"},
			{"example.com/fake", "BenchmarkN-8", "B/op", "", "0B ±0%", "", "", "0+10"},
			{"example.com/fake", "BenchmarkN-8", "allocs/op", "", "0 ±0%", "", "", "0+10"},
		}},
		{"### mem (judged)", [][]string{
			{"example.com/fake", "BenchmarkE-8", "sec/op", "100ns ±0%", "100ns ±0%", "+0.00%", "1.000", "10+10"},
			{"example.com/fake", "BenchmarkE-8", "B/op", "64B ±0%", "128B ±0%", "+100.00%", "0.000", "10+10"},
			{"example.com/fake", "BenchmarkE-8", "allocs/op", "1 ±0%", "1 ±0%", "+0.00%", "1.000", "10+10"},
		}},
		{"### io (report only)", [][]string{
			{"example.com/fake", "BenchmarkD-8", "sec/op", "100ns ±0%", "200ns ±∞", "+100.00%", "0.007", "10+3"},
			{"example.com/fake", "BenchmarkD-8", "B/op", "0B ±0%", "0B ±∞", "+0.00%", "1.000", "10+3"},
			{"example.com/fake", "BenchmarkD-8", "allocs/op", "0 ±0%", "0 ±∞", "+0.00%", "1.000", "10+3"},
		}},
	} {
		s := section(t, all, g.heading)
		if header := "\n| Package | Benchmark | Metric | Base | Head | Delta | p | n | Result |\n"; !strings.Contains(s, header) {
			t.Errorf("%s has no header %q:\n%s", g.heading, header, s)
		}
		var got [][]string
		for _, r := range rows(s) {
			got = append(got, r[:min(len(r), 8)])
		}
		if !slices.EqualFunc(got, g.rows, slices.Equal) {
			t.Errorf("%s rows begin %q, want %q", g.heading, got, g.rows)
		}
	}
}

func TestResultSaysEachVerdictInPlainWords(t *testing.T) {
	t.Parallel()
	all := folded(t, runFull(t).report, "All results")
	for _, c := range []struct{ heading, name, metric, want string }{
		{"### cpu (judged)", "BenchmarkA-8", "sec/op", "regression, confirmed"},
		{"### cpu (judged)", "BenchmarkC-8", "sec/op", "regression, not confirmed"},
		{"### mem (judged)", "BenchmarkE-8", "B/op", "regression, not confirmed"},
		{"### cpu (judged)", "BenchmarkF-8", "sec/op", "improved"},
		{"### cpu (judged)", "BenchmarkA-8", "B/op", "no change (within noise)"},
		{"### cpu (judged)", "BenchmarkZ-8", "sec/op", "too noisy to tell"},
		{"### cpu (judged)", "BenchmarkW-8", "sec/op", "within limit"},
		{"### cpu (judged)", "BenchmarkT-8", "sec/op", "too few samples"},
		{"### cpu (judged)", "BenchmarkN-8", "sec/op", "new"},
		{"### cpu (judged)", "BenchmarkR-8", "sec/op", "removed"},
		{"### io (report only)", "BenchmarkD-8", "sec/op", "not judged"},
	} {
		got := ""
		for _, r := range rows(section(t, all, c.heading)) {
			if r[1] == c.name && r[2] == c.metric {
				got = r[len(r)-1]
			}
		}
		if got != c.want {
			t.Errorf("%s %s %s: result %q, want %q", c.heading, c.name, c.metric, got, c.want)
		}
	}
}

func TestAllResultsLeavesOutGroupsWithoutRows(t *testing.T) {
	t.Parallel()
	noRows := canned{base: "PASS\n", head: "PASS\n", states: []sample.Status{{Package: "p", State: sample.Measured}, {Package: "q", State: sample.Identical}}}
	for _, c := range []struct {
		name       string
		pass       map[string]canned
		kept, left string
	}{
		{"not measured", map[string]canned{"cpu": clean}, "### cpu (judged)", "### io (report only)"},
		{"measured without rows", map[string]canned{"cpu": noRows, "io": clean}, "### io (report only)", "### cpu (judged)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: []map[string]canned{c.pass}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
			wantCode(t, o, 0)
			if all := folded(t, o.report, "All results"); !strings.Contains(all, "\n"+c.kept+"\n") || strings.Contains(all, "\n"+c.left+"\n") {
				t.Errorf("all results %q, want %s and no %s", all, c.kept, c.left)
			}
		})
	}
}

func TestAllResultsIsThereOnlyWithRows(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		pass map[string]canned
		want bool
	}{
		{"rows", map[string]canned{"cpu": clean}, true},
		{"no rows", map[string]canned{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: []map[string]canned{c.pass}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
			wantCode(t, o, 0)
			if got := strings.Contains(o.report, "<summary>All results</summary>"); got != c.want {
				t.Errorf("report has All results %v, want %v:\n%s", got, c.want, o.report)
			}
		})
	}
}

func TestHowThisWasMeasuredIsThereWhenNothingWasMeasured(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	how := folded(t, o.report, "How this was measured")
	for _, part := range []string{"### Trees", "### Environment", "### Policy"} {
		if !strings.Contains(how, "\n"+part+"\n") {
			t.Errorf("how this was measured has no %s:\n%s", part, how)
		}
	}
}

func TestNotMeasuredIsLeftOutWhenEveryPackageWasMeasured(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{"cpu": clean}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), func(o *Options) { o.Groups = []string{"cpu"} })
	wantCode(t, o, 0)
	if how := folded(t, o.report, "How this was measured"); strings.Contains(how, "### Not measured") {
		t.Errorf("how this was measured lists packages not measured though every package was:\n%s", how)
	}
}

func TestLoadPerRoundIsLeftOutWhenNoRoundRan(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	if how := folded(t, o.report, "How this was measured"); strings.Contains(how, "### Load per round") {
		t.Errorf("how this was measured has a load per round though no round ran:\n%s", how)
	}
}

func TestReproduceTextEndsHowThisWasMeasuredVerbatim(t *testing.T) {
	t.Parallel()
	const text = "Check out `<base>` and `<head>`, then run:\n\n```sh\nmake gate BASE=<base> HEAD=<head> # 100% | %s\n```\n"
	how := folded(t, runFull(t, func(o *Options) { o.Reproduce = text }).report, "How this was measured")
	if want := "\n### Reproduce\n\n" + text + "\n"; !strings.HasSuffix(how, want) {
		t.Errorf("how this was measured ends %q, want %q", how[max(0, len(how)-len(want)):], want)
	}
}

func TestHowThisWasMeasuredWithoutReproduceTextHasNoReproducePart(t *testing.T) {
	t.Parallel()
	if how := folded(t, runFull(t).report, "How this was measured"); strings.Contains(how, "### Reproduce") {
		t.Errorf("how this was measured without a reproduce text has a Reproduce part:\n%s", how)
	}
}

func TestFoldedBlocksHaveBlankLinesAroundTheirMarkdown(t *testing.T) {
	t.Parallel()
	// A reproduce text without a final newline ends a block.
	lines := strings.Split(runFull(t, func(o *Options) { o.Reproduce = "Run `make gate`." }).report, "\n")
	opened, closed := 0, 0
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "<summary>"):
			opened++
			if lines[i-1] != "<details>" || i+1 == len(lines) || lines[i+1] != "" {
				t.Errorf("line %d, %q, is not between a <details> line and a blank line", i+1, line)
			}
		case strings.Contains(line, "</details>"):
			closed++
			if line != "</details>" || lines[i-1] != "" {
				t.Errorf("line %d, %q, is not a closing tag alone after a blank line", i+1, line)
			}
		}
	}
	if opened == 0 || closed != opened {
		t.Errorf("report opens %d folded blocks and closes %d, want some and as many closed:\n%s", opened, closed, strings.Join(lines, "\n"))
	}
}

func TestFoldedBlocksNeverSayNone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		passes []map[string]canned
		groups []string
	}{
		{"nothing measured", []map[string]canned{{}}, nil},
		{"every package measured", []map[string]canned{{"cpu": clean}}, []string{"cpu"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: c.passes}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), func(o *Options) { o.Groups = c.groups })
			wantCode(t, o, 0)
			// The folded blocks end the report.
			_, blocks, ok := strings.Cut(o.report, "\n<details>\n")
			if !ok || strings.Contains(blocks, "None.") {
				t.Errorf("folded blocks %q, want some and none saying None.", blocks)
			}
		})
	}
}

func TestBenchmarkNameCannotOpenOrCloseAFoldedBlock(t *testing.T) {
	t.Parallel()
	odd := bench{"BenchmarkA/</details><details>-8", 100, 64, 1}
	f := &fake{passes: []map[string]canned{{"cpu": {base: samples(odd), head: samples(odd)}}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	if opened, closed := strings.Count(o.report, "<details>"), strings.Count(o.report, "</details>"); opened != 2 || closed != 2 {
		t.Errorf("report opens %d folded blocks and closes %d, want the two blocks alone:\n%s", opened, closed, o.report)
	}
	if got := rows(section(t, folded(t, o.report, "All results"), "### cpu (judged)")); len(got) != 3 {
		t.Errorf("all results ends within the rows of %s; rows before its end %q", odd.name, got)
	}
}

// section is the part of report under heading, up to the next heading of its level or above or a details tag.
func section(t *testing.T, report, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(report, "\n"+heading+"\n")
	if !ok {
		t.Fatalf("no %q in the report:\n%s", heading, report)
	}
	level := strings.IndexByte(heading, ' ')
	offset := 0
	for line := range strings.Lines(rest) {
		if n := len(line) - len(strings.TrimLeft(line, "#")); n > 0 && n <= level && strings.HasPrefix(line[n:], " ") {
			return rest[:offset]
		}
		if line == "<details>\n" || line == "</details>\n" {
			return rest[:offset]
		}
		offset += len(line)
	}
	return rest
}

// folded is the part of report folded under summary, from the end of the summary line to the closing tag.
func folded(t *testing.T, report, summary string) string {
	t.Helper()
	_, rest, ok := strings.Cut(report, "<details>\n<summary>"+summary+"</summary>\n")
	if !ok {
		t.Fatalf("no block %q in the report:\n%s", summary, report)
	}
	md, _, _ := strings.Cut(rest, "</details>")
	return md
}

// rows are the cells of the table rows in s, without headers and separators.
func rows(s string) [][]string {
	var table [][]string
	n := 0
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			n = 0
			continue
		}
		if n++; n > 2 {
			table = append(table, strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "| "), " |"), " | "))
		}
	}
	return table
}

// hasRow tells whether a row of table has a cell equal to each of cells.
func hasRow(table [][]string, cells ...string) bool {
	return slices.ContainsFunc(table, func(row []string) bool {
		for _, c := range cells {
			if !slices.Contains(row, c) {
				return false
			}
		}
		return true
	})
}
