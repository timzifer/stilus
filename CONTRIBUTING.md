# Contributing to stilus

Thanks for your interest! Bug reports, benchmarks from real workloads and
pull requests are welcome.

## Before you start

For anything beyond a small fix, please open an issue first so we can agree
on the approach – especially for API changes.

## Ground rules

- The core module stays **dependency-free**, **cgo-free** and builds on all
  GOOS/GOARCH including `js/wasm`. Tooling with dependencies lives in the
  separate `bench` and `harness` modules.
- Drawing performs **zero allocations** after warm-up; the alloc gates in the
  tests must keep passing.
- Accuracy changes need a test against the supersampling reference
  (`ref_test.go`) or a regression test for the specific case.
- Performance changes need numbers: run the relevant benchmarks before and
  after with `-count 10` and compare them with
  [`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat).

## Workflow

1. Fork and create a branch from `main`.
2. Make your change with tests.
3. Run locally:
   ```sh
   gofmt -l .
   go vet ./...
   go test ./...
   ```
4. Open a pull request. `main` is protected; changes land through PRs with
   passing CI.

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).
