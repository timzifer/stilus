package stilus

import (
	"errors"
	"image"
	"math"
	"math/rand"
	"slices"
	"testing"
)

type panicShader struct{}

func (panicShader) ShadeSpan(y, x int, dst []uint32) { panic("test shader") }

func TestCanvasRecoveryClearsBand(t *testing.T) {
	for _, width := range []int{64, 512} {
		for _, reset := range []bool{false, true} {
			img := image.NewRGBA(image.Rect(0, 0, width, 64))
			c := NewCanvas(img)
			var first, second Path
			first.MoveTo(10, 0)
			first.LineTo(float32(width-10), 0)
			first.LineTo(float32(width-10), 30)
			first.Close()
			c.Fill(&first, Identity, NonZero, &Paint{Shader: panicShader{}})
			if !errors.Is(c.Err(), ErrInternal) {
				t.Fatalf("expected shader error, got %v", c.Err())
			}
			if reset {
				c.Reset(img, img.Rect)
			}
			second.MoveTo(30, 0)
			second.LineTo(40, 0)
			second.LineTo(40, 30)
			second.Close()
			c.Fill(&second, Identity, NonZero, white)
			want := renderMask(&second, Identity, img.Rect, NonZero)
			if mean, mx := diff(alphaOfRGBA(img), want); mx != 0 {
				t.Fatalf("width=%d reset=%v: stale band, mean=%g max=%d", width, reset, mean, mx)
			}
			if reset && c.Err() != nil {
				t.Fatal(c.Err())
			}
		}
	}
}

func TestClipRectEmptyBeforeTransform(t *testing.T) {
	for _, r := range []Rect{{8, 8, 2, 2}, {8, 2, 2, 8}, {2, 8, 8, 2}, {2, 2, 2, 8}, {math.NaN(), 2, 8, 8}} {
		for _, m := range []Matrix{Identity, Scale(-1, 1).Mul(Translate(10, 0)), Rotate(0.4)} {
			img := image.NewRGBA(image.Rect(0, 0, 10, 10))
			c := NewCanvas(img)
			c.ClipRect(r, m)
			if !c.Clip().Empty() || c.ClipDepth() != 1 {
				t.Fatalf("r=%v m=%v: clip=%v depth=%d", r, m, c.Clip(), c.ClipDepth())
			}
			var p Path
			p.Rect(0, 0, 10, 10)
			c.Fill(&p, Identity, NonZero, white)
			if inkArea(alphaOfRGBA(img)) != 0 {
				t.Fatal("empty clip drew pixels")
			}
			c.PopClip()
			c.Fill(&p, Identity, NonZero, white)
			if img.RGBAAt(4, 4).A != 255 || c.Err() != nil {
				t.Fatalf("unbalanced empty clip: %v", c.Err())
			}
		}
	}
}

func TestCoverageManyContours(t *testing.T) {
	// Exercise both dense and sparse sweeps, signs, cancellation and parity.
	for _, width := range []float32{4, 300} {
		for _, count := range []int{16384, 32768, 32769} {
			for _, reverse := range []bool{false, true} {
				var p Path
				for i := 0; i < count; i++ {
					if reverse {
						p.MoveTo(1, 1)
						p.LineTo(1, 5)
						p.LineTo(1+width, 5)
						p.LineTo(1+width, 1)
						p.Close()
					} else {
						p.Rect(1, 1, width, 4)
					}
				}
				r := NewRasterizer(image.Rect(0, 0, 320, 8))
				for _, rule := range []FillRule{NonZero, EvenOdd} {
					img := image.NewAlpha(r.Clip())
					r.Fill(&p, Identity, rule, &MaskBlitter{img})
					want := uint8(255)
					if rule == EvenOdd && count%2 == 0 {
						want = 0
					}
					if got := img.AlphaAt(2, 2).A; got != want || r.Truncated() {
						t.Fatalf("width=%g count=%d reverse=%v rule=%v: alpha=%d want=%d", width, count, reverse, rule, got, want)
					}
				}
				// Opposite winding must still cancel even after a large sum.
				for i := 0; i < count; i++ {
					if reverse {
						p.Rect(1, 1, width, 4)
					} else {
						p.MoveTo(1, 1)
						p.LineTo(1, 5)
						p.LineTo(1+width, 5)
						p.LineTo(1+width, 1)
						p.Close()
					}
				}
				img := image.NewAlpha(r.Clip())
				r.Fill(&p, Identity, NonZero, &MaskBlitter{img})
				if inkArea(img) != 0 {
					t.Fatal("opposite contours did not cancel")
				}
			}
		}
	}
}

