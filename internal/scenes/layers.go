package scenes

import (
	"image"
	"math"

	"github.com/timzifer/stilus"
)

func init() { more = append(more, Layers) }

// Layers: 24 overlapping transparency groups of 90 × 70 mm composited
// onto the page, in turn opaque, at constant alpha, and through a soft
// mask at constant alpha. The layers are rendered once per resolution, before the first
// timed rendering; only their compositing is measured.
func Layers() *Scene {
	ops := make([]Op, 0, 24)
	w, h := 90*mm, 70*mm
	for i := 0; i < 24; i++ {
		x := 15*mm + float64(i%6)*62*mm
		y := 15*mm + float64(i/6)*65*mm
		var p stilus.Path
		p.Rect(float32(x), float32(y), float32(w), float32(h))
		kind := (i + i/6) % 3 // varies along rows and columns
		masked := kind == 2
		alpha := uint8(255)
		if kind != 0 {
			alpha = 180
		}
		shader := &stilus.LayerShader{Alpha: alpha}
		paint := &stilus.Paint{Shader: shader}
		scale := 0.0 // the device scale the layer was rendered at
		ops = append(ops, Op{Draw: func(c *stilus.Canvas, m stilus.Matrix) {
			if m[0] != scale {
				scale = m[0]
				shader.Src, shader.Mask = renderLayer(x, y, w, h, m, masked)
			}
			c.Fill(&p, m, stilus.NonZero, paint)
		}})
	}
	return &Scene{Name: "layers-24", Clip: pageClip(), Ops: ops}
}

// renderLayer returns the device pixels of the page rectangle (x, y, w, h)
// under m: diagonal translucent stripes, and a mask fading out from the
// centre if masked.
func renderLayer(x, y, w, h float64, m stilus.Matrix, masked bool) (*image.RGBA, *image.Alpha) {
	x0, y0 := m.Apply(x, y)
	x1, y1 := m.Apply(x+w, y+h)
	r := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1)))
	src := image.NewRGBA(r)
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			o := src.PixOffset(px, py)
			if (px+py)/12%2 == 0 {
				copy(src.Pix[o:o+4], []uint8{200, 60, 20, 255})
			} else {
				copy(src.Pix[o:o+4], []uint8{0, 50, 80, 128})
			}
		}
	}
	if !masked {
		return src, nil
	}
	mask := image.NewAlpha(r)
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	rx, ry := float64(r.Dx())/2, float64(r.Dy())/2
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			dx, dy := (float64(px)+0.5-cx)/rx, (float64(py)+0.5-cy)/ry
			mask.Pix[mask.PixOffset(px, py)] = uint8(255 * max(0, 1-math.Sqrt(dx*dx+dy*dy)))
		}
	}
	return src, mask
}
