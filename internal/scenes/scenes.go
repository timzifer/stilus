// Package scenes provides deterministic benchmark scenes modelled on
// technical drawings: an A3 landscape page in PDF points, drawn under a
// scale to the requested resolution with a rectangular page clip.
package scenes

import (
	"image"
	"image/color"
	"math"
	"math/rand"

	"github.com/timzifer/stilus"
)

// A3 landscape in points.
const (
	PageW = 1190.55
	PageH = 841.89
	mm    = 72 / 25.4
)

// Op is one drawing operation, like a display-list entry.
type Op struct {
	Stroke bool
	Rule   stilus.FillRule
	Path   *stilus.Path
	Style  *stilus.StrokeStyle
	Paint  *stilus.Paint
}

// Scene is a prebuilt list of operations in page space.
type Scene struct {
	Name string
	Clip stilus.Rect // page-space rectangle clip ("re W n")
	Mask *stilus.Path
	Ops  []Op
}

// Size returns the device size of the page at dpi.
func Size(dpi float64) image.Rectangle {
	return image.Rect(0, 0, int(math.Ceil(PageW*dpi/72)), int(math.Ceil(PageH*dpi/72)))
}

// Matrix returns the page-to-device transform at dpi.
func Matrix(dpi float64) stilus.Matrix { return stilus.Scale(dpi/72, dpi/72) }

// Draw plays the scene onto c at dpi.
func (s *Scene) Draw(c *stilus.Canvas, dpi float64) {
	m := Matrix(dpi)
	c.ClipRect(s.Clip, m)
	if s.Mask != nil {
		c.ClipPath(s.Mask, m, stilus.NonZero)
	}
	for i := range s.Ops {
		op := &s.Ops[i]
		if op.Stroke {
			c.Stroke(op.Path, m, op.Style, op.Paint)
		} else {
			c.Fill(op.Path, m, op.Rule, op.Paint)
		}
	}
	if s.Mask != nil {
		c.PopClip()
	}
	c.PopClip()
}

var (
	black = &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}}
	blue  = &stilus.Paint{Color: color.RGBA{0, 40, 140, 255}}
	red   = &stilus.Paint{Color: color.RGBA{190, 20, 20, 255}}
	// 50 % transparent green, premultiplied.
	glass = &stilus.Paint{Color: color.RGBA{0, 64, 20, 128}}
)

func pageClip() stilus.Rect {
	return stilus.Rect{X0: 10 * mm, Y0: 10 * mm, X1: PageW - 10*mm, Y1: PageH - 10*mm}
}

func line(x0, y0, x1, y1 float64) *stilus.Path {
	var p stilus.Path
	p.MoveTo(float32(x0), float32(y0))
	p.LineTo(float32(x1), float32(y1))
	return &p
}

// hatch returns n parallel 45° lines covering the page.
func hatch(n int, st *stilus.StrokeStyle) []Op {
	ops := make([]Op, 0, n)
	span := PageW + PageH
	for i := 0; i < n; i++ {
		o := -PageH + span*(float64(i)+0.5)/float64(n)
		ops = append(ops, Op{Stroke: true, Path: line(o, 0, o+PageH, PageH), Style: st, Paint: black})
	}
	return ops
}

// Hatch: 2 000 long hatch lines drawn as hairlines (the spec's scene).
func Hatch() *Scene {
	return &Scene{Name: "hatch-2000-hairline", Clip: pageClip(), Ops: hatch(2000, &stilus.StrokeStyle{Width: 0})}
}

// HatchThick: the same lines 0.35 mm wide (≈2 px at 150 dpi).
func HatchThick() *Scene {
	return &Scene{Name: "hatch-2000-0.35mm", Clip: pageClip(), Ops: hatch(2000, &stilus.StrokeStyle{Width: 0.35 * mm})}
}

func shortStrokes(n int, st *stilus.StrokeStyle) []Op {
	rng := rand.New(rand.NewSource(20000))
	ops := make([]Op, 0, n)
	for i := 0; i < n; i++ {
		x, y := rng.Float64()*PageW, rng.Float64()*PageH
		l := (2 + rng.Float64()*6) * mm
		a := rng.Float64() * 2 * math.Pi
		ops = append(ops, Op{Stroke: true, Path: line(x, y, x+l*math.Cos(a), y+l*math.Sin(a)), Style: st, Paint: black})
	}
	return ops
}

// Short: 20 000 short strokes, 0.35 mm wide (the spec's scene).
func Short() *Scene {
	return &Scene{Name: "short-20000-0.35mm", Clip: pageClip(), Ops: shortStrokes(20000, &stilus.StrokeStyle{Width: 0.35 * mm})}
}

// ShortHair: 20 000 short hairlines.
func ShortHair() *Scene {
	return &Scene{Name: "short-20000-hairline", Clip: pageClip(), Ops: shortStrokes(20000, &stilus.StrokeStyle{Width: 0})}
}

