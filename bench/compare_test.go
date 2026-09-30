// Package bench compares stilus against other pure-Go rasterizers on the
// spec's scenes. It is a separate module so the core stays dependency-free.
package bench

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"github.com/gogpu/gg"
	"golang.org/x/image/vector"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

const dpi = 150

func benchScenes() []*scenes.Scene { return []*scenes.Scene{scenes.Hatch(), scenes.Short()} }

// deviceWidth returns the stroke width in device pixels, at least 1.
func deviceWidth(st *stilus.StrokeStyle) float64 {
	return math.Max(st.Width*dpi/72, 1)
}

func BenchmarkStilus(b *testing.B) {
	for _, s := range benchScenes() {
		b.Run(s.Name, func(b *testing.B) {
			img := image.NewRGBA(scenes.Size(dpi))
			c := stilus.NewCanvas(img)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				c.Reset(img, img.Bounds())
				s.Draw(c, dpi)
			}
		})
	}
}

// BenchmarkXImageVector fills the stroke outlines produced by the stilus
// stroker with x/image/vector, each rasterizer sized to the outline's box
// (vector's cost is proportional to the box area).
func BenchmarkXImageVector(b *testing.B) {
	for _, s := range benchScenes() {
		b.Run(s.Name, func(b *testing.B) {
			img := image.NewRGBA(scenes.Size(dpi))
			var z vector.Rasterizer
			var sk stilus.Stroker
			var out stilus.Path
			m := scenes.Matrix(dpi)
			src := image.NewUniform(color.Black)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, op := range s.Ops {
					st := *op.Style
					st.Width = deviceWidth(op.Style) * 72 / dpi
					out.Reset()
					sk.Stroke(stilus.PathSink{P: &out}, op.Path, m, &st)
					bb := out.Bounds()
					r := image.Rect(int(math.Floor(bb.X0)), int(math.Floor(bb.Y0)), int(math.Ceil(bb.X1)), int(math.Ceil(bb.Y1))).Intersect(img.Bounds())
					if r.Empty() {
						continue
					}
					z.Reset(r.Dx(), r.Dy())
					ox, oy := float32(r.Min.X), float32(r.Min.Y)
					pi := 0
					for _, v := range out.Verbs {
						switch v {
						case stilus.MoveTo:
							z.MoveTo(out.Points[pi].X-ox, out.Points[pi].Y-oy)
							pi++
						case stilus.LineTo:
							z.LineTo(out.Points[pi].X-ox, out.Points[pi].Y-oy)
							pi++
						case stilus.Close:
							z.ClosePath()
						}
					}
					z.Draw(img, r, src, r.Min)
				}
			}
		})
	}
}

// BenchmarkGG strokes with gogpu/gg's CPU path, without a clip (its
// rectangle clip is far slower still, see the spec).
func BenchmarkGG(b *testing.B) {
	for _, s := range benchScenes() {
		b.Run(s.Name, func(b *testing.B) {
			size := scenes.Size(dpi)
			dc := gg.NewContext(size.Dx(), size.Dy())
			m := scenes.Matrix(dpi)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				dc.SetRGB(0, 0, 0)
				for _, op := range s.Ops {
					p := op.Path.Points
					x0, y0 := m.Apply(float64(p[0].X), float64(p[0].Y))
					x1, y1 := m.Apply(float64(p[1].X), float64(p[1].Y))
					dc.SetLineWidth(deviceWidth(op.Style))
					dc.MoveTo(x0, y0)
					dc.LineTo(x1, y1)
					dc.Stroke()
				}
			}
		})
	}
	_ = draw.Src
}
