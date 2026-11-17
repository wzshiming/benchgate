# benchgate

`benchgate` benchmarks a head source tree against a base tree on one machine and
fails on a confirmed regression. It builds both trees' test binaries with the
`go` command on `PATH` before timing anything, skips packages whose two
binaries are identical, and runs the rest in alternating rounds: both sides
back to back, switching which goes first.

A row, one metric of one benchmark, regresses when both sides have `minSamples`
samples, the difference is significant at `alpha` (Mann-Whitney U test, as in
benchstat), and the head median exceeds the base median by more than the
threshold. Regressed rows are sampled again in fresh rounds and judged on those
samples alone; only a row that regresses again is confirmed. The gate exits 0
without a confirmed regression, 1 with one, and 2 with nothing to conclude: the
head fails to build or run, a fully measured group selects no benchmark, the
configuration or command line is wrong, or the run was interrupted. A package
the base lacks or fails to build or run is listed as not measured.

## Install and run

Install the gate, which needs Go 1.27 or newer, with
`go install github.com/wzshiming/benchgate/cmd/benchgate@latest`, or with
`go install ./cmd/benchgate` in a checkout of this repository. Then check out
the two commits as worktrees of your repository and gate the head against the
base; the output directory must be new or empty:

```sh
git worktree add --detach /tmp/base master
git worktree add --detach /tmp/head HEAD
benchgate -base /tmp/base -head /tmp/head -base-label master -head-label HEAD \
  -config /tmp/head/benchgate.json -out /tmp/gate
```

The labels name the trees in the report. `-groups a,b` runs only those groups;
`-measure-identical[=false]` overrides the configuration's `measureIdentical`.
The report goes to standard output and to `report.md` in the output directory,
the raw samples to `first/` and `recheck/` there, warnings and errors to
standard error. Run a built binary: `go run` would report exit code 2 as 1.

## Configuration

One JSON object, such as this repository's [benchgate.json](benchgate.json); an
unknown field is an error. `alpha` is the significance level, `minSamples` the
samples each side needs for a row to be judged, and `measureIdentical` measures
identical test binaries too. Each of `groups` has a `name`, which names its
sample files; `packages`, directories inside the trees, relative to their
roots; `bench` and `benchtime`, as for `go test`; `rounds`, the samples of each
row on each side, at least 10 and `minSamples` when judged; and `thresholds`,
from `sec/op`, `B/op` or `allocs/op` to the relative increase a regression must
exceed (0.1 for 10%), or `"reportOnly": true` to compare without judging.

## GitHub Action

One step gates a pull request on a Linux or macOS runner; the job needs no
checkout of its own and no permission beyond `contents: read`:

```yaml
on: pull_request
permissions:
  contents: read
jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: wzshiming/benchgate@master # or a full commit sha
        with:
          config: benchgate.json
```

`config` is the configuration's path in the head tree. The pull request's merge
commit is gated against its first parent, unless `base` and `head`, refs or
commits of the repository, are both given; other events need both. The Action
builds and runs whatever given refs hold: never pass an untrusted pull request's
refs in a workflow with secrets or a token that can write, as on `workflow_run`
or `issue_comment`. It refuses `pull_request_target`, which would run the pull
request's code with such a token. `measure-identical`, `true` or `false`, says
whether identical test binaries are measured; left empty, the configuration
says, but without `base` and `head` they are measured when the pull request
changes the configuration or the calling workflow. Both trees, checked out under
`.benchgate/`, are built with the Go, 1.21 or newer, named by the head tree's
`go-version-file` (default `go.mod`), and the gate from the Action's own source.
`artifact-name` defaults to `benchgate-<job>-<matrix index>-<run attempt>`.
[gate.yml](.github/workflows/gate.yml) gates this repository.

## When the check is red

The job summary is the report, the confirmed regressions with both samplings'
numbers first, or the gate's error if it concludes nothing; the artifact holds
the report, the raw samples and the gate's standard error. To reproduce the
run, follow the Reproduce section that ends the report on worktrees of the two
commits it names; it installs `@latest` because the Action builds the gate
without a version, so install the gate at the Action's commit instead. There is
no override: the check stays red, and a maintainer decides whether to merge.

## Platforms and Go API

The command and the Action support Linux and macOS. The module is also a Go
library: `Run` runs the gate as the command does, `LoadConfig` reads a
configuration, and the packages `sample` and `verdict` take the samples and
judge the rows. It is at v0 and untagged: its API may change in any commit.
