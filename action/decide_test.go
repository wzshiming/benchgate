package action

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeMerge stands for the merge commit in tests that check nothing out.
const fakeMerge = "0123456789abcdef0123456789abcdef01234567"

// start is what the first commit of every test repository holds.
var start = map[string]string{
	"test/benchgate.json":        "{}\n",
	".github/workflows/gate.yml": "on: pull_request\n",
	"a.go":                       "package a\n",
}

// changed is a content no file of start has.
const changed = "changed\n"

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

// git runs git in dir and returns its output without the final newline.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	var stderr strings.Builder
	cmd.Dir, cmd.Env, cmd.Stderr = dir, environ(), &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, &stderr)
	}
	return strings.TrimSuffix(string(out), "\n")
}

// commit writes files into the repository dir and commits them.
func commit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "change")
}

// pullRequest makes a repository whose branch pr writes prFiles, whose master then writes masterFiles, and whose refs/pull/1/merge merges pr into master as GitHub does; it returns the repository, the merge commit and the master commit it merges into.
func pullRequest(t *testing.T, prFiles, masterFiles map[string]string) (repo, merge, base string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q", "-b", "master")
	git(t, repo, "config", "uploadpack.allowReachableSHA1InWant", "true")
	commit(t, repo, start)
	git(t, repo, "checkout", "-q", "-b", "pr")
	commit(t, repo, prFiles)
	git(t, repo, "checkout", "-q", "master")
	if masterFiles != nil {
		commit(t, repo, masterFiles)
	}
	base = git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "--detach")
	git(t, repo, "merge", "-q", "--no-ff", "--no-edit", "pr")
	merge = git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-ref", "refs/pull/1/merge", merge)
	return repo, merge, base
}

// checkoutHead runs event.sh, which must accept env, then checks out the head it names from repo at depth, as actions/checkout does.
func checkoutHead(t *testing.T, repo string, env map[string]string, depth int) (head string, event step) {
	t.Helper()
	event = run(t, "event.sh", t.TempDir(), env)
	if event.code != 0 || event.outputs["head"] == "" {
		t.Fatalf("event.sh exited %d with outputs %q, want 0 naming the head\n%s", event.code, event.outputs, event.log)
	}
	return checkout(t, repo, event.outputs["head"], depth), event
}

// checkout checks out ref from repo at depth into a new directory, as actions/checkout does, and returns the directory.
func checkout(t *testing.T, repo, ref string, depth int) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "fetch", "-q", "--depth", strconv.Itoa(depth), "file://"+repo, ref)
	git(t, dir, "checkout", "-q", "--detach", "FETCH_HEAD")
	return dir
}

// step is what a script run as a step of the Action left: its exit code, its log, its outputs and its job summary.
type step struct {
	code    int
	log     string
	outputs map[string]string
	summary string
}

// run runs script in dir with env added to the environment, as a step of the Action runs it.
func run(t *testing.T, script, dir string, env map[string]string) step {
	t.Helper()
	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	output, summary := filepath.Join(t.TempDir(), "output"), filepath.Join(t.TempDir(), "summary")
	for _, file := range []string{output, summary} {
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(path)
	cmd.Dir, cmd.Env = dir, append(environ(), "GITHUB_OUTPUT="+output, "GITHUB_STEP_SUMMARY="+summary)
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
	summarized, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	return step{cmd.ProcessState.ExitCode(), string(log), outputs, string(summarized)}
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

// wantComparison fails t unless compare.sh exited 0 with the outputs base and measure-identical as given.
func wantComparison(t *testing.T, s step, base, measureIdentical string) {
	t.Helper()
	if s.code != 0 || s.outputs["base"] != base || s.outputs["measure-identical"] != measureIdentical {
		t.Fatalf("compare.sh exited %d with outputs %q, want 0 with base=%s measure-identical=%s\n%s", s.code, s.outputs, base, measureIdentical, s.log)
	}
}

func TestPullRequestChangingTheGateForcesMeasurement(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, path, other string }{
		{"configuration", "test/benchgate.json", ".github/workflows/gate.yml"},
		{"calling workflow", ".github/workflows/gate.yml", "test/benchgate.json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, merge, base := pullRequest(t, map[string]string{c.path: changed}, nil)
			head, _ := checkoutHead(t, repo, pullRequestEvent(merge), 2)
			compared := run(t, "compare.sh", head, pullRequestEvent(merge))
			wantComparison(t, compared, base, "true")
			if !strings.Contains(compared.log, c.path) || strings.Contains(compared.log, c.other) {
				t.Errorf("log %q, want it to name %s alone", compared.log, c.path)
			}
		})
	}
}

