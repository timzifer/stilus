package stilus

import (
	"image"
	"math"
	"testing"
)

// The mean coverage of a collapsed pattern matches the area of the same
// pattern walked dash by dash at a resolvable scale, up to the caps at the
// line's ends and the partial period there.
func TestDenseDashMeanCoverage(t *testing.T) {
	const length, w = 3200.0, 12.0
	var p Path
	p.MoveTo(20, 20)
	p.LineTo(20+length, 20)
	clip := image.Rect(0, 0, 3240, 40)
	for _, c := range []Cap{ButtCap, SquareCap, RoundCap} {
		for _, pat := range [][]float64{{2, 6}, {4, 4}, {1, 3, 2, 2}, {1, 1.5, 5}, {0, 4}, {6, 1}} {
			pat := scaled(pat, 4)
			f := meanCoverage(pat, 1, w, c)
			st := &StrokeStyle{Width: w, Cap: c, Dash: pat}
			if _, ok := denseDash(Identity, st); ok {
				t.Fatalf("pattern %v dense at full scale", pat)
			}
			// Aliased: antialiased coverage of overlapping dashes sums
			// within a pixel, which would overstate the union.
			img := image.NewAlpha(clip)
			r := NewRasterizer(clip)
			r.SetAntialias(false)
			var s Stroker
			s.Stroke(r, &p, Identity, st)
			r.Rasterize(NonZero, &MaskBlitter{img})
			checkArea(t, "walked vs mean", inkArea(img), f*length*w, 0.01*length*w)
		}
	}
}

func scaled(pat []float64, k float64) []float64 {
	out := make([]float64, len(pat))
	for i, d := range pat {
		out[i] = d * k
	}
	return out
}

func TestDenseDashCanvas(t *testing.T) {
	var p Path
	p.MoveTo(0, 5)
	p.LineTo(2000, 5)
	for _, tc := range []struct {
		width float64
		cap   Cap
		dash  []float64
		want  uint8 // alpha in the middle of the line
	}{
		{2, ButtCap, []float64{0.002, 0.002}, 128},
		{2, SquareCap, []float64{0.002, 0.002}, 255},
		{2, RoundCap, []float64{0.002, 0.002}, 255},
		{0.5, ButtCap, []float64{0.001, 0.003}, 32}, // hairline: half a pixel per row
		{2, ButtCap, []float64{0.5, 0.004}, 254},    // off entry tiny, period > 1/4
		{2, ButtCap, manyEntries(100, 0.005), 128},  // long pattern of tiny entries
	} {
		img := image.NewRGBA(image.Rect(0, 0, 2000, 10))
		c := NewCanvas(img)
		c.Stroke(&p, Identity, &StrokeStyle{Width: tc.width, Cap: tc.cap, Dash: tc.dash}, white)
		if c.Err() != nil {
			t.Fatalf("%+v: %v", tc, c.Err())
		}
		if a := img.RGBAAt(1000, 4).A; int(a)-int(tc.want) > 2 || int(tc.want)-int(a) > 2 {
			t.Errorf("width %v cap %v dash %v: alpha %d, want %d", tc.width, tc.cap, tc.dash[:2], a, tc.want)
		}
	}
}

func manyEntries(n int, d float64) []float64 {
	pat := make([]float64, n)
	for i := range pat {
		pat[i] = d
	}
	return pat
}

// Under a strongly anisotropic m a pattern resolvable along the widest
// stretch is dense along the narrow one; that subpath is skipped rather
// than walked dash by dash.
func TestDenseDashSqueezed(t *testing.T) {
	var p Path
	p.MoveTo(0, 0)
	p.LineTo(0, 100000) // 1000 device pixels, 50000 transitions
	var s Stroker
	r := NewRasterizer(image.Rect(0, 0, 100, 1000))
	s.Stroke(r, &p, Scale(100, 0.01), &StrokeStyle{Width: 1, Dash: []float64{2, 2}})
	if !s.Truncated() {
		t.Fatal("squeezed dash pattern walked")
	}
	var q Path
	q.MoveTo(0, 0)
	q.LineTo(10, 0) // 1000 device pixels, 5 transitions
	s.Stroke(r, &q, Scale(100, 0.01), &StrokeStyle{Width: 1, Dash: []float64{2, 2}})
	if s.Truncated() || s.Coverage() != 1 {
		t.Fatal("resolvable dash pattern rejected")
	}
}

func TestUncoveredRound(t *testing.T) {
	// Beyond the width, the gap loses exactly one disc to the caps.
	if got, want := uncovered(5, 2, RoundCap), 5*2-math.Pi; math.Abs(got-want) > 1e-12 {
		t.Errorf("wide gap: %v, want %v", got, want)
	}
	// Numerical integration of the uncovered slivers for gaps below it.
	for _, g := range []float64{0, 0.1, 1, 1.9, 2} {
		const n = 200000
		sum := 0.0
		for i := 0; i < n; i++ {
			y := -1 + (float64(i)+0.5)*2/n
			sum += max(g-2*math.Sqrt(1-y*y), 0) * 2 / n
		}
		if got := uncovered(g, 2, RoundCap); math.Abs(got-sum) > 1e-6 {
			t.Errorf("gap %v: %v, want %v", g, got, sum)
		}
	}
}
