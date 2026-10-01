package stilus

import "math"

// An image is drawn by filling the parallelogram its pixel space maps to
// with an ImageShader, which maps every device pixel back into the
// texture: so an image goes through the clip stack and gets antialiased
// edges like any fill, and a band or tile samples only its own pixels.

// Sampler reads a texture at device pixels.
type Sampler struct {
	p *Plane
	// m maps device space to the pixel space of p (x right, y down,
	// pixel (i, j) covering [i, i+1) × [j, j+1)).
	m        Matrix
	bilinear bool
	// unit says that m is a translation by whole pixels, so that nearest
	// sampling copies texels.
	unit bool
}

// Setup chooses the mip level of t for drawing it with toDevice, which
// maps its base pixel space to device space, and reports whether t can be
// drawn so (toDevice is invertible). Magnified textures are sampled at
// the nearest pixel unless smooth is set; reduced ones bilinearly from the
// level at most twice as fine as the device.
func (s *Sampler) Setup(t *Texture, toDevice Matrix, smooth bool) bool {
	inv, ok := toDevice.Invert()
	if !ok || !inv.finite() {
		return false
	}
	// Base pixels per device pixel along the device axes.
	r := min(math.Hypot(inv[0], inv[1]), math.Hypot(inv[2], inv[3]))
	k, top := 0, t.Levels()
	for r >= 2 && k < top {
		r /= 2
		k++
	}
	s.p = t.Level(k)
	if k > 0 {
		inv = inv.Mul(Scale(float64(s.p.W)/float64(t.base.W), float64(s.p.H)/float64(t.base.H)))
	}
	s.m = inv
	s.bilinear = smooth || r > 1+1e-6
	s.unit = !s.bilinear && inv[0] == 1 && inv[1] == 0 && inv[2] == 0 && inv[3] == 1 &&
		inv[4] == math.Trunc(inv[4]) && math.Abs(inv[4]) < 1<<30
	return true
}

// Release drops the sampler's reference to its texture.
func (s *Sampler) Release() { s.p = nil }

// Sample writes the colours of pixels [x, x+len(dst)) of row y. Every
// pixel's coordinates are computed from its own position rather than
// accumulated along the span, so that a pixel samples the same texel
// whichever band, tile or span it is drawn in. Outside the texture the
// edge pixels repeat.
func (s *Sampler) Sample(y, x int, dst []uint32) {
	p, m := s.p, &s.m
	fy := float64(y) + 0.5
	u0 := m[2]*fy + m[4] + m[0]*0.5
	v0 := m[3]*fy + m[5] + m[1]*0.5
	du, dv := m[0], m[1]
	if !s.bilinear {
		if dv == 0 {
			// Axis-aligned: the row is the same for the whole span.
			j := clampIndex(v0, p.H)
			switch p.Kind {
			case PlaneRGBA:
				row := p.Pix32[j*p.Stride:][:p.W]
				if s.unit {
					sampleUnit(dst, row, x+int(m[4]))
					return
				}
				for i := range dst {
					dst[i] = row[clampIndex(u0+du*float64(x+i), p.W)]
				}
			case PlaneIndex:
				row, pal := p.Pix8[j*p.Stride:][:p.W], p.Pal
				for i := range dst {
					dst[i] = pal[row[clampIndex(u0+du*float64(x+i), p.W)]]
				}
			default:
				row, pal := p.Pix8[j*p.Stride:][:p.Stride], p.Pal
				for i := range dst {
					k := clampIndex(u0+du*float64(x+i), p.W)
					dst[i] = pal[row[k>>3]>>(7-uint(k)&7)&1]
				}
			}
			return
		}
		for i := range dst {
			fx := float64(x + i)
			dst[i] = p.At(clampIndex(u0+du*fx, p.W), clampIndex(v0+dv*fx, p.H))
		}
		return
	}
	// Bilinear: weights from the distance to the four nearest centres.
	u0, v0 = u0-0.5, v0-0.5
	if dv == 0 {
		// Axis-aligned: two rows for the whole span, mixed once per
		// column pair, which neighbouring pixels share when the texture
		// is magnified.
		y0, y1, ty := split(v0, p.H)
		last := -1
		var c0, c1 uint32
		switch p.Kind {
		case PlaneRGBA:
			r0, r1 := p.Pix32[y0*p.Stride:][:p.W], p.Pix32[y1*p.Stride:][:p.W]
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.W)
				if x0 != last {
					c0, c1, last = lerp(r0[x0], r1[x0], ty), lerp(r0[x1], r1[x1], ty), x0
				}
				dst[i] = lerp(c0, c1, tx)
			}
		case PlaneIndex:
			r0, r1, pal := p.Pix8[y0*p.Stride:][:p.W], p.Pix8[y1*p.Stride:][:p.W], p.Pal
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.W)
				if x0 != last {
					c0, c1, last = lerp(pal[r0[x0]], pal[r1[x0]], ty), lerp(pal[r0[x1]], pal[r1[x1]], ty), x0
				}
				dst[i] = lerp(c0, c1, tx)
			}
		default:
			r0, r1, pal := p.Pix8[y0*p.Stride:][:p.Stride], p.Pix8[y1*p.Stride:][:p.Stride], p.Pal
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.W)
				if x0 != last {
					s0, s1 := 7-uint(x0)&7, 7-uint(x1)&7
					c0 = lerp(pal[r0[x0>>3]>>s0&1], pal[r1[x0>>3]>>s0&1], ty)
					c1 = lerp(pal[r0[x1>>3]>>s1&1], pal[r1[x1>>3]>>s1&1], ty)
					last = x0
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
}

// Reset clears the shader, keeping its buffer, and drops its textures.
func (s *ImageShader) Reset() {
	*s = ImageShader{buf: s.buf}
}

// SetColor paints the premultiplied colour c (PackRGBA layout) instead of a
// texture: a stencil.
func (s *ImageShader) SetColor(c uint32) {
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

// ShadeSpan implements Shader.
func (s *ImageShader) ShadeSpan(y, x int, dst []uint32) {
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
