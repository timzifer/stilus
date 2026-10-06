package stilus

import "math"

// An image is drawn by filling the parallelogram its pixel space maps to
// with an ImageShader, which maps every device pixel back into the
// texture: so an image goes through the clip stack and gets antialiased
// edges like any fill, and a band or tile samples only its own pixels.

// Sampler reads a texture at device pixels. It caches what nearest
// sampling along the device axes shares between pixels (the texel column
// of every device column, the expanded rows of a magnified texture), so
// it is not safe for concurrent use; use one per worker.
type Sampler struct {
	p *Plane
	// m maps device space to the pixel space of p (x right, y down,
	// pixel (i, j) covering [i, i+1) × [j, j+1)).
	m        Matrix
	bilinear bool
	// unit says that m is a translation by whole pixels, so that nearest
	// sampling copies texels.
	unit bool
	// wrap says that p repeats in both directions with its size as
	// period, rather than its edge pixels.
	wrap bool
	// fu and fv step the coordinates of a periodic texture along rows.
	fu, fv fixedAxis
	// levels says that p is indexed and its colours are levels of alpha
	// (all four channels equal), so that one channel can be mixed for all.
	levels bool
	// opaque says that every pixel of p has alpha 255, and so every
	// sample.
	opaque bool
	// cached says that p is sampled at the nearest pixel along the device
	// axes with u independent of y (m[1] = m[2] = 0), so that the texel
	// column of a device column is the same on every row and is read from
	// nc; rows says that rows also repeat (|m[3]| < 1, magnified), so that
	// the expanded rows are kept too.
	cached, rows bool
	nc           nearestCache
}

// nearestCache holds, for a nearest axis-aligned sampler, the texel
// columns of a range of device columns and the colours of one texel row
// at them. Both are computed by the same expressions as Sample's per-pixel
// path, so a pixel gets the same texel whether or not it is cached.
type nearestCache struct {
	ok   bool    // cols is valid
	u0   float64 // the u0 of Sample that cols was computed for
	x0   int     // the device column of cols[0] and row[0]
	cols []int32
	j    int // the texel row expanded into row, -1 none
	row  []uint32
}

// maxCacheCols bounds the device columns a nearestCache spans.
const maxCacheCols = 1 << 16

// Setup chooses the mip level of t for drawing it with toDevice, which
// maps its base pixel space to device space, and reports whether t can be
// drawn so (toDevice is invertible). Magnified textures are sampled at
// the nearest pixel unless smooth is set; reduced ones bilinearly from the
// level at most twice as fine as the device.
func (s *Sampler) Setup(t *Texture, toDevice Matrix, smooth bool) bool {
	return s.setup(t, toDevice, smooth, false)
}

// SetupWrap is Setup for a texture that repeats in both directions with
// the period of its base size: texture coordinates are taken modulo W and
// H before sampling, and bilinear samples at the border read the opposite
// edge. Its mip levels are periodic too, of the base size over 2^k
// rounded, so that a level still repeats with a whole number of pixels.
// Textures of 2^30 pixels or more along an axis cannot be repeated.
func (s *Sampler) SetupWrap(t *Texture, toDevice Matrix, smooth bool) bool {
	return s.setup(t, toDevice, smooth, true)
}

func (s *Sampler) setup(t *Texture, toDevice Matrix, smooth, wrap bool) bool {
	inv, ok := toDevice.Invert()
	if !ok || !inv.finite() || wrap && max(t.base.W, t.base.H) >= maxWrapSize {
		return false
	}
	// Base pixels per device pixel along the device axes.
	r := min(math.Hypot(inv[0], inv[1]), math.Hypot(inv[2], inv[3]))
	k, top := 0, t.Levels()
	for r >= 2 && k < top {
		r /= 2
		k++
	}
	if wrap {
		s.p = t.wrapLevel(k)
	} else {
		s.p = t.Level(k)
	}
	s.wrap = wrap
	if k > 0 {
		inv = inv.Mul(Scale(float64(s.p.W)/float64(t.base.W), float64(s.p.H)/float64(t.base.H)))
	}
	s.m = inv
	s.bilinear = smooth || r > 1+1e-6
	s.unit = !s.bilinear && inv[0] == 1 && inv[1] == 0 && inv[2] == 0 && inv[3] == 1 &&
		inv[4] == math.Trunc(inv[4]) && math.Abs(inv[4]) < 1<<30
	if wrap {
		s.fu, s.fv = newFixedAxis(inv[0], s.p.W), newFixedAxis(inv[1], s.p.H)
	}
	s.levels = s.p.Kind == PlaneIndex && t.alpha
	s.opaque = t.isOpaque()
	s.cached = !wrap && !s.bilinear && inv[1] == 0 && inv[2] == 0 &&
		!(s.unit && s.p.Kind != PlaneIndex) && s.p.W <= math.MaxInt32
	s.rows = s.cached && math.Abs(inv[3]) < 1
	s.nc.ok, s.nc.j = false, -1
	return true
}

