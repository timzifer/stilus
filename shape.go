package stilus

import (
	"image"
	"math"
	"slices"
	"sync"
	"sync/atomic"
)

// Shape is a path prepared for drawing under one transform: its device
// bounds, and its edges or strips binned by bands of rows, so that a
// canvas drawing it touches only what crosses its clip's rows. The zero
// value is empty; SetFill and SetStroke (re)build it, keeping storage.
// After that it may be drawn by several canvases at once. It copies what
// it needs from the path and the style and keeps no reference to them.
//
// A shape serves a page rendered in bands, one canvas each, every band
// playing the whole operation list: the transform of the points,
// flattening, dashing and the stroke outline with its joins and caps are
// done once, and a band only replays the edges and analytic strips that
// cross its rows. They are made by the first FillShape that needs them
// (a stroke has two forms, for the analytic and the outline path, and a
// draw uses one), so SetFill and SetStroke cost little more than copying
// the path, and the work is done by the canvases' workers.
//
// Canvas.FillShape draws the bytes Canvas.Fill or Canvas.Stroke draw with
// the same path, transform and style, except that Stroke replaces curves
// lying entirely outside its clip by their chords, which moves a curved
// stroke's analytic rows (by a few levels of coverage) where the clip
// cuts it. Drawn in bands, a shape stays within 3/255 of the shape drawn
// whole; Stroke, per band, does not.
type Shape struct {
	kind   shapeKind
	rule   FillRule
	bb     Rect    // device box of the path's points
	pad    float64 // stroke: outline extent beyond bb
	bounds image.Rectangle

	// Fill: the edges Rasterizer.AddPath adds (forms[0]), or a
	// pixel-aligned rectangle (Canvas.Fill's fillRect).
	rect   image.Rectangle
	isRect bool

	// Stroke: what the stroker emits on the analytic path (forms[0]) and
	// on the outline path (forms[1]); Canvas.FillShape picks one as
	// Canvas.Stroke does.
	pr              strokePrep
	overlap, single bool

	// What the forms are made from.
	path Path
	m    Matrix
	st   StrokeStyle
	dash []float64

	mu    sync.Mutex // held while a form is made
	forms [2]shapeForm
}

type shapeKind uint8

const (
	shapeEmpty shapeKind = iota
	shapeFill
	shapeStroke
)

// shapeForm is recorded geometry: device-space edges for the rasterizer,
// each limited to rows [lo, hi), and analytic strips, each binned by rows.
// It is made once (made), and then only read.
type shapeForm struct {
	made      atomic.Bool
	edges     []shapeEdge
	strips    []shapeStrip
	eb, sb    rowBins
	truncated bool // the stroker exceeded its dash budget
}

// shapeEdge is a line as passed to Rasterizer.AddLine, limited to rows
// [lo, hi) (math.MinInt32 and math.MaxInt32 when unlimited). It can only
// reach rows [r0, r1).
type shapeEdge struct {
	x0, y0, x1, y1 float64
	r0, r1, lo, hi int32
}

// shapeStrip is the analytic rows [y0, y1) of a stroke segment, prepared
// by rowCtx.set; neg is the outline's orientation (segFast.setOrientation).
type shapeStrip struct {
	rc     rowCtx
	y0, y1 int32
	neg    bool
}

func (f *shapeForm) reset() {
	f.edges, f.strips = f.edges[:0], f.strips[:0]
	f.truncated = false
	f.made.Store(false)
}

// bin bins the recorded edges and strips by rows; rows is scratch.
func (f *shapeForm) bin(rows *[]int32) {
	r := (*rows)[:0]
	for i := range f.edges {
		r = append(r, f.edges[i].r0, f.edges[i].r1)
	}
	f.eb.build(r)
	r = r[:0]
	for i := range f.strips {
		r = append(r, f.strips[i].y0, f.strips[i].y1)
	}
	f.sb.build(r)
	*rows = r
}