func TestPullRequestChangingOtherFilesForcesNothing(t *testing.T) {
	t.Parallel()
	repo, merge, base := pullRequest(t, map[string]string{"a.go": changed, ".github/workflows/other.yml": changed}, nil)
	head, event := checkoutHead(t, repo, pullRequestEvent(merge), 2)
	if event.outputs["head"] != merge {
		t.Errorf("event.sh named the head %s, want the merge commit %s", event.outputs["head"], merge)
	}
	wantComparison(t, run(t, "compare.sh", head, pullRequestEvent(merge)), base, "")
}

func TestConfigurationChangedOnlyOnTheBaseSideForcesNothing(t *testing.T) {
	t.Parallel()
	repo, merge, base := pullRequest(t, map[string]string{"a.go": changed}, map[string]string{"test/benchgate.json": changed, ".github/workflows/gate.yml": changed})
	head, _ := checkoutHead(t, repo, pullRequestEvent(merge), 2)
	wantComparison(t, run(t, "compare.sh", head, pullRequestEvent(merge)), base, "")
}

func TestPathspecCharactersSelectOnlyTheFileOfThatName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, config, workflow, changed, want string }{
		{"glob in the configuration, another file changed", "test/*.json", ".github/workflows/gate.yml", "test/benchgate.json", ""},
		{"magic in the configuration, that file changed", ":(exclude)test/benchgate.json", ".github/workflows/gate.yml", ":(exclude)test/benchgate.json", "true"},
		{"glob in the calling workflow, another workflow changed", "test/benchgate.json", ".github/workflows/*.yml", ".github/workflows/gate.yml", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, merge, base := pullRequest(t, map[string]string{c.changed: changed}, nil)
			env := pullRequestEvent(merge)
			env["INPUT_CONFIG"], env["GITHUB_WORKFLOW_REF"] = c.config, "owner/repo/"+c.workflow+"@refs/pull/1/merge"
			head, _ := checkoutHead(t, repo, env, 2)
			wantComparison(t, run(t, "compare.sh", head, env), base, c.want)
		})
	}
}

func TestGivenMeasureIdenticalWins(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, event, base, head, given, path string }{
		{"false on a pull request changing the configuration", "pull_request", "", "", "false", "test/benchgate.json"},
		{"true on a pull request changing other files", "pull_request", "", "", "true", "a.go"},
		{"true on given refs", "workflow_dispatch", "master", "pr", "true", "a.go"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, merge, base := pullRequest(t, map[string]string{c.path: changed}, nil)
			env := pullRequestEvent(merge)
			env["GITHUB_EVENT_NAME"], env["INPUT_BASE"], env["INPUT_HEAD"], env["INPUT_MEASURE_IDENTICAL"] = c.event, c.base, c.head, c.given
			if c.base != "" {
				base = c.base
			}
			head, _ := checkoutHead(t, repo, env, 2)
			wantComparison(t, run(t, "compare.sh", head, env), base, c.given)
		})
	}
}