// origin returns the texture coordinates of the centre of pixel 0 of
// device row y. It is the one place they are computed, so that cached and
// sampled pixels round alike.
func (s *Sampler) origin(y int) (u0, v0 float64) {
	m := &s.m
	fy := float64(y) + 0.5
	return m[2]*fy + m[4] + m[0]*0.5, m[3]*fy + m[5] + m[1]*0.5
}

// nearestCol returns the texel column of device column x for a nearest
// sampler whose row starts at u0.
func (s *Sampler) nearestCol(u0 float64, x int) int {
	return clampIndex(u0+s.m[0]*float64(x), s.p.W)
}

// colsFor returns the texel columns of device columns [x, x+n) for a row
// starting at u0, from the cache, which it extends or rebuilds as needed;
// or false if they span too many columns to cache.
func (s *Sampler) colsFor(u0 float64, x, n int) ([]int32, bool) {
	c := &s.nc
	same := c.ok && u0 == c.u0
	if same && x >= c.x0 && x+n <= c.x0+len(c.cols) {
		return c.cols[x-c.x0:][:n], true
	}
	if n > maxCacheCols {
		return nil, false
	}
	lo, hi := x, x+n
	if same && min(lo, c.x0) >= max(hi, c.x0+len(c.cols))-maxCacheCols {
		// Spans of one row usually adjoin: keep what is cached.
		lo, hi = min(lo, c.x0), max(hi, c.x0+len(c.cols))
	}
	if cap(c.cols) < hi-lo {
		c.cols = make([]int32, hi-lo, hi-lo+(hi-lo)/2)
	}
	c.cols = c.cols[:hi-lo]
	for i := range c.cols {
		c.cols[i] = int32(s.nearestCol(u0, lo+i))
	}
	c.ok, c.u0, c.x0, c.j = true, u0, lo, -1
	return c.cols[x-lo:][:n], true
}

// nearestRow returns the colours of pixels [x, x+n) of a device row that
// reads texel row j and starts at u0, from the row cache of a sampler
// with rows set; or false.
func (s *Sampler) nearestRow(j int, u0 float64, x, n int) ([]uint32, bool) {
	if _, ok := s.colsFor(u0, x, n); !ok {
		return nil, false
	}
	c := &s.nc
	if c.j != j {
		if cap(c.row) < len(c.cols) {
			c.row = make([]uint32, len(c.cols), cap(c.cols))
		}
		c.row = c.row[:len(c.cols)]
		s.expand(c.row, j, c.cols)
		c.j = j
	}
	return c.row[x-c.x0:][:n], true
}

// expand sets dst[i] to texel (cols[i], j).
func (s *Sampler) expand(dst []uint32, j int, cols []int32) {
	p := s.p
	dst = dst[:len(cols)]
	switch p.Kind {
	case PlaneRGBA:
		row := p.Pix32[j*p.Stride:][:p.W]
		for i, k := range cols {
			dst[i] = row[k]
		}
	case PlaneIndex:
		row, pal := p.Pix8[j*p.Stride:][:p.W], p.Pal
		for i, k := range cols {
			dst[i] = pal[row[k]]
		}
	default:
		row, pal := p.Pix8[j*p.Stride:][:(p.W+7)/8], p.Pal
		for i, k := range cols {
			dst[i] = pal[row[k>>3]>>(7-uint(k)&7)&1]
		}
	}
}

// keep returns a zero sampler that holds the buffers of s.
func (s *Sampler) keep() Sampler {
	return Sampler{nc: nearestCache{cols: s.nc.cols[:0], row: s.nc.row[:0], j: -1}}
}

// Release drops the sampler's reference to its texture.
func (s *Sampler) Release() { s.p = nil }

