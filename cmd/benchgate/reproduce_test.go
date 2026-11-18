package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

// fullRun is a run of the command whose head label needs quoting and whose configuration is in the head tree.
type fullRun struct {
	base, head, config string
	outcome
}

func runFull(t *testing.T, args ...string) fullRun {
	t.Helper()
	r := fullRun{base: t.TempDir(), head: t.TempDir()}
	r.config = filepath.Join(r.head, "benchgate.json")
	writeFile(t, r.config, fullConfig)
	r.out = filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	r.code = run(t.Context(), append([]string{
		"-base", r.base, "-head", r.head, "-base-label", "abc1234", "-head-label", "def5678 (merge)",
		"-config", r.config, "-out", r.out,
	}, args...), &stdout, &stderr, (&fake{}).run)
	r.report, r.stderr = stdout.String(), stderr.String()
	return r
}

// The library's test of this name checks every section but Reproduce.
func TestReportHasEverySection(t *testing.T) {
	t.Parallel()
	report := runFull(t).report

	t.Run("reproduce", func(t *testing.T) {
		got := section(t, report, "### Reproduce")
		for _, want := range []string{
			"`go install github.com/wzshiming/benchgate/cmd/benchgate@", // at the version of this test binary
			"`benchgate -base <base> -head <head> -base-label abc1234 -head-label 'def5678 (merge)' -config <head>/benchgate.json -out <out>`",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("reproduce %q does not hold %s", got, want)
			}
		}
	})
}

func TestReportNamesLocalPathsOnlyByPlaceholders(t *testing.T) {
	t.Parallel()
	r := runFull(t)
	for _, p := range []string{"<base>", "<head>", "<head>/benchgate.json", "<out>"} {
		if !strings.Contains(r.report, p) {
			t.Errorf("report does not name %s", p)
		}
	}
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

func TestReproduceRepeatsTheOverridesAndHidesPaths(t *testing.T) {
	t.Parallel()
	r := runFull(t, "-groups", "cpu,mem", "-measure-identical=false")
	want := "`benchgate -base <base> -head <head> -base-label abc1234 -head-label 'def5678 (merge)' -config <head>/benchgate.json -groups cpu,mem -measure-identical=false -out <out>`"
	if got := section(t, r.report, "### Reproduce"); !strings.Contains(got, want) {
		t.Errorf("reproduce %q does not hold %s", got, want)
	}

	f := &fake{}
	var stdout bytes.Buffer
	run(t.Context(), []string{
		"-base", t.TempDir(), "-head", t.TempDir(), "-base-label", "it's", "-head-label", "h",
		"-config", fakeConfig(t), "-out", filepath.Join(t.TempDir(), "out"),
	}, &stdout, &bytes.Buffer{}, f.run)
	want = "-base-label 'it'\\''s' -head-label h -config <config> -out <out>`"
	if got := section(t, stdout.String(), "### Reproduce"); !strings.Contains(got, want) {
		t.Errorf("reproduce %q does not hold %s", got, want)
	}
}

func TestReproduceInstallsTheBuiltVersionOrLatest(t *testing.T) {
	t.Parallel()
	o := options{base: "/b", head: "/h", baseLabel: "b", headLabel: "h", config: "/h/benchgate.json", out: "/o"}
	for built, want := range map[string]string{
		"v0.1.0":                                   "v0.1.0",
		"v0.0.0-20261117093000-12c09a65e6a4":       "v0.0.0-20261117093000-12c09a65e6a4",
		"v0.0.0-20261117093000-12c09a65e6a4+dirty": "latest",
		"(devel)": "latest",
		"":        "latest",
	} {
		got := o.reproduce(built)
		if install := "`go install github.com/wzshiming/benchgate/cmd/benchgate@" + want + "`"; !strings.Contains(got, install) {
			t.Errorf("built as %q: reproduce %q does not hold %s", built, got, install)
		}
		if strings.Contains(got, "test/benchgate") {
			t.Errorf("built as %q: reproduce %q names test/benchgate", built, got)
		}
	}
}
