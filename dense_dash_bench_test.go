package stilus

import (
	"image"
	"testing"
)

func BenchmarkDenseDash(b *testing.B) {
	var p Path
	p.MoveTo(0, 5)
	p.LineTo(2000, 5)
	for _, tc := range []struct {
		name  string
		width float64
		cap   Cap
		dash  []float64
	}{
		{"butt", 2, ButtCap, []float64{0.002, 0.002}},
		{"round", 2, RoundCap, []float64{0.002, 0.002}},
		{"hairline", 0.5, ButtCap, []float64{0.002, 0.002}},
		{"wide-round", 8, RoundCap, []float64{0.002, 0.002}},
		// Worst cases that are still walked dash by dash.
		{"walked-period", 2, RoundCap, []float64{0.126, 0.126}},
		{"walked-entries", 2, RoundCap, manyEntries(8, 0.0316)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			img := image.NewRGBA(image.Rect(0, 0, 2000, 10))
			c := NewCanvas(img)
			st := &StrokeStyle{Width: tc.width, Cap: tc.cap, Dash: tc.dash}
			for b.Loop() {
				c.Stroke(&p, Identity, st, white)
			}
		})
	}
}
