//go:build goexperiment.simd && amd64

package stilus

import (
	"fmt"
	"testing"
)

// BenchmarkSpanKernels compares scalar, AVX2 and AVX-512 span kernels per
// span length (ns per span; divide by the length for ns per pixel).
func BenchmarkSpanKernels(b *testing.B) {
	simd, w512 := useSIMD, use512
	defer func() { useSIMD, use512 = simd, w512 }()
	modes := []struct {
		name       string
		simd, w512 bool
	}{{"scalar", false, false}, {"avx2", simd, false}, {"avx512", simd, w512}}
	for _, n := range []int{4, 16, 64, 256, 1024} {
		d := make([]uint32, n)
		cov := make([]uint8, n)
		for i := range cov {
			cov[i] = uint8(37 + i*13)
		}
		c := PackRGBA(rgba(20, 40, 60, 255))
		t := PackRGBA(rgba(10, 20, 30, 128))
		for _, m := range modes {
			useSIMD, use512 = m.simd, m.w512
			minSIMDSpan = 4
			b.Run(fmt.Sprintf("covOpaque/%d/%s", n, m.name), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					covOpaque(d, cov, c, expand(c))
				}
			})
			b.Run(fmt.Sprintf("covOver/%d/%s", n, m.name), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					covOver(d, cov, expand(t))
				}
			})
			b.Run(fmt.Sprintf("runOver/%d/%s", n, m.name), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					runOver(d, t)
				}
			})
		}
	}
	minSIMDSpan = 16
}
