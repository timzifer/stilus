package stilus

import (
	"math"
	"testing"
)

func checkBounds(t *testing.T, got, want Rect) {
	t.Helper()
	g, w := [4]float64{got.X0, got.Y0, got.X1, got.Y1}, [4]float64{want.X0, want.Y0, want.X1, want.Y1}
	for i := range g {
		if math.IsNaN(w[i]) {
			if !math.IsNaN(g[i]) {
				t.Fatalf("bounds = %v, want %v", got, want)
			}
		} else if math.Float64bits(g[i]) != math.Float64bits(w[i]) {
			t.Fatalf("bounds = %v, want %v (including zero signs)", got, want)
		}
	}
}

func TestBoundsSpecialValues(t *testing.T) {
	nan, inf, negzero := math.NaN(), math.Inf(1), math.Copysign(0, -1)
	for _, tc := range []struct {
		name   string
		points []Point
		want   Rect
	}{
		{"empty", nil, Rect{}},
		{"finite", []Point{{3, -4}, {-2, 8}}, Rect{-2, -4, 3, 8}},
		{"signed zero", []Point{{float32(negzero), 0}, {0, float32(negzero)}}, Rect{negzero, negzero, 0, 0}},
		{"nan", []Point{{float32(nan), 1}, {2, 3}}, Rect{nan, 1, nan, 3}},
		// math.Min/Max let the matching infinity dominate NaN in either order.
		{"nan then infinities", []Point{{float32(nan), 1}, {float32(-inf), 2}, {float32(inf), 3}}, Rect{-inf, 1, inf, 3}},
		{"infinities then nan", []Point{{float32(-inf), 1}, {float32(inf), 2}, {float32(nan), 3}}, Rect{-inf, 1, inf, 3}},
		{"nan and negative infinity", []Point{{float32(nan), float32(nan)}, {float32(-inf), float32(-inf)}}, Rect{-inf, -inf, nan, nan}},
		{"nan and positive infinity", []Point{{float32(inf), float32(inf)}, {float32(nan), float32(nan)}}, Rect{nan, nan, inf, inf}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Path{Points: tc.points}
			checkBounds(t, p.Bounds(), tc.want)
		})
	}
}

func TestIntersectSpecialValues(t *testing.T) {
	nan, inf, negzero := math.NaN(), math.Inf(1), math.Copysign(0, -1)
	for _, tc := range []struct {
		name       string
		r, s, want Rect
	}{
		{"overlap", Rect{-2, -3, 5, 8}, Rect{1, 2, 7, 6}, Rect{1, 2, 5, 6}},
		{"disjoint", Rect{0, 0, 1, 1}, Rect{2, 3, 4, 5}, Rect{2, 3, 1, 1}},
		{"signed zero", Rect{negzero, negzero, 0, 0}, Rect{0, 0, negzero, negzero}, Rect{0, 0, negzero, negzero}},
		{"nan", Rect{nan, nan, nan, nan}, Rect{0, 0, 1, 1}, Rect{nan, nan, nan, nan}},
		{"infinity dominates nan", Rect{nan, nan, nan, nan}, Rect{inf, inf, -inf, -inf}, Rect{inf, inf, -inf, -inf}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBounds(t, tc.r.Intersect(tc.s), tc.want)
			checkBounds(t, tc.s.Intersect(tc.r), tc.want)
		})
	}
}

func TestTransformRectOverflowBounds(t *testing.T) {
	// Finite inputs can still produce NaN and both infinities at the corners.
	m := Matrix{math.MaxFloat64, 0, -math.MaxFloat64, 1, 0, 0}
	checkBounds(t, m.transformRect(Rect{0, 0, 2, 2}), Rect{math.Inf(-1), 0, math.Inf(1), 2})
}
