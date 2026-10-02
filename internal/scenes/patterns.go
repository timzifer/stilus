package scenes

import (
	"image/color"
	"math/rand"

	"github.com/timzifer/stilus"
)

func init() { more = append(more, Patterns) }

// Patterns: 200 regions filled with tiling patterns, alternately a 32 × 32
// coloured tile and an 8 × 8 hatch stencil painted in a solid colour, at
// tile sizes of a few millimetres.
func Patterns() *Scene {
	rng := rand.New(rand.NewSource(200))
	const tn, sn = 32, 8
	tile := make([]uint32, tn*tn)
	for y := 0; y < tn; y++ {
		for x := 0; x < tn; x++ {
			v := uint8(160)
			if (x/8+y/8)%2 == 0 {
				v = 230
			}
			tile[y*tn+x] = stilus.PackRGBA(color.RGBA{v, v - 40, 90, 255})
		}
	}
	colored := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: tn, H: tn, Stride: tn, Pix32: tile})
	hatch := make([]uint8, sn*sn)
	for y := 0; y < sn; y++ {
		for x := 0; x < sn; x++ {
			if (x+y)%sn < 2 {
				hatch[y*sn+x] = 255
			}
		}
	}
	stencil := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneIndex, W: sn, H: sn, Stride: sn, Pix8: hatch, Pal: stilus.AlphaPalette})
	ink := stilus.PackRGBA(color.RGBA{0, 30, 110, 255})
	ops := make([]Op, 0, 200)
	for i := 0; i < 200; i++ {
		w, h := (10+rng.Float64()*60)*mm, (10+rng.Float64()*40)*mm
		x, y := rng.Float64()*(PageW-w), rng.Float64()*(PageH-h)
		p := new(stilus.Path)
		p.Rect(float32(x), float32(y), float32(w), float32(h))
		shader := &stilus.ImageShader{}
		paint := &stilus.Paint{Shader: shader}
		var draw func(c *stilus.Canvas, m stilus.Matrix)
		if i%2 == 0 {
			size := (4 + rng.Float64()*6) * mm
			toPage := stilus.Scale(size/tn, size/tn).Mul(stilus.Translate(x, y))
			draw = func(c *stilus.Canvas, m stilus.Matrix) {
				shader.SetImageWrap(colored, toPage.Mul(m), false, 255)
				c.Fill(p, m, stilus.NonZero, paint)
			}
		} else {
			size := (2 + rng.Float64()*3) * mm
			toPage := stilus.Scale(size/sn, size/sn).Mul(stilus.Translate(x, y))
			draw = func(c *stilus.Canvas, m stilus.Matrix) {
				shader.SetColor(ink)
				shader.SetMaskWrap(stencil, toPage.Mul(m), true)
				c.Fill(p, m, stilus.NonZero, paint)
			}
		}
		ops = append(ops, Op{Draw: draw})
	}
	return &Scene{Name: "patterns-200", Clip: pageClip(), Ops: ops}
}
