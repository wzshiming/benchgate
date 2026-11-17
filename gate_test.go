package benchgate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wzshiming/benchgate/sample"
)

// fake is a sampler that answers each call with the next of its passes and records what it was asked and wrote.
type fake struct {
	passes   []map[string]canned  // by group name
	envs     []sample.Environment // by pass, zero beyond
	requests []sample.Request
	written  [][2]string // path and content of each file
}

// canned is a group's outcome: the contents of its sample files, or none, and its packages' states,
// by default all measured with samples and identical without.
type canned struct {
	base, head string
	states     []sample.Status
}

func (f *fake) run(_ context.Context, req sample.Request) (sample.Result, error) {
	f.requests = append(f.requests, req)
	if len(f.requests) > len(f.passes) {
		return sample.Result{}, fmt.Errorf("sampling %d times, want %d", len(f.requests), len(f.passes))
	}
	res := sample.Result{}
	if i := len(f.requests) - 1; i < len(f.envs) {
		res.Env = f.envs[i]
	}
	for _, g := range req.Groups {
		c := f.passes[len(f.requests)-1][g.Name]
		gr := sample.GroupResult{Name: g.Name, Packages: c.states}
		state := sample.Identical
		if c.base != "" {
			state = sample.Measured
			gr.Base, gr.Head = filepath.Join(req.Out, g.Name+".base.txt"), filepath.Join(req.Out, g.Name+".head.txt")
			for _, file := range [][2]string{{gr.Base, c.base}, {gr.Head, c.head}} {
				if err := os.MkdirAll(req.Out, 0o755); err != nil {
					return sample.Result{}, err
				}
				if err := os.WriteFile(file[0], []byte(file[1]), 0o644); err != nil {
					return sample.Result{}, err
				}
				f.written = append(f.written, file)
			}
		}
		if gr.Packages == nil {
			for _, p := range g.Packages {
				gr.Packages = append(gr.Packages, sample.Status{Package: p, State: state})
			}
		}
		res.Groups = append(res.Groups, gr)
	}
	return res, nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// outcome is what a run of the gate left behind, with the exit code the command gives such a run.
type outcome struct {
	code           int
	report, stderr string // the report is what the gate returned, stderr its diagnostics and then its error
	out            string // output directory
}

// runGate loads config and runs the gate on base and head into a new output directory, with options as edits change them.
func runGate(t *testing.T, sampleRun sampler, base, head, config string, edits ...func(*Options)) outcome {
	t.Helper()
	var stderr strings.Builder
	o := outcome{out: filepath.Join(t.TempDir(), "out")}
	cfg, err := LoadConfig(config)
	if err == nil {
		opts := Options{Base: base, Head: head, BaseLabel: "base-commit", HeadLabel: "head-commit", Config: cfg, Out: o.out, Diagnostics: &stderr}
		for _, edit := range edits {
			edit(&opts)
		}
		var report []byte
		var regressed bool
		report, regressed, err = opts.gate(t.Context(), sampleRun)
		o.report = string(report)
		if regressed {
			o.code = 1
		}
	}
	if err != nil {
		fmt.Fprintln(&stderr, "benchgate:", err)
		o.code = 2
	}
	o.stderr = stderr.String()
	return o
}

func wantCode(t *testing.T, o outcome, want int) {
	t.Helper()
	if o.code != want {
		t.Fatalf("exit code %d, want %d\nstderr:\n%s\nreport:\n%s", o.code, want, o.stderr, o.report)
	}
}

// wantNothingDone fails t unless run o sampled nothing through f and left no output directory.
func wantNothingDone(t *testing.T, f *fake, o outcome) {
	t.Helper()
	if len(f.requests) != 0 {
		t.Errorf("sampled %d times, want no build and no sampling", len(f.requests))
	}
	if _, err := os.Stat(o.out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("output directory %s exists (%v), want nothing written", o.out, err)
	}
}

// bench is a benchmark's value of each metric.
type bench struct {
	name              string
	ns, bytes, allocs float64
}

// samples is a sample file of package example.com/fake with ten results of each benchmark.
func samples(benches ...bench) string {
	var b strings.Builder
	b.WriteString("pkg: example.com/fake\n")
	for range 10 {
		for _, x := range benches {
			fmt.Fprintf(&b, "%s 1000 %g ns/op %g B/op %g allocs/op\n", x.name, x.ns, x.bytes, x.allocs)
		}
	}
	return b.String()
}

// fakeConfig writes a configuration whose group cpu judges packages p and q at 10% and whose group io
// reports package r, and returns its path.
func fakeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate.json")
	writeFile(t, path, `{
	"alpha": 0.05,
	"minSamples": 10,
	"groups": [
		{"name": "cpu", "packages": ["p", "q"], "bench": ".", "rounds": 10, "thresholds": {"sec/op": 0.1, "B/op": 0.1, "allocs/op": 0.1}},
		{"name": "io", "packages": ["r"], "bench": ".", "rounds": 10, "reportOnly": true}
	]
}
`)
	return path
}