// rowBins lists record ids per band of 2^shift rows from row0: bin b holds
// ids[start[b]:start[b+1]], in increasing order. A record is listed in
// every bin its rows reach. Without bins (start empty), every record is
// visited.
type rowBins struct {
	row0  int64
	shift uint
	start []int32
	ids   []int32
}

// A form is not binned when it has fewer than shapeBinMin records or
// spans fewer than shapeBinRows rows: a short list, or one whose records
// all reach most bands of a page, is filtered faster than bins are looked
// up.
const (
	shapeBinMin  = 16
	shapeBinRows = 64
)

// build bins the records whose rows [r0, r1) are given in pairs by rows.
// The bins are 16 rows tall or taller, while there would be more bins
// than records or more than four entries per record.
func (b *rowBins) build(rows []int32) {
	b.start, b.ids = b.start[:0], b.ids[:0]
	n := len(rows) / 2
	if n < shapeBinMin {
		return
	}
	lo, hi := int64(math.MaxInt32), int64(math.MinInt32)
	for i := 0; i < len(rows); i += 2 {
		lo, hi = min(lo, int64(rows[i])), max(hi, int64(rows[i+1]))
	}
	if hi-lo < shapeBinRows {
		return
	}
	shift := uint(4)
	total := 0
	for ; ; shift++ {
		if (hi-1-lo)>>shift+1 > int64(n) && shift < 40 {
			continue
		}
		total = 0
		for i := 0; i < len(rows); i += 2 {
			total += int((int64(rows[i+1])-1-lo)>>shift - (int64(rows[i])-lo)>>shift + 1)
		}
		if total <= 4*n || shift >= 40 {
			break
		}
	}
	b.row0, b.shift = lo, shift
	nb := int((hi-1-lo)>>shift + 1)
	// Count into start[k+2], sum, then place each id at start[k+1], which
	// leaves start[k+1] at the end of bin k (as meshBins).
	b.start = grow32(b.start, nb+2)
	clear(b.start)
	for i := 0; i < len(rows); i += 2 {
		for k := b.bin(int64(rows[i])); k <= b.bin(int64(rows[i+1])-1); k++ {
			b.start[k+2]++
		}
	}
	for k := 2; k < len(b.start); k++ {
		b.start[k] += b.start[k-1]
	}
	b.ids = grow32(b.ids, total)
	for i := 0; i < len(rows); i += 2 {
		for k := b.bin(int64(rows[i])); k <= b.bin(int64(rows[i+1])-1); k++ {
			b.ids[b.start[k+1]] = int32(i / 2)
			b.start[k+1]++
		}
	}
	b.start = b.start[:nb+1]
}

func (b *rowBins) bin(r int64) int { return int((r - b.row0) >> b.shift) }

// span returns the bins [b0, b1] that rows [y0, y1) reach; b0 > b1 when
// none.
func (b *rowBins) span(y0, y1 int) (int, int) {
	nb := len(b.start) - 1
	lo, hi := int64(y0)-b.row0, int64(y1)-1-b.row0
	if hi < 0 || lo>>b.shift >= int64(nb) || y0 >= y1 {
		return 1, 0
	}
	return int(max(lo, 0) >> b.shift), int(min(hi>>b.shift, int64(nb-1)))
}

// shapeRec records geometry into a shapeForm. As a LineSink and
// segmentFiller it takes the place of the canvas's rasterizer and segFast
// for the stroker, which then culls nothing.
type shapeRec struct {
	f      *shapeForm
	lo, hi int32
	neg    bool
}

func (r *shapeRec) start(f *shapeForm) {
	r.f, r.neg = f, false
	r.unlimitRows()
}

// rowOf clamps a finite device y to the rows a shape records.
func rowOf(y float64) int32 { return int32(min(max(y, -1<<30), 1<<30)) }

