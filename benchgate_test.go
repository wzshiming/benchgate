package benchgate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wzshiming/benchgate"
)

// packageA has BenchmarkWork, which times Work, which starts with the given statement.
const packageA = `package a

var keep *[16]byte

func Work(n int) int {
	%s
	h := 0
	for i := range n {
		h = h*31 + i
	}
	return h
}
`

const packageATest = `package a

import "testing"

var sink int

func BenchmarkWork(b *testing.B) {
	for range b.N {
		sink = Work(1000)
	}
}
`

// tree writes a module holding package a, whose Work starts with statement, into a new directory.
func tree(t *testing.T, statement string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":      "module example.com/synth\n\ngo 1.22\n",
		"a/a.go":      fmt.Sprintf(packageA, statement),
		"a/a_test.go": packageATest,
	} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
	return dir
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

func TestExportedAPIConfirmsAnExtraAllocation(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "gate.json")
	writeFile(t, config, `{"alpha": 0.05, "minSamples": 10, "groups": [
	{"name": "cpu", "packages": ["a"], "bench": ".", "benchtime": "100x", "rounds": 10, "thresholds": {"allocs/op": 0.5}}
]}
`)
	cfg, err := benchgate.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics strings.Builder
	report, regressed, err := benchgate.Run(t.Context(), benchgate.Options{
		Base:        tree(t, ""),
		Head:        tree(t, "keep = new([16]byte)"),
		BaseLabel:   "base-commit",
		HeadLabel:   "head-commit",
		Config:      cfg,
		Out:         filepath.Join(t.TempDir(), "out"),
		Diagnostics: &diagnostics,
	})
	if err != nil {
		t.Fatalf("Run: %v\ndiagnostics:\n%s", err, &diagnostics)
	}
	if !regressed || !strings.HasPrefix(string(report), "# Benchmark gate: FAIL, 1 benchmark regressed\n") {
		t.Errorf("regressed %v, report:\n%s\nwant one benchmark regressed", regressed, report)
	}
}