// Sample writes the colours of pixels [x, x+len(dst)) of row y. Every
// pixel's coordinates are computed from its own position rather than
// accumulated along the span, so that a pixel samples the same texel
// whichever band, tile or span it is drawn in. Outside the texture the
// edge pixels repeat, or with SetupWrap the texture itself.
func (s *Sampler) Sample(y, x int, dst []uint32) {
	if s.wrap {
		s.sampleWrap(y, x, dst)
		return
	}
	p, m := s.p, &s.m
	u0, v0 := s.origin(y)
	du, dv := m[0], m[1]
	if !s.bilinear {
		if dv == 0 {
			// Axis-aligned: the row is the same for the whole span.
			j := clampIndex(v0, p.H)
			if s.cached {
				// Columns found once for all rows, and magnified
				// rows expanded once for all the device rows on them.
				if s.rows {
					if row, ok := s.nearestRow(j, u0, x, len(dst)); ok {
						copy(dst, row)
						return
					}
				} else if cols, ok := s.colsFor(u0, x, len(dst)); ok {
					s.expand(dst, j, cols)
					return
				}
			}
			switch p.Kind {
			case PlaneRGBA:
				row := p.Pix32[j*p.Stride:][:p.W]
				if s.unit {
					sampleUnit(dst, row, x+int(m[4]))
					return
				}
				for i := range dst {
					dst[i] = row[s.nearestCol(u0, x+i)]
				}
			case PlaneIndex:
				row, pal := p.Pix8[j*p.Stride:][:p.W], p.Pal
				for i := range dst {
					dst[i] = pal[row[s.nearestCol(u0, x+i)]]
				}
			default:
				row, pal := p.Pix8[j*p.Stride:][:(p.W+7)/8], p.Pal
				if s.unit {
					sampleUnitBits(dst, row, p.W, pal, x+int(m[4]))
					return
				}
				for i := range dst {
					k := s.nearestCol(u0, x+i)
					dst[i] = pal[row[k>>3]>>(7-uint(k)&7)&1]
				}
			}
			return
		}
		for i := range dst {
			fx := float64(x + i)
			dst[i] = p.At(s.nearestCol(u0, x+i), clampIndex(v0+dv*fx, p.H))
		}
		return
	}
	// Bilinear: weights from the distance to the four nearest centres.
	u0, v0 = u0-0.5, v0-0.5
	if dv == 0 {
		// Axis-aligned: two rows for the whole span, mixed once per
		// column pair, which neighbouring pixels share when the texture
		// is magnified. The pair is keyed by x0+x1, which tells (k, k)
		// from (k, k+1): at the left clamp x0 stays 0 while x1 moves. A
		// pair that moves on by one column keeps the mix of the shared
		// column.
		y0, y1, ty := split(v0, p.H)
		last := -1
		var c0, c1 uint32
		switch p.Kind {
		case PlaneRGBA:
			r0, r1 := p.Pix32[y0*p.Stride:][:p.W], p.Pix32[y1*p.Stride:][:p.W]
			lx1 := -1
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.W)
				if x0+x1 != last {
					if x0 == lx1 {
						c0 = c1
					} else {
						c0 = lerp(r0[x0], r1[x0], ty)
					}
					c1, last, lx1 = lerp(r0[x1], r1[x1], ty), x0+x1, x1
				}
				dst[i] = lerp(c0, c1, tx)
			}
		case PlaneIndex:
			r0, r1, pal := p.Pix8[y0*p.Stride:][:p.W], p.Pix8[y1*p.Stride:][:p.W], p.Pal
			lx1 := -1
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.W)
				if x0+x1 != last {
					if x0 == lx1 {
						c0 = c1
					} else {
						c0 = lerp(pal[r0[x0]], pal[r1[x0]], ty)
					}
					c1, last, lx1 = lerp(pal[r0[x1]], pal[r1[x1]], ty), x0+x1, x1
				}
				dst[i] = lerp(c0, c1, tx)
			}
		default:
			r0, r1, pal := p.Pix8[y0*p.Stride:][:(p.W+7)/8], p.Pix8[y1*p.Stride:][:(p.W+7)/8], p.Pal
			lx1 := -1
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.W)
				if x0+x1 != last {
					if x0 == lx1 {
						c0 = c1
					} else {
						s0 := 7 - uint(x0)&7
						c0 = lerp(pal[r0[x0>>3]>>s0&1], pal[r1[x0>>3]>>s0&1], ty)
					}
					s1 := 7 - uint(x1)&7
					c1, last, lx1 = lerp(pal[r0[x1>>3]>>s1&1], pal[r1[x1>>3]>>s1&1], ty), x0+x1, x1
				}
				dst[i] = lerp(c0, c1, tx)
			}
		}
		return
	}
	for i := range dst {
		fx := float64(x + i)
		x0, x1, tx := split(u0+du*fx, p.W)
		y0, y1, ty := split(v0+dv*fx, p.H)
		a := lerp(p.At(x0, y0), p.At(x1, y0), tx)
		b := lerp(p.At(x0, y1), p.At(x1, y1), tx)
		dst[i] = lerp(a, b, ty)
	}
}

