package stilus

import "math"

// Cap is the shape of open stroke ends.
type Cap uint8

const (
	ButtCap Cap = iota
	RoundCap
	SquareCap
)

// Join is the shape of stroke corners.
type Join uint8

const (
	MiterJoin Join = iota
	RoundJoin
	BevelJoin
)

// StrokeStyle describes a stroke in user space (the space the path is
// defined in), like the PDF graphics state.
type StrokeStyle struct {
	Width      float64 // 0 means the thinnest line the device can render
	Cap        Cap
	Join       Join
	MiterLimit float64   // PDF default 10; values < 1 are treated as 1
	Dash       []float64 // on/off lengths; empty means solid
	DashPhase  float64
}

// LineSink receives device-space line segments. Segments emitted by the
// Stroker form closed polygons that must be filled with the NonZero rule.
// *Rasterizer implements LineSink.
type LineSink interface {
	AddLine(x0, y0, x1, y1 float64)
}

// PathSink collects line segments into a Path.
type PathSink struct{ P *Path }

func (s PathSink) AddLine(x0, y0, x1, y1 float64) {
	p := s.P
	n := len(p.Points)
	if n == 0 || p.Verbs[len(p.Verbs)-1] == Close ||
		p.Points[n-1] != (Point{float32(x0), float32(y0)}) {
		p.MoveTo(float32(x0), float32(y0))
	}
	p.LineTo(float32(x1), float32(y1))
}

// DefaultMaxDashes bounds dash transitions per subpath. Subpaths estimated
// to exceed the budget are skipped; the subdivision loop also stops at the
// budget. Stroker.Truncated reports either condition.
const DefaultMaxDashes = 1 << 20

const (
	// densePeriod is the device-space dash period at or below which a
	// pattern is drawn as a solid stroke with its mean coverage. A pixel
	// then holds four periods or more, and a pixel's coverage differs from
	// the mean by at most period·on·off fraction: 1/16.
	densePeriod = 0.25
	// denseSteps bounds the pattern entries per device pixel that are
	// walked one by one. Longer patterns of tiny entries collapse to their
	// mean coverage too when every pixel-long stretch of the pattern covers
	// that mean within 1/16; others are walked within dash's per-subpath
	// budget.
	denseSteps = 32
)

// Stroker converts strokes into fill geometry (closed outlines to be filled
// with NonZero). Offsets are computed in user space and transformed
// afterwards, which is exact for anisotropic transforms.
//
// A Stroker is not safe for concurrent use. Buffers are retained.
type Stroker struct {
	// MaxDashes overrides DefaultMaxDashes when non-zero.
	MaxDashes int

	truncated bool
	coverage  float64

	sink LineSink
	dev  bool          // hairline: geometry in device space, one pixel wide
	seg  segmentFiller // optional analytic path for straight segments
	jag  bool          // an inner corner was routed through its vertex
	// With cull set, subpaths whose outline lies outside cb (the sink's
	// clip padded by the widest outline extent) are skipped before
	// flattening; with cullCurves set too, such curves take their chord.
	cull, cullCurves bool
	cb               Rect
	// Dash pieces take the analytic path only on straight subpaths with
	// gaps wide enough that neighbouring pieces never share a pixel.
	dashFast, dashing, dashStraight bool
	fastHits, fastTries             int  // statistics for tests
	noLine                          bool // tests: single segments take fastPoly's general path
	fast                            fastState
	m                               Matrix
	st                              *StrokeStyle
	hw                              float64 // half width, user space
	tolU                            float64 // flattening tolerance, user space
	stepA                           float64 // angular step for round joins/caps; 0 until an arc needs it
	rdev                            float64 // half width in device space, for stepA

	poly   []float64
	dpoly  []float64
	dfirst []float64
	dpts   []float64
	segs   []float64
	piece  []float64
	hbuf   []float64
}

// sigmaMax returns the largest singular value of m's linear part.
func sigmaMax(m Matrix) float64 {
	a, b, c, d := m[0], m[1], m[2], m[3]
	s := a*a + b*b + c*c + d*d
	e := a*a + b*b - c*c - d*d
	f := a*c + b*d
	return math.Sqrt((s + math.Sqrt(e*e+4*f*f)) / 2)
}

// strokePrep holds what both Canvas.Stroke and the stroker derive from m
// and st once per stroke.
type strokePrep struct {
	sm    float64 // sigmaMax(m)
	dense bool    // denseDash: the pattern is drawn solid ...
	f     float64 // ... at this mean coverage
}

