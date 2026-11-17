#!/usr/bin/env bash
# Run in the head checkout after event.sh: name the base to check out and whether to measure identical test binaries.
set -euo pipefail

fail() {
	printf '::error::%s\n' "$1" >&2
	exit 1
}

measure=$INPUT_MEASURE_IDENTICAL
if [[ -n $INPUT_BASE ]]; then
	base=$INPUT_BASE
else
	base=$(git rev-parse --verify --quiet 'HEAD^1^{commit}') ||
		fail "cannot resolve the base: the head checkout lacks the first parent of the merge commit"
	if [[ -z $measure ]]; then
		workflow=${GITHUB_WORKFLOW_REF#"$GITHUB_REPOSITORY"/}
		workflow=${workflow%@*}
		[[ $workflow == .github/workflows/* ]] ||
			fail "cannot tell the calling workflow of $GITHUB_REPOSITORY from GITHUB_WORKFLOW_REF $GITHUB_WORKFLOW_REF"
		changed=$(git diff-tree -r --name-only "$base" HEAD -- "$INPUT_CONFIG" "$workflow") ||
			fail "cannot tell whether the pull request changes $(printf %q "$INPUT_CONFIG") or $workflow"
		if [[ -n $changed ]]; then
			measure=true
			printf 'Measuring identical test binaries too, because the pull request changes:\n%s\n' "$changed"
		fi
	fi
fi
printf 'base=%s\nmeasure-identical=%s\n' "$base" "$measure" >>"$GITHUB_OUTPUT"
