package engine

import (
	"errors"
	"image"
	"math"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// RefFactor is the supersampling factor of the Reference engine per axis.
const RefFactor = 8

// Reference renders synthetic scene pages as the ground truth for accuracy:
// every operation is rasterized at RefFactor times the resolution, its
// coverage summed into the pixels of the page, and the operation then
// composited at the page's resolution with that coverage, in floating
// point.
//
// Supersampling the whole page instead would not be a fair reference:
// coverage-based antialiasing (stilus, PDFium) composites each operation
// with its own coverage, so two shapes sharing a pixel give 1−(1−a)(1−b)
// where the supersampled page gives a+b. Dense hairlines differ by about
// 19/255 that way. Per operation, only the coverage itself is measured.
//
// Strokes thinner than one pixel at the page's resolution are drawn one
// pixel wide there, as stilus and PDFium draw them.
type Reference struct{}

func (Reference) Name() string { return "reference" }
func (Reference) GoHeap() bool { return true }
func (Reference) Close() error { return nil }

func (Reference) Open(data []byte) (Doc, error) {
	s, err := openScene(data)
	if err != nil {
		return nil, errNoReference
	}
	for i := range s.Ops {
		if op := &s.Ops[i]; op.Draw != nil || op.Paint == nil || op.Paint.Shader != nil {
			return nil, errNoReference
		}
	}
	return &refDoc{s: s}, nil
}

var errNoReference = errors.New("reference: only synthetic scene pages of solid fills and strokes")

type refDoc struct{ s *scenes.Scene }

func (d *refDoc) Pages() int { return 1 }
func (d *refDoc) Close()     {}

func (d *refDoc) Render(page int, dpi float64) (image.Image, error) {
	const k = RefFactor
	w, h := int(math.Ceil(scenes.PageW*dpi/72)), int(math.Ceil(scenes.PageH*dpi/72))
	acc := &coverageSum{w: w, sum: make([]uint32, w*h)}
	// Page colour, premultiplied, in [0, 1]: white.
	pix := make([][3]float64, w*h)
	for i := range pix {
		pix[i] = [3]float64{1, 1, 1}
	}

	c := stilus.NewCanvas(image.NewRGBA(image.Rectangle{}))
	c.ResetCompositor(acc, image.Rect(0, 0, w*k, h*k))
	m := stilus.Scale(k*dpi/72, k*dpi/72)
	c.ClipRect(d.s.Clip, m)
	if d.s.Mask != nil {
		c.ClipPath(d.s.Mask, m, stilus.NonZero)
	}
	for i := range d.s.Ops {
		op := &d.s.Ops[i]
		if op.Stroke {
			st := op.Style
			if px := st.Width * dpi / 72; px < 1 {
				wide := *st
				wide.Width = 72 / dpi
				st = &wide
			}
			c.Stroke(op.Path, m, st, op.Paint)
		} else {
			c.Fill(op.Path, m, op.Rule, op.Paint)
		}
		if err := c.Err(); err != nil {
			return nil, err
		}
		col := op.Paint.Color // premultiplied
		r, g, b, a := float64(col.R)/255, float64(col.G)/255, float64(col.B)/255, float64(col.A)/255
		for _, j := range acc.touched {
			cov := float64(acc.sum[j]) / (255 * k * k)
			acc.sum[j] = 0
			p := &pix[j]
			p[0] = r*cov + p[0]*(1-a*cov)
			p[1] = g*cov + p[1]*(1-a*cov)
			p[2] = b*cov + p[2]*(1-a*cov)
		}
		acc.touched = acc.touched[:0]
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, p := range pix {
		q := img.Pix[4*i:]
		q[0], q[1], q[2], q[3] = to8(p[0]), to8(p[1]), to8(p[2]), 0xff
	}
	return img, nil
}

func to8(v float64) uint8 { return uint8(math.Round(min(max(v, 0), 1) * 255)) }

// coverageSum is a stilus.Compositor at RefFactor times the resolution of
// the page that sums the coverage of an operation into the page's pixels.
type coverageSum struct {
	w       int
	sum     []uint32
	touched []int
}

func (s *coverageSum) Blitter(*stilus.Paint) stilus.Blitter { return s }

func (s *coverageSum) add(y, x int, a uint8) {
	if a == 0 {
		return
	}
	j := (y/RefFactor)*s.w + x/RefFactor
	if s.sum[j] == 0 {
		s.touched = append(s.touched, j)
	}
	s.sum[j] += uint32(a)
}

func (s *coverageSum) BlitRun(y, x0, x1 int, a uint8) {
	for x := x0; x < x1; x++ {
		s.add(y, x, a)
	}
}

func (s *coverageSum) BlitCoverage(y, x int, cov []uint8) {
	for i, a := range cov {
		s.add(y, x+i, a)
	}
}