// AddLine records a line as Rasterizer.AddLine would take it, under the
// current row limit. Lines the rasterizer would drop at any clip are not
// recorded.
func (r *shapeRec) AddLine(x0, y0, x1, y1 float64) {
	if y0 == y1 || !(math.Abs(x0) <= math.MaxFloat64 && math.Abs(x1) <= math.MaxFloat64 &&
		math.Abs(y0) <= math.MaxFloat64 && math.Abs(y1) <= math.MaxFloat64) {
		return
	}
	r0 := max(rowOf(math.Floor(min(y0, y1))), r.lo)
	r1 := min(rowOf(math.Ceil(max(y0, y1))), r.hi)
	if r0 >= r1 {
		return
	}
	r.f.edges = append(r.f.edges, shapeEdge{x0, y0, x1, y1, r0, r1, r.lo, r.hi})
}

func (r *shapeRec) middle(ax, ay, bx, by, dx, dy float64, y0, y1 int) {
	if y0 >= y1 {
		return
	}
	s := shapeStrip{y0: int32(y0), y1: int32(y1), neg: r.neg}
	if s.rc.set(ax, ay, bx, by, dx, dy) {
		r.f.strips = append(r.f.strips, s)
	}
}

func (r *shapeRec) setOrientation(neg bool) { r.neg = neg }
func (r *shapeRec) limitRows(y0, y1 int)    { r.lo, r.hi = int32(y0), int32(y1) }
func (r *shapeRec) unlimitRows()            { r.lo, r.hi = math.MinInt32, math.MaxInt32 }

// addPath records the lines Rasterizer.AddPath adds for p under m (a
// finite transform), at any clip: curves are always flattened, where
// AddPath takes the chord of a curve outside its clip, which covers the
// same pixels inside it.
func (r *shapeRec) addPath(p *Path, m Matrix) {
	ident := m == Identity
	pts := p.Points
	var sx, sy, cx, cy float64 // subpath start, current point
	open := false
	pi := 0
	bad := false
	tr := func(q Point) (float64, float64) {
		x, y := float64(q.X), float64(q.Y)
		if !ident {
			x, y = m.Apply(x, y)
		}
		if !(math.Abs(x) <= math.MaxFloat64 && math.Abs(y) <= math.MaxFloat64) {
			bad = true
		}
		return x, y
	}
	for _, v := range p.Verbs {
		if int(v) >= len(numPoints) || pi+numPoints[v] > len(pts) {
			break
		}
		switch v {
		case MoveTo:
			if open {
				r.AddLine(cx, cy, sx, sy)
			}
			sx, sy = tr(pts[pi])
			cx, cy = sx, sy
			open = true
		case LineTo:
			x, y := tr(pts[pi])
			r.AddLine(cx, cy, x, y)
			cx, cy = x, y
			open = true
		case QuadTo:
			x1, y1 := tr(pts[pi])
			x2, y2 := tr(pts[pi+1])
			r.addQuad(cx, cy, x1, y1, x2, y2)
			cx, cy = x2, y2
			open = true
		case CubicTo:
			x1, y1 := tr(pts[pi])
			x2, y2 := tr(pts[pi+1])
			x3, y3 := tr(pts[pi+2])
			r.addCubic(cx, cy, x1, y1, x2, y2, x3, y3)
			cx, cy = x3, y3
			open = true
		case Close:
			if open {
				r.AddLine(cx, cy, sx, sy)
			}
			cx, cy = sx, sy
		}
		if bad {
			// AddPath ignores a path with a non-finite point.
			r.f.edges = r.f.edges[:0]
			return
		}
		pi += numPoints[v]
	}
	if open {
		r.AddLine(cx, cy, sx, sy)
	}
}

