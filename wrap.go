package stilus

import (
	"math"
	"math/bits"
)

// Repeating textures are the tiles of patterns: a cell rasterized once at
// device resolution and painted over an area by a sampler that takes
// texture coordinates modulo the tile's size.
//
// Along a row of device pixels, a texture coordinate is linear in x. A
// periodic sampler keeps it in fixed point with 32 fraction bits, reduced
// modulo the period: a span starts from the row's coordinate at x = 0 plus
// x steps, computed modulo the period in 128 bits, and every further pixel
// adds the step and subtracts the period when it passes it. Integer sums
// are exact, so a pixel's coordinate is the same whichever band, tile or
// span it is drawn in, and the inner loops neither convert nor divide.

// fixedAxis is one texture axis of a periodic sampler.
type fixedAxis struct {
	n      int    // texture size along the axis
	period uint64 // n in fixed point
	step   uint64 // change per device pixel along a row, modulo period
}

// fixOne is 1 in the fixed point of fixedAxis.
const fixOne = 1 << 32

// maxWrapSize bounds the size of periodic textures, so that periods in
// fixed point fit in an int64.
const maxWrapSize = 1 << 30

func newFixedAxis(d float64, n int) fixedAxis {
	a := fixedAxis{n: n, period: uint64(n) << 32}
	a.step = uint64(math.Round(a.reduce(d) * fixOne))
	if a.step >= a.period {
		a.step -= a.period
	}
	return a
}

// reduce returns f modulo n, in [0, n] (n only by rounding).
func (a *fixedAxis) reduce(f float64) float64 {
	fn := float64(a.n)
	if f < 0 || f >= fn {
		f -= math.Floor(f/fn) * fn
		if f < 0 {
			f += fn
		}
	}
	if !(f >= 0) || f > fn { // NaN or ±Inf
		return 0
	}
	return f
}

// at returns the coordinate of pixel x of a row whose coordinate is f at
// x = 0: (f + step·x) modulo period.
func (a *fixedAxis) at(f float64, x int) uint64 {
	u := uint64(a.reduce(f) * fixOne)
	if u >= a.period {
		u -= a.period
	}
	if x == 0 || a.step == 0 {
		return u
	}
	// x modulo the period, which is above any device x that is not
	// negative.
	xm := uint64(x)
	if xm >= a.period {
		p := int64(a.period)
		xi := int64(x) % p
		if xi < 0 {
			xi += p
		}
		xm = uint64(xi)
	}
	hi, lo := bits.Mul64(a.step, xm)
	if u += bits.Rem64(hi, lo, a.period); u >= a.period {
		u -= a.period
	}
	return u
}

// weight returns the fraction of fixed-point coordinate u in 1/256.
func weight(u uint64) uint32 { return uint32(u>>24) & 0xff }

// rowV returns the index of the texture row that the axis-aligned
// periodic sampler s reads for device row y.
func (s *Sampler) rowV(y int) int {
	m := &s.m
	return int(s.fv.at(m[3]*(float64(y)+0.5)+m[5]+m[1]*0.5, 0) >> 32)
}

