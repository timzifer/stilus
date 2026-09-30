package stilus

import (
	"image"
	"testing"
)

// Regression tests for the fourth review round.

// 1. A dash boundary landing exactly on the start point of a closed
// subpath: the dash running through the start point keeps its join, and a
// dot there is drawn once.
func TestDashBoundaryAtClosedStart(t *testing.T) {
	// Perimeter 78 with pattern [6 2]: the last dash ends exactly at the
	// start point and continues into the first, so the corner is joined
	// like the solid stroke's.
	var sq Path
	sq.Rect(10, 10, 19, 20)
	clip := image.Rect(0, 0, 40, 40)
	solid := strokeMask(&sq, Identity, &StrokeStyle{Width: 4, MiterLimit: 10}, clip)
	dashed := strokeMask(&sq, Identity, &StrokeStyle{Width: 4, MiterLimit: 10, Dash: []float64{6, 2}}, clip)
	for y := 7; y < 14; y++ {
		for x := 7; x < 14; x++ {
			if got, want := dashed.AlphaAt(x, y).A, solid.AlphaAt(x, y).A; got != want {
				t.Fatalf("dash through the start corner: pixel %d,%d = %d, solid %d", x, y, got, want)
			}
		}
	}
	// The whole perimeter in one dash ending exactly at the start point.
	dashed = strokeMask(&sq, Identity, &StrokeStyle{Width: 4, MiterLimit: 10, Dash: []float64{78, 10}}, clip)
	if mean, max := diff(solid, dashed); max > 0 {
		t.Errorf("one dash over the whole perimeter differs from solid: mean %.3f max %d", mean, max)
	}

	// Dots at every corner and side centre of a 20×20 square: the dot at
	// the start point is drawn once, like the others.
	sq.Reset()
	sq.Rect(10, 10, 20, 20)
	dots := strokeMask(&sq, Identity, &StrokeStyle{Width: 4, Cap: RoundCap, Dash: []float64{0, 10}}, clip)
	for _, c := range [][2]int{{30, 10}, {30, 30}, {10, 30}} {
		for dy := -4; dy < 4; dy++ {
			for dx := -4; dx < 4; dx++ {
				if got, want := dots.AlphaAt(10+dx, 10+dy).A, dots.AlphaAt(c[0]+dx, c[1]+dy).A; got != want {
					t.Fatalf("dot at the start point: pixel %d,%d = %d, at corner %v %d", 10+dx, 10+dy, got, c, want)
				}
			}
		}
	}
}

// 2. A single-point open subpath (a trailing MoveTo) paints nothing; a
// single-point closed subpath, or segments leading back to the point,
// paint a dot with round caps.
func TestLoneMoveToPaintsNothing(t *testing.T) {
	clip := image.Rect(0, 0, 100, 60)
	for _, dash := range [][]float64{nil, {5, 3}} {
		for _, c := range []Cap{RoundCap, SquareCap, ButtCap} {
			st := &StrokeStyle{Width: 4, Cap: c, Dash: dash}
			var p Path
			p.MoveTo(10, 10)
			p.LineTo(50, 10)
			p.MoveTo(70, 30) // trailing MoveTo
			p.MoveTo(80, 40) // another one, then the end of the path
			img := strokeMask(&p, Identity, st, clip)
			for _, q := range [][2]int{{70, 30}, {80, 40}} {
				if a := img.AlphaAt(q[0], q[1]).A; a != 0 {
					t.Errorf("cap %d dash %v: lone MoveTo at %v painted alpha %d", c, dash, q, a)
				}
			}
			if a := img.AlphaAt(30, 10).A; a != 255 {
				t.Errorf("cap %d dash %v: the line before the lone MoveTo is missing", c, dash)
			}

			p.Reset()
			p.MoveTo(70, 30)
			p.Close() // single-point closed subpath
			p.MoveTo(80, 40)
			p.LineTo(80, 40) // coincident points
			img = strokeMask(&p, Identity, st, clip)
			want := uint8(255)
			if c == ButtCap {
				want = 0
			}
			for _, q := range [][2]int{{70, 30}, {80, 40}} {
				if a := img.AlphaAt(q[0], q[1]).A; a != want {
					t.Errorf("cap %d dash %v: degenerate subpath at %v painted alpha %d, want %d", c, dash, q, a, want)
				}
			}
		}
	}
}
