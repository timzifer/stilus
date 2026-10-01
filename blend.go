package stilus

import (
	"image"
	"math"
	"sync/atomic"
)

// BlendMode is a separable or non-separable blend mode of the W3C
// Compositing and Blending specification, which are also those of PDF
// (ISO 32000-2, 11.3.5) and of SVG and CSS.
type BlendMode uint8

// Blend modes.
const (
	BlendNormal BlendMode = iota
	BlendMultiply
	BlendScreen
	BlendOverlay
	BlendDarken
	BlendLighten
	BlendColorDodge
	BlendColorBurn
	BlendHardLight
	BlendSoftLight
	BlendDifference
	BlendExclusion
	BlendHue
	BlendSaturation
	BlendColor
	BlendLuminosity
)

var blendNames = [...]string{
	"Normal", "Multiply", "Screen", "Overlay", "Darken", "Lighten", "ColorDodge", "ColorBurn",
	"HardLight", "SoftLight", "Difference", "Exclusion", "Hue", "Saturation", "Color", "Luminosity",
}

func (b BlendMode) String() string {
	if int(b) < len(blendNames) {
		return blendNames[b]
	}
	return "BlendMode(?)"
}

// LayerShader composites a layer, an isolated group drawn into an image
// of its own, onto the backdrop it is drawn over: the layer's pixels times
// Alpha and the optional Mask, blended with the backdrop by Blend. Fill the
// layer's rectangle with it through a Canvas, so that the layer is
// composited through the clip stack and antialiased at the clip's edges
// like any fill.
//
// Blend modes are exact under partial coverage: the canvas composites the
// shader's colour s over the backdrop d with coverage a as a·s + d·(1−a·αs),
// and the shader returns s = cs·(1−αb) + αs·αb·B(Cb, Cs) with alpha αs,
// which makes that the compositing formula of the specification
// interpolated by coverage.
type LayerShader struct {
	// Src is the layer; spans must lie within its rectangle.
	Src *image.RGBA
	// Dst is the backdrop the canvas draws onto, read for blend modes
	// other than Normal.
	Dst *image.RGBA
	// Mask, when non-nil, multiplies the layer; it is 0 outside its
	// rectangle.
	Mask *image.Alpha
	// Alpha is the constant opacity of the layer.
	Alpha uint8
	Blend BlendMode
}

// ShadeSpan implements Shader.
func (c *LayerShader) ShadeSpan(y, x int, out []uint32) {
	n := len(out)
	sp := c.Src.Pix[c.Src.PixOffset(x, y):][: 4*n : 4*n]
	var dp []byte
	if c.Blend != BlendNormal {
		dp = c.Dst.Pix[c.Dst.PixOffset(x, y):][: 4*n : 4*n]
	}
	var mrow []uint8
	mx := 0
	if c.Mask != nil {
		mr := c.Mask.Rect
		if y >= mr.Min.Y && y < mr.Max.Y {
			mrow = c.Mask.Pix[(y-mr.Min.Y)*c.Mask.Stride:][:mr.Dx()]
		}
		mx = mr.Min.X
	}
	for i := range out {
		p := sp[4*i : 4*i+4 : 4*i+4]
		r, g, b, a := p[0], p[1], p[2], p[3]
		k := uint32(c.Alpha)
		if c.Mask != nil {
			var m uint8
			if j := x + i - mx; j >= 0 && j < len(mrow) {
				m = mrow[j]
			}
			k = div255(k * uint32(m))
		}
		if k != 255 {
			r, g, b, a = mulByte(r, k), mulByte(g, k), mulByte(b, k), mulByte(a, k)
		}
		if a == 0 {
			out[i] = 0
			continue
		}
		if dp != nil && dp[4*i+3] != 0 {
			q := dp[4*i : 4*i+4 : 4*i+4]
			r, g, b = blendPixel(c.Blend, r, g, b, a, q[0], q[1], q[2], q[3])
		}
		out[i] = pack(r, g, b, a)
	}
}

func mulByte(v uint8, k uint32) uint8 { return uint8(div255(uint32(v) * k)) }

// blendPixel returns the colour a premultiplied source composited onto a
// premultiplied backdrop with blend mode bm must be drawn over it with
// (see LayerShader); the source alpha does not change.
func blendPixel(bm BlendMode, sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b uint8) {
	if sa == 255 && da == 255 && bm < BlendHue {
		t := blendTable(bm)
		return t[int(dr)<<8|int(sr)], t[int(dg)<<8|int(sg)], t[int(db)<<8|int(sb)]
	}
	as, ab := float64(sa)/255, float64(da)/255
	cs := [3]float64{float64(sr) / float64(sa), float64(sg) / float64(sa), float64(sb) / float64(sa)}
	cb := [3]float64{float64(dr) / float64(da), float64(dg) / float64(da), float64(db) / float64(da)}
	for i := range 3 {
		cs[i], cb[i] = min(cs[i], 1), min(cb[i], 1)
	}
	bl := Blend(bm, cb, cs)
	var o [3]uint8
	src := [3]uint8{sr, sg, sb}
	for i := range 3 {
		v := float64(src[i])*(1-ab) + as*ab*bl[i]*255
		o[i] = uint8(min(max(v+0.5, 0), float64(sa)))
	}
	return o[0], o[1], o[2]
}

