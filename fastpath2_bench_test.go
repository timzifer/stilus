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
	for _, tc := range []struct {
		name string
		em   float64
		clip Rect // none if empty
	}{
		{"16px", 16, Rect{}},
		{"32px", 32, Rect{}},
		// A page clip with fractional borders, as from a PDF in points.
		{"16px-fracclip", 16, Rect{0.4, 0.6, 1023.3, 1023.7}},
	} {
		em := tc.em
		b.Run(tc.name, func(b *testing.B) {
			dst := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
			c := NewCanvas(dst)
			if !tc.clip.Empty() {
				c.ClipRect(tc.clip, Identity)
			}
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

// BenchmarkRadialTwoPoint fills with a two-point radial gradient whose
// focal point lies inside the end circle, as in gradients-200.
func BenchmarkRadialTwoPoint(b *testing.B) {
	dst := image.NewRGBA(image.Rect(0, 0, 512, 512))
	c := NewCanvas(dst)
	var g RadialGradient
	g.Ramp, g.Alpha, g.Extend = grayRamp(256), 255, [2]bool{true, true}
	g.Set(180, 200, 0, 256, 256, 240, Identity)
	var p Path
	p.Rect(0, 0, 512, 512)
	paint := &Paint{Shader: &g}
	b.ReportAllocs()
	for b.Loop() {
		c.Fill(&p, Identity, NonZero, paint)
	}
}
