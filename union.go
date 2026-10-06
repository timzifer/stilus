package stilus

import "image"

// Union collects fills and strokes that Canvas.FillUnion paints as one
// shape: their edges are accumulated together and the result is
// composited once.
//
// Drawn one after another, shapes of one paint that overlap or abut along
// antialiased edges leave light seams: each composites its own partial
// coverage over the last, so two edges that together cover a pixel
// completely give 1 − (1 − a)(1 − b) < 1. In a union their areas are summed
// in the accumulator instead, and abutting or overlapping edges cover the
// pixel completely, as one path with several subpaths filled NonZero does.
//
// That works because every element winds the same way. The outlines the
// Stroker produces wind in one direction everywhere they cover, joins and
// caps included (the direction depends only on the sign of the transform's
// determinant, and hairlines, outlined in device space, always wind the
// same way). A fill is taken with the NonZero rule; a path whose area winds
// the other way is reversed as a whole, which keeps its holes. Only a fill
// whose subpaths wind in both directions where they do not overlap (two
// separate squares, one clockwise, one counter-clockwise) still has parts
// that cancel where they overlap other elements.
//
// Elements that cannot be summed are drawn on their own after the union,
// as Canvas.Fill, Canvas.Stroke or Canvas.FillShape draw them: EvenOdd
// fills, and strokes whose dash pattern is too dense to resolve (drawn at
// the pattern's mean coverage).
//
// A Union keeps references to the paths, styles and shapes it is given;
// they must not change until it is drawn. The zero value is empty; Reset
// empties it, keeping its storage, so that it does not allocate once warm.
// FillUnion only reads it, so several canvases may draw one Union at once,
// one band each.
type Union struct {
	ops []unionOp
}

type unionKind uint8

const (
	unionFill unionKind = iota
	unionStroke
	unionShape
)

// unionOp is one element of a union, with what can be derived from it
// once instead of per band.
type unionOp struct {
	kind unionKind
	sep  bool // drawn on its own: EvenOdd, or a dense dash pattern
	neg  bool // its edges are reversed to wind like the other elements
	rule FillRule
	p    *Path
	st   *StrokeStyle
	sh   *Shape
	m    Matrix
	bb   Rect    // device box of the path's points
	pad  float64 // stroke: outline extent beyond bb
	pr   strokePrep
}

// Reset empties the union, keeping its storage.
func (u *Union) Reset() {
	clear(u.ops) // drop the references
	u.ops = u.ops[:0]
}

// Len returns the number of elements added since the last Reset.
func (u *Union) Len() int { return len(u.ops) }

// Fill adds p, transformed by m into device space, filled with rule.
func (u *Union) Fill(p *Path, m Matrix, rule FillRule) {
	if len(p.Points) == 0 || !m.finite() {
		return
	}
	u.ops = append(u.ops, unionOp{
		kind: unionFill, rule: rule, sep: rule != NonZero, p: p, m: m,
		bb:  m.transformRect(p.Bounds()),
		neg: fillReversed(p, m),
	})
}

// Stroke adds the stroke of p with style st (in user space) under m.
func (u *Union) Stroke(p *Path, m Matrix, st *StrokeStyle) {
	if len(p.Points) == 0 || !m.finite() {
		return
	}
	pr := prepStroke(m, st)
	u.ops = append(u.ops, unionOp{
		kind: unionStroke, sep: pr.dense, p: p, st: st, m: m, pr: pr,
		bb:  m.transformRect(p.Bounds()),
		pad: strokePad(st, pr.sm),
		neg: strokeReversed(m, st, pr),
	})
}

// Shape adds the shape s, prepared by SetFill or SetStroke. The shape's
// records are shared with Canvas.FillShape: a union of shapes drawn in
// bands does the stroker's work once per page.
func (u *Union) Shape(s *Shape) {
	op := unionOp{kind: unionShape, sh: s, bb: s.bb, pad: s.pad}
	switch s.kind {
	case shapeFill:
		op.sep = s.rule != NonZero
		op.neg = fillReversed(&s.path, s.m)
	case shapeStroke:
		op.sep = s.pr.dense
		op.neg = strokeReversed(s.m, &s.st, s.pr)
	default:
		return
	}
	u.ops = append(u.ops, op)
}

// strokeReversed reports whether the outline of a stroke under m winds
// against the outlines of strokes under the identity: under a mirroring
// transform, except for hairlines, which are outlined in device space.
func strokeReversed(m Matrix, st *StrokeStyle, pr strokePrep) bool {
	return m.Det() < 0 && !(st.Width*pr.sm < 1)
}

// fillReversed reports whether p under m winds against stroke outlines
// under the identity, which enclose a negative signed area (with the
// shoelace formula's x·y' − x'·y; Stroker builds the left side of a
// polyline forward and its right side backward).
func fillReversed(p *Path, m Matrix) bool {
	return signedArea(p)*m.Det() > 0
}

