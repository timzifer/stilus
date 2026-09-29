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
	if s.jag {
		// A sharp inner corner routed through its vertex makes the stroke
		// overlap itself beyond the corner's band.
		return false
	}

	// Device space and per-vertex bands.
	m := s.m
	f.vlo = grow32(f.vlo, nv)
	f.vhi = grow32(f.vhi, nv)
	f.parent = grow32(f.parent, nv)
	for i := 0; i < nv; i++ {
		f.vlo[i], f.vhi[i] = math.MaxInt32, math.MinInt32
		f.parent[i] = int32(i)
	}
	for k := 0; k < len(pts); k += 2 {
		x, y := m.Apply(pts[k], pts[k+1])
		if !(math.Abs(x) < 1<<30 && math.Abs(y) < 1<<30) {
			return false
		}
		pts[k], pts[k+1] = x, y
		o := own[k/2]
		f.vlo[o] = min(f.vlo[o], int32(math.Floor(y)))
		f.vhi[o] = max(f.vhi[o], int32(math.Ceil(y)))
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
			return false
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
			return false
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
			return false // one cluster: the outline path is the same, cheaper
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

func grow32(s []int32, n int) []int32 {
	if cap(s) < n {
		return make([]int32, n, n+n/2)
	}
	return s[:n]
}

// segFast is the Canvas side of the analytic path.
type segFast struct {
	r   *Rasterizer // the stroke's rasterizer; composited after the stroke
	b   Blitter
	cov []uint8
}

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
	h, mt := (a+b)/2, (a-b)/2
	inv2ab := 0.0
	if b > 1e-9 {
		inv2ab = 1 / (2 * a * b)
	}
	inva := 1 / a
	area := func(u float64) float64 { // area of the pixel with n·(p-c) <= u
		switch {
		case u <= -h:
			return 0
		case u >= h:
			return 1
		case u < -mt:
			t := u + h
			return t * t * inv2ab
		case u > mt:
			t := h - u
			return 1 - t*t*inv2ab
		default:
			return 0.5 + u*inva
		}
	}
	// x of both lines at y: x = px + (y - py)·sl. The left line at a row
	// is leftmost at the row's top when sl < 0, at its bottom otherwise.
	sl := vx / vy
	xl, yl, xr, yr := ax, ay, dx, dy
	if ax+(dy-ay)*sl > dx {
		xl, yl, xr, yr = dx, dy, ax, ay
	}
	dtop := 0.0
	if sl < 0 {
		dtop = 1
	}
	cx0, cx1 := clip.Min.X, clip.Max.X
	if cap(f.cov) < clip.Dx() {
		f.cov = make([]uint8, clip.Dx())
	}
	for j := y0; j < y1; j++ {
		fy := float64(j)
		i0 := max(ffloor(xl+(fy+dtop-yl)*sl), cx0)
		i1 := min(ffloor(xr+(fy+1-dtop-yr)*sl)+1, cx1)
		if i0 >= i1 {
			continue
		}
		cov := f.cov[:i1-i0]
		d := nx*(float64(i0)+0.5) + ny*(fy+0.5)
		for i := range cov {
			// Same quantization as the accumulation rasterizer.
			v := int32((area(k1-d) - area(k2-d)) * 256)
			if v > 255 {
				v = 255
			} else if v < 0 {
				v = 0
			}
			cov[i] = uint8(v)
			d += nx
		}
		emitCoverage(f.b, j, i0, cov)
	}
}

// ffloor is floor for the moderate magnitudes used here (|v| < 2^31).
func ffloor(v float64) int {
	i := int(v)
	if float64(i) > v {
		i--
	}
	return i
}
