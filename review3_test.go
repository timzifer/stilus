package stilus

import (
	"image"
	"math"
	"math/rand/v2"
	"testing"
)

// Regression tests for the third review round.

func rasterLines(clip image.Rectangle, lines ...[4]float64) *image.Alpha {
	img := image.NewAlpha(clip)
	r := NewRasterizer(clip)
	for _, l := range lines {
		r.AddLine(l[0], l[1], l[2], l[3])
	}
	r.Rasterize(NonZero, &MaskBlitter{img})
	return img
}

// 1. Clipping a line whose differences overflow float64 must neither panic
// nor lose the line's position.
func TestAddLineHugeCoordinates(t *testing.T) {
	clip := image.Rect(0, 0, 8, 8)
	for _, k := range []float64{1e308, math.MaxFloat64, 1e200} {
		got := rasterLines(clip,
			[4]float64{-k, -k, k, k}, [4]float64{8, 0, 8, 8}, // triangle below the diagonal
			[4]float64{k, -k, -k, k}, [4]float64{0, 8, 0, 0})
		want := rasterLines(clip,
			[4]float64{-1e6, -1e6, 1e6, 1e6}, [4]float64{8, 0, 8, 8},
			[4]float64{1e6, -1e6, -1e6, 1e6}, [4]float64{0, 8, 0, 0})
		if mean, max := diff(got, want); max > 1 {
			t.Errorf("k=%g: mean %.3f max %d", k, mean, max)
		}
	}
}

// 2. A visible pattern padded with many zero entries is walked, not drawn
// at its mean coverage.
func TestDenseDashVisibleStructure(t *testing.T) {
	pat := append([]float64{100, 100}, make([]float64, 7000)...)
	if _, ok := denseDash(Identity, &StrokeStyle{Width: 4, Dash: pat}); ok {
		t.Fatal("pattern with 100 px dashes collapsed")
	}
	var p Path
	p.MoveTo(0, 5)
	p.LineTo(1000, 5)
	img := image.NewRGBA(image.Rect(0, 0, 1000, 10))
	c := NewCanvas(img)
	c.Stroke(&p, Identity, &StrokeStyle{Width: 4, Dash: pat}, white)
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	for _, x := range []int{50, 250, 850} {
		if a := img.RGBAAt(x, 5).A; a != 255 {
			t.Errorf("dash at x=%d: alpha %d, want 255", x, a)
		}
	}
	for _, x := range []int{150, 350, 950} {
		if a := img.RGBAAt(x, 5).A; a != 0 {
			t.Errorf("gap at x=%d: alpha %d, want 0", x, a)
		}
	}
	// Uniformly fine patterns still collapse.
	if _, ok := denseDash(Identity, &StrokeStyle{Width: 4, Dash: manyEntries(100, 0.02)}); !ok {
		t.Error("uniform pattern of tiny entries walked")
	}
}

// 3. Paths with infinite coordinates, in the path or after the transform,
// are ignored as a whole.
func TestAddPathInfinite(t *testing.T) {
	inf := float32(math.Inf(1))
	clip := image.Rect(0, 0, 10, 10)
	for name, tc := range map[string]struct {
		pts [][2]float32
		m   Matrix
	}{
		"point":     {[][2]float32{{1, 1}, {9, 1}, {9, inf}, {1, 9}}, Identity},
		"first":     {[][2]float32{{-inf, 1}, {9, 1}, {9, 9}}, Identity},
		"transform": {[][2]float32{{1, 1}, {9, 1}, {9, 3e38}, {1, 9}}, Matrix{1, 0, 0, 1e300, 0, 0}},
	} {
		var p Path
		p.MoveTo(tc.pts[0][0], tc.pts[0][1])
		for _, q := range tc.pts[1:] {
			p.LineTo(q[0], q[1])
		}
		img := image.NewAlpha(clip)
		r := NewRasterizer(clip)
		// Edges added before the path stay.
		r.AddLine(0, 0, 0, 10)
		r.AddLine(2, 10, 2, 0)
		r.AddPath(&p, tc.m)
		r.Rasterize(NonZero, &MaskBlitter{img})
		if a := inkArea(img); math.Abs(a-20) > 1e-9 {
			t.Errorf("%s: ink area %v, want 20", name, a)
		}
	}
}

// noCull hides the Rasterizer from the Stroker, which then emits all
// geometry.
type noCull struct{ r *Rasterizer }