func prepStroke(m Matrix, st *StrokeStyle) strokePrep {
	sm := sigmaMax(m)
	f, dense := denseDashSM(st, sm)
	return strokePrep{sm: sm, dense: dense, f: f}
}

// Stroke emits the outline of p stroked with st under m into sink.
//
// A dash pattern too dense to resolve in device space is emitted as a solid
// stroke; Coverage then reports the fraction the caller must scale the
// resulting coverage by.
func (s *Stroker) Stroke(sink LineSink, p *Path, m Matrix, st *StrokeStyle) {
	s.stroke(sink, p, m, st, prepStroke(m, st))
}

// stroke is Stroke with the stroke's preparation already made.
func (s *Stroker) stroke(sink LineSink, p *Path, m Matrix, st *StrokeStyle, pr strokePrep) {
	s.sink, s.seg = sink, nil
	s.run(p, m, st, pr)
}

// Truncated reports whether the last stroke exceeded its dash budget.
// Canvas reports this condition as ErrDashBudget.
func (s *Stroker) Truncated() bool { return s.truncated }

// Coverage reports the factor to apply to the coverage of the last stroke's
// outline: the mean on-fraction of a dash pattern emitted as a solid stroke,
// else 1.
func (s *Stroker) Coverage() float64 { return s.coverage }

// IsHairline reports whether st under m is thinner than one device pixel in
// every direction. Such strokes are drawn one pixel wide in device space,
// like PDFium.
func IsHairline(m Matrix, st *StrokeStyle) bool {
	return st.Width*sigmaMax(m) < 1
}

// segmentFiller is the device side of the analytic stroke path (see
// strip.go): it fills rows bounded by two parallel lines and restricts the
// rows of edges sent to the stroke's LineSink.
type segmentFiller interface {
	// middle fills rows [y0, y1) of the strip between the line through
	// (ax, ay)-(bx, by) and the parallel line through (dx, dy).
	middle(ax, ay, bx, by, dx, dy float64, y0, y1 int)
	// setOrientation tells the filler that the outline winds negatively in
	// device space (a mirroring transform), so pixels it adds to the
	// stroke's accumulator must wind the same way.
	setOrientation(neg bool)
	limitRows(y0, y1 int)
	unlimitRows()
}

// strokeFast is Stroke with a segment fast path.
func (s *Stroker) strokeFast(sink LineSink, seg segmentFiller, p *Path, m Matrix, st *StrokeStyle, pr strokePrep) {
	s.sink, s.seg = sink, seg
	s.run(p, m, st, pr)
	s.seg = nil
}

