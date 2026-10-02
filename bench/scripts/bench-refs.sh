#!/usr/bin/env bash
# Measures BenchmarkScenes at 150 dpi for each ref given, in one run on one
# machine, and writes the snapshot to a JSON file that benchchart renders.
#
#   bench/scripts/bench-refs.sh <out.json> <ref>|<label>=<ref>...
#
# A ref is labelled on the chart by its own name unless given as label=ref.
# The ref @ is the working tree as it is, uncommitted changes included
# (labelled "working tree" unless given a label).
#
# Every ref is built with the scenes of the working tree (internal/scenes),
# not its own, so that a scene added now is charted over past releases too.
# Scene files that do not compile against a ref, because they use API it
# lacks, are left out of it, file by file: such a scene starts at the first
# release that can draw it. If the scene framework itself (scenes.go) does
# not compile against a ref, the ref is measured with its own scenes.
#
# Timings from different machines do not compare, so every ref is measured
# anew each time. The refs are measured round-robin, one run each per round,
# so that drift in the machine's speed spreads over all refs alike.
# BENCH_COUNT (rounds, default 5) and BENCH_TIME (default 1s) tune the runs.
set -euo pipefail

out=$(realpath -m "$1")
shift
root=$(git rev-parse --show-toplevel)
scenes="$root/internal/scenes"
# Where Go builds and runs its binaries, if set: some systems do not run
# executables from the system's temporary directory.
gotmp=$(go env GOTMPDIR)
work=$(mktemp -d ${gotmp:+-p "$gotmp"})
exe=$(go env GOEXE) # test binaries need it on Windows
trap 'for wt in "$work"/wt-*; do git -C "$root" worktree remove --force "$wt" 2>/dev/null || true; done; rm -rf "$work"' EXIT

(cd "$root/bench" && go build -o "$work/benchchart" ./cmd/benchchart)

# build compiles the scene benchmarks of the checkout $1 into $2 with the
# working tree's scenes, leaving out the scene files that do not compile.
build() {
	local dir=$1 bin=$2 ref=$3 log bad f
	mv "$dir/internal/scenes" "$dir/internal/scenes.own"
	mkdir "$dir/internal/scenes"
	cp "$scenes"/*.go "$dir/internal/scenes/"
	rm -f "$dir/internal/scenes"/*_test.go
	while ! log=$(cd "$dir" && go test -c -o "$bin" . 2>&1); do
		bad=$(printf '%s\n' "$log" | grep -oE 'internal[/\\]scenes[/\\][A-Za-z0-9_]+\.go' | sed 's#.*[/\\]##' | sort -u)
		if [ -z "$bad" ] || printf '%s\n' "$bad" | grep -qx scenes.go; then
			echo "$ref: the working tree's scenes do not build, using its own" >&2
			rm -rf "$dir/internal/scenes"
			mv "$dir/internal/scenes.own" "$dir/internal/scenes"
			(cd "$dir" && go test -c -o "$bin" .)
			return
		fi
		for f in $bad; do
			echo "$ref: without $f" >&2
			rm "$dir/internal/scenes/$f"
		done
	done
	rm -rf "$dir/internal/scenes.own"
}

refs=()
labels=()
dirs=()
for arg in "$@"; do
	label=${arg%%=*}
	ref=${arg#*=}
	i=${#refs[@]}
	if [ "$ref" = @ ]; then
		[ "$label" = @ ] && label="working tree"
		echo "build working tree" >&2
		(cd "$root" && go test -c -o "$work/scenes-$i.test$exe" .)
		dirs+=("$root")
	else
		if ! git -C "$root" show "$ref:bench_test.go" 2>/dev/null | grep -q 'func BenchmarkScenes('; then
			echo "skip $ref: no BenchmarkScenes" >&2
			continue
		fi
		git -C "$root" worktree add -q --detach "$work/wt-$i" "$ref"
		echo "build $ref" >&2
		build "$work/wt-$i" "$work/scenes-$i.test$exe" "$ref"
		dirs+=("$work/wt-$i")
	fi
	refs+=("$ref")
	labels+=("$label")
done

for round in $(seq "${BENCH_COUNT:-5}"); do
	for i in "${!refs[@]}"; do
		echo "round $round: ${labels[$i]}" >&2
		# The test binary runs in its package directory, as go test would.
		(cd "${dirs[$i]}" && "$work/scenes-$i.test$exe" -test.run '^$' \
			-test.bench '^BenchmarkScenes$/./^150dpi$' \
			-test.count 1 -test.benchtime "${BENCH_TIME:-1s}") >>"$work/out-$i.txt"
	done
done

rm -f "$out"
for i in "${!refs[@]}"; do
	ref=${refs[$i]}
	if [ "$ref" = @ ]; then
		commit=$(git -C "$root" rev-parse HEAD)
		date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	else
		commit=$(git -C "$root" rev-parse "$ref^{commit}")
		date=$(git -C "$root" log -1 --format=%cI "$ref")
	fi
	"$work/benchchart" record -db "$out" -ref "${labels[$i]}" \
		-commit "$commit" -date "$date" <"$work/out-$i.txt"
done
