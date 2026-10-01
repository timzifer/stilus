package stilus

import (
	"image"
	"math"
	"math/rand/v2"
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
