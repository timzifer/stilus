package stilus

import (
	"image"
	"image/color"
	"testing"
)

// BenchmarkBorderStroke: wide polylines (parts may overlap, so pixels in a
// fractional clip border go to the accumulator) crossing all four borders.
func BenchmarkBorderStroke(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	c := NewCanvas(img)
	var p Path
	// A drawing frame running along the clip border (every row touches the
	// fractional border column) ...
	p.Rect(10, 10, 380, 380)
	// ... and zigzags crossing all four borders.
	for k := 0; k < 8; k++ {
		x := float32(k * 45)
		p.MoveTo(x, -10)
		p.LineTo(x+40, 410)
		p.LineTo(x+80, -10)
		p.LineTo(x+120, 410)
	}
	st := &StrokeStyle{Width: 20}
	paint := &Paint{Color: color.RGBA{0, 0, 0, 255}}
	run := func() {
		c.Reset(img, img.Rect)
		c.ClipRect(Rect{X0: 10.5, Y0: 10.5, X1: 389.5, Y1: 389.5}, Identity)
		c.Stroke(&p, Identity, st, paint)
	}
	run()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		run()
	}
}

// BenchmarkBorderFrame: a wide drawing frame running along a fractional
// clip border, so every row of its vertical sides touches a border column.
func BenchmarkBorderFrame(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	c := NewCanvas(img)
	var p Path
	p.Rect(10, 10, 780, 780)
	st := &StrokeStyle{Width: 40}
	paint := &Paint{Color: color.RGBA{0, 0, 0, 255}}
	run := func() {
		c.Reset(img, img.Rect)
		c.ClipRect(Rect{X0: 10.5, Y0: 10.5, X1: 789.5, Y1: 789.5}, Identity)
		c.Stroke(&p, Identity, st, paint)
	}
	run()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		run()
	}
}
