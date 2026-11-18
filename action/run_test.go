package action

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestMain runs the tests, or acts as the stand-in gate when run.sh runs this binary with STAND_IN_EXIT set.
func TestMain(m *testing.M) {
	if exit, ok := os.LookupEnv("STAND_IN_EXIT"); ok {
		code, err := strconv.Atoi(exit)
		if err == nil {
			err = standIn(code)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "stand-in:", err)
			os.Exit(125)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// The stand-in gate's default report, its one raw sample and its standard error.
const (
	standInReport = "# Benchmark gate: FAIL, 1 benchmark regressed\n\nstand-in report\n"
	standInSample = "BenchmarkStandIn 1 1 ns/op\n"
	standInStderr = "benchgate: stand-in error\n\tsecond line\n"
)

// invocation is what the gate command was given; MeasureIdentical is empty unless the flag was.
type invocation struct {
	Base, Head, BaseLabel, HeadLabel, Config, Out, MeasureIdentical string
}

// standIn acts as the gate command about to exit with code: it records its flags as JSON in the file STAND_IN_RECORD names and writes standInStderr to standard error, and unless code says nothing was concluded it writes standInSample and the report STAND_IN_REPORT holds into -out and the report to standard output.
func standIn(code int) error {
	var o invocation
	fs := flag.NewFlagSet("benchgate", flag.ContinueOnError)
	for name, value := range map[string]*string{
		"base": &o.Base, "head": &o.Head, "base-label": &o.BaseLabel, "head-label": &o.HeadLabel, "config": &o.Config, "out": &o.Out,
	} {
		fs.StringVar(value, name, "", "")
	}
	fs.BoolFunc("measure-identical", "", func(s string) error {
		v, err := strconv.ParseBool(s)
		o.MeasureIdentical = strconv.FormatBool(v)
		return err
	})
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	record, _ := json.Marshal(o)
	if err := os.WriteFile(os.Getenv("STAND_IN_RECORD"), record, 0o644); err != nil {
		return err
	}
	os.Stderr.WriteString(standInStderr)
	if code >= 2 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(o.Out, "first"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(o.Out, "first", "g.base.txt"), []byte(standInSample), 0o644); err != nil {
		return err
	}
	report := os.Getenv("STAND_IN_REPORT")
	if err := os.WriteFile(filepath.Join(o.Out, "report.md"), []byte(report), 0o644); err != nil {
		return err
	}
	_, err := os.Stdout.WriteString(report)
	return err
}

// gateStep adds to env what run.sh reads to run the stand-in gate, which exits with code and reports standInReport, on the checkouts base and head.
func gateStep(t *testing.T, env map[string]string, code int, base, head string) {
	t.Helper()
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env["BENCHGATE"], env["BASE_TREE"], env["HEAD_TREE"], env["MEASURE_IDENTICAL"] = gate, base, head, ""
	env["RESULTS"] = filepath.Join(t.TempDir(), "results")
	env["STAND_IN_EXIT"], env["STAND_IN_RECORD"], env["STAND_IN_REPORT"] = strconv.Itoa(code), filepath.Join(t.TempDir(), "record.json"), standInReport
}

// gating checks out the base and the merge commit of a pull request and returns run.sh's environment for the stand-in gate exiting with code on them, with the two commits.
func gating(t *testing.T, code int) (env map[string]string, base, merge string) {
	t.Helper()
	repo, merge, base := pullRequest(t, map[string]string{"a.go": changed}, nil)
	env = pullRequestEvent(merge)
	gateStep(t, env, code, checkout(t, repo, base, 1), checkout(t, repo, merge, 1))
	return env, base, merge
}

// recorded returns what the stand-in gate was given in the step s that ran with env.
func recorded(t *testing.T, s step, env map[string]string) invocation {
	t.Helper()
	record, err := os.ReadFile(env["STAND_IN_RECORD"])
	if err != nil {
		t.Fatalf("the gate did not run: %v\n%s", err, s.log)
	}
	var got invocation
	if err := json.Unmarshal(record, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestGateRunsOnTheTreesLabelledByTheirCommits(t *testing.T) {
	t.Parallel()
	env, base, merge := gating(t, 0)
	got := recorded(t, run(t, "run.sh", t.TempDir(), env), env)
	want := invocation{
		Base: env["BASE_TREE"], Head: env["HEAD_TREE"], BaseLabel: base, HeadLabel: merge,
		Config: env["HEAD_TREE"] + "/test/benchgate.json", Out: env["RESULTS"] + "/out",
	}
	if got != want {
		t.Errorf("the gate was given %+v, want %+v", got, want)
	}
}

func TestResultsHoldTheReportTheSamplesAndTheGatesErrors(t *testing.T) {
	t.Parallel()
	env, _, _ := gating(t, 1)
	s := run(t, "run.sh", t.TempDir(), env)
	for name, want := range map[string]string{
		"out/report.md":        standInReport,
		"out/first/g.base.txt": standInSample,
		"stderr.txt":           standInStderr,
	} {
		if got, err := os.ReadFile(filepath.Join(env["RESULTS"], name)); string(got) != want {
			t.Errorf("RESULTS/%s holds %q (%v), want %q\n%s", name, got, err, want, s.log)
		}
	}
	if !strings.Contains(s.log, standInStderr) {
		t.Errorf("log %q, want the gate's errors %q in it", s.log, standInStderr)
	}
}

func TestSummaryAndExitCodeFollowTheGate(t *testing.T) {
	t.Parallel()
	fail := "# Benchmark gate\n\n**FAIL**: nothing to conclude, the gate exited %d:\n\n    benchgate: stand-in error\n    \tsecond line\n"
	for _, c := range []struct {
		code    int
		summary string
	}{
		{0, standInReport},
		{1, standInReport},
		{2, fmt.Sprintf(fail, 2)},
		{3, fmt.Sprintf(fail, 3)},
	} {
		t.Run(fmt.Sprint("gate exiting ", c.code), func(t *testing.T) {
			t.Parallel()
			env, _, _ := gating(t, c.code)
			s := run(t, "run.sh", t.TempDir(), env)
			if s.code != c.code || s.summary != c.summary {
				t.Errorf("step exited %d with the summary %q, want %d with %q\n%s", s.code, s.summary, c.code, c.summary, s.log)
			}
		})
	}
}

func TestOutputsFollowTheGate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		code       int
		conclusion string
		reported   bool
	}{
		{0, "pass", true},
		{1, "regression", true},
		{2, "error", false},
		{3, "error", false},
	} {
		t.Run(fmt.Sprint("gate exiting ", c.code), func(t *testing.T) {
			t.Parallel()
			env, _, _ := gating(t, c.code)
			s := run(t, "run.sh", t.TempDir(), env)
			want := map[string]string{"conclusion": c.conclusion, "report": ""}
			if c.reported {
				want["report"] = env["RESULTS"] + "/out/report.md"
			}
			if !maps.Equal(s.outputs, want) {
				t.Errorf("outputs %q, want %q\n%s", s.outputs, want, s.log)
			}
		})
	}
}

func TestAnnotationsFollowTheGate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		code       int
		annotation string
	}{
		{0, ""},
		{1, "::error::Benchmark gate: FAIL, 1 benchmark regressed\n"},
		{2, "::error::Benchmark gate: FAIL, nothing to conclude (exit 2)\n"},
		{3, "::error::Benchmark gate: FAIL, nothing to conclude (exit 3)\n"},
	} {
		t.Run(fmt.Sprint("gate exiting ", c.code), func(t *testing.T) {
			t.Parallel()
			env, _, _ := gating(t, c.code)
			s := run(t, "run.sh", t.TempDir(), env)
			if _, after, ok := strings.Cut(s.log, standInStderr); !ok || after != c.annotation {
				t.Errorf("the log goes on after the gate's errors with %q, want %q\n%s", after, c.annotation, s.log)
			}
		})
	}
}

