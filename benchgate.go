// Package benchgate benchmarks a head source tree against a base tree on one machine and finds confirmed regressions.
package benchgate

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/wzshiming/benchgate/sample"
)

// sampler takes samples as sample.Run does.
type sampler func(context.Context, sample.Request) (sample.Result, error)

// Options say which trees a run of the gate compares, how, and where its output goes.
type Options struct {
	Base, Head           string // source tree roots
	BaseLabel, HeadLabel string // what the report calls the trees
	Config               Config
	Out                  string    // output directory, new or empty
	MeasureIdentical     *bool     // overrides Config.MeasureIdentical unless nil
	Groups               []string  // names of the only groups to run, empty for all
	Diagnostics          io.Writer // gets why a base failed and how many malformed base sample lines were left out; nil discards them
	Reproduce            string    // the report's Reproduce section, verbatim; empty leaves the section out
}

// Run runs the gate, warning on o.Diagnostics, and renders its report; regressed tells whether a regression is confirmed.
func Run(ctx context.Context, o Options) (report []byte, regressed bool, err error) {
	return o.gate(ctx, sample.Run)
}

// gate is Run sampling through sampleRun.
func (o Options) gate(ctx context.Context, sampleRun sampler) (report []byte, regressed bool, err error) {
	for _, f := range []struct{ name, value string }{
		{"Base", o.Base}, {"Head", o.Head}, {"BaseLabel", o.BaseLabel}, {"HeadLabel", o.HeadLabel}, {"Out", o.Out},
	} {
		if f.value == "" {
			return nil, false, fmt.Errorf("missing Options.%s", f.name)
		}
	}
	if err := o.Config.validate(); err != nil {
		return nil, false, fmt.Errorf("Options.Config: %w", err)
	}
	cfg := o.Config
	for _, g := range cfg.Groups {
		for _, p := range g.Packages {
			if !filepath.IsLocal(p) {
				return nil, false, fmt.Errorf("group %q, package %q: want a relative path inside the trees", g.Name, p)
			}
		}
	}
	if o.MeasureIdentical != nil {
		cfg.MeasureIdentical = *o.MeasureIdentical
	}
	if len(o.Groups) > 0 {
		cfg.Groups = slices.Clone(cfg.Groups) // only deletes in place, which would change the caller's Config
		if err := cfg.only(o.Groups); err != nil {
			return nil, false, err
		}
	}
	if entries, err := os.ReadDir(o.Out); err == nil && len(entries) > 0 {
		return nil, false, fmt.Errorf("Options.Out %s is not empty", o.Out)
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, false, err
	}
	res, err := gate(ctx, sampleRun, cfg, o.Base, o.Head, o.Out, cmp.Or(o.Diagnostics, io.Discard))
	if err != nil {
		return nil, false, err
	}
	confirmed, _ := res.split()
	report = render(o, cfg, res)
	if err := os.WriteFile(filepath.Join(o.Out, "report.md"), report, 0o644); err != nil {
		return nil, false, err
	}
	return report, len(confirmed) > 0, nil
}
