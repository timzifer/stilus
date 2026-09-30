package stilus

import (
	"math/rand"
	"slices"
	"testing"
)

// TestSpanKernels checks the dispatched (possibly vectorized) span kernels
// against the scalar reference, bit for bit.
func TestSpanKernels(t *testing.T) {
	t.Logf("SIMD kernels: %v", SIMD())
	rng := rand.New(rand.NewSource(3))
	for n := 0; n < 2000; n++ {
		l := rng.Intn(40)
		dst := make([]uint32, l)
		for i := range dst {
			a := uint8(rng.Intn(256))
			dst[i] = PackRGBA(rgba(uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a))
		}
		cov := make([]uint8, l)
		for i := range cov {
			switch rng.Intn(4) {
			case 0:
				cov[i] = 0
			case 1:
				cov[i] = 255
			default:
				cov[i] = uint8(rng.Intn(256))
			}
		}
		if rng.Intn(3) == 0 { // runs of full/empty blocks
			v := uint8(255 * rng.Intn(2))
			for i := range cov {
				cov[i] = v
			}
		}
		a := uint8(rng.Intn(256))
		col := rgba(uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a)
		opaque := col
		opaque.A = 255
		c, co := PackRGBA(col), PackRGBA(opaque)

		got, want := slices.Clone(dst), slices.Clone(dst)
		covOpaque(got, cov, co, expand(co))
		covOpaqueScalar(want, cov, co, expand(co))
		if !slices.Equal(got, want) {
			t.Fatalf("covOpaque mismatch\n got %x\nwant %x", got, want)
		}
		got, want = slices.Clone(dst), slices.Clone(dst)
		covOver(got, cov, expand(c))
		covOverScalar(want, cov, expand(c))
		if !slices.Equal(got, want) {
			t.Fatalf("covOver mismatch\n got %x\nwant %x", got, want)
		}
		got, want = slices.Clone(dst), slices.Clone(dst)
		runOver(got, c)
		runOverScalar(want, c)
		if !slices.Equal(got, want) {
			t.Fatalf("runOver mismatch\n got %x\nwant %x", got, want)
		}
	}
}