func (s *Stroker) run(p *Path, m Matrix, st *StrokeStyle, pr strokePrep) {
	s.truncated = false
	s.coverage = 1
	if !m.finite() || !(st.Width >= 0) || math.IsInf(st.Width, 0) {
		return
	}
	sm := pr.sm
	if sm == 0 {
		return
	}
	s.m, s.st = m, st
	// Strokes thinner than a device pixel are drawn one pixel wide, like
	// PDFium: their offsets are taken in device space.
	s.dev = st.Width*sm < 1
	s.dashFast = s.seg != nil && dashFastOK(m, st, s.dev, sm)
	s.hw = st.Width / 2
	r := s.hw * sm
	if s.dev {
		s.hw, r = 0.5, 0.5
	}
	s.tolU = flattenTol / sm
	s.setCull(r)
	// Only round joins and caps (and dots) need the arc step: arc makes
	// it on first use.
	s.stepA, s.rdev = 0, r
	dashed := len(st.Dash) > 0
	if dashed {
		sum := 0.0
		for _, d := range st.Dash {
			if !(d >= 0) || math.IsInf(d, 0) {
				sum = 0
				break
			}
			sum += d
		}
		// Invalid or empty patterns retain the solid-stroke fallback.
		// A valid short period still needs its actual on/off coverage.
		if !(sum > 0) || math.IsInf(sum, 0) {
			dashed = false
		}
		if dashed && (math.IsNaN(st.DashPhase) || math.IsInf(st.DashPhase, 0)) {
			return
		}
		if f := pr.f; dashed && pr.dense {
			if f <= 0 {
				s.coverage = 0
				return
			}
			dashed, s.coverage = false, f
		}
	}

	// A chord changes the outline only outside the clip, but it shortens
	// the path a dash pattern walks.
	s.cullCurves = s.cull && !dashed
	pts := p.Points
	pi := 0
	closed := false
	drawn := false     // a segment followed the subpath's MoveTo
	var sx, sy float64 // start of the last closed subpath
	pending := false   // a segment after Close starts at (sx, sy)
	s.poly = s.poly[:0]
	flush := func() {
		// A single point is a dot only as a closed subpath or when its
		// segments all led back to it; a lone MoveTo paints nothing.
		if n := len(s.poly); n > 2 || (n == 2 && (closed || drawn)) {
			if dashed {
				s.dash(s.poly, closed)
			} else {
				s.strokePoly(s.poly, closed, 1, 0)
			}
		}
		s.poly = s.poly[:0]
		closed, drawn = false, false
	}
	begin := func() {
		if len(s.poly) == 0 && pending {
			s.addPt(sx, sy, true)
		}
		pending = false
	}
	verbs := p.Verbs
	for vi := 0; vi < len(verbs); vi++ {
		v := verbs[vi]
		if int(v) >= len(numPoints) || pi+numPoints[v] > len(pts) {
			break
		}
		switch v {
		case MoveTo:
			flush()
			pending = false
			// Subpaths are stroked independently, dash phase included.
			if s.cull {
				if vj, pj, miss := s.subpathMisses(verbs, pts, vi, pi); miss {
					vi, pi = vj-1, pj
					continue
				}
			}
			s.addPt(float64(pts[pi].X), float64(pts[pi].Y), true)
		case LineTo:
			begin()
			s.addPt(float64(pts[pi].X), float64(pts[pi].Y), false)
			drawn = true
		case QuadTo:
			begin()
			s.quad(pts[pi], pts[pi+1])
			drawn = true
		case CubicTo:
			begin()
			s.cubic(pts[pi], pts[pi+1], pts[pi+2])
			drawn = true
		case Close:
			if len(s.poly) > 0 {
				closed = true
				sx, sy = s.poly[0], s.poly[1]
				flush()
				pending = true
			}
		}
		pi += numPoints[v]
	}
	flush()
}

// setCull enables culling when the sink is a Rasterizer, which drops all
// geometry outside its clip; r is the stroke's half width in device space.
func (s *Stroker) setCull(r float64) {
	s.cull = false
	rz, ok := s.sink.(*Rasterizer)
	if !ok || rz.w <= 0 || rz.h <= 0 {
		return
	}
	// Outline points lie within r of the path's control hull, except for
	// square caps' corners and miter tips.
	k := 1.0
	if s.st.Cap == SquareCap {
		k = math.Sqrt2
	}
	if s.st.Join == MiterJoin && s.st.MiterLimit > k {
		k = s.st.MiterLimit
	}
	pad := r*k + 1
	if !(pad < 1<<30) {
		return
	}
	s.cull = true
	s.cb = Rect{rz.cx0 - pad, rz.cy0 - pad, rz.cx1 + pad, rz.cy1 + pad}
}

// misses reports whether the user-space box b lies outside s.cb in device
// space. A NaN box never misses.
func (s *Stroker) misses(b Rect) bool {
	d := s.m.transformRect(b)
	return d.X1 < s.cb.X0 || d.X0 > s.cb.X1 || d.Y1 < s.cb.Y0 || d.Y0 > s.cb.Y1
}

// subpathMisses reports whether the outline of the subpaths from the MoveTo
// at verbs[vi] up to the next MoveTo misses the clip, and where that MoveTo
// and its point are.
func (s *Stroker) subpathMisses(verbs []Verb, pts []Point, vi, pi int) (int, int, bool) {
	x, y := float64(pts[pi].X), float64(pts[pi].Y)
	b := Rect{x, y, x, y}
	pi++
	for vi++; vi < len(verbs); vi++ {
		v := verbs[vi]
		if v == MoveTo || int(v) >= len(numPoints) || pi+numPoints[v] > len(pts) {
			break
		}
		for _, q := range pts[pi : pi+numPoints[v]] {
			x, y := float64(q.X), float64(q.Y)
			b = Rect{min(b.X0, x), min(b.Y0, y), max(b.X1, x), max(b.Y1, y)}
		}
		pi += numPoints[v]
	}
	return vi, pi, s.misses(b)
}