func TestAddLineRejectsNonFinite(t *testing.T) {
	r := NewRasterizer(image.Rect(0, 0, 8, 8))
	for _, v := range []float64{math.Inf(-1), math.Inf(1), math.NaN()} {
		for coordinate := 0; coordinate < 4; coordinate++ {
			xy := [4]float64{1, 1, 5, 5}
			xy[coordinate] = v
			r.AddLine(xy[0], xy[1], xy[2], xy[3])
		}
	}
	if len(r.edges) != 0 {
		t.Fatalf("nonfinite lines accepted: %+v", r.edges)
	}
	r.AddLine(1, 1, 5, 5)
	if len(r.edges) != 1 {
		t.Fatal("finite line rejected")
	}
}

func TestDenseDashCoverageAndBudget(t *testing.T) {
	var p Path
	p.MoveTo(0, 5)
	p.LineTo(96, 5)
	clip := image.Rect(0, 0, 100, 10)
	for _, tc := range []struct {
		pattern []float64
		area    float64
	}{
		{[]float64{0, 0.05}, 0},
		{[]float64{1.0 / 64, 3.0 / 64}, 48},
	} {
		img := strokeMask(&p, Identity, &StrokeStyle{Width: 2, Dash: tc.pattern}, clip)
		// 24.8 coverage quantizes a quarter pixel to 64/255.
		checkArea(t, "dense dash", inkArea(img), tc.area, 0.25)
	}
	img := image.NewRGBA(clip)
	c := NewCanvas(img)
	c.s.MaxDashes = 4
	c.Stroke(&p, Identity, &StrokeStyle{Width: 2, Dash: []float64{1, 1}}, white)
	if !errors.Is(c.Err(), ErrDashBudget) || inkArea(alphaOfRGBA(img)) != 0 {
		t.Fatalf("budget should report and skip, got %v", c.Err())
	}
	c.Reset(img, img.Rect)
	c.Stroke(&p, Identity, &StrokeStyle{Width: 2}, white)
	if c.s.Truncated() || c.Err() != nil || img.RGBAAt(50, 5).A != 255 {
		t.Fatalf("stale dash budget state: %v", c.Err())
	}
}

func TestRectangleFillMatchesRasterizer(t *testing.T) {
	clip := image.Rect(-8, -8, 72, 64)
	for _, geometry := range [][4]float32{{0, 0, 64, 64}, {4, 5, -12, 30}, {10, 10, 0, 20}, {0.25, 0.5, 40, 30}, {80, 10, 5, 5}} {
		var p Path
		p.Rect(geometry[0], geometry[1], geometry[2], geometry[3])
		for _, m := range []Matrix{Identity, Translate(2, -3), Scale(-1, 1).Mul(Translate(64, 0)), {0, 1, -1, 0, 64, 0}, Rotate(0.3)} {
			for _, rule := range []FillRule{NonZero, EvenOdd} {
				for _, paint := range []*Paint{white, {Color: rgba(10, 20, 30, 128)}, {Shader: gradient{}}} {
					for mode := 0; mode < 3; mode++ {
						got, want := image.NewRGBA(clip), image.NewRGBA(clip)
						for i := range got.Pix {
							got.Pix[i], want.Pix[i] = 100, 100
						}
						render := func(img *image.RGBA, generic bool) {
							c := NewCanvas(img)
							if mode == 1 {
								c.ClipRect(Rect{-3.5, -2.25, 62.5, 53.75}, Identity)
							} else if mode == 2 {
								var e Path
								e.Ellipse(30, 25, 30, 24)
								c.ClipPath(&e, Identity, NonZero)
							}
							if generic {
								st := c.top()
								c.setClip(st.bounds)
								c.r.Fill(&p, m, rule, c.chain(st, c.paint(paint)))
							} else {
								c.Fill(&p, m, rule, paint)
							}
						}
						render(got, false)
						render(want, true)
						if !slices.Equal(got.Pix, want.Pix) {
							t.Fatalf("rectangle fill differs: geometry=%v matrix=%v rule=%v mode=%d", geometry, m, rule, mode)
						}
					}
				}
			}
		}
	}
}

