package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wzshiming/benchgate"
)

// fake is a gate that records the options of each run and finds nothing, so its report holds just their Reproduce section.
type fake struct {
	requests []benchgate.Options
}

func (f *fake) run(_ context.Context, o benchgate.Options) ([]byte, bool, error) {
	f.requests = append(f.requests, o)
	report := "# Benchmark gate\n\n"
	if o.Reproduce != "" {
		report += "## Reproduce\n\n" + o.Reproduce
	}
	return []byte(report), false, nil
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

// validConfig judges group cpu and reports group io, with fewer than ten rounds as only a report-only group may.
const validConfig = `{
	"alpha": 0.05,
	"minSamples": 10,
	"groups": [
		{"name": "cpu", "packages": ["p"], "bench": ".", "rounds": 10, "thresholds": {"sec/op": 0.1}},
		{"name": "io", "packages": ["r"], "bench": ".", "rounds": 3, "reportOnly": true}
	]
}`

// edited writes validConfig as edit changes it and returns its path.
func edited(t *testing.T, edit func(c map[string]any, groups []map[string]any)) string {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(validConfig), &c); err != nil {
		t.Fatal(err)
	}
	var groups []map[string]any
	for _, g := range c["groups"].([]any) {
		groups = append(groups, g.(map[string]any))
	}
	edit(c, groups)
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gate.json")
	writeFile(t, path, string(data))
	return path
}

// flags are a complete command line apart from the output directory.
func flags(t *testing.T, config string) []string {
	return []string{"-base", t.TempDir(), "-head", t.TempDir(), "-base-label", "b", "-head-label", "h", "-config", config}
}

func TestMissingFlagExitsTwoNamingIt(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"-base", "-head", "-base-label", "-head-label", "-config", "-out"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			args := append(flags(t, fakeConfig(t)), "-out", filepath.Join(t.TempDir(), "out"))
			i := slices.Index(args, name)
			args = slices.Delete(args, i, i+2)
			f := &fake{}
			var stderr bytes.Buffer
			if code := run(t.Context(), args, &bytes.Buffer{}, &stderr, f.run); code != 2 {
				t.Errorf("exit code %d, want 2", code)
			}
			if len(f.requests) != 0 {
				t.Errorf("sampled %d times, want no sampling", len(f.requests))
			}
			if !strings.Contains(stderr.String(), "missing "+name+"\n") {
				t.Errorf("stderr %q does not name %s", stderr.String(), name)
			}
		})
	}
}

func TestArgumentExitsTwo(t *testing.T) {
	t.Parallel()
	f := &fake{}
	o := command(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), "stray")
	wantCode(t, o, 2)
	if len(f.requests) != 0 || !strings.Contains(o.stderr, `"stray"`) {
		t.Errorf("sampled %d times, stderr %q; want no sampling and the argument named", len(f.requests), o.stderr)
	}
}

func TestOutputDirectoryWithFilesExitsTwo(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	writeFile(t, filepath.Join(out, "old.txt"), "")
	f := &fake{}
	var stderr bytes.Buffer
	if code := run(t.Context(), append(flags(t, fakeConfig(t)), "-out", out), &bytes.Buffer{}, &stderr, f.run); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if len(f.requests) != 0 || !strings.Contains(stderr.String(), "-out") {
		t.Errorf("sampled %d times, stderr %q; want no sampling and -out named", len(f.requests), &stderr)
	}
}

func TestGroupsFlagSamplesOnlyTheNamedGroups(t *testing.T) {
	t.Parallel()
	f := &fake{}
	wantCode(t, command(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), "-groups", "io"), 0)
	if len(f.requests) != 1 || len(f.requests[0].Groups) != 1 || f.requests[0].Groups[0] != "io" {
		t.Errorf("requests %+v, want one of group io alone", f.requests)
	}
}

func TestGroupsFlagWithAnUnknownGroupExitsTwo(t *testing.T) {
	t.Parallel()
	f := &fake{}
	o := command(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), "-groups", "cpu,nope")
	wantCode(t, o, 2)
	if len(f.requests) != 0 || !strings.Contains(o.stderr, `"nope"`) {
		t.Errorf("sampled %d times, stderr %q; want no sampling and group nope named", len(f.requests), o.stderr)
	}
}

func TestMeasureIdenticalFlagOverridesTheConfig(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		config bool
		flags  []string
		want   bool
	}{
		{false, nil, false},
		{true, nil, true},
		{false, []string{"-measure-identical"}, true},
		{true, []string{"-measure-identical=false"}, false},
	} {
		config := edited(t, func(m map[string]any, _ []map[string]any) { m["measureIdentical"] = c.config })
		f := &fake{}
		wantCode(t, command(t, f.run, t.TempDir(), t.TempDir(), config, c.flags...), 0)
		got := f.requests[0].Config.MeasureIdentical
		if m := f.requests[0].MeasureIdentical; m != nil {
			got = *m
		}
		if got != c.want {
			t.Errorf("config %v, flags %q: measured identical binaries %v, want %v", c.config, c.flags, got, c.want)
		}
	}
}