// curveMisses reports whether the outline of a curve with the given
// user-space control points (x, y pairs) misses the clip.
func (s *Stroker) curveMisses(q ...float64) bool {
	b := Rect{q[0], q[1], q[0], q[1]}
	for i := 2; i < len(q); i += 2 {
		b = Rect{min(b.X0, q[i]), min(b.Y0, q[i+1]), max(b.X1, q[i]), max(b.Y1, q[i+1])}
	}
	return s.misses(b)
}

// addPt appends a user-space point, dropping exact duplicates. A subpath
// consisting of a single point is kept (it may produce a dot).
func (s *Stroker) addPt(x, y float64, move bool) {
	if x != x || y != y || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return
	}
	n := len(s.poly)
	if !move && n >= 2 && s.poly[n-2] == x && s.poly[n-1] == y {
		return
	}
	s.poly = append(s.poly, x, y)
}

func (s *Stroker) last() (float64, float64) {
	n := len(s.poly)
	if n == 0 {
		return 0, 0
	}
	return s.poly[n-2], s.poly[n-1]
}

func (s *Stroker) quad(c, e Point) {
	x0, y0 := s.last()
	x1, y1 := float64(c.X), float64(c.Y)
	x2, y2 := float64(e.X), float64(e.Y)
	if s.cullCurves && s.curveMisses(x0, y0, x1, y1, x2, y2) {
		s.addPt(x2, y2, false)
		return
	}
	ddx, ddy := x0-2*x1+x2, y0-2*y1+y2
	n := segCount(math.Sqrt(ddx*ddx+ddy*ddy) * (0.25 / s.tolU))
	for i := 1; i < n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		s.addPt(u*u*x0+2*u*t*x1+t*t*x2, u*u*y0+2*u*t*y1+t*t*y2, false)
	}
	s.addPt(x2, y2, false)
}

func (s *Stroker) cubic(c1, c2, e Point) {
	x0, y0 := s.last()
	x1, y1 := float64(c1.X), float64(c1.Y)
	x2, y2 := float64(c2.X), float64(c2.Y)
	x3, y3 := float64(e.X), float64(e.Y)
	if s.cullCurves && s.curveMisses(x0, y0, x1, y1, x2, y2, x3, y3) {
		s.addPt(x3, y3, false)
		return
	}
	d1x, d1y := x0-2*x1+x2, y0-2*y1+y2
	d2x, d2y := x1-2*x2+x3, y1-2*y2+y3
	l := math.Sqrt(math.Max(d1x*d1x+d1y*d1y, d2x*d2x+d2y*d2y))
	n := segCount(l * (0.75 / s.tolU))
	for i := 1; i < n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
		s.addPt(a*x0+b*x1+c*x2+d*x3, a*y0+b*y1+c*y2+d*y3, false)
	}
	s.addPt(x3, y3, false)
}

