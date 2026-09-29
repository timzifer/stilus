package stilus

import (
	"image"
	"image/color"
	"unsafe"
)

// Pixels are handled as native-endian uint32 views of premultiplied RGBA8
// memory (the layout of image.RGBA). The SWAR arithmetic below treats the
// four channels symmetrically, so only the position of alpha depends on the
// host byte order.
var alphaShift = func() uint {
	b := [4]byte{0, 0, 0, 0xff}
	if *(*uint32)(unsafe.Pointer(&b)) == 0xff000000 {
		return 24
	}
	return 0
}()

// alphaLane is the bit position of alpha in the expanded 64-bit layout.
var alphaLane = map[uint]uint{24: 48, 0: 0}[alphaShift]

// PackRGBA packs a premultiplied color into the native pixel layout used by
// Shader.ShadeSpan.
func PackRGBA(c color.RGBA) uint32 {
	b := [4]byte{c.R, c.G, c.B, c.A}
	return *(*uint32)(unsafe.Pointer(&b))
}

// UnpackRGBA is the inverse of PackRGBA.
func UnpackRGBA(v uint32) color.RGBA {
	b := *(*[4]byte)(unsafe.Pointer(&v))
	return color.RGBA{b[0], b[1], b[2], b[3]}
}

// mul255 multiplies all four 8-bit channels of x by a/255 with exact
// rounding, two channels per multiplication and without division.
func mul255(x, a uint32) uint32 {
	rb := (x&0x00ff00ff)*a + 0x00800080
	rb = ((rb + ((rb >> 8) & 0x00ff00ff)) >> 8) & 0x00ff00ff
	ag := ((x>>8)&0x00ff00ff)*a + 0x00800080
	ag = (ag + ((ag >> 8) & 0x00ff00ff)) & 0xff00ff00
	return rb | ag
}

// over composites premultiplied src over dst.
func over(src, dst uint32) uint32 {
	return src + mul255(dst, 255-(src>>alphaShift)&0xff)
}

// The 64-bit lane layout spreads the four channels of a pixel into 16-bit
// lanes, so one multiplication scales all of them.
const lanes = 0x00ff00ff00ff00ff

func expand(v uint32) uint64 {
	x := uint64(v)
	return (x | x<<24) & lanes
}

func compact(y uint64) uint32 { return uint32(y) | uint32(y>>24) }

// div255x divides every lane by 255 with rounding.
func div255x(y uint64) uint64 {
	y += 0x0080008000800080
	return ((y + (y>>8)&lanes) >> 8) & lanes
}

// lerpx blends expanded opaque src into dst with coverage a:
// dst·(255−a)/255 + src·a/255, two multiplications for four channels.
func lerpx(src uint64, dst uint32, a uint32) uint32 {
	return compact(div255x(expand(dst)*uint64(255-a) + src*uint64(a)))
}

// overx composites expanded premultiplied src, scaled by coverage a, over dst.
func overx(src uint64, dst uint32, a uint32) uint32 {
	s := div255x(src * uint64(a))
	sa := uint32(s>>(alphaLane)) & 0xff
	return compact(s + div255x(expand(dst)*uint64(255-sa)))
}

// fill32 sets every element of d to v; long spans are filled at memmove speed.
func fill32(d []uint32, v uint32) {
	if len(d) < 32 {
		for i := range d {
			d[i] = v
		}
		return
	}
	for i := range d[:16] {
		d[i] = v
	}
	for n := 16; n < len(d); n *= 2 {
		copy(d[n:], d[:n])
	}
}

// pixels returns the uint32 view of dst.Pix.
func pixels(dst *image.RGBA) []uint32 {
	if len(dst.Pix) < 4 {
		return nil
	}
	return unsafe.Slice((*uint32)(unsafe.Pointer(&dst.Pix[0])), len(dst.Pix)/4)
}

// Shader produces premultiplied colors (in PackRGBA layout) for the pixels
// [x, x+len(dst)) on row y. It is called once per span, never per pixel.
type Shader interface {
	ShadeSpan(y, x int, dst []uint32)
}

// target describes the destination image as a uint32 grid.
type target struct {
	pix    []uint32
	stride int // in pixels
	ox, oy int // image origin (dst.Rect.Min)
}

func (t *target) set(dst *image.RGBA) {
	t.pix = pixels(dst)
	t.stride = dst.Stride / 4
	t.ox, t.oy = dst.Rect.Min.X, dst.Rect.Min.Y
}