// addQuad is Rasterizer.addQuad without the clip.
func (r *shapeRec) addQuad(x0, y0, x1, y1, x2, y2 float64) {
	if beyond(x0, y0, x1, y1, x2, y2) {
		r.addQuad(clampCoord(x0), clampCoord(y0), clampCoord(x1), clampCoord(y1), clampCoord(x2), clampCoord(y2))
		return
	}
	n, ax, ay, bx, by := quadPoly(x0, y0, x1, y1, x2, y2)
	if n <= 1 {
		r.AddLine(x0, y0, x2, y2)
		return
	}
	dt := 1 / float64(n)
	px, py := x0, y0
	for i := 1; i < n; i++ {
		t := float64(i) * dt
		qx := (ax*t+bx)*t + x0
		qy := (ay*t+by)*t + y0
		r.AddLine(px, py, qx, qy)
		px, py = qx, qy
	}
	r.AddLine(px, py, x2, y2)
}

// addCubic is Rasterizer.addCubic without the clip.
func (r *shapeRec) addCubic(x0, y0, x1, y1, x2, y2, x3, y3 float64) {
	if beyond(x0, y0, x1, y1, x2, y2, x3, y3) {
		r.addCubic(clampCoord(x0), clampCoord(y0), clampCoord(x1), clampCoord(y1),
			clampCoord(x2), clampCoord(y2), clampCoord(x3), clampCoord(y3))
		return
	}
	n, ax, ay, bx, by, cx, cy := cubicPoly(x0, y0, x1, y1, x2, y2, x3, y3)
	if n <= 1 {
		r.AddLine(x0, y0, x3, y3)
		return
	}
	dt := 1 / float64(n)
	px, py := x0, y0
	for i := 1; i < n; i++ {
		t := float64(i) * dt
		qx := ((ax*t+bx)*t+cx)*t + x0
		qy := ((ay*t+by)*t+cy)*t + y0
		r.AddLine(px, py, qx, qy)
		px, py = qx, qy
	}
	r.AddLine(px, py, x3, y3)
}

func (s *Shape) reset() {
	s.kind, s.isRect = shapeEmpty, false
	s.bounds = image.Rectangle{}
	s.forms[0].reset()
	s.forms[1].reset()
}

// SetFill prepares p, transformed by m into device space, to be filled
// with rule. It reports false, leaving the shape empty, for a non-finite
// transform.
func (s *Shape) SetFill(p *Path, m Matrix, rule FillRule) bool {
	s.reset()
	if !m.finite() {
		return false
	}
	if len(p.Points) == 0 {
		return true // AddPath culls it
	}
	s.kind, s.rule, s.m = shapeFill, rule, m
	s.copyPath(p)
	s.bb = m.transformRect(p.Bounds())
	if r, ok := p.asRect(); ok && m.axisAligned() {
		if ir, ok := pixelRect(m.transformRect(r)); ok {
			s.rect, s.isRect = ir, true
		}
	}
	s.bounds = boxPixels(s.bb, 0)
	return true
}

// pixelRect returns r as integer pixels if its sides lie on pixel borders.
func pixelRect(r Rect) (image.Rectangle, bool) {
	for _, v := range [4]float64{r.X0, r.Y0, r.X1, r.Y1} {
		if !(math.Abs(v) < 1<<30) || v != math.Trunc(v) {
			return image.Rectangle{}, false
		}
	}
	return image.Rect(int(r.X0), int(r.Y0), int(r.X1), int(r.Y1)), true
}

// boxPixels returns the pixels the device box bb widened by pad touches,
// within ±2^30; all of them if bb has NaNs.
func boxPixels(bb Rect, pad float64) image.Rectangle {
	const lim = 1 << 30
	r := image.Rect(-lim, -lim, lim, lim)
	if v := math.Floor(bb.X0 - pad); v > -lim {
		r.Min.X = int(min(v, lim))
	}
	if v := math.Floor(bb.Y0 - pad); v > -lim {
		r.Min.Y = int(min(v, lim))
	}
	if v := math.Ceil(bb.X1 + pad); v < lim {
		r.Max.X = int(max(v, -lim))
	}
	if v := math.Ceil(bb.Y1 + pad); v < lim {
		r.Max.Y = int(max(v, -lim))
	}
	return r.Canon()
}

