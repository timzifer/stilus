package stilus

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"
)

// TestSegmentFastPath compares the analytic segment path (Canvas, opaque
// paint) with the exact outline rasterization of the same stroke.
func TestSegmentFastPath(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	clip := image.Rect(-5, 3, 120, 110)
	var worst, sum float64
	n := 0
	for k := 0; k < 400; k++ {
		var p Path
		p.MoveTo(rng.Float32()*140-15, rng.Float32()*130-10)
		p.LineTo(rng.Float32()*140-15, rng.Float32()*130-10)
		m := Identity
		switch rng.Intn(3) {
		case 1:
			m = Scale(0.5+rng.Float64()*2, 0.5+rng.Float64()*2)
		case 2:
			m = Translate(-60, -55).Mul(Rotate(rng.Float64() * 3)).Mul(Scale(1, 0.4+rng.Float64())).Mul(Translate(60, 55))
		}
		st := &StrokeStyle{Width: 1 + rng.Float64()*8, Cap: Cap(rng.Intn(3))}
		if rng.Intn(3) == 0 {
			st.Dash = []float64{3 + rng.Float64()*10, 1 + rng.Float64()*5}
		}
		if IsHairline(m, st) {
			continue
		}
		img := image.NewRGBA(clip)
		c := NewCanvas(img)
		c.Stroke(&p, m, st, &Paint{Color: color.RGBA{255, 255, 255, 255}})
		got := alphaOfRGBA(img)
		want := strokeMask(&p, m, st, clip)
		mean, mx := diff(got, want)
		sum += mean
		n++
		worst = math.Max(worst, float64(mx))
		if mean > 0.05 || mx > 3 {
			t.Errorf("case %d (w %.2f cap %d dash %v): mean %.4f max %d", k, st.Width, st.Cap, st.Dash != nil, mean, mx)
		}
	}
	t.Logf("%d strokes: mean diff %.5f/255, worst pixel %v/255", n, sum/float64(n), worst)
}