func TestRegressionAnnotationIsTheReportsFirstLineAlone(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"Benchmark gate: FAIL, 2 benchmarks regressed", "Benchmark gate: FAIL, 40312 benchmarks regressed"} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			env, _, _ := gating(t, 1)
			// A second conclusion line shows whether any line but the first is read.
			env["STAND_IN_REPORT"] = "# " + title + "\n\n# Benchmark gate: FAIL, 3 benchmarks regressed\n"
			s := run(t, "run.sh", t.TempDir(), env)
			if _, after, ok := strings.Cut(s.log, standInStderr); !ok || after != "::error::"+title+"\n" {
				t.Errorf("the log goes on after the gate's errors with %q, want only the annotation of %q\n%s", after, title, s.log)
			}
		})
	}
}

func TestActionOutputsAreTheGateSteps(t *testing.T) {
	t.Parallel()
	action, err := os.ReadFile("../action.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^      id: gate\n(?:      .*\n)*?      run: bash "\$GITHUB_ACTION_PATH/action/run\.sh"$`).Match(action) {
		t.Error("action.yml has no step gate running run.sh")
	}
	for _, name := range []string{"conclusion", "report"} {
		declared := `(?m)^outputs:\n(?:  .*\n)*?  ` + name + `:\n(?:    .*\n)*?    value: \$\{\{ steps\.gate\.outputs\.` + name + ` \}\}$`
		if !regexp.MustCompile(declared).Match(action) {
			t.Errorf("action.yml declares no output %s taken from the step gate", name)
		}
	}
}

func TestStepsInTheActionsOrderPassTheirDecisionsToTheGate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, path, given, want string }{
		{"pull request changing the configuration", "test/benchgate.json", "", "true"},
		{"pull request changing other files", "a.go", "", ""},
		{"measure-identical given false", "test/benchgate.json", "false", "false"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, merge, base := pullRequest(t, map[string]string{c.path: changed}, nil)
			env := pullRequestEvent(merge)
			env["INPUT_MEASURE_IDENTICAL"] = c.given
			head, _ := checkoutHead(t, repo, env, 2)
			compared := run(t, "compare.sh", head, env)
			if compared.code != 0 {
				t.Fatalf("compare.sh exited %d\n%s", compared.code, compared.log)
			}
			gateStep(t, env, 1, checkout(t, repo, compared.outputs["base"], 1), head)
			env["MEASURE_IDENTICAL"] = compared.outputs["measure-identical"]
			gated := run(t, "run.sh", t.TempDir(), env)
			if got := recorded(t, gated, env); gated.code != 1 || got.BaseLabel != base || got.HeadLabel != merge || got.MeasureIdentical != c.want {
				t.Errorf("run.sh exited %d after giving the gate %+v, want 1 after base-label %s, head-label %s and measure-identical %q", gated.code, got, base, merge, c.want)
			}
		})
	}
}
