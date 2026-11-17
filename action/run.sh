#!/usr/bin/env bash
# Run after both checkouts and the gate's build: gate the head tree against the base tree, each labelled by its commit.
set -euo pipefail

base=$(git -C "$BASE_TREE" rev-parse HEAD)
head=$(git -C "$HEAD_TREE" rev-parse HEAD)
exec "$BENCHGATE" -base "$BASE_TREE" -head "$HEAD_TREE" -base-label "$base" -head-label "$head" \
	-config "$HEAD_TREE/$INPUT_CONFIG" -out "$RESULTS/out"
