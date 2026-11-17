package action

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

// The stand-in gate's report, its one raw sample and its standard error.
const (
	standInReport = "# Benchmark gate\n\nstand-in report\n"
	standInSample = "BenchmarkStandIn 1 1 ns/op\n"
	standInStderr = "benchgate: stand-in error\n\tsecond line\n"
)

// invocation is what the gate command was given; MeasureIdentical is empty unless the flag was.
type invocation struct {
	Base, Head, BaseLabel, HeadLabel, Config, Out, MeasureIdentical string
}

// standIn acts as the gate command about to exit with code: it records its flags as JSON in the file STAND_IN_RECORD names and writes standInStderr to standard error, and unless code says nothing was concluded it writes standInSample and standInReport into -out and the report to standard output.
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
	if err := os.WriteFile(filepath.Join(o.Out, "report.md"), []byte(standInReport), 0o644); err != nil {
		return err
	}
	_, err := os.Stdout.WriteString(standInReport)
	return err
}

// gateStep adds to env what run.sh reads to run the stand-in gate, which exits with code, on the checkouts base and head.
func gateStep(t *testing.T, env map[string]string, code int, base, head string) {
	t.Helper()
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env["BENCHGATE"], env["BASE_TREE"], env["HEAD_TREE"], env["MEASURE_IDENTICAL"] = gate, base, head, ""
	env["RESULTS"] = filepath.Join(t.TempDir(), "results")
	env["STAND_IN_EXIT"], env["STAND_IN_RECORD"] = strconv.Itoa(code), filepath.Join(t.TempDir(), "record.json")
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