func TestRectangleFillPreservesEdgeBudget(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	c := NewCanvas(img)
	c.r.MaxEdges = 1
	var p Path
	p.Rect(2, 2, 10, 10)
	c.Fill(&p, Identity, NonZero, white)
	if !errors.Is(c.Err(), ErrEdgeBudget) {
		t.Fatalf("fast rectangle lost edge budget: %v", c.Err())
	}
}

func TestFusedStripCompositing(t *testing.T) {
	rng := rand.New(rand.NewSource(47))
	bounds := image.Rect(-8, -8, 128, 128)
	for k := 0; k < 200; k++ {
		ax, ay := rng.Float64()*120-10, rng.Float64()*40-20
		vx, vy := rng.Float64()*180-90, 20+rng.Float64()*160
		if k%2 == 0 {
			vx, vy = -vx, -vy
		}
		w := 0.5 + rng.Float64()*20
		length := math.Hypot(vx, vy)
		dx, dy := ax-vy/length*w, ay+vx/length*w
		got, want := image.NewRGBA(bounds), image.NewRGBA(bounds)
		for i := 0; i < len(got.Pix); i += 4 {
			a := uint8(rng.Intn(256))
			for ch := 0; ch < 3; ch++ {
				got.Pix[i+ch] = uint8(rng.Intn(int(a) + 1))
			}
			got.Pix[i+3] = a
		}
		copy(want.Pix, got.Pix)
		render := func(img *image.RGBA, fused bool) {
			c := NewCanvas(img)
			if k%3 == 0 {
				c.ClipRect(Rect{-3.5, -2.25, 110.75, 115.5}, Identity)
			}
			st := c.top()
			c.setClip(st.bounds)
			b := c.chain(st, c.paint(&Paint{Color: rgba(50, 100, 180, 255)}))
			f := segFast{r: &c.r, b: b, frac: st.frac}
			f.setBorder(st, false)
			if fused {
				f.solid = &c.solid
			}
			f.middle(ax, ay, ax+vx, ay+vy, dx, dy, bounds.Min.Y, bounds.Max.Y)
		}
		render(got, true)
		render(want, false)
		if !slices.Equal(got.Pix, want.Pix) {
			t.Fatalf("fused compositing differs at case %d", k)
		}
	}
}

func BenchmarkRectangleFill(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	c := NewCanvas(img)
	var p Path
	p.Rect(0, 0, 64, 64)
	c.Fill(&p, Identity, NonZero, black)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Fill(&p, Identity, NonZero, black)
	}
}

