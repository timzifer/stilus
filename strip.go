package stilus

import (
	"math"
	"slices"
)

// Analytic stroke path.
//
// A stroked polyline is the region inside its outline (see strokePoly). Each
// vertex – a join, or an end with its cap – owns the outline points it
// produced, and those points span a band of device rows. Between the bands
// of two neighbouring vertices, the stroke of the segment joining them is
// bounded only by its two parallel long sides, so each pixel's covered area
// has a closed form: the difference of two half-plane areas. Those rows are
// filled directly, without cells, sweep or edge walking.
//
// Inside the bands, the outline edges incident to the band's vertices are
// added to the stroke's accumulation rasterizer, restricted to the band's
// rows. There the result is exactly that of the outline path. Bands that
// overlap or leave fewer than minMiddleRows between them are merged into
// clusters; a polyline that collapses into a single cluster simply takes
// the outline path.
//
// At V-shaped corners the two strips run side by side beyond the inner
// notch; the band is extended until they are two pixels apart, so pixels
// they share are still summed in the accumulator. Dash pieces use the path
// only on straight subpaths with gaps of at least two pixels. What remains
// different from the outline path: separate parts of one stroke that touch
// or cross in analytic rows (a polyline crossing itself, separate subpaths
// meeting at an angle) are composited one over the other instead of summed,
// which affects only antialiased pixels, and only for opaque paint – the
// only paint Canvas uses this path for.

// minMiddleRows is the number of analytic rows below which a segment is
// merged into the bands of its ends.
const minMiddleRows = 3

// maxMergeRounds bounds cluster merging; beyond it the outline is used.
const maxMergeRounds = 16

// minInteriorRun is the fully covered width (pixels) from which analytic
// rows emit their interior as a run.
const minInteriorRun = 8

type fastState struct {
	pts    []float64 // outline points, user space, then device space
	own    []int32   // vertex owning each point
	loops  [2]int    // end index (in points) of the first and second loop
	vlo    []int32   // per vertex: band rows [vlo, vhi)
	vhi    []int32
	parent []int32 // union-find over vertices
	hlo    []int32 // per cluster root: rows [hlo, hhi)
	hhi    []int32
	roots  []int32
}

func (f *fastState) find(v int32) int32 {
	for f.parent[v] != v {
		f.parent[v] = f.parent[f.parent[v]]
		v = f.parent[v]
	}
	return v
}

func (f *fastState) union(a, b int32) {
	a, b = f.find(a), f.find(b)
	if a != b {
		f.parent[b] = a
	}
}

