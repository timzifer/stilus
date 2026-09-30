package stilus

import (
	"image"
	"math"
	"math/rand"
	"testing"
)

// refPolys flattens p under m very finely into closed polygons.
func refPolys(p *Path, m Matrix) [][][2]float64 {
	var polys [][][2]float64
	var cur [][2]float64
	pi := 0
	tr := func(q Point) [2]float64 {
		x, y := m.Apply(float64(q.X), float64(q.Y))
		return [2]float64{x, y}
	}
	last := func() [2]float64 { return cur[len(cur)-1] }
	flush := func() {
		if len(cur) > 1 {
			polys = append(polys, cur)
		}
		cur = nil
	}
	for _, v := range p.Verbs {
		switch v {
		case MoveTo:
			flush()
			cur = [][2]float64{tr(p.Points[pi])}
		case LineTo:
			cur = append(cur, tr(p.Points[pi]))
		case QuadTo:
			a, b, c := last(), tr(p.Points[pi]), tr(p.Points[pi+1])
			for i := 1; i <= 64; i++ {
				t := float64(i) / 64
				u := 1 - t
				cur = append(cur, [2]float64{
					u*u*a[0] + 2*u*t*b[0] + t*t*c[0],
					u*u*a[1] + 2*u*t*b[1] + t*t*c[1],
				})
			}
		case CubicTo:
			a, b, c, d := last(), tr(p.Points[pi]), tr(p.Points[pi+1]), tr(p.Points[pi+2])
			for i := 1; i <= 64; i++ {
				t := float64(i) / 64
				u := 1 - t
				cur = append(cur, [2]float64{
					u*u*u*a[0] + 3*u*u*t*b[0] + 3*u*t*t*c[0] + t*t*t*d[0],
					u*u*u*a[1] + 3*u*u*t*b[1] + 3*u*t*t*c[1] + t*t*t*d[1],
				})
			}
		case Close:
			if len(cur) > 0 {
				s := cur[0]
				flush()
				cur = [][2]float64{s}
			}
		}
		pi += numPoints[v]
	}
	flush()
	return polys
}

// refRenderMode with accum=true integrates the signed winding number over
// each pixel and applies the fill rule to the integral afterwards. That is
// the semantics of exact-area accumulation (FreeType, AGG, Skia, PDFium),
// which differs from true coverage only where edges cross inside a pixel.
func refRenderMode(polys [][][2]float64, clip image.Rectangle, rule FillRule, accum bool) *image.Alpha {
	const ss = 16
	img := image.NewAlpha(clip)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		for x := clip.Min.X; x < clip.Max.X; x++ {
			n := 0
			wsum := 0
			for sy := 0; sy < ss; sy++ {
				py := float64(y) + (float64(sy)+0.5)/ss
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					w := 0
					for _, poly := range polys {
						for i := range poly {
							a, b := poly[i], poly[(i+1)%len(poly)]
							if a[1] <= py && b[1] > py {
								if (b[0]-a[0])*(py-a[1])-(px-a[0])*(b[1]-a[1]) > 0 {
									w++
								}
							} else if a[1] > py && b[1] <= py {
								if (b[0]-a[0])*(py-a[1])-(px-a[0])*(b[1]-a[1]) < 0 {
									w--
								}
							}
						}
					}
					wsum += w
					if (rule == NonZero && w != 0) || (rule == EvenOdd && w&1 != 0) {
						n++
					}
				}
			}
			f := float64(n) / (ss * ss)
			if accum {
				f = math.Abs(float64(wsum)) / (ss * ss)
				if rule == EvenOdd {
					f = math.Mod(f, 2)
					if f > 1 {
						f = 2 - f
					}
				}
				f = math.Min(f, 1)
			}
			img.SetAlpha(x, y, alphaOf(f))
		}
	}
	return img
}

func alphaOf(f float64) (a struct{ A uint8 }) {
	a.A = uint8(math.Min(255, math.Floor(f*256)))
	return
}

