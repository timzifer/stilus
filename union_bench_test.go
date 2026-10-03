package stilus_test

import (
	"fmt"
	"image"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// BenchmarkUnion draws the stroke scenes at 150 dpi on one core, in 1 and
// 16 bands: every operation on its own (separate), all of them as one
// Union of paths (union), and as one Union of shapes prepared once per
// page, set-up included (shapes).
func BenchmarkUnion(b *testing.B) {
	const dpi = 150
	m := scenes.Matrix(dpi)
	for _, s := range []*scenes.Scene{scenes.Hatch(), scenes.HatchThick(), scenes.Short()} {
		for _, nb := range []int{1, 16} {
			img := image.NewRGBA(scenes.Size(dpi))
			bands := splitBands(img.Bounds(), nb)
			c := stilus.NewCanvas(img)
			paint := s.Ops[0].Paint
			var u stilus.Union
			shapes := make([]stilus.Shape, len(s.Ops))
			modes := map[string]func(){
				"separate": func() {
					for _, band := range bands {
						c.Reset(img, band)
						s.Draw(c, dpi)
					}
				},
				"union": func() {
					u.Reset()
					for i := range s.Ops {
						u.Stroke(s.Ops[i].Path, m, s.Ops[i].Style)
					}
					for _, band := range bands {
						c.Reset(img, band)
						c.ClipRect(s.Clip, m)
						c.FillUnion(&u, paint)
					}
				},
				"shapes": func() {
					u.Reset()
					for i := range s.Ops {
						shapes[i].SetStroke(s.Ops[i].Path, m, s.Ops[i].Style)
						u.Shape(&shapes[i])
					}
					for _, band := range bands {
						c.Reset(img, band)
						c.ClipRect(s.Clip, m)
						c.FillUnion(&u, paint)
					}
				},
			}
			for _, mode := range []string{"separate", "union", "shapes"} {
				run := modes[mode]
				b.Run(fmt.Sprintf("%s/%d-bands/%s", s.Name, nb, mode), func(b *testing.B) {
					run()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						run()
					}
				})
			}
		}
	}
}
