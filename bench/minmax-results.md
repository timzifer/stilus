# Built-in min/max benchmark — 2026-09-30

Environment: Apple M4, darwin/arm64, Go 1.26.4, GOMAXPROCS=1. The module still declares Go 1.24; Go 1.24 itself was not available locally and was not tested. AMD64 and WebAssembly were cross-compiled, not executed.

Before/after binaries were compiled with the same toolchain and benchmark sources. The before binary uses the original production code; the after binary includes the optimized production code and NaN compatibility fallbacks. Six samples per benchmark, 200 ms per sample, alternating before/after order each round. Scene measurements use 150 dpi. Values below are medians; ranges show minimum–maximum across all six samples. All measured benchmarks reported 0 B/op and 0 allocs/op.

## Results

| Benchmark | Before median | After median | Time change | Before range | After range |
|---|---:|---:|---:|---:|---:|
| Scenes/hatch-2000-hairline | 22.38 ms | 22.19 ms | -0.82% | 22.05 ms–22.89 ms | 21.42 ms–24.79 ms |
| Scenes/hatch-2000-0.35mm | 26.78 ms | 26.43 ms | -1.31% | 25.82 ms–27.07 ms | 25.15 ms–29.82 ms |
| Scenes/short-20000-0.35mm | 15.47 ms | 14.45 ms | -6.61% | 15.02 ms–17.01 ms | 14.35 ms–16.54 ms |
| Scenes/short-20000-hairline | 13.25 ms | 12.01 ms | -9.33% | 12.93 ms–14.30 ms | 11.88 ms–14.55 ms |
| Scenes/glyphs-30000-uncached | 31.86 ms | 24.93 ms | -21.74% | 31.50 ms–32.67 ms | 24.69 ms–25.46 ms |
| Scenes/mixed-drawing | 20.63 ms | 20.40 ms | -1.11% | 20.20 ms–23.69 ms | 20.17 ms–20.79 ms |
| Scenes/contours-3000 | 34.49 ms | 35.07 ms | +1.67% | 33.39 ms–36.39 ms | 32.24 ms–40.14 ms |
| Scenes/hatch-2000-maskclip | 28.01 ms | 28.28 ms | +0.97% | 27.82 ms–28.09 ms | 27.75 ms–29.73 ms |
| RectangleFill | 670.90 ns | 639.65 ns | -4.66% | 665.60 ns–696.50 ns | 629.50 ns–677.40 ns |
| BorderStroke | 300.130 µs | 300.734 µs | +0.20% | 297.770 µs–313.306 µs | 295.470 µs–312.028 µs |
| BorderFrame | 92.701 µs | 91.796 µs | -0.98% | 91.808 µs–96.374 µs | 90.606 µs–94.539 µs |
| PathBounds/2 | 12.46 ns | 2.33 ns | -81.29% | 12.23 ns–13.03 ns | 2.23 ns–2.46 ns |
| PathBounds/4 | 25.84 ns | 3.09 ns | -88.03% | 25.28 ns–26.16 ns | 2.97 ns–3.19 ns |
| PathBounds/1024 | 7.919 µs | 491.85 ns | -93.79% | 7.794 µs–8.052 µs | 483.70 ns–497.30 ns |

Glyph rendering and short strokes benefit most. Long hatch, contour, mask-clip and border scenes have changes small relative to the observed sample spread; these measurements do not establish a gain or regression for those scenes. No statistical significance test was performed. The Bounds microbenchmarks measure the real Path.Bounds method, including the NaN fallback check.

## fill32 comparison

| Pixels | Existing copy-doubling | Simple loop | Loop / doubling |
|---|---:|---:|---:|
| 16 | 5.42 ns | 5.27 ns | 0.97× |
| 64 | 8.90 ns | 16.33 ns | 1.83× |
| 1024 | 43.73 ns | 243.65 ns | 5.57× |
| 8192 | 306.25 ns | 1.865 µs | 6.09× |

fill32 is unchanged. Small spans already use a loop; copy-doubling remains substantially faster for long spans.

## Verification

- `go test ./...`: passed, including scene/rendering regressions and zero-allocation tests.
- `go test -race ./...`: passed.
- `go vet ./...`: passed.
- Added explicit regression cases for empty/finite bounds, signed zero, NaN and both infinities in either order, rectangle intersections and overflowing finite transforms. These cases passed against both the before and after implementations.
- `FuzzFill`: passed (10 s requested; approximately 11 s elapsed, 227,793 executions).
- `FuzzStroke`: passed (10 s requested; approximately 11 s elapsed, 5,289 executions).
- `GOOS=linux GOARCH=amd64 go test -c`: compiled.
- `GOOS=js GOARCH=wasm go test -c`: compiled.
- `gofmt` and `git diff --check`: clean.

## Reproduction

Run with the same Go version and environment for each production-code revision:

```sh
go test -run '^$' -bench '^BenchmarkScenes$/.*/^150dpi$' -benchtime=200ms -benchmem -cpu=1 -count=6 .
go test -run '^$' -bench '^Benchmark(PathBounds|Fill32|BorderStroke|BorderFrame|RectangleFill)$' -benchtime=200ms -benchmem -cpu=1 -count=6 .
```

For stronger control of drift, build both revisions with `go test -c` and alternate the binaries one sample at a time, reversing order each round as done here.

Local raw logs: [before](/tmp/stilus-bounds.upmCuJ/before.txt), [after](/tmp/stilus-bounds.upmCuJ/after.txt).
