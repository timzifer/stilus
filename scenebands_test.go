package stilus_test

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// shapeBands is the number of bands from which an operation is prepared
// as a shape: one that only crosses one band border costs more as a shape
// (its records are cold again by the time the second band draws it) than
// drawn twice.
var shapeBands = 3

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// bandedScene plays a scene band by band like a banded renderer whose
// display list knows each operation's device rows: operations are skipped
// in the bands they do not reach, and with shapes set, those spanning
// shapeBands bands or more are prepared once per page as shapes.
type bandedScene struct {
	s      *scenes.Scene
	shapes []stilus.Shape
	use    []bool
	rows   [][2]int // device rows an operation can reach
}

// prepare finds the operations' rows for a page at dpi cut into bands of
// bandH rows, and builds the shapes if shapes is set.
func (b *bandedScene) prepare(dpi float64, bandH int, shapes bool) {
	m := scenes.Matrix(dpi)
	if len(b.shapes) < len(b.s.Ops) {
		b.shapes = make([]stilus.Shape, len(b.s.Ops))
		b.use = make([]bool, len(b.s.Ops))
		b.rows = make([][2]int, len(b.s.Ops))
	}
	for i := range b.s.Ops {
		op := &b.s.Ops[i]
		b.use[i] = false
		b.rows[i] = [2]int{math.MinInt, math.MaxInt}
		if op.Draw != nil {
			continue
		}
		bb := op.Path.Bounds()
		y0, y1 := bb.Y0*m[3]+m[5], bb.Y1*m[3]+m[5]
		if op.Stroke {
			pad := max(op.Style.Width*m[3], 1)/2*max(op.Style.MiterLimit, 1.5) + 2
			y0, y1 = y0-pad, y1+pad
		}
		r0, r1 := int(math.Floor(y0)), int(math.Ceil(y1))+1
		b.rows[i] = [2]int{r0, r1}
		if !shapes || floorDiv(r1-1, bandH)-floorDiv(r0, bandH) < shapeBands-1 {
			continue
		}
		b.use[i] = true
		if op.Stroke {
			b.shapes[i].SetStroke(op.Path, m, op.Style)
		} else {
			b.shapes[i].SetFill(op.Path, m, op.Rule)
		}
	}
}

// draw plays the scene onto c, a canvas reset to band.
func (b *bandedScene) draw(c *stilus.Canvas, band image.Rectangle, dpi float64) {
	s := b.s
	m := scenes.Matrix(dpi)
	c.ClipRect(s.Clip, m)
	if s.Mask != nil {
		c.ClipPath(s.Mask, m, stilus.NonZero)
	}
	for i := range s.Ops {
		op := &s.Ops[i]
		if b.rows[i][1] <= band.Min.Y || b.rows[i][0] >= band.Max.Y {
			continue
		}
		switch {
		case b.use[i]:
			c.FillShape(&b.shapes[i], op.Paint)
		case op.Draw != nil:
			op.Draw(c, m)
		case op.Stroke:
			c.Stroke(op.Path, m, op.Style, op.Paint)
		default:
			c.Fill(op.Path, m, op.Rule, op.Paint)
		}
	}
	if s.Mask != nil {
		c.PopClip()
	}
	c.PopClip()
}

// BenchmarkSceneBands draws each scene at 150 dpi on one core as n bands
// one after another, each band playing the operation list: every
// operation directly (Fill, Stroke); only the operations reaching the
// band (culled); and those, with the operations spanning shapeBands bands
// or more prepared as shapes once per page, preparation included (shapes).
func BenchmarkSceneBands(b *testing.B) {
	const dpi = 150
	for _, s := range scenes.All() {
		for _, n := range []int{1, 16, 32, 64} {
			img := image.NewRGBA(scenes.Size(dpi))
			bands := splitBands(img.Bounds(), n)
			bandH := (img.Bounds().Dy() + n - 1) / n
			c := stilus.NewCanvas(img)
			bs := &bandedScene{s: s}
			b.Run(fmt.Sprintf("%s/%d-bands/direct", s.Name, n), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					for _, r := range bands {
						c.Reset(img, r)
						s.Draw(c, dpi)
					}
				}
			})
			for _, shapes := range []bool{false, true} {
				name := "culled"
				if shapes {
					name = "shapes"
				}
				b.Run(fmt.Sprintf("%s/%d-bands/%s", s.Name, n, name), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						bs.prepare(dpi, bandH, shapes)
						for _, r := range bands {
							c.Reset(img, r)
							bs.draw(c, r, dpi)
						}
					}
				})
			}
		}
	}
}

// TestSceneBandsShapes renders every scene in bands directly and with
// every operation drawn as a shape, and requires the same bytes (with Stroke's clip culling off, as
// in TestFillShapeEqualsDirect).
func TestSceneBandsShapes(t *testing.T) {
	const dpi = 72
	defer func(n int) { shapeBands = n }(shapeBands)
	shapeBands = 1 // every operation a shape
	for _, s := range scenes.All() {
		for _, n := range []int{1, 7, 24} {
			want := image.NewRGBA(scenes.Size(dpi))
			got := image.NewRGBA(scenes.Size(dpi))
			bands := splitBands(want.Bounds(), n)
			dc, sc := stilus.NewCanvas(want), stilus.NewCanvas(got)
			dc.SetNoCull(true)
			sc.SetNoCull(true) // for the operations drawn directly
			bs := &bandedScene{s: s}
			bs.prepare(dpi, (want.Bounds().Dy()+n-1)/n, true)
			for _, r := range bands {
				dc.Reset(want, r)
				s.Draw(dc, dpi)
				sc.Reset(got, r)
				bs.draw(sc, r, dpi)
			}
			if !bytes.Equal(want.Pix, got.Pix) {
				t.Errorf("%s, %d bands: shapes differ from direct drawing", s.Name, n)
			}
			if err := sc.Err(); err != nil {
				t.Errorf("%s: %v", s.Name, err)
			}
		}
	}
}
