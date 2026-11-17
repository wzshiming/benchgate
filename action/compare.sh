#!/usr/bin/env bash
# Run in the head checkout after event.sh: name the base to check out and whether to measure identical test binaries.
set -euo pipefail

fail() {
	printf '::error::%s\n' "$1" >&2
	exit 1
}

if [[ -n $INPUT_BASE ]]; then
	base=$INPUT_BASE
else
	base=$(git rev-parse --verify --quiet 'HEAD^1^{commit}') ||
		fail "cannot resolve the base: the head checkout lacks the first parent of the merge commit"
fi
printf 'base=%s\nmeasure-identical=\n' "$base" >>"$GITHUB_OUTPUT"
