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

// ffloor is floor for the moderate magnitudes used here (|v| < 2^31).
func ffloor(v float64) int {
	i := int(v)
	if float64(i) > v {
		i--
	}
	return i
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
	ylo, yhi := y0, y1
	if ylo > yhi {
		ylo, yhi = yhi, ylo
	}
	jy0 := max(ffloor(ylo-t), clip.Min.Y)
	jy1 := min(ffloor(yhi+t), clip.Max.Y-1)
	ix0 := max(ffloor(x0), clip.Min.X)
	ix1 := min(ffloor(x1-1e-9), clip.Max.X-1)
	if ix0 > ix1 {
		return
	}
	var ik float64
	if k != 0 {
		ik = 1 / k
	}
	for j := jy0; j <= jy1; j++ {
		fj := float64(j)
		a, b := ix0, ix1
		if k != 0 {
			xa := x0 + (fj-t-y0)*ik
			xb := x0 + (fj+1+t-y0)*ik
			if xa > xb {
				xa, xb = xb, xa
			}
			a = max(a, ffloor(xa)-1)
			b = min(b, ffloor(xb)+1)
		}
		if a > b {
			continue
		}
		cov := h.cov[:b-a+1]
		// Column i covers [i, i+1] ∩ [x0, x1]; its line centre is taken
		// at the middle of that interval.
		fi := float64(a)
		yc := y0 + (fi+0.5-x0)*k
		for i := range cov {
			l, r := fi, fi+1
			ox := 1.0
			ycc := yc
			if l < x0 || r > x1 {
				if l < x0 {
					l = x0
				}
				if r > x1 {
					r = x1
				}
				ox = r - l
				ycc = y0 + ((l+r)/2-x0)*k
			}
			lo, hi := ycc-t, ycc+t
			if lo < fj {
				lo = fj
			}
			if hi > fj+1 {
				hi = fj + 1
			}
			c := (hi - lo) * ox
			if c <= 0 {
				cov[i] = 0
			} else if c >= 1 {
				cov[i] = 255
			} else {
				cov[i] = uint8(c*255 + 0.5)
			}
			fi++
			yc += k
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
	jy0 := max(ffloor(y0), clip.Min.Y)
	jy1 := min(ffloor(y1-1e-9), clip.Max.Y-1)
	cx0, cx1 := clip.Min.X, clip.Max.X-1
	fj := float64(jy0)
	xc := x0 + (fj+0.5-y0)*k
	for j := jy0; j <= jy1; j++ {
		oy, xm := 1.0, xc
		if fj < y0 || fj+1 > y1 {
			lo, hi := fj, fj+1
			if lo < y0 {
				lo = y0
			}
			if hi > y1 {
				hi = y1
			}
			oy = hi - lo
			xm = x0 + ((lo+hi)/2-y0)*k
		}
		fj++
		xc += k
		if oy <= 0 {
			continue
		}
		a, b := xm-t, xm+t
		ia, ib := ffloor(a), ffloor(b)
		if ib > cx1 || ia < cx0 {
			if ia > cx1 || ib < cx0 {
				continue
			}
		}
		cov := h.cov[:ib-ia+1]
		if ia == ib {
			cov[0] = toCov((b - a) * oy)
		} else {
			cov[0] = toCov((float64(ia+1) - a) * oy)
			m := toCov(oy)
			for i := 1; i < len(cov)-1; i++ {
				cov[i] = m
			}
			cov[len(cov)-1] = toCov((b - float64(ib)) * oy)
		}
		// Clip columns.
		if ia < cx0 {
			cov = cov[cx0-ia:]
			ia = cx0
		}
		if ib > cx1 {
			cov = cov[:len(cov)-(ib-cx1)]
		}
		if len(cov) > 0 {
			emitCoverage(h.b, j-1+1, ia, cov)
		}
	}
}
