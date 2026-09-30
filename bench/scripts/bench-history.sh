#!/usr/bin/env bash
# Runs BenchmarkScenes at 150 dpi for each commit given and records the
# results in a history file, which benchchart then renders.
#
#   bench/scripts/bench-history.sh <history.json> <commit>...
#
# BENCH_COUNT and BENCH_TIME tune the go test run (default 5 and 1s).
set -euo pipefail

db=$(realpath -m "$1")
shift
root=$(git rev-parse --show-toplevel)
tool=$(mktemp -d)/benchchart
(cd "$root/bench" && go build -o "$tool" ./cmd/benchchart)

for sha in "$@"; do
	if ! git -C "$root" show "$sha:bench_test.go" 2>/dev/null | grep -q 'func BenchmarkScenes('; then
		echo "skip $sha: no BenchmarkScenes"
		continue
	fi
	wt=$(mktemp -d)
	git -C "$root" worktree add -q --detach "$wt" "$sha"
	echo "bench $sha"
	(cd "$wt" && go test -run '^$' -bench '^BenchmarkScenes$/./^150dpi$' \
		-count "${BENCH_COUNT:-5}" -benchtime "${BENCH_TIME:-1s}" .) |
		tee /dev/stderr |
		"$tool" record -db "$db" -commit "$(git -C "$root" rev-parse "$sha")" \
			-subject "$(git -C "$root" log -1 --format=%s "$sha")" \
			-date "$(git -C "$root" log -1 --format=%cI "$sha")"
	git -C "$root" worktree remove --force "$wt"
done
