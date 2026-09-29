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

// DefaultMaxDashes bounds the number of dashes per subpath; a pattern that
// would exceed it is drawn solid, which is visually indistinguishable at
// that density.
const DefaultMaxDashes = 1 << 20

// Stroker converts strokes into fill geometry (closed outlines to be filled
// with NonZero). Offsets are computed in user space and transformed
// afterwards, which is exact for anisotropic transforms.
//
// A Stroker is not safe for concurrent use. Buffers are retained.
type Stroker struct {
	// MaxDashes overrides DefaultMaxDashes when non-zero.
	MaxDashes int

	sink  LineSink
	hair  *hairliner
	m     Matrix
	st    *StrokeStyle
	hw    float64 // half width, user space
	tolU  float64 // flattening tolerance, user space
	stepA float64 // angular step for round joins/caps
	det   float64

	poly  []float64
	dpoly []float64
	dpts  []float64
	segs  []float64
	piece []float64
}

// sigmaMax returns the largest singular value of m's linear part.
func sigmaMax(m Matrix) float64 {
	a, b, c, d := m[0], m[1], m[2], m[3]
	s := a*a + b*b + c*c + d*d
	e := a*a + b*b - c*c - d*d
	f := a*c + b*d
	return math.Sqrt((s + math.Sqrt(e*e+4*f*f)) / 2)
}

// Stroke emits the outline of p stroked with st under m into sink.
func (s *Stroker) Stroke(sink LineSink, p *Path, m Matrix, st *StrokeStyle) {
	s.sink, s.hair = sink, nil
	s.run(p, m, st)
}

// IsHairline reports whether st under m is thinner than one device pixel in
// every direction. Such strokes are drawn as 1-pixel lines, like PDFium.
func IsHairline(m Matrix, st *StrokeStyle) bool {
	return st.Width*sigmaMax(m) < 1
}

func (s *Stroker) strokeHair(h *hairliner, p *Path, m Matrix, st *StrokeStyle) {
	s.sink, s.hair = nil, h
	s.run(p, m, st)
}

