package stilus

import (
	"image"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// Whole-canvas benchmarks for fixed per-stroke costs, mask clips, layer and
// image compositing, opaque blend modes, translucent lines and glyph cache
// eviction. Each measures the full Canvas call, compositing included.

// BenchmarkStrokeFixedCost strokes 4096 short segments, where the work
// done once per stroke weighs most.
func BenchmarkStrokeFixedCost(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	paths := make([]Path, 4096)
	for i := range paths {
		x, y := rng.Float64()*1000+12, rng.Float64()*1000+12
		a := rng.Float64() * 2 * math.Pi
		l := 4 + rng.Float64()*12
		paths[i].MoveTo(float32(x), float32(y))
		paths[i].LineTo(float32(x+l*math.Cos(a)), float32(y+l*math.Sin(a)))
	}
	for _, c := range []struct {
		name string
		st   StrokeStyle
	}{
		{"butt", StrokeStyle{Width: 2}},
		{"square", StrokeStyle{Width: 2, Cap: SquareCap}},
		{"round", StrokeStyle{Width: 2, Cap: RoundCap}},
		{"hairline", StrokeStyle{Width: 0}},
		{"dashed", StrokeStyle{Width: 2, Dash: []float64{3, 3}}},
	} {
		b.Run(c.name, func(b *testing.B) {
			dst := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
			cv := NewCanvas(dst)
			paint := &Paint{Color: rgba(0, 0, 0, 255)}
			run := func() {
				cv.Reset(dst, dst.Rect)
				for i := range paths {
					cv.Stroke(&paths[i], Identity, &c.st, paint)
				}
			}
			run()
			b.ReportAllocs()
			for b.Loop() {
				run()
			}
		})
	}
}

// BenchmarkMaskClipFill fills the page through masks whose rows have
// several fully covered stretches: only one of them per row is recorded as
// the mask's opaque interval.
func BenchmarkMaskClipFill(b *testing.B) {
	const n = 512
	var ring, stripes Path
	ring.Ellipse(n/2, n/2, n*0.45, n*0.45)
	ring.Ellipse(n/2, n/2, n*0.2, n*0.2)
	for x := 0; x < n; x += 32 {
		stripes.Rect(float32(x)+0.5, 0, 20, n)
	}
	var page Path
	page.Rect(0, 0, n, n)
	for _, c := range []struct {
		name string
		mask *Path
		rule FillRule
	}{{"ring", &ring, EvenOdd}, {"stripes", &stripes, NonZero}} {
		b.Run(c.name, func(b *testing.B) {
			dst := image.NewRGBA(image.Rect(0, 0, n, n))
			cv := NewCanvas(dst)
			paint := &Paint{Color: rgba(0, 40, 140, 255)}
			cv.ClipPath(c.mask, Identity, c.rule)
			b.ReportAllocs()
			for b.Loop() {
				cv.Fill(&page, Identity, NonZero, paint)
			}
		})
	}
}

// BenchmarkLayerComposite composites a layer and an image 1:1 through the
// canvas, compositing onto the destination included.
func BenchmarkLayerComposite(b *testing.B) {
	rng := rand.New(rand.NewPCG(3, 4))
	r := image.Rect(0, 0, 512, 512)
	translucent := randomRGBA(rng, r)
	opaque := image.NewRGBA(r)
	for i := range opaque.Pix {
		opaque.Pix[i] = uint8(rng.IntN(256))
		if i%4 == 3 {
			opaque.Pix[i] = 255
		}
	}
	var full, frac Path
	full.Rect(0, 0, 512, 512)
	frac.Rect(0.5, 0.25, 511, 511.5)
	p := Plane{Kind: PlaneRGBA, W: 512, H: 512, Stride: 512, Pix32: pixels(opaque)}
	tex := NewTexture(p)
	pt := Plane{Kind: PlaneRGBA, W: 512, H: 512, Stride: 512, Pix32: pixels(translucent)}
	ttex := NewTexture(pt)
	for _, c := range []struct {
		name  string
		shade func(dst *image.RGBA) Shader
		path  *Path
	}{
		{"layer-opaque", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: opaque, Dst: dst, Alpha: 255}
		}, &full},
		{"layer-translucent", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: translucent, Dst: dst, Alpha: 255}
		}, &full},
		{"layer-alpha", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: opaque, Dst: dst, Alpha: 128}
		}, &full},
		{"layer-opaque-aa", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: opaque, Dst: dst, Alpha: 255}
		}, &frac},
		{"image-opaque", func(*image.RGBA) Shader {
			var s ImageShader
			s.SetImage(tex, Identity, false, 255)
			return &s
		}, &full},
		{"image-translucent", func(*image.RGBA) Shader {
			var s ImageShader
			s.SetImage(ttex, Identity, false, 255)
			return &s
		}, &full},
		{"multiply-opaque", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: opaque, Dst: dst, Alpha: 255, Blend: BlendMultiply}
		}, &full},
		{"screen-opaque", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: opaque, Dst: dst, Alpha: 255, Blend: BlendScreen}
		}, &full},
		{"overlay-opaque", func(dst *image.RGBA) Shader {
			return &LayerShader{Src: opaque, Dst: dst, Alpha: 255, Blend: BlendOverlay}
		}, &full},
	} {
		b.Run(c.name, func(b *testing.B) {
			dst := image.NewRGBA(r)
			backdrop := image.NewRGBA(r)
			for i := range backdrop.Pix {
				backdrop.Pix[i] = uint8(rng.IntN(256))
				if i%4 == 3 {
					backdrop.Pix[i] = 255
				}
			}
			cv := NewCanvas(dst)
			paint := &Paint{Shader: c.shade(dst)}
			b.SetBytes(int64(4 * r.Dx() * r.Dy()))
			b.ReportAllocs()
			for b.Loop() {
				// A blend reads the backdrop: start from the same one.
				copy(dst.Pix, backdrop.Pix)
				cv.Fill(c.path, Identity, NonZero, paint)
			}
		})
	}
}

