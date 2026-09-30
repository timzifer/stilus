package stilus

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// BenchmarkWideFill fills page-sized shapes whose rows hold many edges far
// apart: the dirty-bitset sweep and interior runs.
func BenchmarkWideFill(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 2481, 1754))
	c := NewCanvas(img)
	var star, circle Path
	for k := 0; k < 64; k++ {
		a := float64(k) / 64 * 2 * math.Pi
		r := 850.0
		if k%2 == 1 {
			r = 300
		}
		x, y := float32(1240+r*math.Cos(a)), float32(877+r*math.Sin(a))
		if k == 0 {
			star.MoveTo(x, y)
		} else {
			star.LineTo(x, y)
		}
	}
	star.Close()
	circle.Ellipse(1240, 877, 1200, 860)
	for _, tc := range []struct {
		name  string
		p     *Path
		paint *Paint
	}{
		{"star-opaque", &star, &Paint{Color: color.RGBA{0, 0, 0, 255}}},
		{"star-alpha", &star, &Paint{Color: color.RGBA{0, 64, 20, 128}}},
		{"ellipse-opaque", &circle, &Paint{Color: color.RGBA{0, 0, 0, 255}}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				c.Fill(tc.p, Identity, NonZero, tc.paint)
			}
		})
	}
}