func (s *Shape) copyPath(p *Path) {
	s.path.Verbs = append(s.path.Verbs[:0], p.Verbs...)
	s.path.Points = append(s.path.Points[:0], p.Points...)
}

// SetStroke prepares the stroke of p with style st (in user space) under
// m. It reports false, leaving the shape empty, for a non-finite
// transform.
func (s *Shape) SetStroke(p *Path, m Matrix, st *StrokeStyle) bool {
	s.reset()
	if !m.finite() {
		return false
	}
	if len(p.Points) == 0 {
		return true // Stroke draws nothing
	}
	s.kind, s.m = shapeStroke, m
	s.copyPath(p)
	s.st = *st
	s.dash = append(s.dash[:0], st.Dash...)
	s.st.Dash = s.dash
	s.pr = prepStroke(m, st)
	s.pad = strokePad(st, s.pr.sm)
	s.bb = m.transformRect(p.Bounds())
	s.overlap, s.single = mayOverlap(p, st), singleSegment(p, st)
	s.bounds = boxPixels(s.bb, s.pad)
	return true
}

// form returns form i, made on first use with c's stroker and scratch.
func (s *Shape) form(i int, c *Canvas) *shapeForm {
	f := &s.forms[i]
	if !f.made.Load() {
		s.make(f, i, c)
	}
	return f
}

func (s *Shape) make(f *shapeForm, i int, c *Canvas) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.made.Load() {
		return
	}
	rec := &c.rec
	rec.start(f)
	switch {
	case s.kind == shapeFill:
		rec.addPath(&s.path, s.m)
	case i == 0:
		c.s.strokeFast(rec, rec, &s.path, s.m, &s.st, s.pr)
		f.truncated = c.s.Truncated()
	default:
		c.s.stroke(rec, &s.path, s.m, &s.st, s.pr)
		f.truncated = c.s.Truncated()
	}
	f.bin(&c.ids)
	rec.f = nil
	f.made.Store(true)
}

// Bounds returns the device pixels the shape can touch.
func (s *Shape) Bounds() image.Rectangle { return s.bounds }

// FillShape draws the shape s, prepared by SetFill or SetStroke, with
// paint: the bytes Fill or Stroke draw with the same path, transform and
// style, at the cost of the records that cross the clip's rows.
func (c *Canvas) FillShape(s *Shape, paint *Paint) {
	defer c.guard()
	cs := c.top()
	if cs.bounds.Empty() || (paint.Shader == nil && paint.Color.A == 0) {
		return
	}
	switch s.kind {
	case shapeFill:
		c.setClip(cs.bounds)
		c.r.Reset()
		if s.isRect && !(c.r.MaxEdges != 0 && c.r.MaxEdges < 2) {
			// As Canvas.fillRect.
			if bounds := s.rect.Intersect(cs.bounds); !bounds.Empty() {
				b := c.chain(cs, c.paint(paint))
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					b.BlitRun(y, bounds.Min.X, bounds.Max.X, 255)
				}
			}
			return
		}
		if !c.r.culledBox(s.bb) {
			c.addEdges(s.form(0, c), cs.bounds)
		}
		c.checkBudget()
		c.r.Rasterize(s.rule, c.chain(cs, c.paint(paint)))
	case shapeStroke:
		if strokeMisses(s.bb, s.pad, cs.bounds) {
			return
		}
		b, ok := c.strokePaint(cs, paint, s.pr)
		if !ok {
			return
		}
		c.setClip(cs.bounds)
		c.r.Reset()
		if c.strokeFast(cs, b, paint, s.pr, s.overlap, s.single) {
			// The edges go to the accumulator between the loads of the
			// destination under the strips and their compositing, which
			// gives the loads time to arrive (see touchStroke).
			f := s.form(0, c)
			c.addStrips(f, cs.bounds, true)
			c.addEdges(f, cs.bounds)
			c.addStrips(f, cs.bounds, false)
			c.strokeDone(f, b)
			return
		}
		c.addEdges(s.form(1, c), cs.bounds)
		c.strokeDone(s.form(1, c), b)
	}
}

