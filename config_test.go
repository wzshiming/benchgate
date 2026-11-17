package benchgate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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

func TestConfigErrorsExitTwoNamingTheField(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{}}}
	wantCode(t, runGate(t, f.run, t.TempDir(), t.TempDir(), edited(t, func(map[string]any, []map[string]any) {})), 0)

	for _, c := range []struct {
		name  string
		edit  func(c map[string]any, groups []map[string]any)
		names string // what stderr says
	}{
		{"unknown field", func(_ map[string]any, g []map[string]any) { g[0]["roundz"] = 10 }, `unknown field "roundz"`},
		{"invalid regular expression", func(_ map[string]any, g []map[string]any) { g[0]["bench"] = "(" }, "groups[0].bench:"},
		{"judged group with nine rounds", func(_ map[string]any, g []map[string]any) { g[0]["rounds"] = 9 }, "groups[0].rounds:"},
		{"negative threshold", func(_ map[string]any, g []map[string]any) { g[0]["thresholds"] = map[string]any{"sec/op": -0.1} }, `groups[0].thresholds["sec/op"]:`},
		{"metric the gate cannot judge", func(_ map[string]any, g []map[string]any) { g[0]["thresholds"] = map[string]any{"ns/op": 0.1} }, `groups[0].thresholds["ns/op"]:`},
		{"neither thresholds nor reportOnly", func(_ map[string]any, g []map[string]any) { delete(g[0], "thresholds") }, "groups[0].thresholds:"},
		{"thresholds in a report-only group", func(_ map[string]any, g []map[string]any) { g[1]["thresholds"] = map[string]any{"sec/op": 0.1} }, "groups[1].thresholds:"},
		{"alpha of 0", func(c map[string]any, _ []map[string]any) { c["alpha"] = 0 }, "alpha:"},
		{"alpha of 1", func(c map[string]any, _ []map[string]any) { c["alpha"] = 1 }, "alpha:"},
		{"no minSamples", func(c map[string]any, _ []map[string]any) { delete(c, "minSamples") }, "minSamples:"},
		{"judged group with fewer rounds than minSamples", func(c map[string]any, _ []map[string]any) { c["minSamples"] = 12 }, "groups[0].rounds:"},
		{"no groups", func(c map[string]any, _ []map[string]any) { c["groups"] = []any{} }, "groups:"},
		{"group without packages", func(_ map[string]any, g []map[string]any) { delete(g[0], "packages") }, "groups[0].packages:"},
		{"empty benchmark expression", func(_ map[string]any, g []map[string]any) { g[1]["bench"] = "" }, "groups[1].bench:"},
		{"benchtime neither a duration nor a count", func(_ map[string]any, g []map[string]any) { g[0]["benchtime"] = "soon" }, "groups[0].benchtime:"},
		{"benchtime of no iterations", func(_ map[string]any, g []map[string]any) { g[1]["benchtime"] = "0x" }, "groups[1].benchtime:"},
		{"negative benchtime", func(_ map[string]any, g []map[string]any) { g[1]["benchtime"] = "-1s" }, "groups[1].benchtime:"},
		{"report-only group without rounds", func(_ map[string]any, g []map[string]any) { delete(g[1], "rounds") }, "groups[1].rounds:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), edited(t, c.edit))
			wantCode(t, o, 2)
			if len(f.requests) != 0 {
				t.Errorf("sampled %d times, want no sampling", len(f.requests))
			}
			if !strings.Contains(o.stderr, c.names) {
				t.Errorf("stderr %q does not say %s", o.stderr, c.names)
			}
		})
	}
}

// codeConfig is validConfig built in code.
func codeConfig() Config {
	return Config{Alpha: 0.05, MinSamples: 10, Groups: []Group{
		{Name: "cpu", Packages: []string{"p"}, Bench: ".", Rounds: 10, Thresholds: map[string]float64{"sec/op": 0.1}},
		{Name: "io", Packages: []string{"r"}, Bench: ".", Rounds: 3, ReportOnly: true},
	}}
}

