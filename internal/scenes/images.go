package scenes

import (
	"image/color"
	"math/rand"

	"github.com/timzifer/stilus"
)

func init() { more = append(more, Images) }

// Images: 60 placements of a 256 × 256 RGBA image, scaled down and up and
// some rotated, half sampled bilinearly, half nearest, some translucent.
func Images() *Scene {
	rng := rand.New(rand.NewSource(60))
	const n = 256
	pix := make([]uint32, n*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			pix[y*n+x] = stilus.PackRGBA(color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	tex := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: pix})
	ops := make([]Op, 0, 60)
	for i := 0; i < 60; i++ {
		w := (15 + rng.Float64()*80) * mm
		h := w * (0.6 + rng.Float64()*0.6)
		x, y := rng.Float64()*(PageW-w), rng.Float64()*(PageH-h)
		// Image space to page space: the unit square of the image's
		// pixels onto the placement, rotated about its centre for every
		// fourth.
		toPage := stilus.Scale(w/n, h/n)
		if i%4 == 3 {
			toPage = toPage.Mul(stilus.Translate(-w/2, -h/2)).Mul(stilus.Rotate(rng.Float64() - 0.5)).Mul(stilus.Translate(w/2, h/2))
		}
		toPage = toPage.Mul(stilus.Translate(x, y))
		outline := quad(toPage, n, n)
		smooth := i%2 == 0
		alpha := uint8(255)
		if i%5 == 0 {
			alpha = 170
		}
		shader := &stilus.ImageShader{}
		paint := &stilus.Paint{Shader: shader}
		ops = append(ops, Op{Draw: func(c *stilus.Canvas, m stilus.Matrix) {
			shader.SetImage(tex, toPage.Mul(m), smooth, alpha)
			c.Fill(outline, m, stilus.NonZero, paint)
		}})
	}
	return &Scene{Name: "images-60", Clip: pageClip(), Ops: ops}
}

// quad returns the rectangle (0, 0, w, h) under m as a path in page space.
func quad(m stilus.Matrix, w, h float64) *stilus.Path {
	var p stilus.Path
	for k, q := range [4][2]float64{{0, 0}, {w, 0}, {w, h}, {0, h}} {
		x, y := m.Apply(q[0], q[1])
		if k == 0 {
			p.MoveTo(float32(x), float32(y))
		} else {
			p.LineTo(float32(x), float32(y))
		}
	}
	p.Close()
	return &p
}