// dash splits a polyline according to the dash pattern and strokes each dash.
func (s *Stroker) dash(poly []float64, closed bool) {
	s.dashing = true
	s.dashStraight = !closed && len(poly) == 4
	defer func() { s.dashing = false }()
	pat := s.st.Dash
	period := 0.0
	for _, d := range pat {
		period += d
	}
	n := len(poly) / 2
	if closed && n > 1 {
		n++ // closing segment
	}
	total, dev := 0.0, 0.0
	for i := 1; i < n; i++ {
		j := i % (len(poly) / 2)
		dx, dy := poly[2*j]-poly[2*i-2], poly[2*j+1]-poly[2*i-1]
		total += math.Hypot(dx, dy)
		dev += math.Hypot(s.m.ApplyVec(dx, dy))
	}
	max := s.MaxDashes
	if max == 0 {
		max = DefaultMaxDashes
	}
	// denseDash bounds the transitions per device pixel along the widest
	// stretch of m; a strongly anisotropic m can still squeeze a pattern
	// along a narrow direction, so the bound is checked per subpath too.
	est := total / period * float64(len(pat))
	if est > float64(max) || est > denseSteps*(dev+2*float64(len(pat))) {
		s.truncated = true
		return
	}
	// An odd pattern repeats with on and off swapped: walk it as if it were
	// written out twice, with the on/off parity taken from the step count.
	np := len(pat)
	steps := np
	if np%2 == 1 {
		steps = 2 * np
		period *= 2
	}
	idx := 0
	phase := math.Mod(s.st.DashPhase, period)
	if phase < 0 {
		phase += period
	}
	for phase >= pat[idx%np] && phase > 0 {
		phase -= pat[idx%np]
		idx = (idx + 1) % steps
	}
	rem := pat[idx%np] - phase
	on := idx%2 == 0

	if len(poly) == 2 {
		if on {
			s.strokePoly(poly, false, 1, 0)
		}
		return
	}
	// On a closed subpath a dash running through the start point is one
	// dash: the first piece is held back and joined to the last.
	hold := closed && on
	held := false
	var fdx, fdy float64
	s.dpoly = s.dpoly[:0]
	if on {
		s.dpoly = append(s.dpoly, poly[0], poly[1])
	}
	var dx, dy float64 = 1, 0
	transitions := 0
	for i := 1; i < n; i++ {
		j := i % (len(poly) / 2)
		x0, y0 := poly[2*i-2], poly[2*i-1]
		x1, y1 := poly[2*j], poly[2*j+1]
		segLen := math.Hypot(x1-x0, y1-y0)
		if segLen == 0 {
			continue
		}
		dx, dy = (x1-x0)/segLen, (y1-y0)/segLen
		// On the closing segment of a subpath whose first piece is held
		// back, a transition exactly at the start point is left to the
		// switch below: a dash ending there continues into the held piece
		// (and keeps the join), a dot there is the held piece.
		strict := hold && i == n-1
		pos := 0.0
		for d := segLen - pos; d > rem || (d == rem && !strict); d = segLen - pos {
			if transitions >= max {
				s.truncated = true
				return
			}
			transitions++
			pos += rem
			x, y := x0+dx*pos, y0+dy*pos
			if on {
				s.dpoly = append(s.dpoly, x, y)
				if hold && !held {
					s.dfirst = append(s.dfirst[:0], s.dpoly...)
					fdx, fdy, held = dx, dy, true
				} else {
					s.strokePoly(s.dpoly, false, dx, dy)
				}
				s.dpoly = s.dpoly[:0]
			} else {
				s.dpoly = append(s.dpoly[:0], x, y)
			}
			on = !on
			idx = (idx + 1) % steps
			rem = pat[idx%np]
		}
		rem -= segLen - pos
		if on {
			s.dpoly = append(s.dpoly, x1, y1)
		}
	}
	switch {
	case on && hold && !held:
		// The whole subpath lies in one dash: it stays closed.
		s.strokePoly(poly, true, dx, dy)
	case on && held:
		// The last dash runs into the first: one piece through the start.
		s.dpoly = append(s.dpoly, s.dfirst[2:]...)
		s.strokePoly(s.dpoly, false, dx, dy)
	default:
		if on && len(s.dpoly) >= 2 {
			s.strokePoly(s.dpoly, false, dx, dy)
		}
		if held {
			s.strokePoly(s.dfirst, false, fdx, fdy)
		}
	}
}

// strokePoly strokes one polyline (x,y pairs in user space). (dx, dy) is the
// direction used for a zero-length polyline with square caps.
//
// The outline is built like AGG's stroker (PDFium's rasterizer): one loop per
// open polyline (left side forward, end cap, right side backward, start
// cap), two loops of opposite orientation per closed one. Inner corners use
// the intersection of the offset lines when it lies within both segments, so
// ordinary outlines do not overlap themselves and coverage is exact; sharper
// inner corners fall back to routing through the vertex, which is still
// correct under NonZero.
func (s *Stroker) strokePoly(pts []float64, closed bool, dx, dy float64) {
	if s.dev {
		// Hairline: take the polyline to device space and stroke it one
		// pixel wide there.
		m := s.m
		d := s.hbuf[:0]
		for i := 0; i+1 < len(pts); i += 2 {
			x, y := m.Apply(pts[i], pts[i+1])
			d = append(d, x, y)
		}
		s.hbuf = d
		dx, dy = m.ApplyVec(dx, dy)
		s.m = Identity
		s.strokePolyAt(d, closed, dx, dy)
		s.m = m
		return
	}
	s.strokePolyAt(pts, closed, dx, dy)
}

