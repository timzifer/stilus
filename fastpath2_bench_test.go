package stilus

import (
	"image"
	"testing"
)

func BenchmarkGlyphCacheHit(b *testing.B) {
	var g Path
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.CubicTo(0.9, 0.3, 0.7, 0.8, 0.3, 0.7)
	g.Close()
	for _, em := range []float64{16, 32} {
		b.Run(map[float64]string{16: "16px", 32: "32px"}[em], func(b *testing.B) {
			dst := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
			c := NewCanvas(dst)
			var gc GlyphCache
			paint := &Paint{Color: rgba(20, 20, 20, 255)}
			// 4096 cache hits: 32 glyph ids in 4 subpixel phases.
			page := func() {
				for k := range 4096 {
					m := Matrix{em, 0, 0, -em, float64(k%64)*15 + 0.25*float64(k%4), float64(k/64)*15 + em}
					gc.FillGlyph(c, 1, int32(k%32), &g, m, paint)
				}
			}
			page()
			b.ReportAllocs()
			for b.Loop() {
				page()
			}
		})
	}
}

func BenchmarkRadialConcentric(b *testing.B) {
	dst := image.NewRGBA(image.Rect(0, 0, 512, 512))
	c := NewCanvas(dst)
	var g RadialGradient
	g.Ramp, g.Alpha = grayRamp(256), 255
	g.Set(256, 256, 0, 256, 256, 240, Identity)
	var p Path
	p.Rect(0, 0, 512, 512)
	paint := &Paint{Shader: &g}
	b.ReportAllocs()
	for b.Loop() {
		c.Fill(&p, Identity, NonZero, paint)
	}
}
