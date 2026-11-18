#!/usr/bin/env bash
# Run after both checkouts and the gate's build: gate the head tree against the base tree, keep its output and errors in RESULTS, summarize and exit as it did.
set -euo pipefail

base=$(git -C "$BASE_TREE" rev-parse HEAD)
head=$(git -C "$HEAD_TREE" rev-parse HEAD)
mkdir -p "$RESULTS"
code=0
"$BENCHGATE" -base "$BASE_TREE" -head "$HEAD_TREE" -base-label "$base" -head-label "$head" \
	-config "$HEAD_TREE/$INPUT_CONFIG" -out "$RESULTS/out" ${MEASURE_IDENTICAL:+"-measure-identical=$MEASURE_IDENTICAL"} \
	2>"$RESULTS/stderr.txt" || code=$?
cat "$RESULTS/stderr.txt" >&2
report=$RESULTS/out/report.md
if ((code <= 1)); then
	cat "$report" >>"$GITHUB_STEP_SUMMARY"
else
	{
		printf '# Benchmark gate\n\n**FAIL**: nothing to conclude, the gate exited %d:\n\n' "$code"
		sed 's/^/    /' "$RESULTS/stderr.txt"
	} >>"$GITHUB_STEP_SUMMARY"
fi
exit "$code"
