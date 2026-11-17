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
		workflow_ref='^([^/]+/[^/]+)/(\.github/workflows/.+)@'
		[[ $GITHUB_WORKFLOW_REF =~ $workflow_ref ]] ||
			fail "cannot tell the calling workflow from GITHUB_WORKFLOW_REF $GITHUB_WORKFLOW_REF"
		repository=${BASH_REMATCH[1]} workflow=${BASH_REMATCH[2]}
		paths=("$INPUT_CONFIG")
		if [[ $repository == "$GITHUB_REPOSITORY" ]]; then
			paths+=("$workflow")
		else
			printf 'Only %q can force measurement: the calling workflow %s/%s is in another repository, which the pull request cannot change.\n' "$INPUT_CONFIG" "$repository" "$workflow"
		fi
		changed=$(git diff-tree -r --name-only "$base" HEAD -- "${paths[@]}") ||
			fail "cannot tell whether the pull request changes$(printf ' %q' "${paths[@]}")"
		if [[ -n $changed ]]; then
			measure=true
			printf 'Measuring identical test binaries too, because the pull request changes:\n%s\n' "$changed"
		fi
	fi
fi
printf 'base=%s\nmeasure-identical=%s\n' "$base" "$measure" >>"$GITHUB_OUTPUT"
