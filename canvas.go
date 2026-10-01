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
	ErrDashBudget = errors.New("stilus: dash budget exceeded, stroke truncated")
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
	scale  scaleBlitter
	writer maskWriter
	stack  []clipState
	masks  []*clipMask
	// overflow counts clips pushed beyond MaxClipDepth; they clip away
	// everything (empty) without growing the stack.
	overflow int
	empty    clipState
	tmp      Path
	err      error
	sink     uint32 // keeps touchStroke's loads
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
	c.stack = append(c.stack[:0], newClipState(region.Intersect(dst.Bounds())))
	c.overflow = 0
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
		c.r.discard()
	}
}

// clipGuard is guard for the clip operations: when the panic struck before
// the clip was pushed, an empty one is pushed instead, so that the
// caller's PopClip stays balanced.
func (c *Canvas) clipGuard(depth int) {
	if v := recover(); v != nil {
		c.setErr(fmt.Errorf("%w: %v", ErrInternal, v))
		c.r.discard()
		if c.ClipDepth() == depth {
			c.push(clipState{})
		}
	}
}

func (c *Canvas) top() *clipState {
	if c.overflow > 0 {
		return &c.empty
	}
	return &c.stack[len(c.stack)-1]
}

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
	if c.fillRect(p, m, st, paint) {
		return
	}
	c.r.AddPath(p, m)
	c.checkBudget()
	c.r.Rasterize(rule, c.chain(st, c.paint(paint)))
}

// fillRect bypasses edge accumulation for pixel-aligned rectangles. Keep
// fractional geometry and small edge budgets on the rasterizer path.
func (c *Canvas) fillRect(p *Path, m Matrix, st *clipState, paint *Paint) bool {
	r, ok := p.asRect()
	if !ok || !m.axisAligned() || !m.finite() || (c.r.MaxEdges != 0 && c.r.MaxEdges < 2) {
		return false
	}
	r = m.transformRect(r)
	for _, v := range [4]float64{r.X0, r.Y0, r.X1, r.Y1} {
		if !(math.Abs(v) < 1<<30) || v != math.Trunc(v) {
			return false
		}
	}
	bounds := image.Rect(int(r.X0), int(r.Y0), int(r.X1), int(r.Y1)).Intersect(st.bounds)
	if !bounds.Empty() {
		b := c.chain(st, c.paint(paint))
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			b.BlitRun(y, bounds.Min.X, bounds.Max.X, 255)
		}
	}
	return true
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
	pad := max(st.Width*sm, 1) / 2 * max(max(st.MiterLimit, 1.5), 1)
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
	c.touchStroke(p, m, max(st.Width*sm, 1)/2)
	b := c.paint(paint)
	f, dense := denseDash(m, st)
	if dense {
		// Drawn as a solid stroke at the pattern's mean coverage.
		c.scale.f, c.scale.next = uint32(math.Round(f*255)), b
		if c.scale.f == 0 {
			return
		}
		b = &c.scale
	}
	b = c.chain(cs, b)
	c.setClip(cs.bounds)
	c.r.Reset()
	overlap := mayOverlap(p, st)
	if !dense && paint.Shader == nil && paint.Color.A == 255 && (cs.mask == nil || !overlap) {
		// Opaque: the analytic middle rows may be composited separately
		// from the rest of the stroke, because layers of one opaque color
		// at full coverage are idempotent. Where parts of the stroke may
		// overlap and a clip reduces coverage, that no longer holds: at a
		// rectangle clip's fractional border the pixels are summed in the
		// accumulator instead, and under a mask the outline path is used.
		c.seg.r, c.seg.b = &c.r, b
		c.seg.solid = nil
		c.seg.frac = cs.frac
		if cs.mask == nil {
			c.seg.solid = &c.solid
		}
		c.seg.setBorder(cs, overlap)
		c.s.strokeFast(&c.r, &c.seg, p, m, st)
	} else {
		c.s.Stroke(&c.r, p, m, st)
	}
	c.checkBudget()
	if c.s.Truncated() {
		c.setErr(ErrDashBudget)
	}
	c.r.Rasterize(NonZero, b)
}

// touchStroke loads the first touchRows rows of segments up to
// touchMaxRows rows tall.
const (
	touchRows    = 64
	touchMaxRows = 256
)