// A clip operation that panics (here: a nil path) still pushes a clip, so
// the caller's PopClip stays balanced and later drawing is not clipped by
// the grandparent instead of the parent.
func TestClipPanicKeepsStackBalanced(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	c := NewCanvas(img)
	c.ClipRect(Rect{X0: 0, Y0: 0, X1: 8, Y1: 16}, Identity)
	c.ClipPath(nil, Identity, NonZero)
	if !errors.Is(c.Err(), ErrInternal) || c.ClipDepth() != 2 {
		t.Fatalf("after a panicking clip: err %v, depth %d", c.Err(), c.ClipDepth())
	}
	var p Path
	p.Rect(0, 0, 16, 16)
	c.Fill(&p, Identity, NonZero, white)
	if inkArea(alphaOfRGBA(img)) != 0 {
		t.Fatal("a failed clip must clip everything away")
	}
	c.PopClip()
	c.Fill(&p, Identity, NonZero, white)
	if c.ClipDepth() != 1 || img.RGBAAt(4, 4).A != 255 || img.RGBAAt(12, 4).A != 0 {
		t.Fatalf("after PopClip: depth %d, alpha inside %d outside %d", c.ClipDepth(), img.RGBAAt(4, 4).A, img.RGBAAt(12, 4).A)
	}
}

// A clip path whose box lies entirely beyond the coordinate limit (or at
// infinity) clips everything away without a mask.
func TestClipPathBeyondLimit(t *testing.T) {
	inf := float32(math.Inf(1))
	for _, pts := range [][2]float32{{inf, 5}, {-inf, -inf}, {2e9, 2e9}, {-3e9, 5}} {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		c := NewCanvas(img)
		var e Path
		e.MoveTo(pts[0], pts[1])
		e.LineTo(pts[0]+1, pts[1])
		e.LineTo(pts[0], pts[1]+1)
		e.Close()
		c.ClipPath(&e, Identity, NonZero)
		var p Path
		p.Rect(0, 0, 16, 16)
		c.Fill(&p, Identity, NonZero, white)
		if c.Err() != nil || c.ClipDepth() != 1 || inkArea(alphaOfRGBA(img)) != 0 {
			t.Errorf("clip at %v: err %v, depth %d, ink %v", pts, c.Err(), c.ClipDepth(), inkArea(alphaOfRGBA(img)))
		}
		c.PopClip()
		c.Fill(&p, Identity, NonZero, white)
		if img.RGBAAt(8, 8).A != 255 {
			t.Errorf("clip at %v: drawing after PopClip clipped", pts)
		}
	}
}

// A gradient pixel has the same colour whichever span it is shaded in:
// right at a hard stop, stepping t along the span from a different start
// used to round to the other side.
func TestGradientSpanIndependent(t *testing.T) {
	ramp := Ramp{pack(255, 0, 0, 255), pack(0, 0, 255, 255)}
	var lin LinearGradient
	lin.Ramp, lin.Alpha, lin.Extend = ramp, 255, [2]bool{true, true}
	lin.Set(0, 0, 31, 0, Identity)
	var rad RadialGradient
	rad.Ramp, rad.Alpha, rad.Extend = ramp, 255, [2]bool{true, true}
	rad.Set(3.3, 1.7, 0, 3.3, 1.7, 31, Identity)
	var rot RadialGradient
	rot.Ramp, rot.Alpha, rot.Extend = ramp, 255, [2]bool{true, true}
	rot.Set(1, 2, 3, 20, 5, 29, Rotate(0.3))
	for name, s := range map[string]Shader{"linear": &lin, "radial": &rad, "radial off-centre": &rot} {
		for _, y := range []int{0, 7, -13} {
			one := make([]uint32, 1)
			for _, x0 := range []int{-100, -37, -20, -3, 1} {
				span := make([]uint32, 140)
				s.ShadeSpan(y, x0, span)
				for i := range span {
					s.ShadeSpan(y, x0+i, one)
					if one[0] != span[i] {
						t.Fatalf("%s: pixel (%d,%d) is %v alone, %v in a span from x=%d", name, x0+i, y, UnpackRGBA(one[0]), UnpackRGBA(span[i]), x0)
					}
				}
			}
		}
	}
}

// A bit plane needs only the bytes of its last row, not a full stride.
func TestSampleBitsLastRowUnpadded(t *testing.T) {
	tex := NewTexture(Plane{Kind: PlaneBits, W: 8, H: 2, Stride: 2, Pix8: []byte{255, 0, 255}, Pal: GrayPalette})
	for _, smooth := range []bool{false, true} {
		var s Sampler
		s.Setup(tex, Identity, smooth)
		dst := make([]uint32, 8)
		s.Sample(1, 0, dst)
		if dst[3] != tex.base.At(3, 1) {
			t.Errorf("smooth=%v: last row sampled as %#x", smooth, dst[3])
		}
	}
}