// BenchmarkTranslucentLine strokes long single segments with a
// translucent colour.
func BenchmarkTranslucentLine(b *testing.B) {
	for _, c := range []struct {
		name string
		w    float64
		cap  Cap
	}{{"w1", 1, ButtCap}, {"w4", 4, ButtCap}, {"w16-round", 16, RoundCap}, {"hairline", 0, ButtCap}} {
		b.Run(c.name, func(b *testing.B) {
			dst := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
			cv := NewCanvas(dst)
			st := &StrokeStyle{Width: c.w, Cap: c.cap}
			paint := &Paint{Color: rgba(0, 64, 20, 128)}
			var lines [16]Path
			for i := range lines {
				x := float32(i*60 + 20)
				lines[i].MoveTo(x, 10)
				lines[i].LineTo(x+40.3, 1010)
			}
			b.ReportAllocs()
			for b.Loop() {
				for i := range lines {
					cv.Stroke(&lines[i], Identity, st, paint)
				}
			}
		})
	}
}

// BenchmarkGlyphCacheChurn draws pages of glyphs with a cache smaller
// than, or just large enough for, their masks: "cyclic" repeats 512 masks
// in order, "zipf" draws 4096 glyphs of 2048 masks with Zipf frequencies,
// like text. Every mask made costs two allocations.
func BenchmarkGlyphCacheChurn(b *testing.B) {
	var g Path
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.CubicTo(0.9, 0.3, 0.7, 0.8, 0.3, 0.7)
	g.Close()
	dst := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	c := NewCanvas(dst)
	paint := &Paint{Color: rgba(20, 20, 20, 255)}
	const em = 16
	zipf := rand.NewZipf(rand.New(rand.NewPCG(13, 14)), 1.1, 1, 2047)
	ids := make([]int32, 4096)
	for k := range ids {
		ids[k] = int32(zipf.Uint64())
	}
	for _, pattern := range []string{"cyclic", "zipf"} {
		page := func(gc *GlyphCache) {
			for k := range 4096 {
				// 128 or 512 glyphs in 4 phases.
				id := int32(k % 128)
				if pattern == "zipf" {
					id = ids[k] / 4
				}
				fx := 0.25 * float64(k%4)
				if pattern == "zipf" {
					fx = 0.25 * float64(ids[k]%4)
				}
				m := Matrix{em, 0, 0, -em, float64(k%64)*15 + fx, float64(k/64)*15 + em}
				gc.FillGlyph(c, 1, id, &g, m, paint)
			}
		}
		var probe GlyphCache
		probe.MaxBytes = 1 << 30
		page(&probe)
		for _, c := range []struct {
			name string
			frac float64
		}{{"fits", 1.1}, {"over-5pct", 0.95}, {"over-25pct", 0.8}, {"half", 0.5}} {
			b.Run(pattern+"/"+c.name, func(b *testing.B) {
				gc := GlyphCache{MaxBytes: int(float64(probe.bytes) * c.frac)}
				page(&gc)
				b.ReportAllocs()
				for b.Loop() {
					page(&gc)
				}
			})
		}
	}
}