// sampleWrap is Sample for a periodic texture.
func (s *Sampler) sampleWrap(y, x int, dst []uint32) {
	p, m := s.p, &s.m
	fy := float64(y) + 0.5
	// Coordinates of pixel 0 of the row; bilinear samples are taken
	// between the texel centres around them.
	u0 := m[2]*fy + m[4] + m[0]*0.5
	v0 := m[3]*fy + m[5] + m[1]*0.5
	if s.unit && p.Kind == PlaneRGBA {
		sampleUnitWrap(dst, p.Pix32[s.rowV(y)*p.Stride:][:p.W], x+int(m[4]))
		return
	}
	if s.bilinear {
		u0, v0 = u0-0.5, v0-0.5
	}
	pu, su, u := s.fu.period, s.fu.step, s.fu.at(u0, x)
	if !s.bilinear {
		if su == 0 && s.fv.step == 0 {
			// One texel for the whole span.
			c := p.At(int(u>>32), int(s.fv.at(v0, x)>>32))
			for i := range dst {
				dst[i] = c
			}
			return
		}
		if s.fv.step == 0 {
			// Axis-aligned: the row is the same for the whole span.
			j := int(s.fv.at(v0, x) >> 32)
			switch p.Kind {
			case PlaneRGBA:
				row := p.Pix32[j*p.Stride:][:p.W]
				for i := range dst {
					dst[i] = row[u>>32]
					if u += su; u >= pu {
						u -= pu
					}
				}
			case PlaneIndex:
				row, pal := p.Pix8[j*p.Stride:][:p.W], p.Pal
				for i := range dst {
					dst[i] = pal[row[u>>32]]
					if u += su; u >= pu {
						u -= pu
					}
				}
			default:
				row, pal := p.Pix8[j*p.Stride:][:p.Stride], p.Pal
				for i := range dst {
					k := uint(u >> 32)
					dst[i] = pal[row[k>>3]>>(7-k&7)&1]
					if u += su; u >= pu {
						u -= pu
					}
				}
			}
			return
		}
		pv, sv, v := s.fv.period, s.fv.step, s.fv.at(v0, x)
		switch p.Kind {
		case PlaneRGBA:
			for i := range dst {
				dst[i] = p.Pix32[int(v>>32)*p.Stride+int(u>>32)]
				if u += su; u >= pu {
					u -= pu
				}
				if v += sv; v >= pv {
					v -= pv
				}
			}
		case PlaneIndex:
			for i := range dst {
				dst[i] = p.Pal[p.Pix8[int(v>>32)*p.Stride+int(u>>32)]]
				if u += su; u >= pu {
					u -= pu
				}
				if v += sv; v >= pv {
					v -= pv
				}
			}
		default:
			for i := range dst {
				k := uint(u >> 32)
				dst[i] = p.Pal[p.Pix8[int(v>>32)*p.Stride+int(k>>3)]>>(7-k&7)&1]
				if u += su; u >= pu {
					u -= pu
				}
				if v += sv; v >= pv {
					v -= pv
				}
			}
		}
		return
	}
	if s.fv.step == 0 {
		// Axis-aligned: two rows for the whole span, mixed once per
		// column pair.
		v := s.fv.at(v0, x)
		y0 := int(v >> 32)
		y1, ty := next(y0, p.H), weight(v)
		last := -1
		var c0, c1 uint32
		switch p.Kind {
		case PlaneRGBA:
			r0, r1 := p.Pix32[y0*p.Stride:][:p.W], p.Pix32[y1*p.Stride:][:p.W]
			for i := range dst {
				if x0 := int(u >> 32); x0 != last {
					x1 := next(x0, p.W)
					c0, c1, last = lerp(r0[x0], r1[x0], ty), lerp(r0[x1], r1[x1], ty), x0
				}
				dst[i] = lerp(c0, c1, weight(u))
				if u += su; u >= pu {
					u -= pu
				}
			}
		case PlaneIndex:
			r0, r1, pal := p.Pix8[y0*p.Stride:][:p.W], p.Pix8[y1*p.Stride:][:p.W], p.Pal
			for i := range dst {
				if x0 := int(u >> 32); x0 != last {
					x1 := next(x0, p.W)
					c0, c1, last = lerp(pal[r0[x0]], pal[r1[x0]], ty), lerp(pal[r0[x1]], pal[r1[x1]], ty), x0
				}
				dst[i] = lerp(c0, c1, weight(u))
				if u += su; u >= pu {
					u -= pu
				}
			}
		default:
			r0, r1, pal := p.Pix8[y0*p.Stride:][:p.Stride], p.Pix8[y1*p.Stride:][:p.Stride], p.Pal
			for i := range dst {
				if x0 := int(u >> 32); x0 != last {
					x1 := next(x0, p.W)
					s0, s1 := 7-uint(x0)&7, 7-uint(x1)&7
					c0 = lerp(pal[r0[x0>>3]>>s0&1], pal[r1[x0>>3]>>s0&1], ty)
					c1 = lerp(pal[r0[x1>>3]>>s1&1], pal[r1[x1>>3]>>s1&1], ty)
					last = x0
				}
				dst[i] = lerp(c0, c1, weight(u))
				if u += su; u >= pu {
					u -= pu
				}
			}
		}
		return
	}
	pv, sv, v := s.fv.period, s.fv.step, s.fv.at(v0, x)
	if s.levels {
		// Stencil tiles: one channel tells the colour. Mixing it alone
		// gives what lerp gives for each of the four equal channels.
		pix, pal, st := p.Pix8, p.Pal, p.Stride
		for i := range dst {
			x0, y0 := int(u>>32), int(v>>32)
			x1, y1 := next(x0, p.W), next(y0, p.H)
			r0, r1 := pix[y0*st:], pix[y1*st:]
			tx, ty := weight(u), weight(v)
			a := (uint32(uint8(pal[r0[x0]]))*(256-tx) + uint32(uint8(pal[r0[x1]]))*tx) >> 8
			b := (uint32(uint8(pal[r1[x0]]))*(256-tx) + uint32(uint8(pal[r1[x1]]))*tx) >> 8
			dst[i] = (a*(256-ty) + b*ty) >> 8 * 0x01010101
			if u += su; u >= pu {
				u -= pu
			}
			if v += sv; v >= pv {
				v -= pv
			}
		}
		return
	}
	if p.Kind == PlaneIndex {
		// One byte a texel, through the palette.
		pix, pal, st := p.Pix8, p.Pal, p.Stride
		for i := range dst {
			x0, y0 := int(u>>32), int(v>>32)
			x1, y1 := next(x0, p.W), next(y0, p.H)
			r0, r1 := pix[y0*st:], pix[y1*st:]
			tx := weight(u)
			a := lerp(pal[r0[x0]], pal[r0[x1]], tx)
			b := lerp(pal[r1[x0]], pal[r1[x1]], tx)
			dst[i] = lerp(a, b, weight(v))
			if u += su; u >= pu {
				u -= pu
			}
			if v += sv; v >= pv {
				v -= pv
			}
		}
		return
	}
	for i := range dst {
		x0, y0 := int(u>>32), int(v>>32)
		x1, y1 := next(x0, p.W), next(y0, p.H)
		tx := weight(u)
		a := lerp(p.At(x0, y0), p.At(x1, y0), tx)
		b := lerp(p.At(x0, y1), p.At(x1, y1), tx)
		dst[i] = lerp(a, b, weight(v))
		if u += su; u >= pu {
			u -= pu
		}
		if v += sv; v >= pv {
			v -= pv
		}
	}
}

// wrapIndex returns i modulo n, in [0, n).
func wrapIndex(i, n int) int {
	if i %= n; i < 0 {
		i += n
	}
	return i
}

// next returns i+1 modulo n, for i in [0, n).
func next(i, n int) int {
	if i++; i == n {
		return 0
	}
	return i
}

// sampleUnitWrap is sampleUnit for a periodic row: dst[i] =
// row[(off+i) mod len(row)]. Once dst holds one period it is doubled by
// copying from itself, so that a tiny tile costs no call per period.
func sampleUnitWrap(dst, row []uint32, off int) {
	o := wrapIndex(off, len(row))
	i := copy(dst, row[o:])
	if i < len(dst) {
		i += copy(dst[i:], row[:o])
	}
	for i < len(dst) {
		i += copy(dst[i:], dst[:i])
	}
}
