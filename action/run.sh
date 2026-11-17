#!/usr/bin/env bash
# Run after both checkouts and the gate's build: gate the head tree against the base tree and keep its output and errors in RESULTS.
set -euo pipefail

base=$(git -C "$BASE_TREE" rev-parse HEAD)
head=$(git -C "$HEAD_TREE" rev-parse HEAD)
mkdir -p "$RESULTS"
code=0
"$BENCHGATE" -base "$BASE_TREE" -head "$HEAD_TREE" -base-label "$base" -head-label "$head" \
	-config "$HEAD_TREE/$INPUT_CONFIG" -out "$RESULTS/out" 2>"$RESULTS/stderr.txt" || code=$?
cat "$RESULTS/stderr.txt" >&2
exit "$code"
