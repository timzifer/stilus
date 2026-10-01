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
		src := make([]uint32, l)
		for i := range src {
			a := uint8(rng.Intn(256))
			if rng.Intn(3) == 0 {
				a = 255
			}
			src[i] = PackRGBA(rgba(uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a))
		}
		for _, ka := range [][2]uint32{{255, 255}, {255, uint32(a)}, {uint32(a), 255}, {uint32(a), uint32(255 - a)}} {
			got, want = slices.Clone(dst), slices.Clone(dst)
			spanOver(got, src, ka[0], ka[1])
			spanOverScalar(want, src, ka[0], ka[1])
			if !slices.Equal(got, want) {
				t.Fatalf("spanOver k %d a %d mismatch\n got %x\nwant %x", ka[0], ka[1], got, want)
			}
			// The reference: the shaded span composited pixel by pixel.
			for i, v := range src {
				v = mul255(mul255(v, ka[0]), ka[1])
				want[i] = over(v, dst[i])
			}
			if !slices.Equal(got, want) {
				t.Fatalf("spanOver k %d a %d differs from over\n got %x\nwant %x", ka[0], ka[1], got, want)
			}
			got, want = slices.Clone(dst), slices.Clone(dst)
			spanOverCov(got, src, ka[0], cov)
			spanOverCovScalar(want, src, ka[0], cov)
			if !slices.Equal(got, want) {
				t.Fatalf("spanOverCov k %d mismatch\n got %x\nwant %x", ka[0], got, want)
			}
		}
	}
}
