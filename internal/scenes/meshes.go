package scenes

import (
	"image/color"
	"math"
	"math/rand"

	"github.com/timzifer/stilus"
)

func init() { more = append(more, Meshes) }

// Meshes: 40 Gouraud-shaded meshes (shadings of type 4–7) of 16 × 12 cells,
// 384 triangles each, with jittered inner vertices, filled through their
// outline: in turn with vertex colours, with ramp parameters, and at
// constant alpha. Set runs per drawing, as for a page.
func Meshes() *Scene {
	rng := rand.New(rand.NewSource(40))
	ramp := make(stilus.Ramp, 256)
	for i := range ramp {
		t := uint8(i)
		ramp[i] = stilus.PackRGBA(color.RGBA{255 - t, t / 2, t, 255})
	}
	const nx, ny = 16, 12
	ops := make([]Op, 0, 40)
	for i := 0; i < 40; i++ {
		w, h := (30+rng.Float64()*60)*mm, (25+rng.Float64()*40)*mm
		x0, y0 := rng.Float64()*(PageW-w), rng.Float64()*(PageH-h)
		var pts [ny + 1][nx + 1]stilus.MeshVertex
		for r := 0; r <= ny; r++ {
			for c := 0; c <= nx; c++ {
				px, py := x0+w*float64(c)/nx, y0+h*float64(r)/ny
				if r > 0 && r < ny && c > 0 && c < nx {
					px += (rng.Float64() - 0.5) * w / nx * 0.6
					py += (rng.Float64() - 0.5) * h / ny * 0.6
				}
				fr, fc := float64(r)/ny, float64(c)/nx
				pts[r][c] = stilus.MeshVertex{
					X: float32(px), Y: float32(py),
					C: stilus.PackRGBA(color.RGBA{uint8(255 * fc), uint8(255 * fr), uint8(255 * (1 - fc*fr)), 255}),
					T: float32(0.5 + 0.5*math.Sin(6*fc+4*fr)),
				}
			}
		}
		tris := make([]stilus.MeshTriangle, 0, 2*nx*ny)
		for r := 0; r < ny; r++ {
			for c := 0; c < nx; c++ {
				a, b, cc, d := pts[r][c], pts[r][c+1], pts[r+1][c], pts[r+1][c+1]
				tris = append(tris, stilus.MeshTriangle{a, b, d}, stilus.MeshTriangle{a, d, cc})
			}
		}
		var outline stilus.Path
		outline.Rect(float32(x0), float32(y0), float32(w), float32(h))
		var rp stilus.Ramp
		if i%3 == 1 {
			rp = ramp
		}
		shader := &stilus.MeshShader{Alpha: 255}
		if i%3 == 2 {
			shader.Alpha = 200
		}
		paint := &stilus.Paint{Shader: shader}
		ops = append(ops, Op{Draw: func(c *stilus.Canvas, m stilus.Matrix) {
			shader.Set(tris, m, rp)
			c.Fill(&outline, m, stilus.NonZero, paint)
		}})
	}
	return &Scene{Name: "meshes-40", Clip: pageClip(), Ops: ops}
}