// sampleUnit sets dst[i] to row[off+i], repeating the edge pixels
// outside the row: the nearest texels of a translation by whole pixels,
// for which u = off+i+0.5.
func sampleUnit(dst, row []uint32, off int) {
	i := 0
	for ; i < len(dst) && off+i < 0; i++ {
		dst[i] = row[0]
	}
	if i < len(dst) && off+i < len(row) {
		i += copy(dst[i:], row[off+i:])
	}
	for ; i < len(dst); i++ {
		dst[i] = row[len(row)-1]
	}
}

// sampleUnitBits is sampleUnit for a one-bit row of w pixels in the
// colours pal[0] and pal[1]: dst[i] is the pixel off+i, clamped to the
// row. Whole bytes of one colour are filled without reading their bits.
func sampleUnitBits(dst []uint32, row []uint8, w int, pal *Palette, off int) {
	bit := func(k int) uint32 { return pal[row[k>>3]>>(7-uint(k)&7)&1] }
	i := 0
	for ; i < len(dst) && off+i < 0; i++ {
		dst[i] = bit(0)
	}
	end := len(dst)
	if off < w {
		end = min(end, w-off)
	} else {
		end = i
	}
	for ; i < end && (off+i)&7 != 0; i++ {
		dst[i] = bit(off + i)
	}
	c0, c1 := pal[0], pal[1]
	for ; i+8 <= end; i += 8 {
		d := dst[i : i+8 : i+8]
		switch b := row[(off+i)>>3]; b {
		case 0:
			d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7] = c0, c0, c0, c0, c0, c0, c0, c0
		case 0xff:
			d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7] = c1, c1, c1, c1, c1, c1, c1, c1
		default:
			for k := range d {
				d[k] = pal[b>>(7-uint(k))&1]
			}
		}
	}
	for ; i < end; i++ {
		dst[i] = bit(off + i)
	}
	if i < len(dst) {
		c := bit(w - 1)
		for ; i < len(dst); i++ {
			dst[i] = c
		}
	}
}

// clampIndex returns the pixel of [0, n) that coordinate u falls in,
// clamped to the edges (NaN gives 0).
func clampIndex(u float64, n int) int {
	if !(u > 0) {
		return 0
	}
	if u >= float64(n) {
		return n - 1
	}
	return int(u)
}

// split returns the pixels on either side of u, clamped to [0, n), and
// the weight of the second in 1/256.
func split(u float64, n int) (i0, i1 int, t uint32) {
	if !(u > 0) {
		return 0, 0, 0
	}
	if u >= float64(n-1) {
		return n - 1, n - 1, 0
	}
	i := int(u)
	return i, i + 1, uint32((u - float64(i)) * 256)
}

// lerp mixes two premultiplied pixels channel-wise: a·(256-t)/256 +
// b·t/256, t in [0, 256].
func lerp(a, b, t uint32) uint32 {
	if a == b {
		return a
	}
	s := 256 - t
	rb := ((a&0x00ff00ff)*s + (b&0x00ff00ff)*t) >> 8 & 0x00ff00ff
	ag := ((a>>8&0x00ff00ff)*s + (b>>8&0x00ff00ff)*t) & 0xff00ff00
	return rb | ag
}

// ImageShader paints a texture (or a solid colour) times a constant alpha,
// times an optional mask texture whose colours are levels of alpha (all
// four channels equal, such as AlphaPalette). The zero value paints
// transparent black; set it up with SetColor or SetImage, and SetMask.
type ImageShader struct {
	col, mask Sampler
	hasCol    bool
	hasMask   bool
	color     uint32 // premultiplied, when !hasCol
	alpha     uint32 // constant alpha of a texture, 0-255
	buf       []uint32
	// stencil holds color times every level of alpha, for painting a
	// solid colour through a mask, when stencilOK.
	stencil   *[256]uint32
	stencilOK bool
}

// Reset clears the shader, keeping its buffers, and drops its textures.
func (s *ImageShader) Reset() {
	*s = ImageShader{buf: s.buf, stencil: s.stencil, col: s.col.keep(), mask: s.mask.keep()}
}

// SetColor paints the premultiplied colour c (PackRGBA layout) instead of a
// texture: a stencil.
func (s *ImageShader) SetColor(c uint32) {
	s.stencilOK = s.stencilOK && s.color == c
	s.hasCol, s.col.p, s.color = false, nil, c
}