// Glyphs: 30 000 small glyph-like outlines (an "o" with counter) filled
// directly, without a glyph cache: an upper bound for uncached text.
func Glyphs() *Scene {
	ops := make([]Op, 0, 30000)
	h := 2.5 * mm
	cols := int(PageW / (h * 0.7))
	for i := 0; i < 30000; i++ {
		x := 5*mm + float64(i%cols)*h*0.62
		y := 8*mm + float64(i/cols)*h*1.4
		var p stilus.Path
		p.Ellipse(float32(x), float32(y), float32(h*0.3), float32(h*0.45))
		p.Ellipse(float32(x), float32(y), float32(h*0.18), float32(h*0.33))
		ops = append(ops, Op{Path: &p, Rule: stilus.EvenOdd, Paint: black})
	}
	return &Scene{Name: "glyphs-30000-uncached", Clip: pageClip(), Ops: ops}
}

// Mixed: a drawing-like page: frame, filled regions with transparency,
// dashed center lines, curves, circles and dimension lines.
func Mixed() *Scene {
	rng := rand.New(rand.NewSource(7))
	var ops []Op
	thick := &stilus.StrokeStyle{Width: 0.5 * mm, Join: stilus.MiterJoin, MiterLimit: 10}
	thin := &stilus.StrokeStyle{Width: 0.18 * mm, Cap: stilus.RoundCap}
	dash := &stilus.StrokeStyle{Width: 0.25 * mm, Dash: []float64{6 * mm, 1.5 * mm, 1 * mm, 1.5 * mm}}
	var frame stilus.Path
	frame.Rect(12*mm, 12*mm, float32(PageW-24*mm), float32(PageH-24*mm))
	ops = append(ops, Op{Stroke: true, Path: &frame, Style: thick, Paint: black})
	for i := 0; i < 60; i++ {
		var p stilus.Path
		cx, cy := 40*mm+rng.Float64()*(PageW-80*mm), 40*mm+rng.Float64()*(PageH-80*mm)
		n := 3 + rng.Intn(9)
		for k := 0; k < n; k++ {
			a := float64(k) / float64(n) * 2 * math.Pi
			r := (10 + rng.Float64()*40) * mm
			x, y := float32(cx+r*math.Cos(a)), float32(cy+r*math.Sin(a))
			if k == 0 {
				p.MoveTo(x, y)
			} else {
				p.LineTo(x, y)
			}
		}
		p.Close()
		ops = append(ops, Op{Path: &p, Paint: glass})
		ops = append(ops, Op{Stroke: true, Path: &p, Style: thick, Paint: blue})
	}
	for i := 0; i < 400; i++ {
		var p stilus.Path
		cx, cy := rng.Float64()*PageW, rng.Float64()*PageH
		r := float32((1 + rng.Float64()*15) * mm)
		p.Ellipse(float32(cx), float32(cy), r, r)
		ops = append(ops, Op{Stroke: true, Path: &p, Style: thin, Paint: black})
	}
	for i := 0; i < 300; i++ {
		x, y := rng.Float64()*PageW, rng.Float64()*PageH
		ops = append(ops, Op{Stroke: true, Path: line(x, y, x+(rng.Float64()-0.5)*300, y+(rng.Float64()-0.5)*300), Style: dash, Paint: red})
	}
	for i := 0; i < 200; i++ {
		var p stilus.Path
		x, y := float32(rng.Float64()*PageW), float32(rng.Float64()*PageH)
		p.MoveTo(x, y)
		p.CubicTo(x+60, y-80, x+120, y+80, x+180, y)
		ops = append(ops, Op{Stroke: true, Path: &p, Style: thin, Paint: blue})
	}
	return &Scene{Name: "mixed-drawing", Clip: pageClip(), Ops: ops}
}

// Contours: CAD-like outlines – 3 000 open polylines of 6 vertices and
// 600 circles, 0.35 mm wide, miter joins.
func Contours() *Scene {
	rng := rand.New(rand.NewSource(3000))
	st := &stilus.StrokeStyle{Width: 0.35 * mm, Join: stilus.MiterJoin, MiterLimit: 10}
	var ops []Op
	for i := 0; i < 3000; i++ {
		var p stilus.Path
		x, y := rng.Float64()*PageW, rng.Float64()*PageH
		p.MoveTo(float32(x), float32(y))
		a := rng.Float64() * 2 * math.Pi
		for k := 0; k < 5; k++ {
			a += (rng.Float64() - 0.5) * 2.5
			l := (5 + rng.Float64()*15) * mm
			x, y = x+l*math.Cos(a), y+l*math.Sin(a)
			p.LineTo(float32(x), float32(y))
		}
		ops = append(ops, Op{Stroke: true, Path: &p, Style: st, Paint: black})
	}
	for i := 0; i < 600; i++ {
		var p stilus.Path
		r := float32((2 + rng.Float64()*20) * mm)
		p.Ellipse(float32(rng.Float64()*PageW), float32(rng.Float64()*PageH), r, r)
		ops = append(ops, Op{Stroke: true, Path: &p, Style: st, Paint: black})
	}
	return &Scene{Name: "contours-3000", Clip: pageClip(), Ops: ops}
}

// MaskClip: 2 000 hatch hairlines through an elliptical clip mask.
func MaskClip() *Scene {
	var m stilus.Path
	m.Ellipse(PageW/2, PageH/2, PageW*0.4, PageH*0.4)
	return &Scene{Name: "hatch-2000-maskclip", Clip: pageClip(), Mask: &m, Ops: hatch(2000, &stilus.StrokeStyle{Width: 0})}
}

// All returns every scene.
func All() []*Scene {
	return []*Scene{Hatch(), HatchThick(), Short(), ShortHair(), Glyphs(), Mixed(), Contours(), MaskClip()}
}