func renderMask(p *Path, m Matrix, clip image.Rectangle, rule FillRule) *image.Alpha {
	img := image.NewAlpha(clip)
	r := NewRasterizer(clip)
	r.Fill(p, m, rule, &MaskBlitter{img})
	return img
}

// diff returns the mean and max absolute difference in 1/255 units.
func diff(a, b *image.Alpha) (mean float64, max int) {
	sum := 0
	for i := range a.Pix {
		d := int(a.Pix[i]) - int(b.Pix[i])
		if d < 0 {
			d = -d
		}
		sum += d
		if d > max {
			max = d
		}
	}
	return float64(sum) / float64(len(a.Pix)), max
}

func checkAgainstRef(t *testing.T, name string, p *Path, m Matrix, clip image.Rectangle, rule FillRule) {
	t.Helper()
	// Curves are flattened with 0.1 px tolerance: up to ~26/255 per pixel.
	const maxDiff = 32
	got := renderMask(p, m, clip, rule)
	want := refRenderMode(refPolys(p, m), clip, rule, true)
	mean, max := diff(got, want)
	if mean > 1 || max > maxDiff {
		t.Errorf("%s: mean diff %.3f/255, max %d/255", name, mean, max)
	}
}

func TestFillSimpleShapes(t *testing.T) {
	clip := image.Rect(0, 0, 64, 48)
	var p Path
	p.Rect(3.3, 4.7, 40.2, 30.9)
	checkAgainstRef(t, "rect", &p, Identity, clip, NonZero)

	p.Reset()
	p.Rect(8, 8, 16, 16) // pixel aligned
	got := renderMask(&p, Identity, clip, NonZero)
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			want := uint8(0)
			if x >= 8 && x < 24 && y >= 8 && y < 24 {
				want = 255
			}
			if a := got.AlphaAt(x, y).A; a != want {
				t.Fatalf("aligned rect: pixel %d,%d = %d want %d", x, y, a, want)
			}
		}
	}

	p.Reset()
	p.MoveTo(5, 5)
	p.LineTo(60, 12)
	p.LineTo(20, 44)
	p.Close()
	checkAgainstRef(t, "triangle", &p, Identity, clip, NonZero)

	p.Reset()
	p.Ellipse(32, 24, 25, 18)
	checkAgainstRef(t, "ellipse", &p, Identity, clip, NonZero)

	// Star: self-intersecting, differs between the rules.
	p.Reset()
	for i := 0; i < 5; i++ {
		a := float64(i) * 4 * math.Pi / 5
		x, y := float32(32+22*math.Sin(a)), float32(24-22*math.Cos(a))
		if i == 0 {
			p.MoveTo(x, y)
		} else {
			p.LineTo(x, y)
		}
	}
	p.Close()
	checkAgainstRef(t, "star nonzero", &p, Identity, clip, NonZero)
	checkAgainstRef(t, "star evenodd", &p, Identity, clip, EvenOdd)

	// Transformed curves.
	p.Reset()
	p.MoveTo(0, 0)
	p.CubicTo(30, -10, 10, 40, 40, 30)
	p.QuadTo(10, 50, 0, 0)
	m := Rotate(0.3).Mul(Scale(1.2, 0.9)).Mul(Translate(12, 6))
	checkAgainstRef(t, "curves", &p, m, clip, NonZero)
}

func TestFillClipped(t *testing.T) {
	// A shape much larger than the clip, and a clip not at the origin.
	clip := image.Rect(100, 50, 164, 98)
	var p Path
	p.MoveTo(-1000, 60)
	p.LineTo(130.5, -2000)
	p.LineTo(5000, 90.25)
	p.LineTo(140, 3000)
	p.Close()
	checkAgainstRef(t, "huge quad", &p, Identity, clip, NonZero)

	p.Reset()
	p.Ellipse(120, 70, 40, 60)
	p.Ellipse(150, 90, 20, 10)
	checkAgainstRef(t, "ellipses", &p, Identity, clip, EvenOdd)
}