// signedArea returns twice the signed area p encloses, its subpaths closed
// implicitly: the integral of x dy − y dx along it, exact for curves.
// Coordinates are taken relative to the first point, which keeps the
// products small for paths far from the origin.
func signedArea(p *Path) float64 {
	pts := p.Points
	if len(pts) == 0 {
		return 0
	}
	ox, oy := float64(pts[0].X), float64(pts[0].Y)
	at := func(i int) (float64, float64) { return float64(pts[i].X) - ox, float64(pts[i].Y) - oy }
	cr := func(ax, ay, bx, by float64) float64 { return ax*by - ay*bx }
	var a, sx, sy, cx, cy float64
	pi := 0
	for _, v := range p.Verbs {
		if int(v) >= len(numPoints) || pi+numPoints[v] > len(pts) {
			break
		}
		switch v {
		case MoveTo:
			a += cr(cx, cy, sx, sy)
			sx, sy = at(pi)
			cx, cy = sx, sy
		case LineTo:
			x, y := at(pi)
			a += cr(cx, cy, x, y)
			cx, cy = x, y
		case QuadTo:
			x1, y1 := at(pi)
			x2, y2 := at(pi + 1)
			a += (2*cr(cx, cy, x1, y1) + cr(cx, cy, x2, y2) + 2*cr(x1, y1, x2, y2)) / 3
			cx, cy = x2, y2
		case CubicTo:
			x1, y1 := at(pi)
			x2, y2 := at(pi + 1)
			x3, y3 := at(pi + 2)
			a += (6*cr(cx, cy, x1, y1) + 3*cr(cx, cy, x2, y2) + cr(cx, cy, x3, y3) +
				3*cr(x1, y1, x2, y2) + 3*cr(x1, y1, x3, y3) + 6*cr(x2, y2, x3, y3)) / 10
			cx, cy = x3, y3
		case Close:
			a += cr(cx, cy, sx, sy)
			cx, cy = sx, sy
		}
		pi += numPoints[v]
	}
	return a + cr(cx, cy, sx, sy)
}

// FillUnion paints the union of the elements of u with paint: their
// coverage is accumulated in one pass, NonZero, and composited once (see
// Union). For opaque paint the result is that of drawing them one by one,
// without the seams between them; translucent paint is applied once where
// elements overlap.
//
// Strokes take Canvas.Stroke's analytic path, whatever the paint and the
// clip: the rows between a segment's ends are not edge-walked but their
// pixels' covered areas computed in closed form, and here they are summed
// in the accumulator with everything else instead of being composited
// directly.
func (c *Canvas) FillUnion(u *Union, paint *Paint) {
	defer c.guard()
	cs := c.top()
	if cs.bounds.Empty() || len(u.ops) == 0 || (paint.Shader == nil && paint.Color.A == 0) {
		return
	}
	c.setClip(cs.bounds)
	r := &c.r
	r.Reset()
	c.ustrip.r = r
	sep, trunc, over := false, false, false
	for i := range u.ops {
		op := &u.ops[i]
		if op.sep {
			sep = true
			continue
		}
		n := len(r.edges)
		switch op.kind {
		case unionFill:
			if r.culledBox(op.bb) {
				continue
			}
			r.addPath(op.p, op.m)
		case unionStroke:
			if strokeMisses(op.bb, op.pad, cs.bounds) {
				continue
			}
			c.s.strokeFast(r, &c.ustrip, op.p, op.m, op.st, op.pr)
			trunc = trunc || c.s.Truncated()
		case unionShape:
			s := op.sh
			switch {
			case s.kind == shapeFill:
				if r.culledBox(s.bb) {
					continue
				}
				f := s.form(0, c)
				c.addEdges(f, cs.bounds)
				over = over || f.overflow
			case strokeMisses(s.bb, s.pad, cs.bounds):
				continue
			default:
				f := s.form(0, c)
				c.addEdges(f, cs.bounds)
				c.addAccStrips(f, cs.bounds)
				trunc = trunc || f.truncated
				over = over || f.overflow
			}
		}
		if op.neg {
			r.reverse(n)
		}
	}
	c.checkBudget()
	if over {
		c.setErr(ErrEdgeBudget)
	}
	if trunc {
		c.setErr(ErrDashBudget)
	}
	r.Rasterize(NonZero, c.chain(cs, c.paint(paint)))
	if !sep {
		return
	}
	for i := range u.ops {
		op := &u.ops[i]
		if !op.sep {
			continue
		}
		switch op.kind {
		case unionFill:
			c.Fill(op.p, op.m, op.rule, paint)
		case unionStroke:
			c.Stroke(op.p, op.m, op.st, paint)
		case unionShape:
			c.FillShape(op.sh, paint)
		}
	}
}

// stripSink is the segmentFiller of a union's strokes: it sums the analytic
// rows into the accumulator (Rasterizer.addStrip). Their orientation does
// not matter: after FillUnion's reversals every element winds like a
// stroke under the identity, whose rows add positive coverage.
type stripSink struct{ r *Rasterizer }

func (k *stripSink) middle(ax, ay, bx, by, dx, dy float64, y0, y1 int) {
	var rc rowCtx
	if rc.set(ax, ay, bx, by, dx, dy) {
		k.r.addStrip(&rc, y0, y1)
	}
}

func (k *stripSink) setOrientation(bool)  {}
func (k *stripSink) limitRows(y0, y1 int) { k.r.limitRows(y0, y1) }
func (k *stripSink) unlimitRows()         { k.r.unlimitRows() }

// addAccStrips sums the strips of f that reach the rows of cb into the
// accumulator. The order does not matter, as for addEdges.
func (c *Canvas) addAccStrips(f *shapeForm, cb image.Rectangle) {
	ss := f.strips
	add := func(s *shapeStrip) {
		if int(s.y1) > cb.Min.Y && int(s.y0) < cb.Max.Y {
			c.r.addStrip(&s.rc, int(s.y0), int(s.y1))
		}
	}
	if len(f.sb.start) == 0 {
		for i := range ss {
			add(&ss[i])
		}
		return
	}
	bins := &f.sb
	b0, b1 := bins.span(cb.Min.Y, cb.Max.Y)
	for bi := b0; bi <= b1; bi++ {
		for _, id := range bins.ids[bins.start[bi]:bins.start[bi+1]] {
			// A strip is taken in the first of the bins it shares with cb.
			if bi == b0 || bins.bin(int64(ss[id].y0)) == bi {
				add(&ss[id])
			}
		}
	}
}