func (n noCull) AddLine(x0, y0, x1, y1 float64) { n.r.AddLine(x0, y0, x1, y1) }

// 4. Culling subpaths and curves outside the clip leaves the output
// unchanged.
func TestStrokeCullExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	clip := image.Rect(20, 20, 60, 60)
	f := func() float32 { return float32(rng.Float64()*120 - 20) }
	for it := range 400 {
		var p Path
		for range 1 + rng.IntN(3) {
			p.MoveTo(f(), f())
			for range 1 + rng.IntN(4) {
				switch rng.IntN(3) {
				case 0:
					p.LineTo(f(), f())
				case 1:
					p.QuadTo(f(), f(), f(), f())
				default:
					p.CubicTo(f(), f(), f(), f(), f(), f())
				}
			}
			if rng.IntN(2) == 0 {
				p.Close()
			}
		}
		st := &StrokeStyle{
			Width:      []float64{0.5, 2, 9}[rng.IntN(3)],
			Cap:        Cap(rng.IntN(3)),
			Join:       Join(rng.IntN(3)),
			MiterLimit: []float64{1, 4, 20}[rng.IntN(3)],
		}
		if rng.IntN(3) == 0 {
			st.Dash = []float64{7, 5}
		}
		m := []Matrix{Identity, {0.7, 0.4, -0.3, 1.2, 5, -3}, {-1, 0, 0, 1, 100, 0}}[rng.IntN(3)]
		var s Stroker
		got, want := image.NewAlpha(clip), image.NewAlpha(clip)
		r := NewRasterizer(clip)
		s.Stroke(r, &p, m, st)
		r.Rasterize(NonZero, &MaskBlitter{got})
		s.Stroke(noCull{r}, &p, m, st)
		r.Rasterize(NonZero, &MaskBlitter{want})
		if mean, max := diff(got, want); max > 0 {
			t.Fatalf("iteration %d, style %+v, m %v: mean %.4f max %d", it, st, m, mean, max)
		}
	}
}

func invisibleCurves(n int) *Path {
	var p Path
	p.MoveTo(10, 10)
	p.LineTo(90, 10)
	for i := range n {
		y := float32(2000 + 10*i)
		p.MoveTo(0, y)
		p.CubicTo(5000, y-900, -5000, y+900, 100, y)
	}
	return &p
}

func BenchmarkStrokeInvisibleCurves(b *testing.B) {
	for _, n := range []int{0, 100} {
		p := invisibleCurves(n)
		b.Run(map[int]string{0: "none", 100: "100"}[n], func(b *testing.B) {
			r := NewRasterizer(image.Rect(0, 0, 100, 100))
			var s Stroker
			st := &StrokeStyle{Width: 2}
			for b.Loop() {
				s.Stroke(r, p, Identity, st)
				r.Reset()
			}
		})
	}
}

type spanCount struct{ rows map[int]bool }

func (c *spanCount) BlitRun(y, x0, x1 int, alpha uint8) { c.rows[y] = true }
func (c *spanCount) BlitCoverage(y, x int, cov []uint8) { c.rows[y] = true }

func twoRects(r *Rasterizer, gap float64) {
	r.AddLine(0, 0, 0, 4)
	r.AddLine(4, 4, 4, 0)
	r.AddLine(0, gap, 0, gap+4)
	r.AddLine(4, gap+4, 4, gap)
}

// 5. Empty bands between the edges of a tall sparse path are skipped.
func TestRasterizeSparseBands(t *testing.T) {
	const gap = 1_000_000
	r := NewRasterizer(image.Rect(0, 0, 8, gap+8))
	twoRects(r, gap)
	c := &spanCount{map[int]bool{}}
	r.Rasterize(NonZero, c)
	if len(c.rows) != 8 || !c.rows[0] || !c.rows[3] || !c.rows[gap] || !c.rows[gap+3] {
		t.Errorf("rows %v", c.rows)
	}
}

func BenchmarkRasterizeSparseBands(b *testing.B) {
	for _, gap := range []float64{100, 1_000_000} {
		b.Run(map[float64]string{100: "near", 1_000_000: "far"}[gap], func(b *testing.B) {
			r := NewRasterizer(image.Rect(0, 0, 8, int(gap)+8))
			c := &spanCount{map[int]bool{}}
			for b.Loop() {
				twoRects(r, gap)
				r.Rasterize(NonZero, c)
			}
		})
	}
}