var (
	steady  = bench{"BenchmarkA-8", 100, 64, 1}
	slower  = bench{"BenchmarkA-8", 200, 64, 1}
	bigger  = bench{"BenchmarkA-8", 100, 128, 1}
	another = bench{"BenchmarkB-8", 50, 0, 0}
)

// regressed is a group whose BenchmarkA-8 got twice as slow; nothing changed in clean.
var (
	regressed = canned{base: samples(steady, another), head: samples(slower, another)}
	clean     = canned{base: samples(steady, another), head: samples(steady, another)}
)

func TestSuspectThatDoesNotHoldOnRecheckIsExcluded(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{"cpu": regressed}, {"cpu": clean}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	if excluded := rows(section(t, o.report, "## Excluded after re-check")); !hasRow(excluded, "cpu", "BenchmarkA-8", "sec/op") {
		t.Errorf("excluded after re-check %q, want the sec/op row of BenchmarkA-8", excluded)
	}
}

func TestSuspectMustRegressInTheSameMetricOnRecheck(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{"cpu": regressed}, {"cpu": {base: samples(steady, another), head: samples(bigger, another)}}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	if excluded := rows(section(t, o.report, "## Excluded after re-check")); !hasRow(excluded, "cpu", "BenchmarkA-8", "sec/op") {
		t.Errorf("excluded after re-check %q, want the sec/op row of BenchmarkA-8", excluded)
	}
}

func TestRecheckAsksForTheSuspectRowsAlone(t *testing.T) {
	t.Parallel()
	both := canned{base: samples(steady, another), head: samples(bench{"BenchmarkA-8", 200, 128, 1}, another)}
	reported := canned{base: samples(bench{"BenchmarkC-8", 100, 0, 0}), head: samples(bench{"BenchmarkC-8", 200, 0, 0})}
	f := &fake{passes: []map[string]canned{{"cpu": both, "io": reported}, {"cpu": clean}}}
	runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), func(o *Options) { o.MeasureIdentical = new(true) })
	if len(f.requests) != 2 {
		t.Fatalf("sampled %d times, want twice", len(f.requests))
	}
	first, recheck := f.requests[0], f.requests[1]
	want := first
	want.Groups = []sample.Group{first.Groups[0]}
	want.Groups[0].Rows = []sample.Row{{Pkg: "example.com/fake", Name: "BenchmarkA-8"}}
	want.Out = recheck.Out
	if !reflect.DeepEqual(recheck, want) {
		t.Errorf("re-check request %+v, want %+v", recheck, want)
	}
}

func TestRechecksOnlyWithSuspects(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		passes []map[string]canned
	}{
		{"suspect", []map[string]canned{{"cpu": regressed}, {"cpu": clean}}},
		{"none", []map[string]canned{{"cpu": clean}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: c.passes}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
			wantCode(t, o, 0)
			if len(f.requests) != len(c.passes) {
				t.Errorf("sampled %d times, want %d", len(f.requests), len(c.passes))
			}
		})
	}
}

func TestMeasuredGroupWithoutHeadRowsExitsTwo(t *testing.T) {
	t.Parallel()
	const none = "pkg: example.com/fake\nPASS\n"
	for _, c := range []struct {
		name  string
		pass  map[string]canned
		code  int
		names string // what stderr says
	}{
		{"judged", map[string]canned{"cpu": {base: none, head: none}}, 2, `group "cpu"`},
		{"report only", map[string]canned{"io": {base: samples(another), head: none}}, 2, `group "io"`},
		{"not measured", map[string]canned{}, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: []map[string]canned{c.pass}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
			wantCode(t, o, c.code)
			if !strings.Contains(o.stderr, c.names) {
				t.Errorf("stderr %q does not name %s", o.stderr, c.names)
			}
		})
	}
}

func TestPartlyMeasuredGroupWithoutHeadRowsPasses(t *testing.T) {
	t.Parallel()
	for _, state := range []sample.State{sample.Identical, sample.BaseMissing, sample.BaseFailed} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			states := []sample.Status{{Package: "p", State: sample.Measured}, {Package: "q", State: state}}
			f := &fake{passes: []map[string]canned{{"cpu": {base: "PASS\n", head: "PASS\n", states: states}}}}
			wantCode(t, runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t)), 0)
		})
	}
}