func (s *Stroker) run(p *Path, m Matrix, st *StrokeStyle) {
	if !m.finite() || !(st.Width >= 0) || math.IsInf(st.Width, 0) {
		return
	}
	sm := sigmaMax(m)
	if sm == 0 {
		return
	}
	s.m, s.st = m, st
	s.det = m.Det()
	s.hw = st.Width / 2
	s.tolU = flattenTol / sm
	if r := s.hw * sm; r > flattenTol {
		s.stepA = 2 * math.Acos(1-flattenTol/r)
	} else {
		s.stepA = math.Pi
	}
	if s.hair == nil && s.hw == 0 {
		return
	}
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
		// Invisible period: draw solid (it would average to near-solid).
		if !(sum*sm >= 0.1) {
			dashed = false
		}
	}

	pts := p.Points
	pi := 0
	closed := false
	var sx, sy float64 // start of the last closed subpath
	pending := false   // a segment after Close starts at (sx, sy)
	s.poly = s.poly[:0]
	flush := func() {
		if len(s.poly) > 0 {
			if dashed {
				s.dash(s.poly, closed)
			} else {
				s.strokePoly(s.poly, closed, 1, 0)
			}
		}
		s.poly = s.poly[:0]
		closed = false
	}
	begin := func() {
		if len(s.poly) == 0 && pending {
			s.addPt(sx, sy, true)
		}
		pending = false
	}
	for _, v := range p.Verbs {
		if int(v) >= len(numPoints) || pi+numPoints[v] > len(pts) {
			break
		}
		switch v {
		case MoveTo:
			flush()
			pending = false
			s.addPt(float64(pts[pi].X), float64(pts[pi].Y), true)
		case LineTo:
			begin()
			s.addPt(float64(pts[pi].X), float64(pts[pi].Y), false)
		case QuadTo:
			begin()
			s.quad(pts[pi], pts[pi+1])
		case CubicTo:
			begin()
			s.cubic(pts[pi], pts[pi+1], pts[pi+2])
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
	pat := s.st.Dash
	period := 0.0
	for _, d := range pat {
		period += d
	}
	n := len(poly) / 2
	if closed && n > 1 {
		n++ // closing segment
	}
	total := 0.0
	for i := 1; i < n; i++ {
		j := i % (len(poly) / 2)
		total += math.Hypot(poly[2*j]-poly[2*i-2], poly[2*j+1]-poly[2*i-1])
	}
	max := s.MaxDashes
	if max == 0 {
		max = DefaultMaxDashes
	}
	if total/period*float64(len(pat)) > float64(max) {
		s.strokePoly(poly, closed, 1, 0)
		return
	}
	// Position in the pattern after the phase.
	idx := 0
	phase := math.Mod(s.st.DashPhase, period)
	if phase < 0 {
		phase += period
	}
	for phase >= pat[idx] && phase > 0 {
		phase -= pat[idx]
		idx = (idx + 1) % len(pat)
	}
	rem := pat[idx] - phase
	on := idx%2 == 0

	if len(poly) == 2 {
		if on {
			s.strokePoly(poly, false, 1, 0)
		}
		return
	}
	s.dpoly = s.dpoly[:0]
	if on {
		s.dpoly = append(s.dpoly, poly[0], poly[1])
	}
	var dx, dy float64 = 1, 0
	for i := 1; i < n; i++ {
		j := i % (len(poly) / 2)
		x0, y0 := poly[2*i-2], poly[2*i-1]
		x1, y1 := poly[2*j], poly[2*j+1]
		segLen := math.Hypot(x1-x0, y1-y0)
		if segLen == 0 {
			continue
		}
		dx, dy = (x1-x0)/segLen, (y1-y0)/segLen
		pos := 0.0
		for segLen-pos >= rem {
			pos += rem
			x, y := x0+dx*pos, y0+dy*pos
			if on {
				s.dpoly = append(s.dpoly, x, y)
				s.strokePoly(s.dpoly, false, dx, dy)
				s.dpoly = s.dpoly[:0]
			} else {
				s.dpoly = append(s.dpoly[:0], x, y)
			}
			on = !on
			idx = (idx + 1) % len(pat)
			rem = pat[idx]
		}
		rem -= segLen - pos
		if on {
			s.dpoly = append(s.dpoly, x1, y1)
		}
	}
	if on && len(s.dpoly) >= 2 {
		s.strokePoly(s.dpoly, false, dx, dy)
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
	n := len(pts) / 2
	// Drop duplicate closing point.
	if closed && n > 1 && pts[0] == pts[2*n-2] && pts[1] == pts[2*n-1] {
		n--
	}
	if n == 0 {
		return
	}
	if s.hair != nil {
		if n == 1 || (n == 2 && pts[0] == pts[2] && pts[1] == pts[3]) {
			s.dot(pts[0], pts[1], dx, dy)
			return
		}
		m := s.m
		px, py := m.Apply(pts[0], pts[1])
		for i := 1; i < n; i++ {
			x, y := m.Apply(pts[2*i], pts[2*i+1])
			s.hair.line(px, py, x, y)
			px, py = x, y
		}
		if closed {
			x, y := m.Apply(pts[0], pts[1])
			s.hair.line(px, py, x, y)
		}
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
	if s.hair != nil {
		if s.st.Cap == ButtCap {
			return
		}
		// A one-pixel dot along the direction.
		ddx, ddy := s.m.ApplyVec(dx, dy)
		l := math.Hypot(ddx, ddy)
		if l == 0 {
			ddx, ddy, l = 1, 0, 1
		}
		ddx, ddy = ddx/l*0.5, ddy/l*0.5
		cx, cy := s.m.Apply(x, y)
		s.hair.line(cx-ddx, cy-ddy, cx+ddx, cy+ddy)
		return
	}
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
	n := int(math.Ceil(math.Abs(ang) / s.stepA))
	if n < 1 {
		n = 1
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
