package stilus

import (
	"image"
	"image/color"
	"testing"
)

// Regression tests for the second review round.

var black = &Paint{Color: color.RGBA{0, 0, 0, 255}}

// 1. Pixels summed in the accumulator at a fractional clip border must wind
// like the outline, also under mirroring transforms.
func TestInjectOrientation(t *testing.T) {
	type pts [4]float32
	render := func(m Matrix, a, b pts, w float64) *image.Alpha {
		img := image.NewRGBA(image.Rect(0, 0, 40, 100))
		c := NewCanvas(img)
		c.ClipRect(Rect{X0: 10.5, Y0: 0, X1: 40, Y1: 100}, Identity)
		var p Path
		p.MoveTo(a[0], a[1])
		p.LineTo(a[2], a[3])
		p.MoveTo(b[0], b[1])
		p.LineTo(b[2], b[3])
		c.Stroke(&p, m, &StrokeStyle{Width: w}, black)
		return alphaOfRGBA(img)
	}
	// Device geometry: vertical line x=11, y 5..95; horizontal y=50, x 5..20.
	for _, w := range []float64{4, 0} {
		want := render(Identity, pts{11, 5, 11, 95}, pts{5, 50, 20, 50}, w)
		for name, tc := range map[string]struct {
			m    Matrix
			a, b pts
		}{
			"mirror x": {Scale(-1, 1), pts{-11, 5, -11, 95}, pts{-5, 50, -20, 50}},
			"mirror y": {Scale(1, -1), pts{11, -5, 11, -95}, pts{5, -50, 20, -50}},
		} {
			got := render(tc.m, tc.a, tc.b, w)
			if mean, max := diff(got, want); max > 1 {
				t.Errorf("%s, width %g: mean %.3f max %d; pixel (10,50) %d want %d",
					name, w, mean, max, got.AlphaAt(10, 50).A, want.AlphaAt(10, 50).A)
			}
		}
		if w == 4 {
			if a := want.AlphaAt(10, 50).A; a < 127 || a > 128 {
				t.Errorf("reference pixel (10,50) = %d, want 128", a)
			}
		}
	}
}

// 2. Whether dashes take the analytic path must not depend on the previous
// stroke.
func TestDashFastIndependentOfPrevious(t *testing.T) {
	var p Path
	// Vertical, so the pieces overlap in analytic rows; edges at .5 so the
	// overlap meets in partial pixels.
	p.MoveTo(20.5, 5)
	p.LineTo(20.5, 95)
	st := &StrokeStyle{Width: 10, Cap: SquareCap, Dash: []float64{20, 4}}
	fresh := image.NewRGBA(image.Rect(0, 0, 40, 100))
	NewCanvas(fresh).Stroke(&p, Identity, st, black)

	img := image.NewRGBA(fresh.Rect)
	c := NewCanvas(img)
	var h Path
	h.MoveTo(0, 0)
	h.LineTo(1, 0)
	c.Stroke(&h, Identity, &StrokeStyle{Width: 0}, black) // hairline first
	for i := range img.Pix {
		img.Pix[i] = 0
	}
	c.Stroke(&p, Identity, st, black)
	if mean, max := diff(alphaOfRGBA(img), alphaOfRGBA(fresh)); max > 0 {
		t.Errorf("after a hairline: mean %.3f max %d", mean, max)
	}
}

// 3. Truncated describes the last path, also after Fill.
func TestTruncatedAfterFill(t *testing.T) {
	r := NewRasterizer(image.Rect(0, 0, 64, 64))
	r.MaxEdges = 8
	img := image.NewAlpha(r.Clip())
	var e, tri Path
	e.Ellipse(32, 32, 20, 20)
	tri.MoveTo(5, 5)
	tri.LineTo(30, 10)
	tri.LineTo(10, 30)
	tri.Close()
	r.Fill(&e, Identity, NonZero, &MaskBlitter{img})
	if !r.Truncated() {
		t.Fatal("ellipse beyond the edge budget: Truncated() = false")
	}
	r.Fill(&tri, Identity, NonZero, &MaskBlitter{img})
	if r.Truncated() {
		t.Fatal("triangle within budget: Truncated() = true")
	}
	// Same through AddPath/Rasterize without Reset in between.
	r.AddPath(&e, Identity)
	r.Rasterize(NonZero, &MaskBlitter{img})
	if !r.Truncated() {
		t.Fatal("AddPath/Rasterize: Truncated() = false")
	}
	r.AddPath(&tri, Identity)
	r.Rasterize(NonZero, &MaskBlitter{img})
	if r.Truncated() {
		t.Fatal("next path via AddPath: Truncated() = true")
	}
}

// Analytic pixels injected at fractional clip borders must work at negative
// x (targets with a negative origin, tiles). Each case is compared with the
// same scene shifted by +dx into positive coordinates.
func TestBorderInjectNegativeX(t *testing.T) {
	render := func(dx float32, target image.Rectangle, clip Rect, x float32) *image.Alpha {
		img := image.NewRGBA(target.Add(image.Pt(int(dx), 0)))
		c := NewCanvas(img)
		clip.X0 += float64(dx)
		clip.X1 += float64(dx)
		c.ClipRect(clip, Identity)
		var p Path
		for k := 0; k < 2; k++ { // two identical subpaths: parts may overlap
			p.MoveTo(x+dx, 5)
			p.LineTo(x+dx, 95)
		}
		c.Stroke(&p, Identity, &StrokeStyle{Width: 4}, black)
		return alphaOfRGBA(img)
	}
	for name, tc := range map[string]struct {
		target image.Rectangle
		clip   Rect
		x      float32
		px     image.Point // pixel expected at 128
	}{
		// Fractional top border row (review example).
		"border row": {image.Rect(-20, 0, 0, 100), Rect{X0: -20, Y0: 10.5, X1: 0, Y1: 100}, -10, image.Pt(-10, 10)},
		// Fractional left border column.
		"border column": {image.Rect(-40, 0, 0, 100), Rect{X0: -9.5, Y0: 0, X1: 0, Y1: 100}, -9, image.Pt(-10, 50)},
	} {
		got := render(0, tc.target, tc.clip, tc.x)
		want := render(40, tc.target, tc.clip, tc.x)
		if a := got.AlphaAt(tc.px.X, tc.px.Y).A; a < 127 || a > 128 {
			t.Errorf("%s: pixel %v = %d, want 128", name, tc.px, a)
		}
		max := 0
		for y := tc.target.Min.Y; y < tc.target.Max.Y; y++ {
			for x := tc.target.Min.X; x < tc.target.Max.X; x++ {
				d := int(got.AlphaAt(x, y).A) - int(want.AlphaAt(x+40, y).A)
				max = maxInt(max, d, -d)
			}
		}
		if max > 1 {
			t.Errorf("%s: differs from the shifted scene by %d", name, max)
		}
	}
}

func maxInt(v ...int) int {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}
