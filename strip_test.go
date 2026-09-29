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

// TestPolylineFastPath compares the analytic path for polylines with the
// outline path on shapes that do not cross themselves.
func TestPolylineFastPath(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	clip := image.Rect(-5, 3, 160, 130)
	var worst, sum float64
	n, hits, tries := 0, 0, 0
	for k := 0; k < 500; k++ {
		var p Path
		switch k % 4 {
		case 0, 1: // open zigzag, monotone in x
			x := float32(rng.Float64()*10 - 5)
			p.MoveTo(x, float32(rng.Float64()*120))
			for i := 0; i < 2+rng.Intn(8); i++ {
				x += 8 + float32(rng.Float64()*30)
				p.LineTo(x, float32(rng.Float64()*120))
			}
		case 2: // convex polygon
			cx, cy, r := 80+rng.Float64()*20, 65+rng.Float64()*20, 20+rng.Float64()*50
			nv := 3 + rng.Intn(7)
			a0 := rng.Float64()
			for i := 0; i < nv; i++ {
				a := a0 + 2*math.Pi*(float64(i)+0.3*rng.Float64())/float64(nv)
				x, y := float32(cx+r*math.Cos(a)), float32(cy+r*math.Sin(a))
				if i == 0 {
					p.MoveTo(x, y)
				} else {
					p.LineTo(x, y)
				}
			}
			p.Close()
		case 3: // curves: flattened into many short segments
			if rng.Intn(2) == 0 {
				p.Ellipse(80, 65, float32(20+rng.Float64()*50), float32(15+rng.Float64()*40))
			} else {
				p.MoveTo(10, 120)
				p.CubicTo(40, float32(rng.Float64()*100), 90, float32(rng.Float64()*120), 150, 20)
			}
		}
		m := Identity
		switch rng.Intn(3) {
		case 1:
			m = Scale(0.5+rng.Float64(), 0.5+rng.Float64())
		case 2:
			m = Translate(-80, -65).Mul(Rotate(rng.Float64() * 3)).Mul(Scale(1, 0.5+rng.Float64()*0.5)).Mul(Translate(80, 65))
		}
		st := &StrokeStyle{Width: 1 + rng.Float64()*5, Cap: Cap(rng.Intn(3)), Join: Join(rng.Intn(3)), MiterLimit: 1 + rng.Float64()*9}
		if rng.Intn(4) == 0 {
			st.Dash = []float64{4 + rng.Float64()*20, 3 + rng.Float64()*8}
		}
		if IsHairline(m, st) {
			continue
		}
		img := image.NewRGBA(clip)
		c := NewCanvas(img)
		c.Stroke(&p, m, st, &Paint{Color: color.RGBA{255, 255, 255, 255}})
		hits += c.s.fastHits
		tries += c.s.fastTries
		got := alphaOfRGBA(img)
		want := strokeMask(&p, m, st, clip)
		mean, mx := diff(got, want)
		sum += mean
		n++
		worst = math.Max(worst, float64(mx))
		if (mean > 0.05 || mx > 3) && k == dbgCase {
			t.Logf("path %v verbs %v m %v st %+v", p.Points, p.Verbs, m, *st)
			for y := clip.Min.Y; y < clip.Max.Y; y++ {
				for x := clip.Min.X; x < clip.Max.X; x++ {
					if d := int(got.AlphaAt(x, y).A) - int(want.AlphaAt(x, y).A); d > 3 || d < -3 {
						t.Logf("  px %d,%d got %d want %d", x, y, got.AlphaAt(x, y).A, want.AlphaAt(x, y).A)
					}
				}
			}
		}
		if mean > 0.05 || mx > 3 {
			t.Errorf("case %d (kind %d, w %.2f cap %d join %d dash %v): mean %.4f max %d", k, k%4, st.Width, st.Cap, st.Join, st.Dash != nil, mean, mx)
		}
	}
	t.Logf("%d polylines: mean diff %.5f/255, worst pixel %v/255; analytic path taken %d of %d times", n, sum/float64(n), worst, hits, tries)
}

var dbgCase = 492