// fastPoly strokes the deduplicated polyline v (len(v)/2 vertices; seg
// holds per-segment unit direction and length) on the analytic path. It
// returns false, having emitted nothing, when the outline path should be
// used instead.
func (s *Stroker) fastPoly(v, seg []float64, closed bool) bool {
	s.fastTries++
	nv := len(v) / 2
	nseg := len(seg) / 3
	if nseg < 1 || (s.dashing && (!s.dashFast || !s.dashStraight)) {
		return false
	}
	f := &s.fast
	hw := s.hw
	pts := f.pts[:0]
	own := f.own[:0]
	s.jag = false
	mark := func(i int) {
		for len(own) < len(pts)/2 {
			own = append(own, int32(i))
		}
	}
	if !closed {
		// Left side forward, end cap, right side backward, start cap:
		// the same sequence as the outline path.
		pts = append(pts, v[0]-seg[1]*hw, v[1]+seg[0]*hw)
		mark(0)
		for i := 1; i < nv-1; i++ {
			pts = s.joinPts(pts, v[2*i], v[2*i+1], seg[3*i-3], seg[3*i-2], seg[3*i], seg[3*i+1], seg[3*i-1], seg[3*i+2])
			mark(i)
		}
		k := 3 * (nseg - 1)
		ex, ey := v[2*nv-2], v[2*nv-1]
		pts = append(pts, ex-seg[k+1]*hw, ey+seg[k]*hw)
		pts = s.capPts(pts, ex, ey, seg[k], seg[k+1])
		mark(nv - 1)
		for i := nv - 2; i >= 1; i-- {
			pts = s.joinPts(pts, v[2*i], v[2*i+1], -seg[3*i], -seg[3*i+1], -seg[3*i-3], -seg[3*i-2], seg[3*i+2], seg[3*i-1])
			mark(i)
		}
		pts = append(pts, v[0]+seg[1]*hw, v[1]-seg[0]*hw)
		pts = s.capPts(pts, v[0], v[1], -seg[0], -seg[1])
		pts = pts[:len(pts)-2] // the cap ends on the loop's first point
		mark(0)
		f.loops = [2]int{len(pts) / 2, len(pts) / 2}
	} else {
		for i := 0; i < nv; i++ {
			p := (i + nv - 1) % nv
			pts = s.joinPts(pts, v[2*i], v[2*i+1], seg[3*p], seg[3*p+1], seg[3*i], seg[3*i+1], seg[3*p+2], seg[3*i+2])
			mark(i)
		}
		f.loops[0] = len(pts) / 2
		for i := nv - 1; i >= 0; i-- {
			p := (i + nv - 1) % nv
			pts = s.joinPts(pts, v[2*i], v[2*i+1], -seg[3*i], -seg[3*i+1], -seg[3*p], -seg[3*p+1], seg[3*i+2], seg[3*p+2])
			mark(i)
		}
		f.loops[1] = len(pts) / 2
	}
	f.pts, f.own = pts, own

	// Device space and per-vertex bands. From here on every way out uses
	// the outline just built: either split into bands and analytic rows, or
	// emitted whole (emitOutline) when the analytic path does not apply.
	m := s.m
	f.vlo = grow32(f.vlo, nv)
	f.vhi = grow32(f.vhi, nv)
	f.parent = grow32(f.parent, nv)
	for i := 0; i < nv; i++ {
		f.vlo[i], f.vhi[i] = math.MaxInt32, math.MinInt32
		f.parent[i] = int32(i)
	}
	huge := false
	for k := 0; k < len(pts); k += 2 {
		x, y := m.Apply(pts[k], pts[k+1])
		if !(math.Abs(x) < 1<<30 && math.Abs(y) < 1<<30) {
			huge = true
			x, y = clampCoord(x), clampCoord(y)
		}
		pts[k], pts[k+1] = x, y
		o := own[k/2]
		f.vlo[o] = min(f.vlo[o], int32(math.Floor(y)))
		f.vhi[o] = max(f.vhi[o], int32(math.Ceil(y)))
	}
	if s.jag || huge {
		// A sharp inner corner routed through its vertex makes the stroke
		// overlap itself beyond the corner's band.
		return s.emitOutline()
	}

	// At a V-shaped corner (both segments leave the vertex towards the same
	// vertical side) the two strips run side by side beyond the inner notch
	// and would share pixels; extend the band until their inner sides are
	// two pixels apart, so shared pixels are always summed in the
	// accumulator like on the outline path.
	for i := 0; i < nv; i++ {
		if !closed && (i == 0 || i == nv-1) {
			continue
		}
		ka, kb := (i+nseg-1)%nseg, i
		ax, ay := m.ApplyVec(seg[3*ka], seg[3*ka+1])
		bx, by := m.ApplyVec(seg[3*kb], seg[3*kb+1])
		if -ay*by <= 0 {
			continue // one segment above the vertex, one below
		}
		// Inner notch: intersection of the inner offset lines (user space).
		ux0, uy0, ux1, uy1 := seg[3*ka], seg[3*ka+1], seg[3*kb], seg[3*kb+1]
		cross, dot := ux0*uy1-uy0*ux1, ux0*ux1+uy0*uy1
		if 1+dot < 1e-9 {
			return s.emitOutline()
		}
		sg := -hw / (1 + dot) // right side is inner for cross < 0
		if cross > 0 {
			sg = -sg
		}
		_, qy := m.Apply(v[2*i]+sg*(-uy0-uy1), v[2*i+1]+sg*(ux0+ux1))
		_, py := m.Apply(v[2*i], v[2*i+1])
		di := math.Abs(ax/ay - bx/by) // horizontal divergence per row
		t := 4096.0
		if di > 2.0/4096 {
			t = 2 / di
		}
		if qy >= py {
			f.vhi[i] = max(f.vhi[i], int32(math.Ceil(qy+t))+1)
		} else {
			f.vlo[i] = min(f.vlo[i], int32(math.Floor(qy-t))-1)
		}
	}

	// Merge bands into clusters until every remaining segment has enough
	// analytic rows and no two clusters share rows.
	f.hlo = grow32(f.hlo, nv)
	f.hhi = grow32(f.hhi, nv)
	for round := 0; ; round++ {
		if round == maxMergeRounds {
			return s.emitOutline()
		}
		for i := 0; i < nv; i++ {
			f.hlo[i], f.hhi[i] = math.MaxInt32, math.MinInt32
		}
		f.roots = f.roots[:0]
		for i := int32(0); i < int32(nv); i++ {
			r := f.find(i)
			if f.hlo[r] == math.MaxInt32 {
				f.roots = append(f.roots, r)
			}
			f.hlo[r] = min(f.hlo[r], f.vlo[i])
			f.hhi[r] = max(f.hhi[r], f.vhi[i])
		}
		if len(f.roots) == 1 {
			return s.emitOutline() // one cluster: the plain outline, cheaper
		}
		changed := false
		for k := 0; k < nseg; k++ {
			a, b := f.find(int32(k)), f.find(int32((k+1)%nv))
			if a == b {
				continue
			}
			ddx, ddy := m.ApplyVec(seg[3*k], seg[3*k+1])
			if !(math.Abs(ddy) > 1e-6*math.Hypot(ddx, ddy)) ||
				!(f.hhi[a]+minMiddleRows <= f.hlo[b] || f.hhi[b]+minMiddleRows <= f.hlo[a]) {
				f.union(a, b)
				changed = true
			}
		}
		if !changed {
			roots := f.roots
			slices.SortFunc(roots, func(a, b int32) int { return int(f.hlo[a] - f.hlo[b]) })
			for i := 1; i < len(roots); i++ {
				if f.hlo[roots[i]] < f.hhi[roots[i-1]] {
					f.union(roots[i-1], roots[i])
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	// Band rows: outline edges incident to each cluster, limited to its rows.
	sink, fill := s.sink, s.seg
	start := 0
	for _, end := range f.loops {
		for k := start; k < end; k++ {
			k1 := k + 1
			if k1 == end {
				k1 = start
			}
			ca, cb := f.find(own[k]), f.find(own[k1])
			x0, y0, x1, y1 := pts[2*k], pts[2*k+1], pts[2*k1], pts[2*k1+1]
			fill.limitRows(int(f.hlo[ca]), int(f.hhi[ca]))
			sink.AddLine(x0, y0, x1, y1)
			if cb != ca {
				fill.limitRows(int(f.hlo[cb]), int(f.hhi[cb]))
				sink.AddLine(x0, y0, x1, y1)
			}
		}
		start = end
	}
	fill.unlimitRows()
	// The outline winds +1 in device space unless m mirrors (in hairline
	// mode m is the identity: the outline was built in device space).
	fill.setOrientation(m.Det() < 0)

	// Analytic rows between the clusters of each segment's ends.
	for k := 0; k < nseg; k++ {
		a, b := f.find(int32(k)), f.find(int32((k+1)%nv))
		if a == b {
			continue
		}
		y0, y1 := f.hhi[a], f.hlo[b]
		if f.hhi[b] <= f.hlo[a] {
			y0, y1 = f.hhi[b], f.hlo[a]
		}
		ux, uy := seg[3*k], seg[3*k+1]
		nx, ny := -uy*hw, ux*hw
		j := (k + 1) % nv
		ax, ay := m.Apply(v[2*k]+nx, v[2*k+1]+ny)
		bx, by := m.Apply(v[2*j]+nx, v[2*j+1]+ny)
		dx, dy := m.Apply(v[2*k]-nx, v[2*k+1]-ny)
		fill.middle(ax, ay, bx, by, dx, dy, int(y0), int(y1))
	}
	s.fastHits++
	return true
}

// emitOutline sends the whole device-space outline built by fastPoly to the
// sink, as the outline path would, and reports the polyline as done.
func (s *Stroker) emitOutline() bool {
	f := &s.fast
	pts, start := f.pts, 0
	for _, end := range f.loops {
		for k := start; k < end; k++ {
			k1 := k + 1
			if k1 == end {
				k1 = start
			}
			s.sink.AddLine(pts[2*k], pts[2*k+1], pts[2*k1], pts[2*k1+1])
		}
		start = end
	}
	return true
}

func grow32(s []int32, n int) []int32 {
	if cap(s) < n {
		return make([]int32, n, n+n/2)
	}
	return s[:n]
}

// segFast is the Canvas side of the analytic path.
type segFast struct {
	r     *Rasterizer // the stroke's rasterizer; composited after the stroke
	b     Blitter
	cov   []uint8
	solid *SolidBlitter // optional fused compositing for thin opaque strips
	frac  [4]uint8      // fractional clip borders bypassing solid is forbidden
	// Border columns and rows of a rectangle clip with partial coverage
	// (-1 when none). Pixels there are not composited directly: two parts
	// of the stroke covering the same pixel would be reduced by the clip
	// twice. They are summed in the stroke's accumulator instead (inject).
	bx0, bx1, by0, by1 int
	neg                bool // outline winds negatively in device space
}

// setBorder takes the partially covered border columns and rows of cs, if
// parts of the stroke may overlap.
func (f *segFast) setBorder(cs *clipState, overlap bool) {
	f.bx0, f.bx1, f.by0, f.by1 = -1<<31, -1<<31, -1<<31, -1<<31
	if !overlap {
		return
	}
	if cs.frac[0] != 255 {
		f.bx0 = cs.bounds.Min.X
	}
	if cs.frac[2] != 255 {
		f.bx1 = cs.bounds.Max.X - 1
	}
	if cs.frac[1] != 255 {
		f.by0 = cs.bounds.Min.Y
	}
	if cs.frac[3] != 255 {
		f.by1 = cs.bounds.Max.Y - 1
	}
}

// inject adds a pixel's covered area c to the stroke's accumulator: a pair
// of vertical edges at x and x+1 of height c covers exactly that pixel.
func (f *segFast) inject(x, y int, c float64) {
	if c <= 0 {
		return
	}
	f.injectRect(x, x+1, y, min(c, 1))
}

// injectRect adds coverage c to the pixels [x0, x1) of row y: two vertical
// edges, wound like the stroke's outline (negatively under a mirroring
// transform) so they add to the outline's winding instead of cancelling it.
func (f *segFast) injectRect(x0, x1, y int, c float64) {
	fx0, fx1, fy := float64(x0), float64(x1), float64(y)
	if f.neg {
		f.r.AddLine(fx0, fy+c, fx0, fy)
		f.r.AddLine(fx1, fy, fx1, fy+c)
		return
	}
	f.r.AddLine(fx0, fy, fx0, fy+c)
	f.r.AddLine(fx1, fy+c, fx1, fy)
}

func (f *segFast) setOrientation(neg bool) { f.neg = neg }

func (f *segFast) limitRows(y0, y1 int) { f.r.limitRows(y0, y1) }
func (f *segFast) unlimitRows()         { f.r.unlimitRows() }

// middle fills rows [y0, y1) of the strip between the line through A and B
// and the parallel line through D. Pixel coverage is the area of the pixel
// between the lines: the difference of two half-plane areas, each the
// cumulative distribution of the pixel's projection onto the normal (a
// trapezoid).
func (f *segFast) middle(ax, ay, bx, by, dx, dy float64, y0, y1 int) {
	clip := f.r.clip
	y0, y1 = max(y0, clip.Min.Y), min(y1, clip.Max.Y)
	vx, vy := bx-ax, by-ay
	l := math.Hypot(vx, vy)
	if y0 >= y1 || !(l > 0) || vy == 0 {
		return
	}
	nx, ny := -vy/l, vx/l
	k1, k2 := nx*ax+ny*ay, nx*dx+ny*dy
	if k1 < k2 {
		k1, k2 = k2, k1
	}
	a, b := math.Abs(nx), math.Abs(ny)
	if a < b {
		a, b = b, a
	}
	tr := trapezoid{h: (a + b) / 2, mt: (a - b) / 2, inva: 1 / a}
	if b > 1e-9 {
		tr.inv2ab = 1 / (2 * a * b)
	}
	h := tr.h
	// x of both lines at y: x = px + (y - py)·sl. The left line is
	// leftmost at the row's bottom when sl < 0, at its top otherwise; the
	// right line is rightmost at the other end.
	sl := vx / vy
	xl, yl, xr, yr := ax, ay, dx, dy
	if ax+(dy-ay)*sl > dx {
		xl, yl, xr, yr = dx, dy, ax, ay
	}
	dtop := 0.0
	if sl < 0 {
		dtop = 1
	}
	if cap(f.cov) < clip.Dx() {
		f.cov = make([]uint8, clip.Dx())
	}
	// A pixel is fully covered when its whole projection lies between the
	// lines: k2 + h <= n·c <= k1 - h. n·c is linear in the column, so the
	// covered columns of a row form one interval, emitted as a run.
	inx := 1 / nx
	rc := rowCtx{
		nx: nx, ny: ny, inx: inx, k1: k1, k2: k2, lo: k2 + h, hi: k1 - h, tr: tr,
		xl: xl + (dtop-yl)*sl, xr: xr + (1-dtop-yr)*sl, sl: sl,
		// Only strips wide enough to have a real interior are split into
		// edge pixels and a run; for thin strokes one span is cheaper.
		runs: (k1-k2-2*h)*math.Abs(inx) >= minInteriorRun,
	}
	if f.solid != nil && !rc.runs {
		f.solidRows(y0, y1, &rc)
	} else {
		f.blitRows(y0, y1, &rc)
	}
}

// rowCtx holds what the rows of one strip share.
type rowCtx struct {
	nx, ny, inx, k1, k2 float64
	xl, xr, sl          float64 // x of the left and right line at row y: xl + y·sl, xr + y·sl
	lo, hi              float64 // a pixel is fully covered when lo <= n·c <= hi
	runs                bool
	col0, col1          int // partially covered clip border columns (solidRows)
	tr                  trapezoid
}

func (rc *rowCtx) cov(d float64) float64 { return rc.tr.area(rc.k1-d) - rc.tr.area(rc.k2-d) }

// span returns the pixels [i0, i1) of row j the strip may cover, clipped
// to [cx0, cx1), and the row as a float. (Small enough to inline: it runs
// once per row.)
func (rc *rowCtx) span(j, cx0, cx1 int) (i0, i1 int, fy float64) {
	fy = float64(j)
	return max(ffloor(rc.xl+fy*rc.sl), cx0), min(ffloor(rc.xr+fy*rc.sl)+1, cx1), fy
}

// centre returns n·c of the centre of pixel (i0, fy).
func (rc *rowCtx) centre(i0 int, fy float64) float64 {
	return rc.nx*(float64(i0)+0.5) + rc.ny*(fy+0.5)
}

// borders returns the clip's partially covered border columns (the first
// and last ones, or math.MinInt when fully covered): rows touching them
// are composited through the frac blitter or summed in the accumulator
// instead of directly. The border rows are the clip's first and last.
func (f *segFast) borders() (col0, col1 int) {
	col0, col1 = math.MinInt, math.MinInt
	if f.frac[0] != 255 {
		col0 = f.r.clip.Min.X
	}
	if f.frac[2] != 255 {
		col1 = f.r.clip.Max.X
	}
	return
}

// solidRows composites rows [y0, y1) of an opaque thin strip directly: the
// same coverage and SWAR blend as the scalar blitter, without a coverage
// buffer or a second pass over the span. Rows on a partially covered clip
// border take the general path (row); with SIMD kernels wide rows do too.
func (f *segFast) solidRows(y0, y1 int, rc *rowCtx) {
	clip := f.r.clip
	cx0, cx1 := clip.Min.X, clip.Max.X
	// The clip's first and last row can only be the strip's first and
	// last; they are taken out of the loop.
	if y0 == clip.Min.Y && f.frac[1] != 255 {
		if i0, i1, fy := rc.span(y0, cx0, cx1); i0 < i1 {
			f.row(y0, i0, i1, rc.centre(i0, fy), rc)
		}
		y0++
	}
	last := -1
	if y1 == clip.Max.Y && f.frac[3] != 255 && y1 > y0 {
		y1--
		last = y1
	}
	rc.col0, rc.col1 = f.borders()
	solid := f.solid
	nx, k1, k2, tr := rc.nx, rc.k1, rc.k2, rc.tr
	for j := y0; j < y1; j++ {
		i0, i1, fy := rc.span(j, cx0, cx1)
		if i0 >= i1 {
			continue
		}
		d := rc.centre(i0, fy)
		if i0 == rc.col0 || i1 == rc.col1 || (SIMD() && i1-i0 >= 16) {
			f.row(j, i0, i1, d, rc)
			continue
		}
		row := solid.t.row(j, i0, i1)
		for i, dst := range row {
			a := quant(tr.area(k1-d) - tr.area(k2-d))
			d += nx
			switch a {
			case 0:
			case 255:
				row[i] = solid.c
			default:
				row[i] = lerpx(solid.cx, dst, uint32(a))
			}
		}
	}
	if last >= 0 {
		if i0, i1, fy := rc.span(last, cx0, cx1); i0 < i1 {
			f.row(last, i0, i1, rc.centre(i0, fy), rc)
		}
	}
}

// blitRows emits rows [y0, y1) through the blitter chain: edge pixels as
// coverage, a fully covered interior as a run. Rows on a partially
// covered clip border row, or crossing a border column, of a stroke whose
// parts may overlap go to the accumulator instead.
func (f *segFast) blitRows(y0, y1 int, rc *rowCtx) {
	cx0, cx1 := f.r.clip.Min.X, f.r.clip.Max.X
	nx, k1, k2, tr := rc.nx, rc.k1, rc.k2, rc.tr
	for j := y0; j < y1; j++ {
		i0, i1, fy := rc.span(j, cx0, cx1)
		if i0 >= i1 {
			continue
		}
		d0 := rc.centre(i0, fy)
		if j == f.by0 || j == f.by1 {
			f.borderRow(j, i0, i1, d0, rc)
			continue
		}
		if (f.bx0 >= i0 && f.bx0 < i1) || (f.bx1 >= i0 && f.bx1 < i1) {
			f.borderCols(j, i0, i1, d0, rc)
			continue
		}
		if rc.runs {
			f.emitRow(j, i0, i1, d0, rc)
			continue
		}
		// A thin strip's row: one coverage span (emitRow, inline).
		cov := f.cov[:i1-i0]
		d := d0
		for i := range cov {
			cov[i] = quant(tr.area(k1-d) - tr.area(k2-d))
			d += nx
		}
		emitCoverage(f.b, j, i0, cov)
	}
}

// row handles pixels [i0, i1) of row j (d0 = n·centre of pixel i0) that
// solidRows cannot take: rows in a partially covered clip border row or
// crossing a border column go to the accumulator, the rest through the
// blitter chain.
func (f *segFast) row(j, i0, i1 int, d0 float64, rc *rowCtx) {
	if j == f.by0 || j == f.by1 {
		f.borderRow(j, i0, i1, d0, rc)
		return
	}
	if (f.bx0 >= i0 && f.bx0 < i1) || (f.bx1 >= i0 && f.bx1 < i1) {
		f.borderCols(j, i0, i1, d0, rc)
		return
	}
	f.emitRow(j, i0, i1, d0, rc)
}

// emitRow blits pixels [i0, i1) of row j (d0 = n·centre of pixel i0)
// through the blitter chain: edge pixels as coverage, a fully covered
// interior as a run.
func (f *segFast) emitRow(j, i0, i1 int, d0 float64, rc *rowCtx) {
	n := i1 - i0
	a, b := n, n // interior [a, b) relative to i0; none by default
	if rc.runs {
		// n·c is linear in the column, so the covered columns of a row
		// form one interval.
		ta, tb := (rc.lo-d0)*rc.inx, (rc.hi-d0)*rc.inx
		if ta > tb {
			ta, tb = tb, ta
		}
		if ia, ib := max(int(math.Ceil(ta-1e-9)), 0), min(ffloor(tb+1e-9)+1, n); ia < ib {
			a, b = ia, ib
		}
	}
	cov := f.cov[:n]
	d := d0
	for i := 0; i < a; i++ {
		cov[i] = quant(rc.cov(d))
		d += rc.nx
	}
	if a > 0 {
		emitCoverage(f.b, j, i0, cov[:a])
	}
	if b > a {
		f.b.BlitRun(j, i0+a, i0+b, 255)
	}
	d = d0 + float64(b)*rc.nx
	for i := b; i < n; i++ {
		cov[i] = quant(rc.cov(d))
		d += rc.nx
	}
	if n > b {
		emitCoverage(f.b, j, i0+b, cov[b:n])
	}
}

// borderCols handles a row crossing a partially covered clip border
// column: the border pixels go to the accumulator, the pieces between them
// are emitted like any other row.
func (f *segFast) borderCols(j, i0, i1 int, d0 float64, rc *rowCtx) {
	x := i0
	for _, bx := range [2]int{min(f.bx0, f.bx1), max(f.bx0, f.bx1)} {
		if bx < x || bx >= i1 {
			continue
		}
		if bx > x {
			f.emitRow(j, x, bx, d0+float64(x-i0)*rc.nx, rc)
		}
		f.inject(bx, j, rc.cov(d0+float64(bx-i0)*rc.nx))
		x = bx + 1
	}
	if x < i1 {
		f.emitRow(j, x, i1, d0+float64(x-i0)*rc.nx, rc)
	}
}

// borderRow sends a row lying in a partially covered clip border row to
// the accumulator: fully covered stretches as one rectangle each, edge
// pixels one by one.
func (f *segFast) borderRow(j, i0, i1 int, d float64, rc *rowCtx) {
	// The current fully covered stretch starts at runStart while inRun;
	// x can be negative, so no coordinate doubles as the marker.
	inRun, runStart := false, 0
	for x := i0; x < i1; x++ {
		c := rc.cov(d)
		d += rc.nx
		if c >= 1-1e-12 {
			if !inRun {
				inRun, runStart = true, x
			}
			continue
		}
		if inRun {
			f.injectRect(runStart, x, j, 1)
			inRun = false
		}
		f.inject(x, j, c)
	}
	if inRun {
		f.injectRect(runStart, i1, j, 1)
	}
}

// trapezoid is the distribution of a unit pixel projected onto a unit
// normal (a >= b are the normal's absolute components): half-width h, flat
// top of half-width mt and height 1/a.
type trapezoid struct{ h, mt, inv2ab, inva float64 }

// area of the pixel on the side n·(p-c) <= u.
func (t trapezoid) area(u float64) float64 {
	switch {
	case u <= -t.h:
		return 0
	case u >= t.h:
		return 1
	case u < -t.mt:
		v := u + t.h
		return v * v * t.inv2ab
	case u > t.mt:
		v := t.h - u
		return 1 - v*v*t.inv2ab
	default:
		return 0.5 + u*t.inva
	}
}

// quant maps a covered area to coverage like the accumulation rasterizer.
func quant(c float64) uint8 {
	v := int32(c * 256)
	if v > 255 {
		return 255
	} else if v < 0 {
		return 0
	}
	return uint8(v)
}

// ffloor is floor for the moderate magnitudes used here (|v| < 2^31).
func ffloor(v float64) int {
	i := int(v)
	if float64(i) > v {
		i--
	}
	return i
}
