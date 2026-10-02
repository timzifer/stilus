package scenes

import (
	"image/color"
	"math/rand"

	"github.com/timzifer/stilus"
)

func init() { more = append(more, Gradients) }

// Gradients: 200 rectangles and ellipses filled with gradients, a third
// each linear, concentric radial and two-point radial, some translucent.
func Gradients() *Scene {
	rng := rand.New(rand.NewSource(200))
	ramp := make(stilus.Ramp, 256)
	for i := range ramp {
		t := uint8(i)
		ramp[i] = stilus.PackRGBA(color.RGBA{t, 90, 255 - t, 255})
	}
	ops := make([]Op, 0, 200)
	for i := 0; i < 200; i++ {
		w, h := (10+rng.Float64()*60)*mm, (10+rng.Float64()*40)*mm
		x, y := rng.Float64()*(PageW-w), rng.Float64()*(PageH-h)
		cx, cy := x+w/2, y+h/2
		p := new(stilus.Path)
		if i%2 == 0 {
			p.Rect(float32(x), float32(y), float32(w), float32(h))
		} else {
			p.Ellipse(float32(cx), float32(cy), float32(w/2), float32(h/2))
		}
		alpha := uint8(255)
		if i%5 == 0 {
			alpha = 160
		}
		var draw func(c *stilus.Canvas, m stilus.Matrix)
		switch i % 3 {
		case 0:
			g := &stilus.LinearGradient{}
			g.Ramp, g.Alpha, g.Extend = ramp, alpha, [2]bool{true, true}
			paint := &stilus.Paint{Shader: g}
			draw = func(c *stilus.Canvas, m stilus.Matrix) {
				g.Set(x, y, x+w, y+h, m)
				c.Fill(p, m, stilus.NonZero, paint)
			}
		case 1:
			g := &stilus.RadialGradient{}
			g.Ramp, g.Alpha, g.Extend = ramp, alpha, [2]bool{true, true}
			paint := &stilus.Paint{Shader: g}
			r := max(w, h) / 2
			draw = func(c *stilus.Canvas, m stilus.Matrix) {
				g.Set(cx, cy, 0, cx, cy, r, m)
				c.Fill(p, m, stilus.NonZero, paint)
			}
		default:
			g := &stilus.RadialGradient{}
			g.Ramp, g.Alpha, g.Extend = ramp, alpha, [2]bool{true, true}
			paint := &stilus.Paint{Shader: g}
			fx, fy, r := x+w/4, y+h/3, max(w, h)/2
			draw = func(c *stilus.Canvas, m stilus.Matrix) {
				g.Set(fx, fy, 0, cx, cy, r, m)
				c.Fill(p, m, stilus.NonZero, paint)
			}
		}
		ops = append(ops, Op{Draw: draw})
	}
	return &Scene{Name: "gradients-200", Clip: pageClip(), Ops: ops}
}
