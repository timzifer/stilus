package gpu

import (
	"encoding/binary"
	"math"

	"github.com/timzifer/stilus"
)

// Gradient records (spans.wgsl, gradColor), in words:
//
//	0      flags: gradRadial, gradExtend0, gradExtend1, gradKnots, gradLinearEq
//	1      n, the ramp's length
//	2, 3   word offsets of the ramp and the knots, relative to the record
//	4      Outside, premultiplied RGBA8, R in the low byte
//	5      Alpha
//	6…16   float32: linear A, B and t at the origin's centre; radial
//	       Inv[0…3], u and v at the origin's centre, R0, DX, DY, DR, QA
//	17, 18 int32 origin: the first pixel the operation draws
//	19…    the ramp (n words), the knots (n float32) if any
//
// t (u, v) at a pixel is the origin's plus the linear part times the
// pixel's integer distance from it, so that float32 holds the numbers of
// the operation, not of the page; the record's origin words are written
// when it is uploaded (gradOrigin).
const (
	gradRadial = 1 << iota
	gradExtend0
	gradExtend1
	gradKnots
	gradLinearEq

	gradHead = 19
)

// gradient returns the blitter of a gradient the GPU computes, its record
// made, or nil for a shader it does not know, which is shaded on the CPU.
// A gradient that paints nothing gets a blitter that records nothing.
func (t *Target) gradient(sh stilus.Shader) stilus.Blitter {
	var g *stilus.Gradient
	var f stilus.GradientForm
	var ok bool
	switch s := sh.(type) {
	case *stilus.LinearGradient:
		g = &s.Gradient
		f, ok = s.Form()
	case *stilus.RadialGradient:
		g = &s.Gradient
		f, ok = s.Form()
	case *stilus.ImageShader:
		return t.imageBlitter(s)
	case *stilus.MeshShader:
		return t.meshBlitter(s)
	case *stilus.MaskShader:
		return t.alphaMaskBlitter(s)
	case *stilus.LayerShader, *Layer:
		return t.layerBlitter(s)
	default:
		return nil
	}
	if !ok {
		return nothing{}
	}
	b := &t.grad
	b.recOK, b.img, b.gform = false, nil, nil
	n := len(g.Ramp)
	flags := uint32(0)
	if f.Radial {
		flags |= gradRadial
	}
	if g.Extend[0] {
		flags |= gradExtend0
	}
	if g.Extend[1] {
		flags |= gradExtend1
	}
	if f.Knots != nil {
		flags |= gradKnots
	}
	if f.LinearEq {
		flags |= gradLinearEq
	}
	r := b.rec[:0]
	put := func(v uint32) { r = binary.LittleEndian.AppendUint32(r, v) }
	putf := func(v float64) { put(math.Float32bits(float32(v))) }
	put(flags)
	put(uint32(n))
	put(gradHead)
	put(uint32(gradHead + n))
	put(rgba(g.Outside))
	put(uint32(g.Alpha))
	for range 11 + 2 {
		put(0) // the form at the origin: gradOrigin
	}
	for _, c := range g.Ramp {
		put(rgba(c))
	}
	for _, k := range f.Knots {
		putf(k)
	}
	b.rec = r
	t.gform = f
	b.gform = &t.gform
	return b
}

// gradOrigin writes into a gradient record the form at the origin, the
// centre of pixel (x, y).
func gradOrigin(r []byte, f *stilus.GradientForm, x, y int) {
	fx, fy := float64(x)+0.5, float64(y)+0.5
	var v [11]float64
	if f.Radial {
		m := f.Inv
		v = [11]float64{m[0], m[1], m[2], m[3],
			m[0]*fx + m[2]*fy + m[4] - f.X0, m[1]*fx + m[3]*fy + m[5] - f.Y0,
			f.R0, f.DX, f.DY, f.DR, f.QA}
	} else {
		v = [11]float64{f.A, f.B, f.A*fx + f.B*fy + f.C}
	}
	for i, k := range v {
		binary.LittleEndian.PutUint32(r[(6+i)*4:], math.Float32bits(float32(k)))
	}
	binary.LittleEndian.PutUint32(r[17*4:], uint32(int32(x)))
	binary.LittleEndian.PutUint32(r[18*4:], uint32(int32(y)))
}

// rgba converts a colour in PackRGBA layout to the GPU's: R in the low
// byte.
func rgba(c uint32) uint32 {
	k := stilus.UnpackRGBA(c)
	return uint32(k.R) | uint32(k.G)<<8 | uint32(k.B)<<16 | uint32(k.A)<<24
}

// nothing is the blitter of a paint that paints nothing.
type nothing struct{}

func (nothing) BlitRun(y, x0, x1 int, alpha uint8) {}
func (nothing) BlitCoverage(y, x int, cov []uint8) {}