// strokePolyAt strokes a polyline in the space s.m maps to device space.
func (s *Stroker) strokePolyAt(pts []float64, closed bool, dx, dy float64) {
	n := len(pts) / 2
	// Drop duplicate closing point.
	if closed && n > 1 && pts[0] == pts[2*n-2] && pts[1] == pts[2*n-1] {
		n--
	}
	if n == 0 {
		return
	}
	// Vertices without zero-length segments, and per-segment direction and
	// length: seg[i] leads from v[i] to v[i+1] (closing: v[n-1] to v[0]).
	v := s.dpts[:0]
	for i := 0; i < n; i++ {
		x, y := pts[2*i], pts[2*i+1]
		if k := len(v); k > 0 && v[k-2] == x && v[k-1] == y {
			continue
		}
		v = append(v, x, y)
	}
	if closed && len(v) > 2 && v[0] == v[len(v)-2] && v[1] == v[len(v)-1] {
		v = v[:len(v)-2]
	}
	s.dpts = v
	nv := len(v) / 2
	if nv == 1 {
		s.dot(v[0], v[1], dx, dy)
		return
	}
	if nv == 2 {
		closed = false // a closed two-point path strokes like an open one
	}
	nseg := nv - 1
	if closed {
		nseg = nv
	}
	seg := s.segs[:0]
	for i := 0; i < nseg; i++ {
		j := (i + 1) % nv
		ex, ey := v[2*j]-v[2*i], v[2*j+1]-v[2*i+1]
		l := math.Hypot(ex, ey)
		seg = append(seg, ex/l, ey/l, l)
	}
	s.segs = seg
	hw := s.hw
	if s.seg != nil && s.fastPoly(v, seg, closed) {
		return
	}
	pc := s.piece[:0]
	if !closed {
		// Left side forward.
		pc = append(pc, v[0]-seg[1]*hw, v[1]+seg[0]*hw)
		for i := 1; i < nv-1; i++ {
			pc = s.joinPts(pc, v[2*i], v[2*i+1], seg[3*i-3], seg[3*i-2], seg[3*i], seg[3*i+1], seg[3*i-1], seg[3*i+2])
		}
		k := 3 * (nseg - 1)
		ex, ey := v[2*nv-2], v[2*nv-1]
		pc = append(pc, ex-seg[k+1]*hw, ey+seg[k]*hw)
		pc = s.capPts(pc, ex, ey, seg[k], seg[k+1])
		// Right side backward.
		for i := nv - 2; i >= 1; i-- {
			pc = s.joinPts(pc, v[2*i], v[2*i+1], -seg[3*i], -seg[3*i+1], -seg[3*i-3], -seg[3*i-2], seg[3*i+2], seg[3*i-1])
		}
		pc = append(pc, v[0]+seg[1]*hw, v[1]-seg[0]*hw)
		pc = s.capPts(pc, v[0], v[1], -seg[0], -seg[1])
		s.piece = pc
		s.emitLoop(pc)
		return
	}
	for i := 0; i < nv; i++ {
		p := (i + nv - 1) % nv
		pc = s.joinPts(pc, v[2*i], v[2*i+1], seg[3*p], seg[3*p+1], seg[3*i], seg[3*i+1], seg[3*p+2], seg[3*i+2])
	}
	s.emitLoop(pc)
	pc = pc[:0]
	for i := nv - 1; i >= 0; i-- {
		p := (i + nv - 1) % nv
		pc = s.joinPts(pc, v[2*i], v[2*i+1], -seg[3*i], -seg[3*i+1], -seg[3*p], -seg[3*p+1], seg[3*i+2], seg[3*p+2])
	}
	s.piece = pc
	s.emitLoop(pc)
}

// joinPts appends the left-side outline points at vertex (x, y) between the
// incoming direction a and the outgoing direction b (unit vectors) with the
// adjacent segment lengths la and lb. The left normal of d is (-d.y, d.x).
func (s *Stroker) joinPts(pc []float64, x, y, ax, ay, bx, by, la, lb float64) []float64 {
	hw := s.hw
	cross := ax*by - ay*bx
	dot := ax*bx + ay*by
	nax, nay := -ay*hw, ax*hw
	nbx, nby := -by*hw, bx*hw
	if math.Abs(cross) < 1e-9 && dot > 0 {
		return append(pc, x+nax, y+nay)
	}
	if cross > 0 {
		// Inner corner: intersection of the offset lines when it lies
		// within both segments (half their length, so that neighbouring
		// corners can never overlap), else route through the vertex.
		if 1+dot > 1e-9 && hw*cross/(1+dot) <= 0.5*math.Min(la, lb) {
			k := 1 / (1 + dot)
			return append(pc, x+(nax+nbx)*k, y+(nay+nby)*k)
		}
		s.jag = true
		return append(pc, x+nax, y+nay, x, y, x+nbx, y+nby)
	}
	// Outer corner.
	pc = append(pc, x+nax, y+nay)
	switch s.st.Join {
	case RoundJoin:
		ang := math.Atan2(cross, dot)
		if cross == 0 {
			ang = -math.Pi // U-turn: go around the front
		}
		pc = s.arc(pc, x, y, nax, nay, ang)
		return pc
	case MiterJoin:
		ml := s.st.MiterLimit
		if ml < 1 {
			ml = 1
		}
		// Miter length / width = 1/sin(φ/2) = sqrt(2/(1+dot)).
		if 1+dot > 1e-12 && 2/(1+dot) <= ml*ml {
			k := 1 / (1 + dot)
			pc = append(pc, x+(nax+nbx)*k, y+(nay+nby)*k)
		}
	}
	return append(pc, x+nbx, y+nby)
}

