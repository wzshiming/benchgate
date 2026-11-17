package action

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMerge stands for the merge commit in tests that check nothing out.
const fakeMerge = "0123456789abcdef0123456789abcdef01234567"

// pullRequestEvent is the environment of the Action's steps on pull request 1 of owner/repo, whose merge commit is merge, with only the input config given.
func pullRequestEvent(merge string) map[string]string {
	return map[string]string{
		"GITHUB_EVENT_NAME":       "pull_request",
		"GITHUB_SHA":              merge,
		"GITHUB_REPOSITORY":       "owner/repo",
		"GITHUB_WORKFLOW_REF":     "owner/repo/.github/workflows/gate.yml@refs/pull/1/merge",
		"INPUT_BASE":              "",
		"INPUT_HEAD":              "",
		"INPUT_CONFIG":            "test/benchgate.json",
		"INPUT_MEASURE_IDENTICAL": "",
	}
}

// environ is this process's environment without GitHub's variables, its git kept from the machine's configuration.
func environ() []string {
	env := []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=benchgate", "GIT_AUTHOR_EMAIL=benchgate@example.com",
		"GIT_COMMITTER_NAME=benchgate", "GIT_COMMITTER_EMAIL=benchgate@example.com",
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "GITHUB_") {
			env = append(env, kv)
		}
	}
	return env
}

// step is what a script run as a step of the Action left: its exit code, its log and its outputs.
type step struct {
	code    int
	log     string
	outputs map[string]string
}

// run runs script in dir with env added to the environment, as a step of the Action runs it.
func run(t *testing.T, script, dir string, env map[string]string) step {
	t.Helper()
	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(output, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	cmd.Dir, cmd.Env = dir, append(environ(), "GITHUB_OUTPUT="+output)
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	log, err := cmd.CombinedOutput()
	if err != nil && cmd.ProcessState == nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{}
	for line := range strings.Lines(string(written)) {
		name, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		if !ok {
			t.Fatalf("%s wrote the output line %q, want name=value", script, line)
		}
		outputs[name] = value
	}
	return step{cmd.ProcessState.ExitCode(), string(log), outputs}
}

// wantFailure fails t unless s exited 1 without outputs, with an error annotation containing text.
func wantFailure(t *testing.T, s step, text string) {
	t.Helper()
	annotated := false
	for line := range strings.Lines(s.log) {
		annotated = annotated || strings.HasPrefix(line, "::error::") && strings.Contains(line, text)
	}
	if s.code != 1 || !annotated || len(s.outputs) != 0 {
		t.Fatalf("exited %d with outputs %q, want 1 without outputs and an error annotation containing %q\n%s", s.code, s.outputs, text, s.log)
	}
}

func TestPullRequestTargetIsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, base, head string }{
		{"without refs", "", ""},
		{"with base and head", "master", "pr"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := pullRequestEvent(fakeMerge)
			env["GITHUB_EVENT_NAME"], env["INPUT_BASE"], env["INPUT_HEAD"] = "pull_request_target", c.base, c.head
			wantFailure(t, run(t, "event.sh", t.TempDir(), env), "pull_request_target")
		})
	}
}

func TestEventWithoutBothRefsFails(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ event, base, head string }{
		{"push", "", ""},
		{"workflow_dispatch", "master", ""},
		{"schedule", "", "pr"},
		{"pull_request", "master", ""},
	} {
		t.Run(c.event+" base="+c.base+" head="+c.head, func(t *testing.T) {
			t.Parallel()
			env := pullRequestEvent(fakeMerge)
			env["GITHUB_EVENT_NAME"], env["INPUT_BASE"], env["INPUT_HEAD"] = c.event, c.base, c.head
			wantFailure(t, run(t, "event.sh", t.TempDir(), env), "base and head")
		})
	}
}