func TestUnresolvableBaseFailsBeforeTheGate(t *testing.T) {
	t.Parallel()
	repo, merge, _ := pullRequest(t, map[string]string{"a.go": changed}, nil)
	head, _ := checkoutHead(t, repo, pullRequestEvent(merge), 1)
	wantFailure(t, run(t, "compare.sh", head, pullRequestEvent(merge)), "base")
}

func TestUndeterminableChangedPathsFail(t *testing.T) {
	t.Parallel()
	t.Run("base tree missing", func(t *testing.T) {
		t.Parallel()
		repo, merge, base := pullRequest(t, map[string]string{"a.go": changed}, nil)
		head, _ := checkoutHead(t, repo, pullRequestEvent(merge), 2)
		tree := git(t, head, "rev-parse", base+"^{tree}")
		if err := os.Remove(filepath.Join(head, ".git", "objects", tree[:2], tree[2:])); err != nil {
			t.Fatal(err)
		}
		wantFailure(t, run(t, "compare.sh", head, pullRequestEvent(merge)), "pull request changes")
	})
	for _, c := range []struct{ name, ref string }{
		{"workflow outside .github/workflows", "owner/repo/gate.yml@refs/pull/1/merge"},
		{"workflow without a repository", ".github/workflows/gate.yml@refs/pull/1/merge"},
		{"empty GITHUB_WORKFLOW_REF", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, merge, _ := pullRequest(t, map[string]string{"a.go": changed}, nil)
			env := pullRequestEvent(merge)
			env["GITHUB_WORKFLOW_REF"] = c.ref
			head, _ := checkoutHead(t, repo, env, 2)
			wantFailure(t, run(t, "compare.sh", head, env), "GITHUB_WORKFLOW_REF")
		})
	}
}

func TestOnlyTheConfigurationCountsForAWorkflowOfAnotherRepository(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, path, want string }{
		{"pull request changing a workflow of the same name", ".github/workflows/gate.yml", ""},
		{"pull request changing the configuration", "test/benchgate.json", "true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, merge, base := pullRequest(t, map[string]string{c.path: changed}, nil)
			env := pullRequestEvent(merge)
			env["GITHUB_WORKFLOW_REF"] = "other/repo/.github/workflows/gate.yml@refs/heads/master"
			head, _ := checkoutHead(t, repo, env, 2)
			compared := run(t, "compare.sh", head, env)
			wantComparison(t, compared, base, c.want)
			if !strings.Contains(compared.log, "Only test/benchgate.json can force measurement") || !strings.Contains(compared.log, "other/repo/.github/workflows/gate.yml") {
				t.Errorf("log %q, want it to say that only test/benchgate.json can force measurement and name the other repository's workflow", compared.log)
			}
		})
	}
}

func TestGivenRefsAreComparedAsGiven(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"workflow_dispatch", "pull_request"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			repo, merge, _ := pullRequest(t, map[string]string{"test/benchgate.json": changed}, nil)
			env := pullRequestEvent(merge)
			env["GITHUB_EVENT_NAME"], env["INPUT_BASE"], env["INPUT_HEAD"] = event, "master", "pr"
			head, accepted := checkoutHead(t, repo, env, 2)
			if accepted.outputs["head"] != "pr" {
				t.Errorf("event.sh named the head %s, want pr", accepted.outputs["head"])
			}
			wantComparison(t, run(t, "compare.sh", head, env), "master", "")
		})
	}
}

func TestMalformedInputIsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, input, value string }{
		{"base", "INPUT_BASE", "master branch"},
		{"head", "INPUT_HEAD", "pr\nbase=evil"},
		{"measure-identical", "INPUT_MEASURE_IDENTICAL", "yes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := pullRequestEvent(fakeMerge)
			env["GITHUB_EVENT_NAME"], env["INPUT_BASE"], env["INPUT_HEAD"] = "workflow_dispatch", "master", "pr"
			env[c.input] = c.value
			wantFailure(t, run(t, "event.sh", t.TempDir(), env), "input "+c.name)
		})
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