func TestRecheckWithoutHeadRowsExitsTwo(t *testing.T) {
	t.Parallel()
	// The re-check samples only package p, which holds the suspect.
	recheck := canned{base: "PASS\n", head: "PASS\n", states: []sample.Status{{Package: "p", State: sample.Measured}}}
	f := &fake{passes: []map[string]canned{{"cpu": regressed}, {"cpu": recheck}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 2)
	if !strings.Contains(o.stderr, `group "cpu"`) {
		t.Errorf("stderr %q does not name group cpu", o.stderr)
	}
}

func TestMalformedBaseLinesAreLeftOutWithAWarning(t *testing.T) {
	t.Parallel()
	const malformed = "BenchmarkA-8\tprinted by the benchmark\n"
	for _, c := range []struct {
		name   string
		cpu    canned
		code   int
		stderr string
	}{
		{"base", canned{base: clean.base + malformed, head: clean.head}, 0, `group "cpu": malformed base sample lines left out: 1`},
		{"head", canned{base: clean.base, head: clean.head + malformed}, 2, "cpu.head.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: []map[string]canned{{"cpu": c.cpu}}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
			wantCode(t, o, c.code)
			if !strings.Contains(o.stderr, c.stderr) {
				t.Errorf("stderr %q does not say %s", o.stderr, c.stderr)
			}
		})
	}
}

func TestBaseFailureReasonGoesToStderrNotTheReport(t *testing.T) {
	t.Parallel()
	const reason = "run in round 1 failed: exit status 1\n/home/runner/work/repo/p/p_test.go:12: boom"
	states := []sample.Status{{Package: "p", State: sample.BaseFailed, Reason: reason}, {Package: "q", State: sample.Identical}}
	f := &fake{passes: []map[string]canned{{"cpu": {states: states}}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	wantCode(t, o, 0)
	if want := `group "cpu", package "p": base ` + reason; !strings.Contains(o.stderr, want) {
		t.Errorf("stderr %q does not say %q", o.stderr, want)
	}
	if strings.Contains(o.report, "boom") || strings.Contains(o.report, "/home/runner") {
		t.Errorf("report holds the reason:\n%s", o.report)
	}
	if notMeasured := rows(section(t, o.report, "## Not measured")); !hasRow(notMeasured, "cpu", "p", "base failed to build or run") {
		t.Errorf("not measured %q, want package p for its failing base", notMeasured)
	}
}

func TestBothPassesKeepTheirSamplesInTheOutputDirectory(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{"cpu": regressed}, {"cpu": clean}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t))
	if len(f.requests) != 2 {
		t.Fatalf("sampled %d times, want twice", len(f.requests))
	}
	for _, file := range f.written {
		if !strings.HasPrefix(file[0], o.out+string(filepath.Separator)) {
			t.Errorf("sample file %s is outside the output directory %s", file[0], o.out)
		}
		if got, err := os.ReadFile(file[0]); err != nil || string(got) != file[1] {
			t.Errorf("%s does not hold the samples written there (%v)", file[0], err)
		}
	}
}

func TestGroupSubsetLeavesTheCallersConfigAlone(t *testing.T) {
	t.Parallel()
	cfg, err := LoadConfig(fakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(cfg.Groups)
	f := &fake{passes: []map[string]canned{{}}}
	o := Options{Base: t.TempDir(), Head: t.TempDir(), BaseLabel: "base-commit", HeadLabel: "head-commit", Config: cfg, Out: filepath.Join(t.TempDir(), "out"), Groups: []string{"io"}, Diagnostics: io.Discard}
	if _, _, err := o.gate(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Groups, want) {
		t.Errorf("groups after a run of group io alone %+v, want %+v", cfg.Groups, want)
	}
}

func TestRunRejectsMissingOptions(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		field string
		clear func(*Options)
	}{
		{"Base", func(o *Options) { o.Base = "" }},
		{"Head", func(o *Options) { o.Head = "" }},
		{"BaseLabel", func(o *Options) { o.BaseLabel = "" }},
		{"HeadLabel", func(o *Options) { o.HeadLabel = "" }},
		{"Out", func(o *Options) { o.Out = "" }},
	} {
		t.Run(c.field, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: []map[string]canned{{}}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), c.clear)
			if want := "benchgate: missing Options." + c.field + "\n"; o.stderr != want {
				t.Errorf("stderr %q, want %q", o.stderr, want)
			}
			wantNothingDone(t, f, o)
		})
	}
}

func TestNilDiagnosticsAreDiscarded(t *testing.T) {
	t.Parallel()
	// Package q's failing base and the malformed base line each give a diagnostic.
	states := []sample.Status{{Package: "p", State: sample.Measured}, {Package: "q", State: sample.BaseFailed, Reason: "run in round 1 failed: exit status 1"}}
	cpu := canned{base: clean.base + "BenchmarkA-8\tprinted by the benchmark\n", head: clean.head, states: states}
	f := &fake{passes: []map[string]canned{{"cpu": cpu}}}
	o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), func(o *Options) { o.Diagnostics = nil })
	wantCode(t, o, 0)
}
