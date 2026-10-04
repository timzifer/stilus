package stilus

import (
	"cmp"
	"image"
	"math"
	"math/bits"
	"slices"
	"unsafe"
)

// FillRule selects how winding numbers map to inside/outside.
type FillRule uint8

const (
	NonZero FillRule = iota
	EvenOdd
)

// Blitter receives the output of the rasterizer, one span at a time, in
// increasing y order and, within a row, in increasing x order. Spans never
// overlap and always lie inside the rasterizer's clip.
//
// Implementations must not retain cov after the call returns.
type Blitter interface {
	// BlitRun paints the constant coverage alpha over [x0, x1) on row y:
	// the constant stretches between the edges of wide shapes.
	BlitRun(y, x0, x1 int, alpha uint8)
	// BlitCoverage paints per-pixel coverage for [x, x+len(cov)) on row y.
	// Narrow shapes (strokes, glyphs) arrive here as a whole, fully
	// covered pixels included.
	BlitCoverage(y, x int, cov []uint8)
}

const (
	bandShift = 5
	bandH     = 1 << bandShift // rows per accumulation band
	blkShift  = 2              // one dirty bit per 1<<blkShift cells
	// maxCoord bounds the control points of curves flattened by AddPath;
	// curves reaching beyond it are flattened between clamped control
	// points, which keeps the flattening arithmetic finite. Lines are
	// clipped at their exact coordinates.
	maxCoord = 1 << 40
	// DefaultMaxEdges is the default edge budget per path.
	DefaultMaxEdges = 1 << 22
	// narrowCells is the path width up to which rows are swept densely.
	narrowCells = 96
	// flattenTol is the curve flattening tolerance in device pixels. Chords
	// always lie inside convex arcs, so the error is one-sided; 0.1 px keeps
	// the mean deviation from exact coverage well below 1/255.
	flattenTol = 0.1
	maxSegs    = 1024
)

// edge is a line segment in fixed point 24.8, x relative to the clip's left
// edge and y relative to its top edge, oriented so that y0 < y1.
type edge struct {
	x0, y0, x1, y1 int32
	dir            int32   // +1 downward in the original path, -1 upward
	dxdy           float64 // (x1-x0)/(y1-y0)
	dydx           float64 // |(y1-y0)/(x1-x0)|; 0 until first needed (vertical edges never need it)
}

// xAt returns the edge's x at fixed-point y (y0 <= y <= y1). The same formula
// is used on both sides of every band and row boundary, so adjacent pieces of
// an edge always meet exactly.
func (e *edge) xAt(y int32) int32 {
	if y >= e.y1 {
		return e.x1
	}
	return e.x0 + int32(float64(y-e.y0)*e.dxdy+0.5+float64(1<<20)) - (1 << 20)
}

// Rasterizer converts paths into coverage spans. Its cost per path is the
// length of the path's edges in pixels plus the number of covered spans; it
// never touches the area of the bounding box.
//
// A Rasterizer is not safe for concurrent use; use one per worker. All
// buffers grow on demand and are retained, so steady-state use performs no
// allocations.
type Rasterizer struct {
	clip   image.Rectangle
	ry0    float64 // rows accepted by AddLine: the clip, or a sub-range
	ry1    float64
	cx0    float64 // clip in float
	cy0    float64
	cx1    float64
	cy1    float64
	w, h   int
	stride int // cells per accumulation row: w + 2
	nw     int // dirty words per row

	// MaxEdges bounds the number of edges per path. Edges beyond it are
	// dropped and Truncated reports true. Zero means DefaultMaxEdges.
	MaxEdges  int
	truncated bool
	done      bool // a path was rasterized; the next edge starts a new one
	aliased   bool

	edges  []edge
	minX   int32 // fixed, relative to clip left
	maxX   int32
	narrow bool  // path spans few cells: skip dirty bits, sweep row ranges
	minY   int32 // fixed, relative to clip top
	maxY   int32
	acc    []int64 // one edge contributes up to 1<<17; paths may have millions
	dirty  []uint64
	rmin   [bandH]int32 // per band row: first and last touched cell
	rmax   [bandH]int32
	cov    []uint8
	bucket []int32
	order  []int32
	active []int32

	// Analytic stroke rows summed with the edges (see addStrip).
	strips  []accStrip
	sorder  []int32
	sactive []int32
}

// accStrip is the analytic rows [y0, y1) (relative to the clip's top) of
// a stroke segment (see strip.go), summed into the accumulator.
type accStrip struct {
	rc     rowCtx
	y0, y1 int32
}

// NewRasterizer returns a rasterizer clipped to clip.
func NewRasterizer(clip image.Rectangle) *Rasterizer {
	r := &Rasterizer{}
	r.SetClip(clip)
	return r
}