// TestDirectRowsMatchShadedSpans checks that layers and images composited
// straight from their rows give the bytes of their shaded spans.
func TestDirectRowsMatchShadedSpans(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	r := image.Rect(0, 0, 96, 64)
	src := randomRGBA(rng, image.Rect(-8, -8, 104, 72))
	for i := 3; i < len(src.Pix); i += 64 {
		src.Pix[i] = 255 // some opaque pixels
	}
	tex := NewTexture(Plane{Kind: PlaneRGBA, W: 112, H: 80, Stride: src.Stride / 4, Pix32: pixels(src)})
	var ring Path
	ring.Ellipse(48, 32, 40, 28)
	ring.Ellipse(48, 32, 15, 10)
	var shapes [3]Path
	shapes[0].Rect(0, 0, 96, 64)
	shapes[1].Rect(3.5, 2.25, 80.5, 50)
	shapes[2].Ellipse(40, 30, 33.3, 21.7)
	for _, alpha := range []uint8{255, 77} {
		for si := range shapes {
			for _, masked := range []bool{false, true} {
				for _, kind := range []string{"layer", "image", "image-edge"} {
					backdrop := randomRGBA(rng, r)
					draw := func(direct bool) *image.RGBA {
						dst := image.NewRGBA(r)
						copy(dst.Pix, backdrop.Pix)
						var sh Shader
						switch kind {
						case "layer":
							sh = &LayerShader{Src: src, Dst: dst, Alpha: alpha}
						case "image":
							var is ImageShader
							is.SetImage(tex, Translate(-8, -8), false, alpha)
							sh = &is
						default: // spans reach beyond the texture
							var is ImageShader
							is.SetImage(tex, Translate(10, -3), false, alpha)
							sh = &is
						}
						if !direct {
							sh = struct{ Shader }{sh} // hides srcRow
						}
						c := NewCanvas(dst)
						if masked {
							c.ClipPath(&ring, Identity, EvenOdd)
						}
						c.Fill(&shapes[si], Identity, NonZero, &Paint{Shader: sh})
						return dst
					}
					got, want := draw(true), draw(false)
					if !slices.Equal(got.Pix, want.Pix) {
						t.Fatalf("%s alpha %d shape %d mask %v: direct rows differ from shaded spans", kind, alpha, si, masked)
					}
				}
			}
		}
	}
}

// TestDirectRowsOverlap checks a layer drawn onto its own image, moved
// right: its rows overlap the destination's behind it, so they are shaded
// into a copy first.
func TestDirectRowsOverlap(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	img := randomRGBA(rng, image.Rect(0, 0, 64, 16))
	// Pixel x of the layer is pixel x-3 of img.
	moved := &image.RGBA{Pix: img.Pix, Stride: img.Stride, Rect: image.Rect(3, 0, 67, 16)}
	want := image.NewRGBA(img.Rect)
	copy(want.Pix, img.Pix)
	for y := range 16 {
		for x := 3; x < 64; x++ {
			o, so := want.PixOffset(x, y), img.PixOffset(x-3, y)
			s := pack(img.Pix[so], img.Pix[so+1], img.Pix[so+2], img.Pix[so+3])
			d := pack(want.Pix[o], want.Pix[o+1], want.Pix[o+2], want.Pix[o+3])
			r, g, b, a := unpack(over(s, d))
			want.Pix[o], want.Pix[o+1], want.Pix[o+2], want.Pix[o+3] = r, g, b, a
		}
	}
	var p Path
	p.Rect(3, 0, 61, 16)
	c := NewCanvas(img)
	c.Fill(&p, Identity, NonZero, &Paint{Shader: &LayerShader{Src: moved, Dst: img, Alpha: 255}})
	if !slices.Equal(img.Pix, want.Pix) {
		t.Fatal("overlapping layer rows were composited in place")
	}
}

