package scenes

import (
	"math/rand"

	"github.com/timzifer/stilus"
)

func init() { more = append(more, TextCached) }

// TextCached: 30 000 glyphs like Glyphs, but 64 different outlines drawn
// through a glyph cache at their own positions, as text is drawn. The
// cache stays warm from one rendering to the next.
func TextCached() *Scene {
	rng := rand.New(rand.NewSource(30000))
	// Outlines in em units: an "o" whose counter, an ellipse mirrored to
	// wind the other way, stays open under the nonzero rule.
	var outlines [64]*stilus.Path
	for g := range outlines {
		rx, ry := 0.22+0.12*float32(g%8)/8, 0.3+0.15*float32(g/8)/8
		p := new(stilus.Path)
		p.Ellipse(0, 0, rx, ry)
		p.Ellipse(0, 0, -rx*0.6, ry*0.72)
		outlines[g] = p
	}
	gc := &stilus.GlyphCache{}
	h := 2.5 * mm
	cols := int(PageW / (h * 0.7))
	ops := make([]Op, 0, 30000)
	for i := 0; i < 30000; i++ {
		gid := int32(rng.Intn(len(outlines)))
		// Advances that are not whole device pixels, so that glyphs land
		// on all subpixel phases.
		em := stilus.Scale(h, h).Mul(stilus.Translate(5*mm+float64(i%cols)*h*0.61, 8*mm+float64(i/cols)*h*1.4))
		ops = append(ops, Op{Draw: func(c *stilus.Canvas, m stilus.Matrix) {
			gc.FillGlyph(c, 1, gid, outlines[gid], em.Mul(m), black)
		}})
	}
	return &Scene{Name: "text-30000-cached", Clip: pageClip(), Ops: ops}
}