// capPts appends the cap at (x, y) for a stroke ending in direction (ux, uy),
// going from the left offset (already appended) to the right offset.
func (s *Stroker) capPts(pc []float64, x, y, ux, uy float64) []float64 {
	hw := s.hw
	nx, ny := -uy*hw, ux*hw
	switch s.st.Cap {
	case SquareCap:
		pc = append(pc, x+nx+ux*hw, y+ny+uy*hw, x-nx+ux*hw, y-ny+uy*hw)
	case RoundCap:
		pc = s.arc(pc, x, y, nx, ny, -math.Pi)
		return pc
	}
	return append(pc, x-nx, y-ny)
}

// dot draws a zero-length subpath: a disc for round caps, a square aligned
// with (dx, dy) for square caps, nothing for butt caps.
func (s *Stroker) dot(x, y, dx, dy float64) {
	hw := s.hw
	pc := s.piece[:0]
	switch s.st.Cap {
	case RoundCap:
		pc = append(pc, x+hw, y)
		pc = s.arc(pc, x, y, hw, 0, 2*math.Pi)
	case SquareCap:
		l := math.Hypot(dx, dy)
		if l == 0 {
			dx, dy, l = 1, 0, 1
		}
		ux, uy := dx/l*hw, dy/l*hw
		nx, ny := -uy, ux
		pc = append(pc, x-ux+nx, y-uy+ny, x+ux+nx, y+uy+ny, x+ux-nx, y+uy-ny, x-ux-nx, y-uy-ny)
	default:
		return
	}
	s.piece = pc
	s.emitLoop(pc)
}

// arc appends points on the circle around (cx, cy), rotating the vector
// (ax, ay) by ang radians; the start point is excluded, the end included.
func (s *Stroker) arc(pc []float64, cx, cy, ax, ay, ang float64) []float64 {
	if s.stepA == 0 {
		s.stepA = math.Pi
		if r := s.rdev; r > flattenTol {
			s.stepA = 2 * math.Acos(1-flattenTol/r)
		}
	}
	n := 1
	if v := math.Abs(ang) / s.stepA; v > 1 {
		n = int(math.Ceil(min(v, maxSegs)))
	}
	sn, cs := math.Sincos(ang / float64(n))
	x, y := ax, ay
	for i := 0; i < n; i++ {
		x, y = x*cs-y*sn, x*sn+y*cs
		pc = append(pc, cx+x, cy+y)
	}
	return pc
}

// emitLoop transforms a closed user-space polygon into device space and
// emits its edges.
func (s *Stroker) emitLoop(pc []float64) {
	n := len(pc) / 2
	if n < 2 {
		return
	}
	m := s.m
	k := s.sink
	fx, fy := m.Apply(pc[0], pc[1])
	px, py := fx, fy
	for i := 1; i < n; i++ {
		x, y := m.Apply(pc[2*i], pc[2*i+1])
		k.AddLine(px, py, x, y)
		px, py = x, y
	}
	k.AddLine(px, py, fx, fy)
}

// dashFastOK reports whether every dash gap stays at least two device
// pixels wide after the caps' extension.
func dashFastOK(m Matrix, st *StrokeStyle, dev bool, sm float64) bool {
	if len(st.Dash) == 0 {
		return true
	}
	if sm == 0 {
		return false
	}
	smin := math.Abs(m.Det()) / sm
	ext := 0.0 // cap extension in device pixels
	if st.Cap != ButtCap {
		ext = st.Width * sm
		if dev {
			ext = 1
		}
	}
	gap := math.Inf(1)
	for i, d := range st.Dash {
		// With an odd count the pattern repeats with on and off swapped.
		if i%2 == 1 || len(st.Dash)%2 == 1 {
			gap = min(gap, d)
		}
	}
	return gap*smin-ext >= 2
}

