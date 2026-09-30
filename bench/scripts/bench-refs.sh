#!/usr/bin/env bash
# Measures BenchmarkScenes at 150 dpi for each ref given, in one run on one
# machine, and writes the snapshot to a JSON file that benchchart renders.
#
#   bench/scripts/bench-refs.sh <out.json> <ref>|<label>=<ref>...
#
# A ref is labelled on the chart by its own name unless given as label=ref.
# Timings from different machines do not compare, so every ref is measured
# anew each time. The refs are measured round-robin, one run each per round,
# so that drift in the machine's speed spreads over all refs alike.
# BENCH_COUNT (rounds, default 5) and BENCH_TIME (default 1s) tune the runs.
set -euo pipefail

out=$(realpath -m "$1")
shift
root=$(git rev-parse --show-toplevel)
work=$(mktemp -d)
trap 'for wt in "$work"/wt-*; do git -C "$root" worktree remove --force "$wt" 2>/dev/null || true; done; rm -rf "$work"' EXIT

(cd "$root/bench" && go build -o "$work/benchchart" ./cmd/benchchart)

refs=()
labels=()
for arg in "$@"; do
	label=${arg%%=*}
	ref=${arg#*=}
	if ! git -C "$root" show "$ref:bench_test.go" 2>/dev/null | grep -q 'func BenchmarkScenes('; then
		echo "skip $ref: no BenchmarkScenes" >&2
		continue
	fi
	i=${#refs[@]}
	git -C "$root" worktree add -q --detach "$work/wt-$i" "$ref"
	echo "build $ref" >&2
	(cd "$work/wt-$i" && go test -c -o "$work/scenes-$i.test" .)
	refs+=("$ref")
	labels+=("$label")
done

for round in $(seq "${BENCH_COUNT:-5}"); do
	for i in "${!refs[@]}"; do
		echo "round $round: ${labels[$i]}" >&2
		# The test binary runs in its package directory, as go test would.
		(cd "$work/wt-$i" && "$work/scenes-$i.test" -test.run '^$' \
			-test.bench '^BenchmarkScenes$/./^150dpi$' \
			-test.count 1 -test.benchtime "${BENCH_TIME:-1s}") >>"$work/out-$i.txt"
	done
done

rm -f "$out"
for i in "${!refs[@]}"; do
	ref=${refs[$i]}
	"$work/benchchart" record -db "$out" -ref "${labels[$i]}" \
		-commit "$(git -C "$root" rev-parse "$ref^{commit}")" \
		-date "$(git -C "$root" log -1 --format=%cI "$ref")" <"$work/out-$i.txt"
done