// SetClip sets the device-space clip rectangle. Every span produced lies
// inside it. Buffers are resized only when the clip grows.
func (r *Rasterizer) SetClip(clip image.Rectangle) {
	clip = clip.Canon()
	r.clip = clip
	r.cx0, r.cy0 = float64(clip.Min.X), float64(clip.Min.Y)
	r.cx1, r.cy1 = float64(clip.Max.X), float64(clip.Max.Y)
	r.ry0, r.ry1 = r.cy0, r.cy1
	r.w, r.h = clip.Dx(), clip.Dy()
	r.stride = r.w + 2
	r.nw = (r.stride>>blkShift)/64 + 1
	if n := bandH * r.stride; cap(r.acc) < n {
		r.acc = make([]int64, n)
	} else {
		r.acc = r.acc[:n]
	}
	if n := bandH * r.nw; cap(r.dirty) < n {
		r.dirty = make([]uint64, n)
	} else {
		r.dirty = r.dirty[:n]
	}
	if cap(r.cov) < r.stride {
		r.cov = make([]uint8, r.stride)
	}
	for i := range r.rmin {
		r.rmin[i], r.rmax[i] = math.MaxInt32, -1
	}
	r.Reset()
}

// Clip returns the current clip rectangle.
func (r *Rasterizer) Clip() image.Rectangle { return r.clip }

// SetAntialias enables (default) or disables antialiasing. Without
// antialiasing a pixel is covered when at least half of it is inside.
func (r *Rasterizer) SetAntialias(aa bool) { r.aliased = !aa }

// Truncated reports whether the last path exceeded the edge budget.
func (r *Rasterizer) Truncated() bool { return r.truncated }

// Reset discards all accumulated edges and the Truncated state.
func (r *Rasterizer) Reset() {
	r.clearEdges()
	r.truncated, r.done = false, false
}

// discard also clears a band interrupted by a panicking blitter. Ordinary
// Reset needs no clearing pass because a completed sweep zeroes its cells.
func (r *Rasterizer) discard() {
	clear(r.acc)
	clear(r.dirty)
	for i := range r.rmin {
		r.rmin[i], r.rmax[i] = math.MaxInt32, -1
	}
	r.unlimitRows()
	r.Reset()
}

// clearEdges discards the edges of a rasterized path. Truncated keeps
// describing that path until the next one starts (Reset, AddPath, AddLine).
func (r *Rasterizer) clearEdges() {
	r.edges = r.edges[:0]
	r.strips = r.strips[:0]
	r.minY = math.MaxInt32
	r.minX = math.MaxInt32
	r.maxX = math.MinInt32
	r.maxY = math.MinInt32
}

// begin starts a new path after a Rasterize.
func (r *Rasterizer) begin() {
	if r.done {
		r.truncated, r.done = false, false
	}
}

// Fill rasterizes p transformed by m with the given fill rule. It is
// equivalent to Reset, AddPath and Rasterize.
func (r *Rasterizer) Fill(p *Path, m Matrix, rule FillRule, b Blitter) {
	r.Reset()
	r.AddPath(p, m)
	r.Rasterize(rule, b)
}

// Culled reports whether the device-space box of p under m misses the clip.
func (r *Rasterizer) culled(p *Path, m Matrix) bool {
	if len(p.Points) == 0 {
		return true
	}
	return r.culledBox(m.transformRect(p.Bounds()))
}

// culledBox reports whether a path's device-space box bb misses the clip.
func (r *Rasterizer) culledBox(bb Rect) bool {
	// NaN comparisons are false, so a NaN box is never culled here; the
	// per-point checks drop it later.
	return bb.X1 < r.cx0 || bb.Y1 <= r.cy0 || bb.X0 >= r.cx1 || bb.Y0 >= r.cy1
}

// AddPath adds the edges of p transformed by m. Open subpaths are closed
// implicitly. Curves are flattened in device space with a tolerance of a
// quarter pixel. Paths containing NaN or infinite values are ignored.
func (r *Rasterizer) AddPath(p *Path, m Matrix) {
	r.begin()
	if !m.finite() || r.w <= 0 || r.h <= 0 || r.culled(p, m) {
		return
	}
	r.addPath(p, m)
}

// addPath is AddPath for a finite m and a path not culled.
func (r *Rasterizer) addPath(p *Path, m Matrix) {
	ident := m == Identity
	pts := p.Points
	var sx, sy, cx, cy float64 // subpath start, current point
	open := false
	pi := 0
	// Edges added before a non-finite point are taken back.
	ne, minX, maxX, minY, maxY, trunc := len(r.edges), r.minX, r.maxX, r.minY, r.maxY, r.truncated
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
			r.edges = r.edges[:ne]
			r.minX, r.maxX, r.minY, r.maxY, r.truncated = minX, maxX, minY, maxY, trunc
			return
		}
		pi += numPoints[v]
	}
	if open {
		r.AddLine(cx, cy, sx, sy)
	}
}

func clampCoord(v float64) float64 {
	if v > maxCoord {
		return maxCoord
	}
	if v < -maxCoord {
		return -maxCoord
	}
	return v // NaN passes through and is rejected by AddLine
}

// boxMisses reports whether a curve's control box lies entirely outside the
// clip; such a curve is replaced by its chord, which contributes exactly the
// same coverage inside the clip (see AddLine).
func (r *Rasterizer) boxMisses(x0, x1, x2, x3, y0, y1, y2, y3 float64) bool {
	return (y0 <= r.cy0 && y1 <= r.cy0 && y2 <= r.cy0 && y3 <= r.cy0) ||
		(y0 >= r.cy1 && y1 >= r.cy1 && y2 >= r.cy1 && y3 >= r.cy1) ||
		(x0 >= r.cx1 && x1 >= r.cx1 && x2 >= r.cx1 && x3 >= r.cx1) ||
		(x0 <= r.cx0 && x1 <= r.cx0 && x2 <= r.cx0 && x3 <= r.cx0)
}

