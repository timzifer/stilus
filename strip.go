package stilus

import "math"

// segFast draws straight stroke segments of at least one device pixel width.
//
// The segment is a parallelogram A, B, C, D in device space whose long sides
// AB and DC are parallel. Rows that touch a cap edge (AD or BC) are added,
// restricted to those rows, to the stroke's own accumulation rasterizer, so
// segments meeting at their ends (dashes, polylines exported as separate
// segments) are united exactly as the outline path would. Every other row
// is bounded only by the two parallel long sides, so the covered area of
// each pixel has a closed form: the difference of the areas of two
// half-planes within the pixel. Those rows need no cell
// buffer, no sweep and no edge walking, which is where thin strokes spend
// their time otherwise.
type segFast struct {
	r   *Rasterizer // the stroke's rasterizer; composited after the stroke
	b   Blitter
	cov []uint8
}

// minMiddleRows is the number of analytic rows below which the ordinary
// outline is cheaper.
const minMiddleRows = 3

func (f *segFast) fillSegment(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	vx, vy := bx-ax, by-ay
	l := math.Hypot(vx, vy)
	if !(l > 0) || math.Abs(vy) < 1e-6*l {
		return false
	}
	clip := f.r.clip
	// Row ranges of the two caps.
	s0, s1 := floorInt(math.Min(ay, dy)), ceilInt(math.Max(ay, dy))
	e0, e1 := floorInt(math.Min(by, cy)), ceilInt(math.Max(by, cy))
	if e0 < s0 {
		s0, s1, e0, e1 = e0, e1, s0, s1
	}
	m0, m1 := max(s1, clip.Min.Y), min(e0, clip.Max.Y) // analytic rows
	if m1-m0 < minMiddleRows {
		return false
	}
	f.capRows(s0, s1, ax, ay, bx, by, cx, cy, dx, dy)

	// Unit normal of the long sides and the strip k2 <= n·p <= k1.
	nx, ny := -vy/l, vx/l
	k1, k2 := nx*ax+ny*ay, nx*dx+ny*dy
	if k1 < k2 {
		k1, k2 = k2, k1
	}
	// Projection of the unit pixel onto n: a trapezoid with half-width h,
	// flat top of half-width m and height 1/a (a >= b).
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
	// x of the long sides at y: x = px + (y - py)·vx/vy.
	sl := vx / vy
	cx0, cx1 := clip.Min.X, clip.Max.X
	if cap(f.cov) < clip.Dx() {
		f.cov = make([]uint8, clip.Dx())
	}
	// Per row the strip spans x from min to max of both long sides at the
	// row's top and bottom; lo/hi pick the side and end once.
	xl, xr := ax, dx // side that is further left / right at equal y
	yl, yr := ay, dy
	if ax+(dy-ay)*sl > dx {
		xl, xr, yl, yr = dx, ax, dy, ay
	}
	dtop := 0.0 // which row edge gives the leftmost x: top if sl > 0
	if sl < 0 {
		dtop = 1
	}
	for j := m0; j < m1; j++ {
		fy := float64(j)
		lo := xl + (fy+dtop-yl)*sl
		hi := xr + (fy+1-dtop-yr)*sl
		i0 := max(ffloor(lo), cx0)
		i1 := min(ffloor(hi)+1, cx1)
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

	f.capRows(e0, e1, ax, ay, bx, by, cx, cy, dx, dy)
	return true
}

// capRows adds the parallelogram's edges within rows [y0, y1) to the
// stroke's rasterizer.
func (f *segFast) capRows(y0, y1 int, ax, ay, bx, by, cx, cy, dx, dy float64) {
	r := f.r
	r.limitRows(y0, y1)
	r.AddLine(ax, ay, bx, by)
	r.AddLine(bx, by, cx, cy)
	r.AddLine(cx, cy, dx, dy)
	r.AddLine(dx, dy, ax, ay)
	r.unlimitRows()
}

func floorInt(v float64) int { return int(math.Floor(v)) }
func ceilInt(v float64) int  { return int(math.Ceil(v)) }
