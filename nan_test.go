package stilus

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// TestCurveNotNumbers draws curves with a control point that is not a
// number: they draw nothing, and drawing returns. Such a point was
// clamped and the curve flattened again, without end.
func TestCurveNotNumbers(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	var cubic, quad Path
	cubic.MoveTo(1, 1)
	cubic.CubicTo(nan, 2, 5, 9, 12, 3)
	cubic.Close()
	quad.MoveTo(1, 1)
	quad.QuadTo(4, nan, 12, 3)
	quad.Close()
	// A point at infinity turned by a quarter is not a number either.
	var turned Path
	turned.MoveTo(1, 1)
	turned.CubicTo(inf, 2, 5, 9, 12, 3)
	turned.Close()
	quarter := Matrix{0, 1, -1, 0, 20, 0}

	clip := image.Rect(0, 0, 32, 32)
	for _, tt := range []struct {
		name string
		p    *Path
		m    Matrix
	}{{"cubic", &cubic, Identity}, {"quad", &quad, Identity}, {"turned", &turned, quarter}} {
		t.Run(tt.name, func(t *testing.T) {
			mask := image.NewAlpha(clip)
			r := NewRasterizer(clip)
			r.Fill(tt.p, tt.m, NonZero, &MaskBlitter{mask})
			for _, a := range mask.Pix {
				if a != 0 {
					t.Fatal("a curve with a point that is not a number was drawn")
				}
			}

			dst := image.NewRGBA(clip)
			c := NewCanvas(dst)
			paint := &Paint{Color: color.RGBA{0, 0, 0, 255}}
			c.Fill(tt.p, tt.m, EvenOdd, paint)
			c.Stroke(tt.p, tt.m, &StrokeStyle{Width: 2}, paint)
			c.ClipPath(tt.p, tt.m, NonZero)
			c.PopClip()
			var s Shape
			if s.SetFill(tt.p, tt.m, NonZero) {
				c.FillShape(&s, paint)
			}
			if s.SetStroke(tt.p, tt.m, &StrokeStyle{Width: 2}) {
				c.FillShape(&s, paint)
			}
			if err := c.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
