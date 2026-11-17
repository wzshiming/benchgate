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
if [[ -z $INPUT_BASE || -z $INPUT_HEAD ]]; then
	if [[ $GITHUB_EVENT_NAME != pull_request ]]; then
		fail "a $GITHUB_EVENT_NAME event needs both inputs base and head"
	elif [[ -n $INPUT_BASE || -n $INPUT_HEAD ]]; then
		fail "a pull_request event takes both inputs base and head, or neither"
	fi
fi
printf 'head=%s\n' "$GITHUB_SHA" >>"$GITHUB_OUTPUT"