func TestRunRejectsAnInvalidConfigBuiltInCode(t *testing.T) {
	t.Parallel()
	f := &fake{passes: []map[string]canned{{}}}
	wantCode(t, runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), func(o *Options) { o.Config = codeConfig() }), 0)

	for _, c := range []struct {
		name  string
		edit  func(c *Config)
		names string // what the error says after naming Options.Config
	}{
		{"invalid regular expression", func(c *Config) { c.Groups[0].Bench = "(" }, "groups[0].bench:"},
		{"judged group with nine rounds", func(c *Config) { c.Groups[0].Rounds = 9 }, "groups[0].rounds:"},
		{"negative threshold", func(c *Config) { c.Groups[0].Thresholds["sec/op"] = -0.1 }, `groups[0].thresholds["sec/op"]:`},
		{"threshold of NaN", func(c *Config) { c.Groups[0].Thresholds["sec/op"] = math.NaN() }, `groups[0].thresholds["sec/op"]:`},
		{"infinite threshold", func(c *Config) { c.Groups[0].Thresholds["sec/op"] = math.Inf(1) }, `groups[0].thresholds["sec/op"]:`},
		{"metric the gate cannot judge", func(c *Config) { c.Groups[0].Thresholds = map[string]float64{"ns/op": 0.1} }, `groups[0].thresholds["ns/op"]:`},
		{"neither thresholds nor reportOnly", func(c *Config) { c.Groups[0].Thresholds = nil }, "groups[0].thresholds:"},
		{"thresholds in a report-only group", func(c *Config) { c.Groups[1].Thresholds = map[string]float64{"sec/op": 0.1} }, "groups[1].thresholds:"},
		{"alpha of 0", func(c *Config) { c.Alpha = 0 }, "alpha:"},
		{"alpha of 1", func(c *Config) { c.Alpha = 1 }, "alpha:"},
		{"alpha of NaN", func(c *Config) { c.Alpha = math.NaN() }, "alpha:"},
		{"no minSamples", func(c *Config) { c.MinSamples = 0 }, "minSamples:"},
		{"judged group with fewer rounds than minSamples", func(c *Config) { c.MinSamples = 12 }, "groups[0].rounds:"},
		{"no groups", func(c *Config) { c.Groups = nil }, "groups:"},
		{"group without packages", func(c *Config) { c.Groups[0].Packages = nil }, "groups[0].packages:"},
		{"empty benchmark expression", func(c *Config) { c.Groups[1].Bench = "" }, "groups[1].bench:"},
		{"benchtime neither a duration nor a count", func(c *Config) { c.Groups[0].Benchtime = "soon" }, "groups[0].benchtime:"},
		{"report-only group without rounds", func(c *Config) { c.Groups[1].Rounds = 0 }, "groups[1].rounds:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := codeConfig()
			c.edit(&cfg)
			f := &fake{passes: []map[string]canned{{}}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), fakeConfig(t), func(o *Options) { o.Config = cfg })
			if want := "benchgate: Options.Config: " + c.names; !strings.HasPrefix(o.stderr, want) {
				t.Errorf("stderr %q, want it to start %q", o.stderr, want)
			}
			wantNothingDone(t, f, o)
		})
	}
}

func TestDataAfterTheConfigurationExitsTwo(t *testing.T) {
	t.Parallel()
	for _, trailing := range []string{" this is not JSON", "\n{}", "}"} {
		path := filepath.Join(t.TempDir(), "gate.json")
		writeFile(t, path, validConfig+trailing)
		f := &fake{}
		o := runGate(t, f.run, t.TempDir(), t.TempDir(), path)
		if o.code != 2 || len(f.requests) != 0 || !strings.Contains(o.stderr, "after the configuration") {
			t.Errorf("with %q after the configuration: exit code %d, sampled %d times, stderr %q; want 2, no sampling and the data named",
				trailing, o.code, len(f.requests), o.stderr)
		}
	}
}

func TestPackagesMustStayInsideTheTrees(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, pkg string
		accepted  bool
	}{
		{"absolute", filepath.Join(t.TempDir(), "p"), false},
		{"parent", "..", false},
		{"leaving through the parent", "../p", false},
		{"leaving below a level", "p/../../q", false},
		{"empty", "", false},
		{"root", ".", true},
		{"nested", "p/q/r", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{passes: []map[string]canned{{}}}
			o := runGate(t, f.run, t.TempDir(), t.TempDir(), edited(t, func(_ map[string]any, g []map[string]any) { g[0]["packages"] = []any{"p", c.pkg} }))
			if c.accepted {
				wantCode(t, o, 0)
				if len(f.requests) != 1 || !slices.Equal(f.requests[0].Groups[0].Packages, []string{"p", c.pkg}) {
					t.Errorf("requests %+v, want group cpu sampled with packages p and %q", f.requests, c.pkg)
				}
				return
			}
			wantCode(t, o, 2)
			if want := fmt.Sprintf("group %q, package %q", "cpu", c.pkg); !strings.Contains(o.stderr, want) {
				t.Errorf("stderr %q does not name %s", o.stderr, want)
			}
			if len(f.requests) != 0 {
				t.Errorf("sampled %d times, want no build and no sampling", len(f.requests))
			}
			filepath.WalkDir(o.out, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					t.Errorf("output directory holds %s, want no samples", path)
				}
				return nil
			})
		})
	}
}