// blendTables hold B(b, s) of the separable blend modes for opaque 8-bit
// colours, indexed by b<<8 | s, made on first use.
var blendTables [BlendHue]atomic.Pointer[[1 << 16]uint8]

func blendTable(bm BlendMode) *[1 << 16]uint8 {
	if t := blendTables[bm].Load(); t != nil {
		return t
	}
	t := new([1 << 16]uint8)
	for i := range t {
		v := blendChannel(bm, float64(i>>8)/255, float64(i&255)/255)
		t[i] = uint8(min(max(v, 0), 1)*255 + 0.5)
	}
	blendTables[bm].Store(t)
	return t
}

// Blend returns B(Cb, Cs), the blend function of bm, for a backdrop and a
// source colour of straight components in [0, 1].
func Blend(bm BlendMode, cb, cs [3]float64) [3]float64 {
	switch bm {
	case BlendHue:
		return setLum(setSat(cs, sat(cb)), lum(cb))
	case BlendSaturation:
		return setLum(setSat(cb, sat(cs)), lum(cb))
	case BlendColor:
		return setLum(cs, lum(cb))
	case BlendLuminosity:
		return setLum(cb, lum(cs))
	}
	var r [3]float64
	for i := range 3 {
		r[i] = blendChannel(bm, cb[i], cs[i])
	}
	return r
}

func blendChannel(bm BlendMode, b, s float64) float64 {
	switch bm {
	case BlendMultiply:
		return b * s
	case BlendScreen:
		return b + s - b*s
	case BlendOverlay:
		return hardLight(s, b)
	case BlendDarken:
		return min(b, s)
	case BlendLighten:
		return max(b, s)
	case BlendColorDodge:
		switch {
		case b == 0:
			return 0
		case s >= 1:
			return 1
		}
		return min(1, b/(1-s))
	case BlendColorBurn:
		switch {
		case b >= 1:
			return 1
		case s <= 0:
			return 0
		}
		return 1 - min(1, (1-b)/s)
	case BlendHardLight:
		return hardLight(b, s)
	case BlendSoftLight:
		if s <= 0.5 {
			return b - (1-2*s)*b*(1-b)
		}
		d := math.Sqrt(b)
		if b <= 0.25 {
			d = ((16*b-12)*b + 4) * b
		}
		return b + (2*s-1)*(d-b)
	case BlendDifference:
		return math.Abs(b - s)
	case BlendExclusion:
		return b + s - 2*b*s
	}
	return s
}

func hardLight(b, s float64) float64 {
	if s <= 0.5 {
		return b * 2 * s
	}
	s = 2*s - 1
	return b + s - b*s
}

func lum(c [3]float64) float64 { return 0.3*c[0] + 0.59*c[1] + 0.11*c[2] }

func setLum(c [3]float64, l float64) [3]float64 {
	d := l - lum(c)
	for i := range c {
		c[i] += d
	}
	// Clip into [0, 1], keeping the luminosity.
	l = lum(c)
	n, x := min(c[0], c[1], c[2]), max(c[0], c[1], c[2])
	for i := range c {
		if n < 0 && l-n > 0 {
			c[i] = l + (c[i]-l)*l/(l-n)
		}
		if x > 1 && x-l > 0 {
			c[i] = l + (c[i]-l)*(1-l)/(x-l)
		}
		c[i] = min(max(c[i], 0), 1)
	}
	return c
}

func sat(c [3]float64) float64 { return max(c[0], c[1], c[2]) - min(c[0], c[1], c[2]) }

func setSat(c [3]float64, s float64) [3]float64 {
	// Indices of the smallest, middle and largest component.
	lo, mid, hi := 0, 1, 2
	if c[lo] > c[mid] {
		lo, mid = mid, lo
	}
	if c[mid] > c[hi] {
		mid, hi = hi, mid
	}
	if c[lo] > c[mid] {
		lo, mid = mid, lo
	}
	var r [3]float64
	if c[hi] > c[lo] {
		r[mid] = (c[mid] - c[lo]) * s / (c[hi] - c[lo])
		r[hi] = s
	}
	return r
}