// denseDash reports whether st's dash pattern is too dense under m to be
// walked dash by dash, and if so the fraction of the stroke it covers. The
// fraction counts the caps' extension into the gaps; it is taken in user
// space, where areas scale uniformly under m, except for hairlines, whose
// width and caps are one device pixel.
func denseDash(m Matrix, st *StrokeStyle) (float64, bool) {
	if len(st.Dash) == 0 {
		return 1, false
	}
	return denseDashSM(st, sigmaMax(m))
}

// denseDashSM is denseDash with sm = sigmaMax(m).
func denseDashSM(st *StrokeStyle, sm float64) (float64, bool) {
	pat := st.Dash
	np := len(pat)
	if np == 0 {
		return 1, false
	}
	period := 0.0
	for _, d := range pat {
		if !(d >= 0) || math.IsInf(d, 0) {
			return 1, false
		}
		period += d
	}
	steps := np
	if np%2 == 1 {
		steps, period = 2*np, 2*period
	}
	pd := period * sm // device period along the widest stretch
	if !(period > 0) || math.IsInf(period, 0) || !(sm > 0) ||
		pd > densePeriod && (float64(steps) <= denseSteps*pd || !uniformDash(pat, steps, 1/sm)) {
		return 1, false
	}
	scale, w := 1.0, st.Width
	if st.Width*sm < 1 {
		scale, w = sm, 1
	}
	return meanCoverage(pat, scale, w, st.Cap), true
}

// uniformDash reports whether every stretch of length l along pat, walked
// for steps entries starting with a dash, covers the pattern's mean
// on-fraction within 1/16. Only then does drawing a long pattern of tiny
// entries at its mean coverage keep every pixel close to its own coverage.
func uniformDash(pat []float64, steps int, l float64) bool {
	np := len(pat)
	period, on := 0.0, 0.0
	for i := 0; i < steps; i++ {
		period += pat[i%np]
		if i%2 == 0 {
			on += pat[i%np]
		}
	}
	lit := func(i int) float64 { return float64(1 - i%2) }
	// g is the dash length within the window [x, x+l). The window starts
	// in entry a, ra before its end, and ends in entry b, rb before its end.
	k := math.Floor(l / period)
	g := k * on
	b, pos := 0, l-k*period
	for n := 0; n < steps && pos >= pat[b%np]; n++ {
		g += lit(b) * pat[b%np]
		pos -= pat[b%np]
		b = (b + 1) % steps
	}
	g += lit(b) * pos
	rb := max(pat[b%np]-pos, 0)
	a, ra := 0, pat[0]
	mean, tol := on/period*l, l/16
	for a < steps {
		if math.Abs(g-mean) > tol {
			return false
		}
		// Slide the window to the next entry boundary at either end.
		d := min(ra, rb)
		g += d * (lit(b) - lit(a))
		ra -= d
		rb -= d
		if ra <= 0 {
			a++
			ra = pat[a%np]
		}
		if rb <= 0 {
			b = (b + 1) % steps
			rb = pat[b%np]
		}
	}
	return true
}

// meanCoverage returns the fraction of a stroke of width w that pat covers,
// with pattern lengths multiplied by scale.
func meanCoverage(pat []float64, scale, w float64, c Cap) float64 {
	np := len(pat)
	steps := np
	if np%2 == 1 {
		steps = 2 * np
	}
	period, off := 0.0, 0.0
	for i := 0; i < steps; i++ {
		d := pat[i%np] * scale
		period += d
		if i%2 == 1 {
			off += uncovered(d, w, c)
		}
	}
	return min(max(1-off/(period*w), 0), 1)
}

// uncovered returns the area a gap of length g leaves uncovered between two
// dashes of width w with the given caps.
func uncovered(g, w float64, c Cap) float64 {
	switch c {
	case SquareCap:
		return max(g-w, 0) * w
	case RoundCap:
		// The half discs of the neighbouring dashes cover the gap except
		// where 2·sqrt(r²-y²) < g, i.e. |y| > y0.
		r := w / 2
		y0 := math.Sqrt(max(r*r-g*g/4, 0))
		disc := func(y float64) float64 { // ∫ sqrt(r²-y²) dy
			return (y*math.Sqrt(max(r*r-y*y, 0)) + r*r*math.Asin(min(y/r, 1))) / 2
		}
		return 2 * (g*(r-y0) - 2*(disc(r)-disc(y0)))
	}
	return g * w
}
