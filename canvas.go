package stilus

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
)

// Paint describes what a fill or stroke paints: a solid premultiplied color,
// or a Shader when Shader is non-nil.
type Paint struct {
	Color  color.RGBA // premultiplied
	Shader Shader
}

// Errors reported by Canvas.Err.
var (
	ErrEdgeBudget = errors.New("stilus: edge budget exceeded, path truncated")
	ErrClipDepth  = errors.New("stilus: clip stack too deep")
	ErrInternal   = errors.New("stilus: internal error")
)

// DefaultMaxClipDepth bounds the clip stack of a Canvas.
const DefaultMaxClipDepth = 256

// Canvas draws paths onto a caller-owned *image.RGBA (premultiplied, as
// image.RGBA always is). It combines the rasterizer, the stroker
// and a clip stack, and maps one to one onto a display-list device:
// Fill/Stroke/ClipPath/ClipRect/PopClip.
//
// Device space is the pixel space of the destination image, so a tile or
// band is rendered by passing an image whose Rect is that tile, or by
// restricting Reset's region. A Canvas is not safe for concurrent use; use
// one per worker. After warm-up a Canvas does not allocate.
type Canvas struct {
	// MaxClipDepth overrides DefaultMaxClipDepth when non-zero.
	MaxClipDepth int

	dst    *image.RGBA
	r      Rasterizer
	seg    segFast
	s      Stroker
	solid  SolidBlitter
	shader ShaderBlitter
	mread  maskBlitter
	frac   fracBlitter
	writer maskWriter
	stack  []clipState
	masks  []*clipMask
	tmp    Path
	err    error
}

// NewCanvas returns a canvas drawing onto dst.
func NewCanvas(dst *image.RGBA) *Canvas {
	c := &Canvas{}
	c.Reset(dst, dst.Bounds())
	return c
}

// Reset retargets the canvas to dst, limited to region (in dst's
// coordinates), clears the clip stack and the error.
func (c *Canvas) Reset(dst *image.RGBA, region image.Rectangle) {
	c.dst = dst
	c.solid.t.set(dst)
	c.shader.t.set(dst)
	c.stack = append(c.stack[:0], clipState{bounds: region.Intersect(dst.Bounds()), frac: noFrac})
	c.err = nil
}

// Err returns the first error since Reset, if any. Drawing continues after
// an error; the error only reports that output may be incomplete.
func (c *Canvas) Err() error { return c.err }

func (c *Canvas) setErr(err error) {
	if c.err == nil {
		c.err = err
	}
}

// guard converts a panic into ErrInternal; the canvas stays usable.
func (c *Canvas) guard() {
	if v := recover(); v != nil {
		c.setErr(fmt.Errorf("%w: %v", ErrInternal, v))
		c.r.Reset()
	}
}

func (c *Canvas) top() *clipState { return &c.stack[len(c.stack)-1] }

// Clip returns the device-space bounds of the current clip.
func (c *Canvas) Clip() image.Rectangle { return c.top().bounds }

// chain wraps b with the current clip's mask and border fractions.
func (c *Canvas) chain(st *clipState, b Blitter) Blitter {
	if st.mask != nil {
		c.mread.m, c.mread.next = st.mask, b
		b = &c.mread
	}
	if st.frac != noFrac {
		c.frac.b, c.frac.f, c.frac.next = st.bounds, st.frac, b
		b = &c.frac
	}
	return b
}

func (c *Canvas) paint(p *Paint) Blitter {
	if p.Shader != nil {
		c.shader.s = p.Shader
		return &c.shader
	}
	c.solid.SetColor(p.Color)
	return &c.solid
}

func (c *Canvas) setClip(b image.Rectangle) {
	if c.r.clip != b || len(c.r.acc) == 0 {
		c.r.SetClip(b)
	}
}

func (c *Canvas) checkBudget() {
	if c.r.truncated {
		c.setErr(ErrEdgeBudget)
	}
}

// Fill fills p, transformed by m into device space, with the fill rule.
func (c *Canvas) Fill(p *Path, m Matrix, rule FillRule, paint *Paint) {
	defer c.guard()
	st := c.top()
	if st.bounds.Empty() || (paint.Shader == nil && paint.Color.A == 0) {
		return
	}
	c.setClip(st.bounds)
	c.r.Reset()
	c.r.AddPath(p, m)
	c.checkBudget()
	c.r.Rasterize(rule, c.chain(st, c.paint(paint)))
}