// touchStroke reads, before a stroke is composited, the destination words
// under the first rows of each of its short segments (w is the device
// half-width).
//
// Scattered short strokes are bound by memory latency: each row of the
// stroke is a cold cache line and often a cold page, and compositing
// fetches them one after another, because the arithmetic between two rows
// fills the out-of-order window. These loads are independent, so their
// misses overlap, and compositing then finds the rows in cache. Further
// down a segment, and along long ones, the hardware prefetcher has picked
// up the row stride. Curves are followed along their control polygon,
// which is close enough for a prefetch.
func (c *Canvas) touchStroke(p *Path, m Matrix, w float64) {
	cb := c.top().bounds
	t := &c.solid.t
	var s uint32
	var px, py float64
	pi := 0
	for _, v := range p.Verbs {
		if int(v) >= len(numPoints) || pi+numPoints[v] > len(p.Points) {
			break
		}
		for _, q := range p.Points[pi : pi+numPoints[v]] {
			x, y := m.Apply(float64(q.X), float64(q.Y))
			if v != MoveTo {
				s += touchSegment(t, cb, px, py, x, y, w)
			}
			px, py = x, y
		}
		pi += numPoints[v]
	}
	c.sink += s
}

func touchSegment(t *target, cb image.Rectangle, x0, y0, x1, y1, w float64) uint32 {
	fy0, fy1 := min(y0, y1)-w, max(y0, y1)+w
	if !(fy0 > -1<<30 && fy1 < fy0+touchMaxRows) {
		return 0 // long (the prefetcher follows it), or NaN
	}
	iy0 := max(int(fy0), cb.Min.Y)
	iy1 := min(int(fy1)+1, cb.Max.Y, iy0+touchRows)
	if iy0 >= iy1 {
		return 0
	}
	var dxdy float64
	if y1 != y0 {
		dxdy = (x1 - x0) / (y1 - y0)
	}
	// The loads of a row can only overlap with those of the rows that fit
	// in the reorder buffer with it, so the loop is kept to few
	// instructions: the segment's x per row by a running sum, clamped to
	// its ends, the row's offset by a running sum, and one load at each
	// end of the touched stretch (rows within a few pixels of it share a
	// cache line with their neighbours). Wider strokes load every 16th
	// pixel of the stretch. No index is used before it is clamped to cb.
	lo, hi := min(x0, x1), max(x0, x1)
	xc := x0 + (float64(iy0)+0.5-y0)*dxdy
	pix, stride := t.pix, t.stride
	o := (iy0-t.oy)*stride - t.ox
	xmin, xmax := cb.Min.X, cb.Max.X-1
	iw := int(w) + 1
	var s uint32
	if iw <= 8 {
		for y := iy0; y < iy1; y++ {
			c := xc
			if c < lo {
				c = lo
			} else if c > hi {
				c = hi
			}
			ic := int(c)
			a, b := ic-iw, ic+iw
			if a < xmin {
				a = xmin
			}
			if b > xmax {
				b = xmax
			}
			if a <= b {
				s += pix[o+a] + pix[o+b]
			}
			xc += dxdy
			o += stride
		}
		return s
	}
	for y := iy0; y < iy1; y++ {
		c := xc
		if c < lo {
			c = lo
		} else if c > hi {
			c = hi
		}
		ic := int(c)
		a, b := ic-iw, ic+iw
		if a < xmin {
			a = xmin
		}
		if b > xmax {
			b = xmax
		}
		for k := a; k <= b; k += 16 {
			s += pix[o+k]
		}
		if a <= b {
			s += pix[o+b]
		}
		xc += dxdy
		o += stride
	}
	return s
}

// full reports whether the clip stack is at MaxClipDepth.
func (c *Canvas) full() bool {
	max := c.MaxClipDepth
	if max == 0 {
		max = DefaultMaxClipDepth
	}
	return c.overflow > 0 || len(c.stack) >= max
}

func (c *Canvas) push(s clipState) {
	if c.full() {
		c.setErr(ErrClipDepth)
		c.overflow++ // clip everything rather than draw unclipped
		return
	}
	c.stack = append(c.stack, s)
}

// ClipRect intersects the clip with rectangle r in user space. An
// axis-aligned result costs nothing per pixel; a rotated one becomes a mask.
func (c *Canvas) ClipRect(r Rect, m Matrix) {
	defer c.clipGuard(c.ClipDepth())
	if r.Empty() {
		c.push(clipState{})
		return
	}
	if m.axisAligned() && m.finite() {
		c.push(c.top().intersectRect(m.transformRect(r)))
		return
	}
	c.tmp.Reset()
	c.tmp.Rect(float32(r.X0), float32(r.Y0), float32(r.X1-r.X0), float32(r.Y1-r.Y0))
	c.clipMask(&c.tmp, m, NonZero, nil)
}

