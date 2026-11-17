#!/usr/bin/env bash
# Run before any checkout: refuse what the gate must not run on and name the head to check out.
set -euo pipefail

fail() {
	printf '::error::%s\n' "$1" >&2
	exit 1
}

if [[ $GITHUB_EVENT_NAME == pull_request_target ]]; then
	fail "refusing pull_request_target: the gate would run the pull request's code with a token that can write"
fi
if [[ -n $INPUT_BASE && -n $INPUT_HEAD ]]; then
	git check-ref-format --allow-onelevel "$INPUT_BASE" 2>/dev/null ||
		fail "input base is $(printf %q "$INPUT_BASE"), want a ref or a commit id"
	git check-ref-format --allow-onelevel "$INPUT_HEAD" 2>/dev/null ||
		fail "input head is $(printf %q "$INPUT_HEAD"), want a ref or a commit id"
	head=$INPUT_HEAD
elif [[ $GITHUB_EVENT_NAME != pull_request ]]; then
	fail "a $GITHUB_EVENT_NAME event needs both inputs base and head"
elif [[ -n $INPUT_BASE || -n $INPUT_HEAD ]]; then
	fail "a pull_request event takes both inputs base and head, or neither"
else
	head=$GITHUB_SHA
fi
printf 'head=%s\n' "$head" >>"$GITHUB_OUTPUT"