func TestFillBandBoundaries(t *testing.T) {
	// Tall shapes crossing many bands, with vertices exactly on band rows.
	clip := image.Rect(0, 0, 40, 200)
	var p Path
	p.MoveTo(2, 0)
	p.LineTo(38, 32)
	p.LineTo(5, 64)
	p.LineTo(35.5, 96)
	p.LineTo(20, 199.9)
	p.LineTo(1, 150)
	p.Close()
	checkAgainstRef(t, "zigzag", &p, Identity, clip, NonZero)
}

func TestFillRandom(t *testing.T) {
	// Random polygons. Curves are covered by the shape tests; random cubics
	// form sub-pixel loops where any area rasterizer and point sampling
	// disagree by design.
	rng := rand.New(rand.NewSource(1))
	clip := image.Rect(0, 0, 32, 28)
	for n := 0; n < 60; n++ {
		var p Path
		for s := 0; s < 1+rng.Intn(3); s++ {
			p.MoveTo(rng.Float32()*40-4, rng.Float32()*36-4)
			for k := 0; k < 2+rng.Intn(6); k++ {
				p.LineTo(rng.Float32()*40-4, rng.Float32()*36-4)
			}
			p.Close()
		}
		rule := FillRule(rng.Intn(2))
		got := renderMask(&p, Identity, clip, rule)
		want := refRenderMode(refPolys(&p, Identity), clip, rule, true)
		if mean, max := diff(got, want); mean > 0.5 || max > 16 {
			t.Errorf("random %d: mean diff %.3f/255, max %d/255", n, mean, max)
		}
	}
}

func TestFillDegenerate(t *testing.T) {
	clip := image.Rect(0, 0, 16, 16)
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	cases := []func(p *Path){
		func(p *Path) {},
		func(p *Path) { p.MoveTo(1, 1) },
		func(p *Path) { p.MoveTo(1, 1); p.LineTo(5, 5) },
		func(p *Path) { p.MoveTo(nan, 1); p.LineTo(5, 5); p.LineTo(1, 8) },
		func(p *Path) { p.MoveTo(inf, 1); p.LineTo(5, -inf); p.LineTo(1, 8) },
		func(p *Path) { p.MoveTo(3e38, 1); p.LineTo(-3e38, 5); p.LineTo(1, 3e38) },
		func(p *Path) { p.Verbs = append(p.Verbs, CubicTo) }, // missing points
	}
	for i, f := range cases {
		var p Path
		f(&p)
		img := image.NewAlpha(clip)
		r := NewRasterizer(clip)
		r.Fill(&p, Identity, NonZero, &MaskBlitter{img})
		r.Fill(&p, Matrix{1e300, 0, 0, 1e300, 0, 0}, NonZero, &MaskBlitter{img})
		r.Fill(&p, Matrix{math.NaN(), 0, 0, 1, 0, 0}, NonZero, &MaskBlitter{img})
		_ = i
	}
}

func TestFillAliased(t *testing.T) {
	clip := image.Rect(0, 0, 32, 32)
	var p Path
	p.Ellipse(16, 16, 11.3, 7.7)
	img := image.NewAlpha(clip)
	r := NewRasterizer(clip)
	r.SetAntialias(false)
	r.Fill(&p, Identity, NonZero, &MaskBlitter{img})
	for _, a := range img.Pix {
		if a != 0 && a != 255 {
			t.Fatalf("aliased output has coverage %d", a)
		}
	}
}

func TestFillNoAllocs(t *testing.T) {
	clip := image.Rect(0, 0, 800, 600)
	var p Path
	for i := 0; i < 50; i++ {
		p.Ellipse(float32(10+i*15), float32(20+i*11), 30, 20)
	}
	img := image.NewRGBA(clip)
	b := NewSolidBlitter(img, rgba(0, 0, 0, 255))
	r := NewRasterizer(clip)
	r.Fill(&p, Identity, NonZero, b)
	allocs := testing.AllocsPerRun(20, func() {
		r.Fill(&p, Identity, NonZero, b)
	})
	if allocs != 0 {
		t.Fatalf("Fill allocates %.1f times per run", allocs)
	}
}
