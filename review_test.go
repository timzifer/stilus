package stilus

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// Regression tests for review findings.

// 1. Overlapping parts of one stroke must not be composited twice where a
// clip reduces coverage.
func TestStrokeOverlapUnderClip(t *testing.T) {
	var p Path
	p.MoveTo(10, 2)
	p.LineTo(10, 40)
	p.MoveTo(10, 2) // identical second subpath
	p.LineTo(10, 40)
	st := &StrokeStyle{Width: 6}
	for _, clip := range []func(c *Canvas){
		func(c *Canvas) { c.ClipRect(Rect{X0: 0, Y0: 0, X1: 9.5, Y1: 50}, Identity) }, // half pixel column
		func(c *Canvas) {
			var e Path
			e.Ellipse(9.5, 20, 6.3, 30)
			c.ClipPath(&e, Identity, NonZero)
		},
	} {
		img := image.NewRGBA(image.Rect(0, 0, 32, 48))
		c := NewCanvas(img)
		clip(c)
		c.Stroke(&p, Identity, st, &Paint{Color: color.RGBA{0, 0, 0, 255}})
		c.PopClip()

		ref := image.NewRGBA(img.Rect)
		c.Reset(ref, ref.Rect)
		clip(c)
		var once Path
		once.MoveTo(10, 2)
		once.LineTo(10, 40)
		c.Stroke(&once, Identity, st, &Paint{Color: color.RGBA{0, 0, 0, 255}})
		if mean, max := diff(alphaOfRGBA(img), alphaOfRGBA(ref)); max > 1 {
			t.Errorf("overlap under clip: mean %.3f max %d", mean, max)
		}
	}
}

// 2. Rectangle clips intersect geometrically.
func TestRectClipGeometric(t *testing.T) {
	cov := func(rs ...Rect) uint8 {
		img := image.NewRGBA(image.Rect(0, 0, 20, 4))
		c := NewCanvas(img)
		for _, r := range rs {
			c.ClipRect(r, Identity)
		}
		var p Path
		p.Rect(0, 0, 20, 4)
		c.Fill(&p, Identity, NonZero, &Paint{Color: color.RGBA{0, 0, 0, 255}})
		return img.RGBAAt(10, 1).A
	}
	half := Rect{X0: 0, Y0: 0, X1: 10.5, Y1: 4}
	if a := cov(half, half); a < 127 || a > 128 {
		t.Errorf("same half-pixel clip twice: alpha %d, want 128", a)
	}
	if a := cov(Rect{X0: 10.1, Y0: 0, X1: 10.4, Y1: 4}, Rect{X0: 10.6, Y0: 0, X1: 10.9, Y1: 4}); a != 0 {
		t.Errorf("disjoint clips: alpha %d, want 0", a)
	}
	if a := cov(Rect{X0: 10.1, Y0: 0, X1: 10.9, Y1: 4}, Rect{X0: 10.5, Y0: 0, X1: 20, Y1: 4}); a < 101 || a > 103 {
		t.Errorf("overlapping clips: alpha %d, want 102 (0.4 px)", a)
	}
}

// 3. Odd dash arrays repeat with on and off swapped.
func TestDashPhaseOdd(t *testing.T) {
	var p Path
	p.MoveTo(0, 5)
	p.LineTo(12, 5)
	clip := image.Rect(0, 0, 12, 10)
	st := &StrokeStyle{Width: 2, Dash: []float64{3}, DashPhase: 3}
	img := strokeMask(&p, Identity, st, clip)
	// Phase 3 into [3 on, 3 off, 3 on, …]: off 0–3, on 3–6, off 6–9, on 9–12.
	for x, want := range []uint8{0, 0, 0, 255, 255, 255, 0, 0, 0, 255, 255, 255} {
		if a := img.AlphaAt(x, 5).A; a != want {
			t.Fatalf("x=%d: %d, want %d", x, a, want)
		}
	}
}

// 4. A dash running across the start of a closed subpath keeps the join.
func TestDashClosedJoin(t *testing.T) {
	var sq Path
	sq.Rect(10, 10, 20, 20) // perimeter 80
	clip := image.Rect(0, 0, 40, 40)
	solid := strokeMask(&sq, Identity, &StrokeStyle{Width: 4, MiterLimit: 10}, clip)
	dashed := strokeMask(&sq, Identity, &StrokeStyle{Width: 4, MiterLimit: 10, Dash: []float64{100, 10}}, clip)
	if mean, max := diff(solid, dashed); max > 0 {
		t.Errorf("whole perimeter in one dash differs from solid: mean %.3f max %d", mean, max)
	}
	// A dash crossing the start: joined there like the solid stroke.
	dashed = strokeMask(&sq, Identity, &StrokeStyle{Width: 4, MiterLimit: 10, Dash: []float64{70, 5}, DashPhase: 5}, clip)
	if a := dashed.AlphaAt(9, 9).A; a != 255 {
		t.Errorf("outer miter corner at the start point: alpha %d, want 255", a)
	}
}

// 5. Reused clip masks with spans at negative x.
func TestClipMaskNegativeX(t *testing.T) {
	// Wide enough (> narrowCells) that the two spans of a row are emitted
	// separately, leaving a gap the mask writer must clear.
	img := image.NewRGBA(image.Rect(-400, 0, 0, 10))
	c := NewCanvas(img)
	var a, b, fill Path
	// A non-rectangular clip (a mask) that fills the rows completely.
	a.MoveTo(-400, -1)
	a.LineTo(0, -1)
	a.LineTo(1, 11)
	a.LineTo(-401, 11)
	a.Close()
	b.Rect(-380, 0, 4, 10) // two separate spans per row, with a gap
	b.Rect(-10, 0, 4, 10)
	fill.Rect(-400, 0, 400, 10)
	c.ClipPath(&a, Identity, NonZero)
	c.PopClip()
	c.ClipPath(&b, Identity, NonZero) // reuses the mask of the first clip
	c.Fill(&fill, Identity, NonZero, &Paint{Color: color.RGBA{0, 0, 0, 255}})
	if a := img.RGBAAt(-200, 5).A; a != 0 {
		t.Errorf("gap between spans painted: alpha %d", a)
	}
	if a := img.RGBAAt(-378, 5).A; a != 255 {
		t.Errorf("inside first span: alpha %d", a)
	}
}

// 6. The clip stack stops growing at MaxClipDepth; PopClip stays balanced.
func TestClipDepthBounded(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	c := NewCanvas(img)
	c.MaxClipDepth = 4
	for i := 0; i < 1000; i++ {
		c.ClipRect(Rect{X0: 0, Y0: 0, X1: 8, Y1: 8}, Identity)
	}
	if len(c.stack) > 4 || c.ClipDepth() != 1000 || c.Err() != ErrClipDepth {
		t.Fatalf("stack %d, depth %d, err %v", len(c.stack), c.ClipDepth(), c.Err())
	}
	var p Path
	p.Rect(0, 0, 8, 8)
	c.Fill(&p, Identity, NonZero, &Paint{Color: color.RGBA{0, 0, 0, 255}})
	if img.RGBAAt(4, 4).A != 0 {
		t.Fatal("drawing inside an overflowed clip stack must be clipped away")
	}
	for i := 0; i < 1000; i++ {
		c.PopClip()
	}
	c.Fill(&p, Identity, NonZero, &Paint{Color: color.RGBA{0, 0, 0, 255}})
	if c.ClipDepth() != 0 || img.RGBAAt(4, 4).A != 255 {
		t.Fatalf("after popping: depth %d, alpha %d", c.ClipDepth(), img.RGBAAt(4, 4).A)
	}
	_ = math.Pi
}
