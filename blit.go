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
	runOver(d, s)
}

func (b *SolidBlitter) BlitCoverage(y, x int, cov []uint8) {
	d := b.t.row(y, x, x+len(cov))
	if b.opaque {
		covOpaque(d, cov, b.c, b.cx)
		return
	}
	covOver(d, cov, b.cx)
}

// Scalar span kernels. simd_*.go dispatches to vector versions where the
// CPU and toolchain allow it.

// runOverScalar composites the premultiplied constant s over every pixel:
// one multiplication per pixel in the 64-bit lane layout.
func runOverScalar(d []uint32, s uint32) {
	inv := uint64(255 - (s>>alphaShift)&0xff)
	sx := expand(s)
	for i, v := range d {
		d[i] = compact(sx + div255x(expand(v)*inv))
	}
}

// covOpaqueScalar blends an opaque color c (cx expanded) with coverage.
func covOpaqueScalar(d []uint32, cov []uint8, c uint32, cx uint64) {
	d = d[:len(cov)]
	for i, a := range cov {
		switch a {
		case 0:
		case 255:
			d[i] = c
		default:
			d[i] = lerpx(cx, d[i], uint32(a))
		}
	}
}

// covOverScalar composites a premultiplied color (expanded) with coverage.
func covOverScalar(d []uint32, cov []uint8, cx uint64) {
	d = d[:len(cov)]
	for i, a := range cov {
		if a != 0 {
			d[i] = overx(cx, d[i], uint32(a))
		}
	}
}

// spanOverScalar composites the premultiplied pixels s, scaled by k/255
// and then by a/255, over d: the bytes of over(mul255(mul255(s, k), a), d).
func spanOverScalar(d, s []uint32, k, a uint32) {
	d = d[:len(s)]
	if k == 255 && a == 255 {
		for i, v := range s {
			if v>>alphaShift&0xff == 255 {
				d[i] = v
			} else {
				d[i] = over(v, d[i])
			}
		}
		return
	}
	for i, v := range s {
		if k != 255 {
			v = mul255(v, k)
		}
		if a != 255 {
			v = mul255(v, a)
		}
		d[i] = over(v, d[i])
	}
}

// spanOverCovScalar composites the premultiplied pixels s, scaled by k/255
// and then by the coverage, over d.
func spanOverCovScalar(d, s []uint32, k uint32, cov []uint8) {
	d, s = d[:len(cov)], s[:len(cov)]
	for i, a := range cov {
		if a == 0 {
			continue
		}
		v := s[i]
		if k != 255 {
			v = mul255(v, k)
		}
		if a != 255 {
			v = mul255(v, uint32(a))
		}
		d[i] = over(v, d[i])
	}
}

// rowSource is implemented by shaders whose span can be a row of pixels
// they hold, scaled by a constant alpha: ShaderBlitter composites such
// rows straight from that memory instead of from a shaded copy.
type rowSource interface {
	// srcRow returns pixels s and k such that ShadeSpan(y, x, out) with
	// len(out) == n would set out[i] to mul255(s[i], k) (s[i] when k is
	// 255), or ok false.
	srcRow(y, x, n int) (s []uint32, k uint32, ok bool)
}

// ShaderBlitter composites the output of a Shader onto an image.RGBA.
type ShaderBlitter struct {
	t       target
	s       Shader
	rs      rowSource // s, if it is one
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
	b.setShader(s)
}

func (b *ShaderBlitter) setShader(s Shader) {
	b.s = s
	b.rs, _ = s.(rowSource)
}

// direct returns the shader's own row for the n pixels at (x, y), if it
// has one that does not overlap d (in place, s == d, is fine: each pixel
// is read before it is written).
func (b *ShaderBlitter) direct(y, x, n int, d []uint32) ([]uint32, uint32, bool) {
	if b.rs == nil {
		return nil, 0, false
	}
	s, k, ok := b.rs.srcRow(y, x, n)
	if !ok || len(s) != n {
		return nil, 0, false
	}
	ps, pd := uintptr(unsafe.Pointer(&s[0])), uintptr(unsafe.Pointer(&d[0]))
	if ps != pd && ps < pd+uintptr(4*n) && pd < ps+uintptr(4*n) {
		return nil, 0, false
	}
	return s, k, true
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
	d := b.t.row(y, x0, x1)
	if s, k, ok := b.direct(y, x0, x1-x0, d); ok {
		spanOver(d, s, k, uint32(alpha))
		return
	}
	spanOver(d, b.span(y, x0, x1-x0), 255, uint32(alpha))
}

func (b *ShaderBlitter) BlitCoverage(y, x int, cov []uint8) {
	d := b.t.row(y, x, x+len(cov))
	if s, k, ok := b.direct(y, x, len(cov), d); ok {
		spanOverCov(d, s, k, cov)
		return
	}
	spanOverCov(d, b.span(y, x, len(cov)), 255, cov)
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
