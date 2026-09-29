package stilus

import (
	"image"
	"math"
)

// hairliner draws one-pixel-wide antialiased lines directly as coverage
// runs, without building a polygon. This is the common case in technical
// drawings.
//
// Coverage is the box-filtered area of a 1 px wide line: along the major
// axis each column (or row) is intersected with the segment's extent, along
// the minor axis with the line's thickness measured in that axis
// (1/cos θ). Segment ends are butt, so polylines meet without gaps or
// double coverage at their vertices. One BlitCoverage call is made per row.
//
// Unlike the rasterizer, spans of different segments may overlap and are
// not ordered; blitters used for hairlines must be order-independent.
type hairliner struct {
	clip image.Rectangle
	b    Blitter
	cov  []uint8
}

func (h *hairliner) reset(clip image.Rectangle, b Blitter) {
	h.clip, h.b = clip, b
	if cap(h.cov) < clip.Dx()+4 {
		h.cov = make([]uint8, clip.Dx()+4)
	}
}

// overlap returns the length of [a0, a1] ∩ [b0, b1].
func overlap(a0, a1, b0, b1 float64) float64 {
	return math.Min(a1, b1) - math.Max(a0, b0)
}

func toCov(f float64) uint8 {
	if f >= 1 {
		return 255
	}
	if f <= 0 {
		return 0
	}
	return uint8(f*255 + 0.5)
}

// line draws a hairline segment in device space.
func (h *hairliner) line(x0, y0, x1, y1 float64) {
	if x0 != x0 || y0 != y0 || x1 != x1 || y1 != y1 {
		return
	}
	// Clip to the clip rectangle grown by 2 px (Liang–Barsky); the margin
	// keeps partial coverage at the border exact.
	cx0, cy0 := float64(h.clip.Min.X)-2, float64(h.clip.Min.Y)-2
	cx1, cy1 := float64(h.clip.Max.X)+2, float64(h.clip.Max.Y)+2
	dx, dy := x1-x0, y1-y0
	t0, t1 := 0.0, 1.0
	for _, e := range [4][2]float64{{-dx, x0 - cx0}, {dx, cx1 - x0}, {-dy, y0 - cy0}, {dy, cy1 - y0}} {
		p, q := e[0], e[1]
		if p == 0 {
			if q < 0 {
				return
			}
			continue
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return
			}
			if r < t1 {
				t1 = r
			}
		}
	}
	if t0 > 0 || t1 < 1 {
		x0, y0, x1, y1 = x0+t0*dx, y0+t0*dy, x0+t1*dx, y0+t1*dy
		dx, dy = x1-x0, y1-y0
	}
	if dx == 0 && dy == 0 {
		return
	}
	if math.Abs(dx) >= math.Abs(dy) {
		h.xMajor(x0, y0, x1, y1)
	} else {
		h.yMajor(x0, y0, x1, y1)
	}
}

func (h *hairliner) xMajor(x0, y0, x1, y1 float64) {
	if x0 > x1 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	k := (y1 - y0) / (x1 - x0)
	t := 0.5 * math.Sqrt(1+k*k)
	clip := h.clip
	jy0 := int(math.Floor(math.Min(y0, y1) - t))
	jy1 := int(math.Floor(math.Max(y0, y1) + t))
	if jy0 < clip.Min.Y {
		jy0 = clip.Min.Y
	}
	if jy1 >= clip.Max.Y {
		jy1 = clip.Max.Y - 1
	}
	ix0 := int(math.Floor(x0))
	ix1 := int(math.Ceil(x1)) - 1
	if ix0 < clip.Min.X {
		ix0 = clip.Min.X
	}
	if ix1 >= clip.Max.X {
		ix1 = clip.Max.X - 1
	}
	if ix0 > ix1 {
		return
	}
	for j := jy0; j <= jy1; j++ {
		fj := float64(j)
		a, b := ix0, ix1
		if k != 0 {
			xa := x0 + (fj-t-y0)/k
			xb := x0 + (fj+1+t-y0)/k
			if xa > xb {
				xa, xb = xb, xa
			}
			if lo := int(math.Floor(xa)) - 1; lo > a {
				a = lo
			}
			if hi := int(math.Floor(xb)) + 1; hi < b {
				b = hi
			}
		}
		if a > b {
			continue
		}
		cov := h.cov[:b-a+1]
		for i := a; i <= b; i++ {
			fi := float64(i)
			l, r := math.Max(fi, x0), math.Min(fi+1, x1)
			c := 0.0
			if r > l {
				yc := y0 + ((l+r)/2-x0)*k
				c = overlap(yc-t, yc+t, fj, fj+1)
				if c > 0 {
					c *= r - l
				}
			}
			cov[i-a] = toCov(c)
		}
		emitCoverage(h.b, j, a, cov)
	}
}

func (h *hairliner) yMajor(x0, y0, x1, y1 float64) {
	if y0 > y1 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	k := (x1 - x0) / (y1 - y0)
	t := 0.5 * math.Sqrt(1+k*k)
	clip := h.clip
	jy0 := int(math.Floor(y0))
	jy1 := int(math.Ceil(y1)) - 1
	if jy0 < clip.Min.Y {
		jy0 = clip.Min.Y
	}
	if jy1 >= clip.Max.Y {
		jy1 = clip.Max.Y - 1
	}
	for j := jy0; j <= jy1; j++ {
		fj := float64(j)
		lo, hi := math.Max(fj, y0), math.Min(fj+1, y1)
		oy := hi - lo
		if oy <= 0 {
			continue
		}
		xc := x0 + ((lo+hi)/2-y0)*k
		a := int(math.Floor(xc - t))
		b := int(math.Floor(xc + t))
		if a < clip.Min.X {
			a = clip.Min.X
		}
		if b >= clip.Max.X {
			b = clip.Max.X - 1
		}
		if a > b {
			continue
		}
		cov := h.cov[:b-a+1]
		for i := a; i <= b; i++ {
			fi := float64(i)
			cov[i-a] = toCov(overlap(xc-t, xc+t, fi, fi+1) * oy)
		}
		emitCoverage(h.b, j, a, cov)
	}
}