// A glyph whose mask would cover no pixel is not cached as a full, empty
// mask.
func TestEmptyGlyphMaskNotStored(t *testing.T) {
	var p Path
	p.MoveTo(10, 10)
	p.LineTo(90, 90)
	c := NewCanvas(image.NewRGBA(image.Rect(0, 0, 100, 100)))
	var gc GlyphCache
	gc.FillGlyph(c, 1, 1, &p, Identity, white)
	for _, e := range gc.ents {
		if e.mask != nil {
			t.Errorf("empty glyph cached as %v, %d bytes", e.mask.Rect, len(e.mask.Pix))
		}
	}
}

// A glyph too large for integer mask bounds is filled as a path, not
// dropped.
func TestHugeGlyphFilled(t *testing.T) {
	var p Path
	p.Rect(0, 0, 1<<31, 1)
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	c := NewCanvas(img)
	var gc GlyphCache
	gc.FillGlyph(c, 1, 1, &p, Identity, white)
	if img.RGBAAt(1, 0).A != 255 {
		t.Errorf("huge glyph dropped: alpha %d", img.RGBAAt(1, 0).A)
	}
}

// The mask area budget holds where int is 32 bits: 65536² wrapped to 0.
func TestGlyphMaskAreaNoOverflow(t *testing.T) {
	var p Path
	p.Rect(0, 0, 65534, 65534)
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	c := NewCanvas(img)
	var gc GlyphCache
	gc.FillGlyph(c, 1, 1, &p, Identity, white)
	if c.Err() != nil || img.RGBAAt(5, 5).A != 255 {
		t.Errorf("err %v, alpha %d", c.Err(), img.RGBAAt(5, 5).A)
	}
}

// The masked Normal path scales each pixel like the general one: every
// channel by the layer alpha times the mask.
func TestLayerNormalMasked(t *testing.T) {
	r := image.Rect(0, 0, 64, 4)
	src := image.NewRGBA(r)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < len(src.Pix); i += 4 {
		a := uint8(rng.Intn(256))
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a
	}
	mask := image.NewAlpha(image.Rect(5, 1, 60, 3))
	for i := range mask.Pix {
		mask.Pix[i] = uint8(rng.Intn(256))
	}
	for _, alpha := range []uint8{0, 1, 128, 200, 255} {
		s := &LayerShader{Src: src, Mask: mask, Alpha: alpha}
		out := make([]uint32, r.Dx())
		for y := r.Min.Y; y < r.Max.Y; y++ {
			s.ShadeSpan(y, 0, out)
			for x, v := range out {
				k := uint32(0)
				if (image.Point{x, y}).In(mask.Rect) {
					k = div255(uint32(alpha) * uint32(mask.AlphaAt(x, y).A))
				}
				p := src.RGBAAt(x, y)
				w := pack(mulByte(p.R, k), mulByte(p.G, k), mulByte(p.B, k), mulByte(p.A, k))
				if w>>alphaShift&0xff == 0 {
					w = 0
				}
				if v != w {
					t.Fatalf("alpha %d at (%d,%d): %#x, want %#x", alpha, x, y, v, w)
				}
			}
		}
	}
}

func BenchmarkLayerNormalMasked(b *testing.B) {
	r := image.Rect(0, 0, 512, 512)
	src := image.NewRGBA(r)
	mask := image.NewAlpha(r)
	for i := range src.Pix {
		src.Pix[i] = 255
	}
	for i := range mask.Pix {
		mask.Pix[i] = uint8(i)
	}
	c := NewCanvas(image.NewRGBA(r))
	paint := &Paint{Shader: &LayerShader{Src: src, Mask: mask, Alpha: 200}}
	var p Path
	p.Rect(0, 0, 512, 512)
	c.Fill(&p, Identity, NonZero, paint)
	b.ReportAllocs()
	for b.Loop() {
		c.Fill(&p, Identity, NonZero, paint)
	}
}

