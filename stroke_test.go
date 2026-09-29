package stilus

import (
	"image"
	"math"
	"testing"
)

func strokeMask(p *Path, m Matrix, st *StrokeStyle, clip image.Rectangle) *image.Alpha {
	img := image.NewAlpha(clip)
	r := NewRasterizer(clip)
	var s Stroker
	s.Stroke(r, p, m, st)
	r.Rasterize(NonZero, &MaskBlitter{img})
	return img
}

func inkArea(img *image.Alpha) float64 {
	sum := 0
	for _, a := range img.Pix {
		sum += int(a)
	}
	return float64(sum) / 255
}

func checkArea(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: area %.3f, want %.3f ± %.3f", name, got, want, tol)
	}
}

func TestStrokeAreas(t *testing.T) {
	clip := image.Rect(0, 0, 100, 80)
	line := func() *Path {
		var p Path
		p.MoveTo(10.3, 20.6)
		p.LineTo(50.3, 20.6)
		return &p
	}
	st := &StrokeStyle{Width: 4, MiterLimit: 10}
	checkArea(t, "butt", inkArea(strokeMask(line(), Identity, st, clip)), 160, 0.5)
	st.Cap = SquareCap
	checkArea(t, "square", inkArea(strokeMask(line(), Identity, st, clip)), 176, 0.5)
	st.Cap = RoundCap
	checkArea(t, "round", inkArea(strokeMask(line(), Identity, st, clip)), 160+4*math.Pi, 1)

	var sq Path
	sq.Rect(20.5, 20.5, 20, 20)
	st = &StrokeStyle{Width: 2, MiterLimit: 10}
	checkArea(t, "square miter", inkArea(strokeMask(&sq, Identity, st, clip)), 160, 0.5)
	st.Join = BevelJoin
	checkArea(t, "square bevel", inkArea(strokeMask(&sq, Identity, st, clip)), 158, 0.5)
	st.Join = RoundJoin
	checkArea(t, "square round", inkArea(strokeMask(&sq, Identity, st, clip)), 160-4+math.Pi, 0.5)
	st.Join = MiterJoin
	st.MiterLimit = 1.2 // below √2: right angles are beveled
	checkArea(t, "square miterlimit", inkArea(strokeMask(&sq, Identity, st, clip)), 158, 0.5)

	// Anisotropic transform: offsets are computed in user space.
	var hl Path
	hl.MoveTo(5, 10)
	hl.LineTo(45, 10)
	st = &StrokeStyle{Width: 2}
	checkArea(t, "anisotropic", inkArea(strokeMask(&hl, Scale(1.5, 3), st, clip)), 60*6, 0.5)

	// Circle ring.
	var c Path
	c.Ellipse(50, 40, 20, 20)
	st = &StrokeStyle{Width: 6}
	checkArea(t, "ring", inkArea(strokeMask(&c, Identity, st, clip)), math.Pi*(23*23-17*17), 2)
}

func TestStrokeDash(t *testing.T) {
	clip := image.Rect(0, 0, 120, 20)
	var p Path
	p.MoveTo(5, 10)
	p.LineTo(105, 10)
	st := &StrokeStyle{Width: 2, Dash: []float64{10, 10}}
	checkArea(t, "dash", inkArea(strokeMask(&p, Identity, st, clip)), 100, 0.5)
	st.DashPhase = 5
	checkArea(t, "dash phase", inkArea(strokeMask(&p, Identity, st, clip)), 5*2+4*20+5*2, 0.5)
	// Dots: zero-length dashes with round caps.
	st = &StrokeStyle{Width: 4, Dash: []float64{0, 10}, Cap: RoundCap}
	checkArea(t, "dots", inkArea(strokeMask(&p, Identity, st, clip)), 11*4*math.Pi, 0.1*11*4*math.Pi)
	// Pathological pattern: drawn solid rather than exploding.
	st = &StrokeStyle{Width: 2, Dash: []float64{1e-9, 1e-9}}
	checkArea(t, "tiny dash", inkArea(strokeMask(&p, Identity, st, clip)), 200, 0.5)
}

func TestHairline(t *testing.T) {
	clip := image.Rect(0, 0, 100, 80)
	st := &StrokeStyle{Width: 0}
	var p Path
	p.MoveTo(10, 20.5)
	p.LineTo(50, 20.5)
	img := strokeMask(&p, Identity, st, clip)
	for x := 0; x < 100; x++ {
		want := uint8(0)
		if x >= 10 && x < 50 {
			want = 255
		}
		if a := img.AlphaAt(x, 20).A; a != want {
			t.Fatalf("hairline pixel %d = %d, want %d", x, a, want)
		}
	}
	checkArea(t, "horizontal", inkArea(img), 40, 0.01)
	for _, ang := range []float64{0.1, 0.5, math.Pi / 4, 1.0, 1.4, 2.5} {
		p.Reset()
		l := 50.0
		x0, y0 := 50-l/2*math.Cos(ang), 40-l/2*math.Sin(ang)
		p.MoveTo(float32(x0), float32(y0))
		p.LineTo(float32(x0+l*math.Cos(ang)), float32(y0+l*math.Sin(ang)))
		checkArea(t, "diagonal", inkArea(strokeMask(&p, Identity, st, clip)), l, 0.02*l)
		// Thin but non-zero width under scaling also takes the hairline path.
		checkArea(t, "thin", inkArea(strokeMask(&p, Identity, &StrokeStyle{Width: 0.3}, clip)), l, 0.02*l)
	}
	// Clipped far outside.
	p.Reset()
	p.MoveTo(-1e9, -1e9)
	p.LineTo(1e9, 1e9)
	checkArea(t, "huge", inkArea(strokeMask(&p, Identity, st, clip)), 80*math.Sqrt2, 2)
}

func TestStrokeNoAllocs(t *testing.T) {
	clip := image.Rect(0, 0, 400, 300)
	var p Path
	p.MoveTo(10, 10)
	for i := 0; i < 40; i++ {
		p.CubicTo(float32(10+i*9), 200, float32(15+i*9), 20, float32(20+i*9), 100)
	}
	img := image.NewRGBA(clip)
	b := NewSolidBlitter(img, rgba(0, 0, 0, 255))
	r := NewRasterizer(clip)
	var s Stroker
	st := &StrokeStyle{Width: 3, Join: RoundJoin, Cap: RoundCap, Dash: []float64{7, 3}}
	run := func() {
		s.Stroke(r, &p, Identity, st)
		r.Rasterize(NonZero, b)
	}
	run()
	if allocs := testing.AllocsPerRun(10, run); allocs != 0 {
		t.Fatalf("Stroke allocates %.1f times per run", allocs)
	}
}