// Stroke strokes p with style st (in user space) under m. Strokes thinner
// than a device pixel are drawn one device pixel wide.
func (c *Canvas) Stroke(p *Path, m Matrix, st *StrokeStyle, paint *Paint) {
	defer c.guard()
	cs := c.top()
	if cs.bounds.Empty() || (paint.Shader == nil && paint.Color.A == 0) || len(p.Points) == 0 {
		return
	}
	sm := sigmaMax(m)
	// Cull against the clip with the widest possible outline extent.
	pad := math.Max(st.Width*sm, 1) / 2 * math.Max(math.Max(st.MiterLimit, 1.5), 1)
	if !(pad < 1<<30) {
		pad = 1 << 30
	}
	pad += 2
	bb := m.transformRect(p.Bounds())
	cb := cs.bounds
	if bb.X1+pad < float64(cb.Min.X) || bb.X0-pad > float64(cb.Max.X) ||
		bb.Y1+pad < float64(cb.Min.Y) || bb.Y0-pad > float64(cb.Max.Y) {
		return
	}
	b := c.chain(cs, c.paint(paint))
	c.setClip(cs.bounds)
	c.r.Reset()
	if paint.Shader == nil && paint.Color.A == 255 {
		// Opaque: the analytic middle rows of straight segments may be
		// composited before the rest of the stroke (the order of opaque
		// layers of one color does not matter).
		c.seg.r, c.seg.b = &c.r, b
		c.s.strokeFast(&c.r, &c.seg, p, m, st)
	} else {
		c.s.Stroke(&c.r, p, m, st)
	}
	c.checkBudget()
	c.r.Rasterize(NonZero, b)
}

func (c *Canvas) push(s clipState) {
	max := c.MaxClipDepth
	if max == 0 {
		max = DefaultMaxClipDepth
	}
	if len(c.stack) >= max {
		c.setErr(ErrClipDepth)
		s = clipState{} // clip everything rather than draw unclipped
	}
	c.stack = append(c.stack, s)
}

// ClipRect intersects the clip with rectangle r in user space. An
// axis-aligned result costs nothing per pixel; a rotated one becomes a mask.
func (c *Canvas) ClipRect(r Rect, m Matrix) {
	defer c.guard()
	if m.axisAligned() && m.finite() {
		c.push(c.top().intersectRect(m.transformRect(r)))
		return
	}
	c.tmp.Reset()
	c.tmp.Rect(float32(r.X0), float32(r.Y0), float32(r.X1-r.X0), float32(r.Y1-r.Y0))
	c.clipMask(&c.tmp, m, NonZero)
}

// ClipPath intersects the clip with p under m. Rectangles ("re W n") take
// the rectangle fast path; other shapes are rasterized once into a mask
// over their bounds.
func (c *Canvas) ClipPath(p *Path, m Matrix, rule FillRule) {
	defer c.guard()
	if r, ok := p.asRect(); ok && m.axisAligned() && m.finite() {
		c.push(c.top().intersectRect(m.transformRect(r)))
		return
	}
	c.clipMask(p, m, rule)
}

func (c *Canvas) clipMask(p *Path, m Matrix, rule FillRule) {
	cur := *c.top()
	if cur.bounds.Empty() || !m.finite() {
		c.push(clipState{})
		return
	}
	bb := m.transformRect(p.Bounds())
	if !(bb.X0 <= bb.X1 && bb.Y0 <= bb.Y1) {
		c.push(clipState{})
		return
	}
	const lim = 1 << 30
	ib := image.Rect(
		int(math.Floor(math.Max(bb.X0, -lim))), int(math.Floor(math.Max(bb.Y0, -lim))),
		int(math.Ceil(math.Min(bb.X1, lim))), int(math.Ceil(math.Min(bb.Y1, lim))),
	).Intersect(cur.bounds)
	if ib.Empty() {
		c.push(clipState{})
		return
	}
	depth := len(c.stack)
	for len(c.masks) <= depth {
		c.masks = append(c.masks, &clipMask{})
	}
	mk := c.masks[depth]
	mk.reset(ib)
	c.writer.m = mk
	c.setClip(ib)
	c.r.Reset()
	c.r.AddPath(p, m)
	c.checkBudget()
	c.r.Rasterize(rule, c.chain(&cur, &c.writer))
	c.push(clipState{bounds: ib, frac: noFrac, mask: mk})
}

// PopClip removes the most recent clip.
func (c *Canvas) PopClip() {
	if len(c.stack) > 1 {
		c.stack = c.stack[:len(c.stack)-1]
	}
}

// ClipDepth returns the number of clips pushed since Reset.
func (c *Canvas) ClipDepth() int { return len(c.stack) - 1 }