// TestTranslucentSegmentOnce checks that a translucent single segment on
// the analytic path composites every pixel exactly once, with the coverage
// the same stroke gets when opaque: white on black gives that coverage.
func TestTranslucentSegmentOnce(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	r := image.Rect(0, 0, 120, 110)
	var ring Path
	ring.Ellipse(60, 55, 50, 45)
	ring.Ellipse(60, 55, 20, 15)
	col := rgba(30, 60, 90, 140)
	hits := 0
	for k := range 400 {
		var p Path
		p.MoveTo(float32(rng.Float64()*140-10), float32(rng.Float64()*130-10))
		p.LineTo(float32(rng.Float64()*140-10), float32(rng.Float64()*130-10))
		m := Identity
		if k%3 == 1 {
			m = Translate(-60, -55).Mul(Rotate(rng.Float64() * 3)).Mul(Scale(1, 0.4+rng.Float64())).Mul(Translate(60, 55))
		}
		st := &StrokeStyle{Width: rng.Float64() * 9, Cap: Cap(rng.IntN(3))}
		clip := k % 4
		draw := func(dst *image.RGBA, paint *Paint) {
			c := NewCanvas(dst)
			switch clip {
			case 1:
				c.ClipRect(Rect{X0: 7.5, Y0: 5.25, X1: 101.5, Y1: 99.75}, Identity)
			case 2:
				c.ClipPath(&ring, Identity, EvenOdd)
			}
			c.Stroke(&p, m, st, paint)
			if paint.Color.A != 255 {
				hits += c.s.fastHits
			}
		}
		cov := image.NewRGBA(r)
		for i := 3; i < len(cov.Pix); i += 4 {
			cov.Pix[i] = 255
		}
		draw(cov, &Paint{Color: rgba(255, 255, 255, 255)})
		backdrop := randomRGBA(rng, r)
		var paints []*Paint
		paints = append(paints, &Paint{Color: col})
		if k%2 == 0 {
			// A shader composites through the same rows.
			paints = append(paints, &Paint{Shader: &LayerShader{Src: backdrop, Dst: backdrop, Alpha: 140}})
		}
		for _, paint := range paints {
			got := image.NewRGBA(r)
			copy(got.Pix, backdrop.Pix)
			draw(got, paint)
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					o := got.PixOffset(x, y)
					a := uint32(cov.Pix[o])
					d := pack(backdrop.Pix[o], backdrop.Pix[o+1], backdrop.Pix[o+2], backdrop.Pix[o+3])
					s := PackRGBA(col)
					if paint.Shader != nil {
						s = mul255(d, 140)
					}
					want := d
					if a != 0 {
						want = over(mul255(s, a), d)
					}
					if g := pack(got.Pix[o], got.Pix[o+1], got.Pix[o+2], got.Pix[o+3]); g != want {
						t.Fatalf("case %d (w %.2f cap %d clip %d shader %v) pixel (%d, %d): %x, want %x (coverage %d)",
							k, st.Width, st.Cap, clip, paint.Shader != nil, x, y, g, want, a)
					}
				}
			}
		}
	}
	if hits < 100 {
		t.Fatalf("only %d translucent strokes took the analytic path", hits)
	}
}

// TestGlyphCacheEviction draws glyphs through caches far smaller than
// their masks: they stay within budget and consistent, and draw the bytes
// of a cache that keeps everything.
func TestGlyphCacheEviction(t *testing.T) {
	var g Path
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.CubicTo(0.9, 0.3, 0.7, 0.8, 0.3, 0.7)
	g.Close()
	rng := rand.New(rand.NewPCG(15, 16))
	r := image.Rect(0, 0, 400, 300)
	want, got := image.NewRGBA(r), image.NewRGBA(r)
	cw, cg := NewCanvas(want), NewCanvas(got)
	var all GlyphCache
	all.MaxBytes = 1 << 30
	small := GlyphCache{MaxBytes: 3000}
	paint := &Paint{Color: rgba(20, 20, 120, 200)}
	for k := range 3000 {
		em := float64(8 + rng.IntN(4)*6)
		m := Matrix{em, 0, 0, -em, rng.Float64() * 380, rng.Float64()*280 + em}
		id := int32(rng.IntN(40))
		all.FillGlyph(cw, 1, id, &g, m, paint)
		small.FillGlyph(cg, 1, id, &g, m, paint)
		if small.bytes > small.MaxBytes && len(small.ents) > 1 {
			t.Fatalf("glyph %d: %d bytes cached, budget %d", k, small.bytes, small.MaxBytes)
		}
		n := 0
		for i := range small.ents {
			e := &small.ents[i]
			if j, ok := small.masks[e.key]; !ok || int(j) != i {
				t.Fatalf("glyph %d: entry %d indexed as %d (%v)", k, i, j, ok)
			}
			n += e.size()
		}
		if n != small.bytes || len(small.masks) != len(small.ents) {
			t.Fatalf("glyph %d: %d entries of %d bytes, counted %d in %d", k, len(small.masks), n, small.bytes, len(small.ents))
		}
	}
	if !slices.Equal(got.Pix, want.Pix) {
		t.Fatal("a cache that evicts draws other bytes")
	}
}