func BenchmarkEmptyGlyphCached(b *testing.B) {
	var p Path
	p.MoveTo(10, 10)
	p.LineTo(90, 90)
	c := NewCanvas(image.NewRGBA(image.Rect(0, 0, 100, 100)))
	var gc GlyphCache
	gc.FillGlyph(c, 1, 1, &p, Identity, white)
	b.ReportAllocs()
	for b.Loop() {
		gc.FillGlyph(c, 1, 1, &p, Identity, white)
	}
}

// AddPath clips lines at their exact coordinates: clamping each to ±2^40
// first turned the slope 1/2 of the long edge into 1.
func TestAddPathHugeLineSlope(t *testing.T) {
	var p Path
	p.MoveTo(0, 0)
	p.LineTo(1<<42, 1<<41)
	p.LineTo(1<<42, 0)
	p.Close()
	clip := image.Rect(0, 0, 16, 16)
	a, b := image.NewAlpha(clip), image.NewAlpha(clip)
	r := NewRasterizer(clip)
	r.Fill(&p, Identity, NonZero, &MaskBlitter{a})
	for i := range 3 {
		q, z := p.Points[i], p.Points[(i+1)%3]
		r.AddLine(float64(q.X), float64(q.Y), float64(z.X), float64(z.Y))
	}
	r.Rasterize(NonZero, &MaskBlitter{b})
	if !slices.Equal(a.Pix, b.Pix) {
		t.Errorf("AddPath differs from its edges added directly: (10,7) %d vs %d", a.AlphaAt(10, 7).A, b.AlphaAt(10, 7).A)
	}
	if a.AlphaAt(10, 7).A != 0 || a.AlphaAt(10, 3).A != 255 {
		t.Errorf("(10,7) %d, (10,3) %d: want 0 and 255 under the line y = x/2", a.AlphaAt(10, 7).A, a.AlphaAt(10, 3).A)
	}
}

// Curves reaching beyond maxCoord, flattened between clamped control
// points, still close the outline with the exact lines next to them.
func TestAddPathHugeCurveClosed(t *testing.T) {
	clip := image.Rect(0, 0, 16, 16)
	for _, cubic := range []bool{false, true} {
		var p Path
		p.MoveTo(-4, -4)
		p.LineTo(1<<44, -4)
		if cubic {
			p.CubicTo(1<<45, 1<<43, 1<<45, 1<<44, 1<<44, 1<<45)
		} else {
			p.QuadTo(1<<45, 1<<44, 1<<44, 1<<45)
		}
		p.LineTo(-4, 1<<45)
		p.Close()
		a := image.NewAlpha(clip)
		NewRasterizer(clip).Fill(&p, Identity, NonZero, &MaskBlitter{a})
		for i, v := range a.Pix {
			if v != 255 {
				t.Fatalf("cubic=%v: pixel %d alpha %d, want the clip covered", cubic, i, v)
			}
		}
	}
}

// A stroke far beyond the coordinate range is clipped at its exact
// outline, like a fill of that outline.
func TestStrokeHugeSlope(t *testing.T) {
	var p Path
	p.MoveTo(0, 0)
	p.LineTo(1<<42, 1<<41)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	c := NewCanvas(img)
	c.Stroke(&p, Identity, &StrokeStyle{Width: 2}, white)
	for _, q := range []image.Point{{10, 5}, {14, 7}} {
		if img.RGBAAt(q.X, q.Y).A == 0 {
			t.Errorf("(%d,%d) on the line y = x/2 not covered", q.X, q.Y)
		}
	}
	if img.RGBAAt(10, 9).A != 0 {
		t.Errorf("(10,9) off the line covered: alpha %d", img.RGBAAt(10, 9).A)
	}
}
