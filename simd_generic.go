//go:build !(goexperiment.simd && amd64)

package stilus

// SIMD reports whether vectorized span kernels are in use. They require
// GOEXPERIMENT=simd (Go 1.26+) on amd64 with AVX2.
func SIMD() bool { return false }

func runOver(d []uint32, s uint32)                           { runOverScalar(d, s) }
func covOpaque(d []uint32, cov []uint8, c uint32, cx uint64) { covOpaqueScalar(d, cov, c, cx) }
func covOver(d []uint32, cov []uint8, cx uint64)             { covOverScalar(d, cov, cx) }
func spanOver(d, s []uint32, k, a uint32)                    { spanOverScalar(d, s, k, a) }
func spanOverCov(d, s []uint32, k uint32, cov []uint8)       { spanOverCovScalar(d, s, k, cov) }
