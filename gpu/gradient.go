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
//	6…16   float32: linear A, B, C; radial Inv[0…3], Inv[4] − X0,
//	       Inv[5] − Y0, R0, DX, DY, DR, QA
//	17…    the ramp (n words), the knots (n float32) if any
const (
	gradRadial = 1 << iota
	gradExtend0
	gradExtend1
	gradKnots
	gradLinearEq

	gradHead = 17
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
	default:
		return nil
	}
	if !ok {
		return nothing{}
	}
	b := &t.grad
	b.recOK, b.img = false, nil
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
	if f.Radial {
		m := f.Inv
		for _, v := range [...]float64{m[0], m[1], m[2], m[3], m[4] - f.X0, m[5] - f.Y0, f.R0, f.DX, f.DY, f.DR, f.QA} {
			putf(v)
		}
	} else {
		for _, v := range [...]float64{f.A, f.B, f.C, 0, 0, 0, 0, 0, 0, 0, 0} {
			putf(v)
		}
	}
	for _, c := range g.Ramp {
		put(rgba(c))
	}
	for _, k := range f.Knots {
		putf(k)
	}
	b.rec = r
	return b
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