func (t *target) row(y, x0, x1 int) []uint32 {
	o := (y-t.oy)*t.stride - t.ox
	return t.pix[o+x0 : o+x1]
}

// SolidBlitter composites a single premultiplied color onto an image.RGBA.
type SolidBlitter struct {
	t      target
	c      uint32
	cx     uint64 // expanded c
	opaque bool
}

// NewSolidBlitter returns a blitter painting c (premultiplied) onto dst.
func NewSolidBlitter(dst *image.RGBA, c color.RGBA) *SolidBlitter {
	b := &SolidBlitter{}
	b.Reset(dst, c)
	return b
}

// Reset retargets the blitter without allocating.
func (b *SolidBlitter) Reset(dst *image.RGBA, c color.RGBA) {
	b.t.set(dst)
	b.SetColor(c)
}

// SetColor changes the paint color (premultiplied).
func (b *SolidBlitter) SetColor(c color.RGBA) {
	b.c = PackRGBA(c)
	b.cx = expand(b.c)
	b.opaque = c.A == 255
}

func (b *SolidBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	d := b.t.row(y, x0, x1)
	if alpha == 255 && b.opaque {
		fill32(d, b.c)
		return
	}
	s := b.c
	if alpha != 255 {
		s = mul255(s, uint32(alpha))
	}
	if s == 0 {
		return
	}
	inv := 255 - (s>>alphaShift)&0xff
	for i, v := range d {
		d[i] = s + mul255(v, inv)
	}
}

func (b *SolidBlitter) BlitCoverage(y, x int, cov []uint8) {
	d := b.t.row(y, x, x+len(cov))
	d = d[:len(cov)]
	c, cx := b.c, b.cx
	if b.opaque {
		for i, a := range cov {
			switch a {
			case 0:
			case 255:
				d[i] = c
			default:
				d[i] = lerpx(cx, d[i], uint32(a))
			}
		}
		return
	}
	for i, a := range cov {
		if a != 0 {
			d[i] = overx(cx, d[i], uint32(a))
		}
	}
}

// ShaderBlitter composites the output of a Shader onto an image.RGBA.
type ShaderBlitter struct {
	t       target
	s       Shader
	scratch []uint32
}

// NewShaderBlitter returns a blitter painting s onto dst.
func NewShaderBlitter(dst *image.RGBA, s Shader) *ShaderBlitter {
	b := &ShaderBlitter{}
	b.Reset(dst, s)
	return b
}

// Reset retargets the blitter without allocating.
func (b *ShaderBlitter) Reset(dst *image.RGBA, s Shader) {
	b.t.set(dst)
	b.s = s
}

func (b *ShaderBlitter) span(y, x, n int) []uint32 {
	if cap(b.scratch) < n {
		b.scratch = make([]uint32, n+n/2+64)
	}
	sc := b.scratch[:n]
	b.s.ShadeSpan(y, x, sc)
	return sc
}

func (b *ShaderBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	sc := b.span(y, x0, x1-x0)
	d := b.t.row(y, x0, x1)
	if alpha == 255 {
		for i, s := range sc {
			if s>>alphaShift&0xff == 255 {
				d[i] = s
			} else {
				d[i] = over(s, d[i])
			}
		}
		return
	}
	a := uint32(alpha)
	for i, s := range sc {
		d[i] = over(mul255(s, a), d[i])
	}
}

func (b *ShaderBlitter) BlitCoverage(y, x int, cov []uint8) {
	sc := b.span(y, x, len(cov))
	d := b.t.row(y, x, x+len(cov))
	for i, a := range cov {
		switch a {
		case 0:
		case 255:
			d[i] = over(sc[i], d[i])
		default:
			d[i] = over(mul255(sc[i], uint32(a)), d[i])
		}
	}
}

// MaskBlitter accumulates coverage into an 8-bit alpha image (for clip
// masks, soft masks and tests). Coverage is combined with "max", which is
// exact for the non-overlapping spans of a single rasterization.
type MaskBlitter struct {
	Mask *image.Alpha
}

func (b *MaskBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	o := b.Mask.PixOffset(x0, y)
	row := b.Mask.Pix[o : o+x1-x0]
	for i := range row {
		if row[i] < alpha {
			row[i] = alpha
		}
	}
}

func (b *MaskBlitter) BlitCoverage(y, x int, cov []uint8) {
	o := b.Mask.PixOffset(x, y)
	row := b.Mask.Pix[o : o+len(cov)]
	for i, a := range cov {
		if row[i] < a {
			row[i] = a
		}
	}
}