// SetImage paints t, whose base pixel space toDevice maps to device space,
// with constant alpha. It reports false if t cannot be drawn so.
func (s *ImageShader) SetImage(t *Texture, toDevice Matrix, smooth bool, alpha uint8) bool {
	s.alpha = uint32(alpha)
	s.hasCol = s.col.Setup(t, toDevice, smooth)
	return s.hasCol
}

// SetMask multiplies the paint by mask texture t, whose base pixel space
// toDevice maps to device space. It reports false if t cannot be drawn so.
func (s *ImageShader) SetMask(t *Texture, toDevice Matrix, smooth bool) bool {
	s.hasMask = s.mask.Setup(t, toDevice, smooth)
	return s.hasMask
}

// SetImageWrap is SetImage for a texture that repeats in both directions
// with the period of its base size (see Sampler.SetupWrap): the tile of a
// coloured pattern.
func (s *ImageShader) SetImageWrap(t *Texture, toDevice Matrix, smooth bool, alpha uint8) bool {
	s.alpha = uint32(alpha)
	s.hasCol = s.col.SetupWrap(t, toDevice, smooth)
	return s.hasCol
}

// SetMaskWrap is SetMask for a repeating mask texture: with SetColor, the
// stencil tile of an uncoloured pattern.
func (s *ImageShader) SetMaskWrap(t *Texture, toDevice Matrix, smooth bool) bool {
	s.hasMask = s.mask.SetupWrap(t, toDevice, smooth)
	return s.hasMask
}

// srcRow implements rowSource: an RGBA texture moved by whole pixels,
// without a mask, is its own pixels times alpha where the span lies within
// the texture, or within one period of a repeating one (elsewhere, Sample
// repeats the edge pixels or the period). A magnified texture sampled at
// the nearest pixel along the device axes is its expanded row.
func (s *ImageShader) srcRow(y, x, n int) ([]uint32, uint32, bool) {
	c := &s.col
	if !s.hasCol || s.hasMask {
		return nil, 0, false
	}
	if c.rows {
		u0, v0 := c.origin(y)
		row, ok := c.nearestRow(clampIndex(v0, c.p.H), u0, x, n)
		return row, s.alpha, ok
	}
	if !c.unit || c.p.Kind != PlaneRGBA {
		return nil, 0, false
	}
	p, m := c.p, &c.m
	off := x + int(m[4])
	// Sample's row, computed the same way.
	if c.wrap {
		off = wrapIndex(off, p.W)
	}
	if off < 0 || off+n > p.W {
		return nil, 0, false
	}
	var j int
	if c.wrap {
		j = c.rowV(y)
	} else {
		j = clampIndex(m[3]*(float64(y)+0.5)+m[5]+m[1]*0.5, p.H)
	}
	return p.Pix32[j*p.Stride+off:][:n], s.alpha, true
}

// opaqueSpan implements opaqueSource: an opaque texture at full alpha,
// without a mask, read from memory other than d.
func (s *ImageShader) opaqueSpan(d []uint32) bool {
	return s.hasCol && !s.hasMask && s.alpha == 255 && s.col.opaque && !s.col.p.overlaps(d)
}

// ShadeSpan implements Shader.
func (s *ImageShader) ShadeSpan(y, x int, dst []uint32) {
	if !s.hasCol && s.hasMask {
		// A stencil: the mask's level picks the colour times it.
		if !s.stencilOK {
			if s.stencil == nil {
				s.stencil = new([256]uint32)
			}
			for a := range s.stencil {
				s.stencil[a] = mul255(s.color, uint32(a))
			}
			s.stencilOK = true
		}
		s.mask.Sample(y, x, dst)
		lut := s.stencil
		for i, c := range dst {
			// Mask colours are levels of alpha: all four channels are equal.
			dst[i] = lut[uint8(c)]
		}
		return
	}
	if s.hasCol {
		s.col.Sample(y, x, dst)
		if s.alpha != 255 {
			for i, c := range dst {
				dst[i] = mul255(c, s.alpha)
			}
		}
	} else {
		for i := range dst {
			dst[i] = s.color
		}
	}
	if !s.hasMask {
		return
	}
	if cap(s.buf) < len(dst) {
		s.buf = make([]uint32, len(dst)+len(dst)/2+64)
	}
	m := s.buf[:len(dst)]
	s.mask.Sample(y, x, m)
	for i, c := range m {
		// Mask colours are levels of alpha: all four channels are equal.
		switch a := c & 0xff; a {
		case 0:
			dst[i] = 0
		case 255:
		default:
			dst[i] = mul255(dst[i], a)
		}
	}
}
