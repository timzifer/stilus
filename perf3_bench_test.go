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

// BenchmarkGlyphCacheChurn draws a working set just above the cache's
// budget, over and over.
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
	// 128 glyphs in 4 phases: 512 masks of about 16·19 bytes plus 64.
	page := func(gc *GlyphCache) {
		for k := range 4096 {
			m := Matrix{em, 0, 0, -em, float64(k%64)*15 + 0.25*float64(k%4), float64(k/64)*15 + em}
			gc.FillGlyph(c, 1, int32(k%128), &g, m, paint)
		}
	}
	var probe GlyphCache
	page(&probe)
	for _, c := range []struct {
		name string
		frac float64
	}{{"fits", 1.1}, {"over-5pct", 0.95}, {"over-25pct", 0.8}} {
		b.Run(c.name, func(b *testing.B) {
			gc := GlyphCache{MaxBytes: int(float64(probe.bytes) * c.frac)}
			page(&gc)
			b.ReportAllocs()
			for b.Loop() {
				page(&gc)
			}
		})
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