// ClipPath intersects the clip with p under m. Rectangles ("re W n") take
// the rectangle fast path; other shapes are rasterized once into a mask
// over their bounds.
func (c *Canvas) ClipPath(p *Path, m Matrix, rule FillRule) {
	defer c.clipGuard(c.ClipDepth())
	if r, ok := p.asRect(); ok && m.axisAligned() && m.finite() {
		c.push(c.top().intersectRect(m.transformRect(r)))
		return
	}
	c.clipMask(p, m, rule, nil)
}

// ClipStroke intersects the clip with the area a stroke of p with style st
// (in user space) under m paints, with the stroke's coverage: text in a
// stroking clip mode, or a stroke painted with a shader that only reaches
// the stroke as a clip. The area is rasterized once into a mask.
func (c *Canvas) ClipStroke(p *Path, m Matrix, st *StrokeStyle) {
	defer c.clipGuard(c.ClipDepth())
	if len(p.Points) == 0 {
		c.push(clipState{})
		return
	}
	c.clipMask(p, m, NonZero, st)
}

// clipMask pushes the clip of p filled with rule, or stroked with st if st
// is not nil.
func (c *Canvas) clipMask(p *Path, m Matrix, rule FillRule, st *StrokeStyle) {
	cur := *c.top()
	if c.full() || cur.bounds.Empty() || !m.finite() {
		c.push(clipState{})
		return
	}
	bb := m.transformRect(p.Bounds())
	if st != nil {
		pad := max(st.Width*sigmaMax(m), 1)/2*max(st.MiterLimit, 1.5) + 2
		if !(pad < 1<<30) {
			pad = 1 << 30
		}
		bb = Rect{bb.X0 - pad, bb.Y0 - pad, bb.X1 + pad, bb.Y1 + pad}
	}
	const lim = 1 << 30
	// NaN boxes, and boxes entirely beyond the coordinate limit (infinite
	// ones included), clip everything away.
	if !(bb.X0 <= bb.X1 && bb.Y0 <= bb.Y1 && bb.X1 >= -lim && bb.X0 <= lim && bb.Y1 >= -lim && bb.Y0 <= lim) {
		c.push(clipState{})
		return
	}
	ib := image.Rect(
		int(math.Floor(max(bb.X0, -lim))), int(math.Floor(max(bb.Y0, -lim))),
		int(math.Ceil(min(bb.X1, lim))), int(math.Ceil(min(bb.Y1, lim))),
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
	// The mask holds the path's coverage times the enclosing masks; the
	// rectangle clips stay exact in rect and are applied when drawing.
	var b Blitter = &c.writer
	if st != nil {
		if f, dense := denseDash(m, st); dense {
			c.scale.f, c.scale.next = uint32(math.Round(f*255)), b
			b = &c.scale
		}
		c.s.Stroke(&c.r, p, m, st)
		if c.s.Truncated() {
			c.setErr(ErrDashBudget)
		}
		rule = NonZero
	} else {
		c.r.AddPath(p, m)
	}
	c.checkBudget()
	if cur.mask != nil {
		c.mread.m, c.mread.next = cur.mask, b
		b = &c.mread
	}
	c.r.Rasterize(rule, b)
	n := cur
	n.mask = mk
	n.lim = cur.lim.Intersect(ib)
	n.derive()
	c.push(n)
}

// PopClip removes the most recent clip.
func (c *Canvas) PopClip() {
	if c.overflow > 0 {
		c.overflow--
		return
	}
	if len(c.stack) > 1 {
		c.stack = c.stack[:len(c.stack)-1]
	}
}

// ClipDepth returns the number of clips pushed since Reset.
func (c *Canvas) ClipDepth() int { return len(c.stack) - 1 + c.overflow }

// mayOverlap reports whether separate parts of a stroke of p can cover the
// same pixel outside the bands around its corners: several subpaths,
// curves (a cubic can loop), dashes, or three or more segments. A single
// segment or a two-segment polyline cannot.
func mayOverlap(p *Path, st *StrokeStyle) bool {
	if len(st.Dash) > 0 {
		return true
	}
	moves, segs := 0, 0
	for _, v := range p.Verbs {
		switch v {
		case MoveTo:
			moves++
		case LineTo, Close:
			segs++
		default:
			return true
		}
	}
	return moves > 1 || segs > 2
}