// strokeDone rasterizes the edges of a stroke drawn from form f.
func (c *Canvas) strokeDone(f *shapeForm, b Blitter) {
	c.checkBudget()
	if f.truncated {
		c.setErr(ErrDashBudget)
	}
	c.r.Rasterize(NonZero, b)
}

// addEdges adds the edges of f that reach the rows of cb to the rasterizer,
// each under its row limit. The order of edges does not matter: the
// accumulator sums them in integers.
func (c *Canvas) addEdges(f *shapeForm, cb image.Rectangle) {
	r := &c.r
	y0, y1 := int32(cb.Min.Y), int32(cb.Max.Y)
	es := f.edges
	if len(f.eb.start) == 0 {
		for i := range es {
			if e := &es[i]; e.r1 > y0 && e.r0 < y1 {
				r.limitRows(int(e.lo), int(e.hi))
				r.AddLine(e.x0, e.y0, e.x1, e.y1)
			}
		}
		r.unlimitRows()
		return
	}
	bins := &f.eb
	b0, b1 := bins.span(cb.Min.Y, cb.Max.Y)
	for bi := b0; bi <= b1; bi++ {
		for _, id := range bins.ids[bins.start[bi]:bins.start[bi+1]] {
			e := &es[id]
			// An edge is taken in the first of the bins it shares with cb.
			if (bi > b0 && bins.bin(int64(e.r0)) != bi) || e.r1 <= y0 || e.r0 >= y1 {
				continue
			}
			r.limitRows(int(e.lo), int(e.hi))
			r.AddLine(e.x0, e.y0, e.x1, e.y1)
		}
	}
	r.unlimitRows()
}

// addStrips draws the strips of f that reach the rows of cb, in the order
// the stroker emitted them, through c.seg; with touch set, it only loads
// the destination under them (see touchStroke).
func (c *Canvas) addStrips(f *shapeForm, cb image.Rectangle, touch bool) {
	ss := f.strips
	if len(f.sb.start) == 0 {
		for i := range ss {
			c.addStrip(&ss[i], cb, touch)
		}
		return
	}
	bins := &f.sb
	b0, b1 := bins.span(cb.Min.Y, cb.Max.Y)
	if b0 == b1 || touch {
		// Touching in any order, or more than once, does no harm.
		for bi := b0; bi <= b1; bi++ {
			for _, id := range bins.ids[bins.start[bi]:bins.start[bi+1]] {
				c.addStrip(&ss[id], cb, touch)
			}
		}
		return
	}
	// Strips composite directly, so overlapping ones must keep their
	// order: gather the strips of all bins once each, and sort them.
	ids := c.ids[:0]
	for bi := b0; bi <= b1; bi++ {
		for _, id := range bins.ids[bins.start[bi]:bins.start[bi+1]] {
			if bi == b0 || bins.bin(int64(ss[id].y0)) == bi {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		c.addStrip(&ss[id], cb, false)
	}
	c.ids = ids
}

func (c *Canvas) addStrip(s *shapeStrip, cb image.Rectangle, touch bool) {
	if int(s.y1) <= cb.Min.Y || int(s.y0) >= cb.Max.Y {
		return
	}
	if touch {
		// The strip's centre line from its first to its last row, as
		// touchStroke follows a segment, half as wide as the strip.
		y0, y1 := float64(s.y0), float64(s.y1)
		xc := (s.rc.xl + s.rc.xr) / 2
		w := math.Abs(s.rc.xr-s.rc.xl) / 2
		c.sink += touchSegment(&c.solid.t, cb, xc+y0*s.rc.sl, y0, xc+y1*s.rc.sl, y1, w)
		return
	}
	rc := s.rc // strip writes to it; the shape is shared
	c.seg.setOrientation(s.neg)
	c.seg.strip(&rc, int(s.y0), int(s.y1))
}