// clampInf clamps infinities to ±maxCoord, which keeps an overflowed
// outline closed; finite values are clipped exactly by AddLine.
func clampInf(v float64) float64 {
	if math.IsInf(v, 0) {
		return clampCoord(v)
	}
	return v
}

// beyond reports whether any of the coordinates exceeds maxCoord.
func beyond(v ...float64) bool {
	for _, c := range v {
		if !(math.Abs(c) <= maxCoord) {
			return true
		}
	}
	return false
}

func (r *Rasterizer) addQuad(x0, y0, x1, y1, x2, y2 float64) {
	if r.boxMisses(x0, x1, x2, x2, y0, y1, y2, y2) {
		r.AddLine(x0, y0, x2, y2)
		return
	}
	if beyond(x0, y0, x1, y1, x2, y2) {
		// The gap between an exact end and its clamped counterpart needs
		// no edge: it is horizontal or lies above or below the clip.
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

func (r *Rasterizer) addCubic(x0, y0, x1, y1, x2, y2, x3, y3 float64) {
	if r.boxMisses(x0, x1, x2, x3, y0, y1, y2, y3) {
		r.AddLine(x0, y0, x3, y3)
		return
	}
	if beyond(x0, y0, x1, y1, x2, y2, x3, y3) {
		// As in addQuad.
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

// quadPoly returns the number of chords a quadratic is flattened into and
// its polynomial B(t) = a t² + b t + p0.
func quadPoly(x0, y0, x1, y1, x2, y2 float64) (n int, ax, ay, bx, by float64) {
	ax, ay = x0-2*x1+x2, y0-2*y1+y2
	n = segCount(math.Sqrt(ax*ax+ay*ay) * (0.25 / flattenTol))
	return n, ax, ay, 2 * (x1 - x0), 2 * (y1 - y0)
}

// cubicPoly returns the number of chords a cubic is flattened into and its
// polynomial B(t) = a t³ + b t² + c t + p0.
func cubicPoly(x0, y0, x1, y1, x2, y2, x3, y3 float64) (n int, ax, ay, bx, by, cx, cy float64) {
	d1x, d1y := x0-2*x1+x2, y0-2*y1+y2
	d2x, d2y := x1-2*x2+x3, y1-2*y2+y3
	l := math.Sqrt(math.Max(d1x*d1x+d1y*d1y, d2x*d2x+d2y*d2y))
	n = segCount(l * (0.75 / flattenTol))
	cx, cy = 3*(x1-x0), 3*(y1-y0)
	bx, by = 3*(x2-x1)-cx, 3*(y2-y1)-cy
	ax, ay = x3-x0-cx-bx, y3-y0-cy-by
	return n, ax, ay, bx, by, cx, cy
}

// segCount returns ceil(sqrt(v)) clamped to [1, maxSegs].
func segCount(v float64) int {
	if !(v > 1) { // also catches NaN
		return 1
	}
	if v >= maxSegs*maxSegs {
		return maxSegs
	}
	return int(math.Ceil(math.Sqrt(v)))
}

// AddLine adds a device-space line segment. Segments are clipped
// analytically: parts above or below the clip are dropped, parts right of it
// are dropped, and parts left of it become vertical edges on the clip's left
// border, which preserves the winding of everything inside.
func (r *Rasterizer) AddLine(x0, y0, x1, y1 float64) {
	r.begin()
	if y0 == y1 || !(math.Abs(x0) <= math.MaxFloat64 && math.Abs(x1) <= math.MaxFloat64 &&
		math.Abs(y0) <= math.MaxFloat64 && math.Abs(y1) <= math.MaxFloat64) {
		return
	}
	dir := int32(1)
	if y0 > y1 {
		x0, y0, x1, y1 = x1, y1, x0, y0
		dir = -1
	}
	cy0, cy1 := r.ry0, r.ry1
	if y1 <= cy0 || y0 >= cy1 {
		return
	}
	if y0 < cy0 {
		x0 = lineAt(cy0, y0, y1, x0, x1)
		y0 = cy0
	}
	if y1 > cy1 {
		x1 = lineAt(cy1, y0, y1, x0, x1)
		y1 = cy1
	}
	cx0, cx1 := r.cx0, r.cx1
	if x0 >= cx1 && x1 >= cx1 {
		return
	}
	if x0 <= cx0 && x1 <= cx0 {
		r.emit(cx0, y0, cx0, y1, dir)
		return
	}
	if x0 < cx0 || x1 < cx0 {
		yc := lineAt(cx0, x0, x1, y0, y1)
		if x0 < cx0 {
			r.emit(cx0, y0, cx0, yc, dir)
			x0, y0 = cx0, yc
		} else {
			r.emit(cx0, yc, cx0, y1, dir)
			x1, y1 = cx0, yc
		}
	}
	if x0 > cx1 || x1 > cx1 {
		yc := lineAt(cx1, x0, x1, y0, y1)
		if x0 > cx1 {
			x0, y0 = cx1, yc
		} else {
			x1, y1 = cx1, yc
		}
	}
	r.emit(x0, y0, x1, y1, dir)
}

// lineAt returns the value at v of the line through (a, fa) and (b, fb),
// with a != b and v between a and b. Near the float64 range the differences
// overflow; the value is then interpolated at half scale.
func lineAt(v, a, b, fa, fb float64) float64 {
	if f := fa + (v-a)*(fb-fa)/(b-a); math.Abs(f) <= math.MaxFloat64 && !math.IsInf(b-a, 0) {
		return f
	}
	t := (v*0.5 - a*0.5) / (b*0.5 - a*0.5)
	return fa*(1-t) + fb*t
}

// fixed converts a device coordinate at offset o into 24.8 fixed point,
// clamped to [0, n·256]. Clipping keeps coordinates in range up to rounding;
// the clamp guards the unchecked indexing in rasterEdge.
func fixed(v, o float64, n int) int32 {
	f := (v-o)*256 + 0.5
	if !(f >= 0) { // also NaN
		return 0
	}
	if hi := float64(n << 8); f > hi {
		return int32(n << 8)
	}
	return int32(f)
}

// emit stores a clipped segment (y0 <= y1) as a fixed-point edge.
func (r *Rasterizer) emit(x0, y0, x1, y1 float64, dir int32) {
	fy0 := fixed(y0, r.cy0, r.h)
	fy1 := fixed(y1, r.cy0, r.h)
	if fy0 >= fy1 {
		return
	}
	limit := r.MaxEdges
	if limit == 0 {
		limit = DefaultMaxEdges
	}
	if len(r.edges) >= limit {
		r.truncated = true
		return
	}
	fx0 := fixed(x0, r.cx0, r.w)
	fx1 := fixed(x1, r.cx0, r.w)
	// dydx is computed when a row of the edge first crosses a cell border
	// (rasterEdge); most edges of small shapes never do.
	r.edges = append(r.edges, edge{
		x0: fx0, y0: fy0, x1: fx1, y1: fy1, dir: dir,
		dxdy: float64(fx1-fx0) / float64(fy1-fy0),
	})
	r.minX = min(r.minX, fx0, fx1)
	r.maxX = max(r.maxX, fx0, fx1)
	if fy0 < r.minY {
		r.minY = fy0
	}
	if fy1 > r.maxY {
		r.maxY = fy1
	}
}

// reverse reverses the orientation of the edges added since there were n.
func (r *Rasterizer) reverse(n int) {
	for i := n; i < len(r.edges); i++ {
		r.edges[i].dir = -r.edges[i].dir
	}
}

// Rasterize converts all added edges into spans and resets the rasterizer.
func (r *Rasterizer) Rasterize(rule FillRule, b Blitter) {
	defer func() { r.clearEdges(); r.done = true }()
	if len(r.edges) == 0 && len(r.strips) == 0 {
		return
	}
	// Narrow paths (most strokes, glyphs, small shapes) are swept over their
	// per-row cell range directly; the dirty bitset only pays off when a row
	// holds distant edges.
	r.narrow = r.maxX>>8-r.minX>>8 <= narrowCells
	row0 := int(r.minY >> 8)
	row1 := int((r.maxY + 255) >> 8)
	nb := (row1 - row0 + bandH - 1) >> bandShift
	if nb == 1 {
		var mask uint64
		for i := range r.edges {
			mask |= r.rasterEdge(&r.edges[i], row0)
		}
		for i := range r.strips {
			mask |= r.rasterStrip(&r.strips[i], row0)
		}
		r.sweep(row0, mask, rule, b)
		return
	}
	if len(r.strips) > 0 {
		r.rasterizeStrips(row0, nb, rule, b)
		return
	}
	band := func(ei int32) int { return (int(r.edges[ei].y0>>8) - row0) >> bandShift }
	order := r.sortEdges(row0, nb)
	active := r.active[:0]
	next := 0
	for bi := 0; bi < nb; bi++ {
		if len(active) == 0 {
			// Nothing reaches this band: skip to the next edge's.
			if next == len(order) {
				break
			}
			bi = max(bi, band(order[next]))
		}
		for ; next < len(order) && band(order[next]) <= bi; next++ {
			active = append(active, order[next])
		}
		bandRow := row0 + bi<<bandShift
		bandEnd := int32(bandRow+bandH) << 8
		var mask uint64
		j := 0
		for _, ei := range active {
			e := &r.edges[ei]
			mask |= r.rasterEdge(e, bandRow)
			if e.y1 > bandEnd {
				active[j] = ei
				j++
			}
		}
		active = active[:j]
		if mask != 0 {
			r.sweep(bandRow, mask, rule, b)
		}
	}
	r.active = active[:0]
}

// sortEdges returns the edges' indices sorted by their first band: a
// counting sort when bands are few relative to edges, else a comparison
// sort, so that the empty bands of a tall sparse path cost nothing.
func (r *Rasterizer) sortEdges(row0, nb int) []int32 {
	band := func(ei int32) int { return (int(r.edges[ei].y0>>8) - row0) >> bandShift }
	if cap(r.order) < len(r.edges) {
		r.order = make([]int32, len(r.edges))
	}
	order := r.order[:len(r.edges)]
	if nb <= 4*len(r.edges) {
		bucket := r.buckets(nb)
		for i := range r.edges {
			bucket[band(int32(i))+1]++
		}
		for i := 1; i <= nb; i++ {
			bucket[i] += bucket[i-1]
		}
		for i := range r.edges {
			bi := band(int32(i))
			order[bucket[bi]] = int32(i)
			bucket[bi]++
		}
	} else {
		for i := range order {
			order[i] = int32(i)
		}
		slices.SortFunc(order, func(a, b int32) int { return cmp.Compare(r.edges[a].y0, r.edges[b].y0) })
	}
	return order
}

// sortStrips is sortEdges for the strips.
func (r *Rasterizer) sortStrips(row0, nb int) []int32 {
	band := func(si int32) int { return (int(r.strips[si].y0) - row0) >> bandShift }
	r.sorder = grow32(r.sorder, len(r.strips))
	order := r.sorder
	if nb <= 4*len(r.strips) {
		bucket := r.buckets(nb)
		for i := range r.strips {
			bucket[band(int32(i))+1]++
		}
		for i := 1; i <= nb; i++ {
			bucket[i] += bucket[i-1]
		}
		for i := range r.strips {
			bi := band(int32(i))
			order[bucket[bi]] = int32(i)
			bucket[bi]++
		}
	} else {
		for i := range order {
			order[i] = int32(i)
		}
		slices.SortFunc(order, func(a, b int32) int { return cmp.Compare(r.strips[a].y0, r.strips[b].y0) })
	}
	return order
}

// buckets returns nb+1 zeroed counters.
func (r *Rasterizer) buckets(nb int) []int32 {
	if cap(r.bucket) < nb+1 {
		r.bucket = make([]int32, nb+1)
	}
	b := r.bucket[:nb+1]
	clear(b)
	return b
}

// rasterizeStrips is Rasterize's band loop for edges and strips.
func (r *Rasterizer) rasterizeStrips(row0, nb int, rule FillRule, b Blitter) {
	band := func(ei int32) int { return (int(r.edges[ei].y0>>8) - row0) >> bandShift }
	sband := func(si int32) int { return (int(r.strips[si].y0) - row0) >> bandShift }
	order := r.sortEdges(row0, nb)
	sorder := r.sortStrips(row0, nb)
	active, sactive := r.active[:0], r.sactive[:0]
	next, snext := 0, 0
	for bi := 0; bi < nb; bi++ {
		if len(active) == 0 && len(sactive) == 0 {
			// Nothing reaches this band: skip to the next edge's or strip's.
			nbi := nb
			if next < len(order) {
				nbi = band(order[next])
			}
			if snext < len(sorder) {
				nbi = min(nbi, sband(sorder[snext]))
			}
			if nbi == nb {
				break
			}
			bi = max(bi, nbi)
		}
		for ; next < len(order) && band(order[next]) <= bi; next++ {
			active = append(active, order[next])
		}
		for ; snext < len(sorder) && sband(sorder[snext]) <= bi; snext++ {
			sactive = append(sactive, sorder[snext])
		}
		bandRow := row0 + bi<<bandShift
		bandEnd := int32(bandRow + bandH)
		var mask uint64
		j := 0
		for _, ei := range active {
			e := &r.edges[ei]
			mask |= r.rasterEdge(e, bandRow)
			if e.y1 > bandEnd<<8 {
				active[j] = ei
				j++
			}
		}
		active = active[:j]
		j = 0
		for _, si := range sactive {
			st := &r.strips[si]
			mask |= r.rasterStrip(st, bandRow)
			if st.y1 > bandEnd {
				sactive[j] = si
				j++
			}
		}
		sactive = sactive[:j]
		if mask != 0 {
			r.sweep(bandRow, mask, rule, b)
		}
	}
	r.active, r.sactive = active[:0], sactive[:0]
}

// fullArea is a fully covered pixel in accumulator units: the area in
// 1/256 pixels times the 512 of a cell's two halves.
const fullArea = 256 * 512

// addStrip adds rows [y0, y1) (device rows) of the analytic strip rc to
// the accumulator, as coverage summed with the edges', winding like an
// edge going down on its left. Rows outside the clip are dropped.
func (r *Rasterizer) addStrip(rc *rowCtx, y0, y1 int) {
	r.begin()
	oy := r.clip.Min.Y
	y0, y1 = max(y0, oy), min(y1, r.clip.Max.Y)
	if y0 >= y1 || r.w <= 0 {
		return
	}
	// The strip's pixels lie between its left line at its leftmost row and
	// its right line at its rightmost one (see rowCtx.span).
	f0, f1 := float64(y0), float64(y1-1)
	xa := min(rc.xl+f0*rc.sl, rc.xl+f1*rc.sl)
	xb := max(rc.xr+f0*rc.sl, rc.xr+f1*rc.sl) + 1
	r.minX = min(r.minX, fixed(math.Floor(xa), r.cx0, r.w))
	r.maxX = max(r.maxX, fixed(math.Floor(xb), r.cx0, r.w))
	r.minY = min(r.minY, int32(y0-oy)<<8)
	r.maxY = max(r.maxY, int32(y1-oy)<<8)
	r.strips = append(r.strips, accStrip{rc: *rc, y0: int32(y0 - oy), y1: int32(y1 - oy)})
}

// rasterStrip accumulates the rows of s inside the band starting at
// bandRow (relative to the clip) and returns the mask of touched band
// rows. Each pixel adds its covered area, as segFast composites it: the
// difference to its left neighbour's goes to its cell.
func (r *Rasterizer) rasterStrip(s *accStrip, bandRow int) uint64 {
	j0, j1 := max(int(s.y0), bandRow), min(int(s.y1), bandRow+bandH)
	rc := &s.rc
	nx, k1, k2, tr := rc.nx, rc.k1, rc.k2, rc.tr
	ox, oy := r.clip.Min.X, r.clip.Min.Y
	stride, nw := r.stride, r.nw
	var mask uint64
	for j := j0; j < j1; j++ {
		i0, i1, fy := rc.span(j+oy, ox, ox+r.w)
		if i0 >= i1 {
			continue
		}
		d := rc.centre(i0, fy)
		ri := j - bandRow
		c0, n := i0-ox, i1-i0
		acc := r.acc[ri*stride+c0 : ri*stride+c0+n+1]
		a, b := n, n // fully covered interior [a, b), as segFast.emitRow
		if rc.runs {
			ta, tb := (rc.lo-d)*rc.inx, (rc.hi-d)*rc.inx
			if ta > tb {
				ta, tb = tb, ta
			}
			if ia, ib := max(int(math.Ceil(ta-1e-9)), 0), min(ffloor(tb+1e-9)+1, n); ia < ib {
				a, b = ia, ib
			}
		}
		var prev int64
		for i := 0; i < a; i++ {
			v := int64((tr.area(k1-d) - tr.area(k2-d)) * fullArea)
			acc[i] += v - prev
			prev = v
			d += nx
		}
		if b > a {
			acc[a] += fullArea - prev
			prev = fullArea
			d += float64(b-a) * nx
		}
		for i := b; i < n; i++ {
			v := int64((tr.area(k1-d) - tr.area(k2-d)) * fullArea)
			acc[i] += v - prev
			prev = v
			d += nx
		}
		acc[n] -= prev
		if !r.narrow {
			markRange(r.dirty[ri*nw:(ri+1)*nw], c0, c0+n)
		}
		r.rmin[ri] = min(r.rmin[ri], int32(c0))
		r.rmax[ri] = max(r.rmax[ri], int32(c0+n))
		mask |= 1 << uint(ri)
	}
	return mask
}

// rasterEdge accumulates the part of e inside the band starting at bandRow
// (relative to the clip) and returns the mask of touched band rows.
func (r *Rasterizer) rasterEdge(e *edge, bandRow int) uint64 {
	yA := e.y0
	if lo := int32(bandRow) << 8; yA < lo {
		yA = lo
	}
	yB := e.y1
	if hi := int32(bandRow+bandH) << 8; yB > hi {
		yB = hi
	}
	if yA >= yB {
		return 0
	}
	stride := r.stride
	acc := r.acc
	dirty := r.dirty
	nw := r.nw
	dir := e.dir
	var mask uint64

	if e.x0 == e.x1 {
		// Vertical edge: one cell per row.
		c := int(e.x0 >> 8)
		fx2 := (e.x0 & 255) * 2
		wA := dir * (512 - fx2)
		wB := dir * fx2
		bw, bb := c>>(blkShift+6), uint64(1)<<(uint(c>>blkShift)&63)
		bw1, bb1 := (c+1)>>(blkShift+6), uint64(1)<<(uint((c+1)>>blkShift)&63)
		for y := yA; y < yB; {
			rowEnd := (y | 255) + 1
			if rowEnd > yB {
				rowEnd = yB
			}
			dy := rowEnd - y
			ri := int(y>>8) - bandRow
			o := ri*stride + c
			acc[o] += int64(dy * wA)
			acc[o+1] += int64(dy * wB)
			if !r.narrow {
				d := ri * nw
				dirty[d+bw] |= bb
				dirty[d+bw1] |= bb1
			}
			if int32(c) < r.rmin[ri] {
				r.rmin[ri] = int32(c)
			}
			if int32(c+1) > r.rmax[ri] {
				r.rmax[ri] = int32(c + 1)
			}
			mask |= 1 << uint(ri)
			y = rowEnd
		}
		return mask
	}

	x := e.xAt(yA)
	// Indices below are in range by construction: x ∈ [0, w·256], so a cell
	// and its right neighbour are < stride, and ri < bandH. The single-cell
	// case, by far the most common, is written without bounds checks.
	accp := unsafe.Pointer(unsafe.SliceData(acc))
	dirp := unsafe.Pointer(unsafe.SliceData(dirty))
	for y := yA; y < yB; {
		rowEnd := (y | 255) + 1
		if rowEnd > yB {
			rowEnd = yB
		}
		xn := e.xAt(rowEnd)
		ri := int(y>>8) - bandRow
		c0, c1 := x>>8, xn>>8
		var lo, hi int32
		if c0 == c1 {
			d := (rowEnd - y) * dir
			sx := (x & 255) + (xn & 255)
			p := (*[2]int64)(unsafe.Add(accp, (ri*stride+int(c0))*8))
			p[0] += int64(d * (512 - sx))
			p[1] += int64(d * sx)
			if !r.narrow {
				b0, b1 := uint(c0)>>blkShift, uint(c0+1)>>blkShift
				w := (*uint64)(unsafe.Add(dirp, (ri*nw+int(b0>>6))*8))
				*w |= 1 << (b0 & 63)
				w = (*uint64)(unsafe.Add(dirp, (ri*nw+int(b1>>6))*8))
				*w |= 1 << (b1 & 63)
			}
			lo, hi = c0, c0+1
		} else {
			base := y &^ 255
			var dr []uint64
			if !r.narrow {
				dr = dirty[ri*nw : (ri+1)*nw]
			}
			if e.dydx == 0 {
				e.dydx = math.Abs(float64(e.y1-e.y0) / float64(e.x1-e.x0))
			}
			lo, hi = cellLine(acc[ri*stride:(ri+1)*stride], dr, x, y-base, xn, rowEnd-base, dir, e.dydx)
		}
		if lo < r.rmin[ri] {
			r.rmin[ri] = lo
		}
		if hi > r.rmax[ri] {
			r.rmax[ri] = hi
		}
		mask |= 1 << uint(ri)
		x, y = xn, rowEnd
	}
	return mask
}

// cellLine accumulates a segment that lies within one pixel row, from
// (x0, fy0) to (x1, fy1), with 0 <= fy0 < fy1 <= 256, and returns the range
// of touched cells [lo, hi].
func cellLine(acc []int64, dirty []uint64, x0, fy0, x1, fy1, dir int32, dydx float64) (lo, hi int32) {
	c0 := x0 >> 8
	c1 := x1 >> 8
	if c0 == c1 {
		d := (fy1 - fy0) * dir
		s := (x0 & 255) + (x1 & 255)
		acc[c0] += int64(d * (512 - s))
		acc[c0+1] += int64(d * s)
		markRange(dirty, int(c0), int(c0)+1)
		return c0, c0 + 1
	}
	if x1 > x0 {
		xa, ya := x0, fy0
		for c := c0; c < c1; c++ {
			bx := (c + 1) << 8
			yb := min(fy0+int32(float64(bx-x0)*dydx+0.5), fy1)
			d := (yb - ya) * dir
			s := (xa - c<<8) + 256
			acc[c] += int64(d * (512 - s))
			acc[c+1] += int64(d * s)
			xa, ya = bx, yb
		}
		d := (fy1 - ya) * dir
		s := x1 - c1<<8
		acc[c1] += int64(d * (512 - s))
		acc[c1+1] += int64(d * s)
		markRange(dirty, int(c0), int(c1)+1)
		return c0, c1 + 1
	}
	xa, ya := x0, fy0
	for c := c0; c > c1; c-- {
		bx := c << 8
		yb := min(fy0+int32(float64(x0-bx)*dydx+0.5), fy1)
		d := (yb - ya) * dir
		s := xa - c<<8
		acc[c] += int64(d * (512 - s))
		acc[c+1] += int64(d * s)
		xa, ya = bx, yb
	}
	d := (fy1 - ya) * dir
	s := 256 + (x1 - c1<<8)
	acc[c1] += int64(d * (512 - s))
	acc[c1+1] += int64(d * s)
	markRange(dirty, int(c1), int(c0)+1)
	return c1, c0 + 1
}

// markRange sets the dirty bits covering cells [lo, hi].
func markRange(dirty []uint64, lo, hi int) {
	if dirty == nil {
		return
	}
	b0 := lo >> blkShift
	b1 := hi >> blkShift
	w0, w1 := b0>>6, b1>>6
	if w0 == w1 {
		dirty[w0] |= (^uint64(0) << uint(b0&63)) & (^uint64(0) >> uint(63-b1&63))
		return
	}
	dirty[w0] |= ^uint64(0) << uint(b0&63)
	for w := w0 + 1; w < w1; w++ {
		dirty[w] = ^uint64(0)
	}
	dirty[w1] |= ^uint64(0) >> uint(63-b1&63)
}

// sweep integrates the accumulated rows of a band, emits spans and leaves
// the touched cells zeroed.
func (r *Rasterizer) sweep(bandRow int, mask uint64, rule FillRule, b Blitter) {
	w := r.w
	stride := r.stride
	nw := r.nw
	ox, oy := r.clip.Min.X, r.clip.Min.Y
	cov := r.cov
	for mask != 0 {
		ri := bits.TrailingZeros64(mask)
		mask &= mask - 1
		y := oy + bandRow + ri
		acc := r.acc[ri*stride : (ri+1)*stride]
		dirty := r.dirty[ri*nw : (ri+1)*nw]
		if r.narrow {
			r.sweepRange(acc, y, ri, rule, b)
			continue
		}
		if r.rmax[ri]-r.rmin[ri] <= narrowCells {
			// Narrow row of a wide path: clear its bits, sweep the range.
			for wi := int(r.rmin[ri]) >> (blkShift + 6); wi <= int(r.rmax[ri])>>(blkShift+6); wi++ {
				dirty[wi] = 0
			}
			r.sweepRange(acc, y, ri, rule, b)
			continue
		}
		var s int64
		x := 0 // first cell not yet emitted
		wlo := int(r.rmin[ri]) >> (blkShift + 6)
		whi := int(r.rmax[ri]) >> (blkShift + 6)
		r.rmin[ri], r.rmax[ri] = math.MaxInt32, -1
		for wi := wlo; wi <= whi; wi++ {
			word := dirty[wi]
			if word == 0 {
				continue
			}
			dirty[wi] = 0
			for word != 0 {
				tz := bits.TrailingZeros64(word)
				run := bits.TrailingZeros64(^(word >> uint(tz)))
				if tz+run >= 64 {
					word = 0
				} else {
					word &^= (uint64(1)<<uint(run) - 1) << uint(tz)
				}
				c0 := (wi<<6 + tz) << blkShift
				c1 := c0 + run<<blkShift
				if c1 > stride {
					c1 = stride
				}
				if c0 >= c1 {
					continue
				}
				// Gap before this block group: constant coverage.
				if x < c0 && s != 0 && x < w {
					if a := r.alpha(s, rule); a != 0 {
						e := c0
						if e > w {
							e = w
						}
						b.BlitRun(y, ox+x, ox+e, a)
					}
				}
				// Integrate the block group.
				e := c1
				if e > w {
					e = w
				}
				if c0 < e {
					cv := cov[c0:e]
					switch {
					case r.aliased:
						ac := acc[c0:e]
						ac = ac[:len(cv)]
						for i := range cv {
							s += ac[i]
							ac[i] = 0
							cv[i] = aliasAlpha(s, rule)
						}
					case rule == EvenOdd:
						ac := acc[c0:e]
						ac = ac[:len(cv)]
						for i := range cv {
							s += ac[i]
							ac[i] = 0
							cv[i] = evenOddAlpha(s)
						}
					default:
						ac := acc[c0:e]
						ac = ac[:len(cv)]
						for i := range cv {
							s += ac[i]
							ac[i] = 0
							cv[i] = nonZeroAlpha(s)
						}
					}
					emitCoverage(b, y, ox+c0, cv)
				}
				for i := e; i < c1; i++ {
					s += acc[i]
					acc[i] = 0
				}
				x = c1
			}
		}
		if x < w && s != 0 {
			if a := r.alpha(s, rule); a != 0 {
				b.BlitRun(y, ox+x, ox+w, a)
			}
		}
	}
}

// emitCoverage trims zero coverage at both ends.
func emitCoverage(b Blitter, y, x int, cv []uint8) {
	i, j := 0, len(cv)
	for i < j && cv[i] == 0 {
		i++
	}
	for j > i && cv[j-1] == 0 {
		j--
	}
	if i < j {
		b.BlitCoverage(y, x+i, cv[i:j])
	}
}

func nonZeroAlpha(s int64) uint8 {
	m := s >> 63
	return uint8(min(((s^m)-m)>>9, 255))
}

func evenOddAlpha(s int64) uint8 {
	if s < 0 {
		s = -s
	}
	s = (s >> 9) & 511
	if s > 256 {
		s = 512 - s
	}
	if s > 255 {
		return 255
	}
	return uint8(s)
}

func aliasAlpha(s int64, rule FillRule) uint8 {
	var a uint8
	if rule == EvenOdd {
		a = evenOddAlpha(s)
	} else {
		a = nonZeroAlpha(s)
	}
	if a >= 128 {
		return 255
	}
	return 0
}

func (r *Rasterizer) alpha(s int64, rule FillRule) uint8 {
	switch {
	case r.aliased:
		return aliasAlpha(s, rule)
	case rule == EvenOdd:
		return evenOddAlpha(s)
	default:
		return nonZeroAlpha(s)
	}
}

// sweepRange integrates the touched cell range of one row of a narrow path.
func (r *Rasterizer) sweepRange(acc []int64, y, ri int, rule FillRule, b Blitter) {
	c0 := int(r.rmin[ri])
	c1 := min(int(r.rmax[ri])+1, r.stride)
	r.rmin[ri], r.rmax[ri] = math.MaxInt32, -1
	w := r.w
	e := min(c1, w)
	var s int64
	if c0 < e {
		cv := r.cov[c0:e]
		ac := acc[c0:e]
		ac = ac[:len(cv)]
		switch {
		case r.aliased:
			for i := range cv {
				s += ac[i]
				ac[i] = 0
				cv[i] = aliasAlpha(s, rule)
			}
		case rule == EvenOdd:
			for i := range cv {
				s += ac[i]
				ac[i] = 0
				cv[i] = evenOddAlpha(s)
			}
		default:
			for i := range cv {
				s += ac[i]
				ac[i] = 0
				cv[i] = nonZeroAlpha(s)
			}
		}
		emitCoverage(b, y, r.clip.Min.X+c0, cv)
	}
	for i := max(e, c0); i < c1; i++ {
		s += acc[i]
		acc[i] = 0
	}
	// Coverage continuing past the range: edges right of the clip were dropped.
	if c1 < w && s != 0 {
		if a := r.alpha(s, rule); a != 0 {
			b.BlitRun(y, r.clip.Min.X+c1, r.clip.Min.X+w, a)
		}
	}
}

// limitRows restricts subsequently added edges to device rows [y0, y1)
// within the clip; unlimitRows lifts the restriction.
func (r *Rasterizer) limitRows(y0, y1 int) {
	r.ry0 = max(r.cy0, float64(y0))
	r.ry1 = min(r.cy1, float64(y1))
}

func (r *Rasterizer) unlimitRows() { r.ry0, r.ry1 = r.cy0, r.cy1 }
