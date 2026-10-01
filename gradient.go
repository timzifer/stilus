package stilus

import "math"

// Gradients are shaders that compute a parameter t per device pixel and
// look its colour up in a Ramp. t is computed at the pixel's centre from
// the pixel's own position, so a pixel gets the same colour whichever band,
// tile or span it is drawn in.

// Ramp is a colour ramp: premultiplied colours (PackRGBA layout) at evenly
// spaced parameters from 0 to 1. A gradient with an empty ramp paints
// nothing.
type Ramp []uint32

// At returns the colour of the ramp nearest to t, clamped to [0, 1].
func (r Ramp) At(t float64) uint32 {
	n := len(r)
	if !(t > 0) {
		return r[0]
	}
	if t >= 1 {
		return r[n-1]
	}
	return r[int(t*float64(n-1)+0.5)]
}

// Gradient is what linear and radial gradients share.
type Gradient struct {
	// Ramp maps the gradient's parameter to colours.
	Ramp Ramp
	// Extend continues the colour at t = 0 before the start (Extend[0])
	// and the colour at t = 1 past the end (Extend[1]); where the gradient
	// does not extend it paints Outside.
	Extend [2]bool
	// Outside is the premultiplied colour (PackRGBA layout) painted
	// where the gradient is not defined; 0 is transparent.
	Outside uint32
	// Alpha multiplies the gradient's colours; 255 is opaque, 0 paints
	// nothing.
	Alpha uint8
}

// color returns the colour at parameter t, Outside where the gradient does
// not extend.
func (g *Gradient) color(t float64) uint32 {
	switch {
	case t < 0:
		if !g.Extend[0] {
			return g.Outside
		}
		t = 0
	case t > 1:
		if !g.Extend[1] {
			return g.Outside
		}
		t = 1
	case !(t >= 0): // NaN
		return g.Outside
	}
	r := g.Ramp
	return r[int(t*float64(len(r)-1)+0.5)]
}

func (g *Gradient) scale(dst []uint32) {
	if g.Alpha == 255 {
		return
	}
	a := uint32(g.Alpha)
	for i, c := range dst {
		dst[i] = mul255(c, a)
	}
}

// LinearGradient is an axial gradient: t runs from 0 at P0 to 1 at P1 and
// is constant on lines perpendicular to P0P1 (in the gradient's own space,
// which may be mapped to device space by any affine transform).
type LinearGradient struct {
	Gradient
	// t = a·x + b·y + c at device pixel centres.
	a, b, c float64
	ok      bool
}

// Set places the gradient from (x0, y0) to (x1, y1) in a space that m maps
// to device space, and reports whether it can be drawn: false for
// coincident points and singular or non-finite transforms. It keeps Ramp,
// Extend, Outside and Alpha.
func (g *LinearGradient) Set(x0, y0, x1, y1 float64, m Matrix) bool {
	g.ok = false
	inv, ok := m.Invert()
	dx, dy := x1-x0, y1-y0
	den := dx*dx + dy*dy
	if !ok || !inv.finite() || !(den > 0) || math.IsInf(den, 0) {
		return false
	}
	// t(u, v) = ((u−x0)·dx + (v−y0)·dy)/den with (u, v) = inv(x, y).
	ku, kv := dx/den, dy/den
	g.a = ku*inv[0] + kv*inv[1]
	g.b = ku*inv[2] + kv*inv[3]
	g.c = ku*(inv[4]-x0) + kv*(inv[5]-y0)
	g.ok = !math.IsNaN(g.a+g.b+g.c) && !math.IsInf(g.a+g.b+g.c, 0)
	return g.ok
}

// ShadeSpan implements Shader.
func (g *LinearGradient) ShadeSpan(y, x int, dst []uint32) {
	if !g.ok || len(g.Ramp) == 0 || g.Alpha == 0 {
		clear(dst)
		return
	}
	t0 := g.a*(float64(x)+0.5) + g.b*(float64(y)+0.5) + g.c
	if g.a == 0 {
		fill32(dst, g.color(t0))
	} else {
		for i := range dst {
			dst[i] = g.color(t0 + g.a*float64(i))
		}
	}
	g.scale(dst)
}

// RadialGradient is a gradient between two circles: the circle at t has
// its centre at C0 + t·(C1 − C0) and its radius r0 + t·(r1 − r0), and a
// point takes the colour of the largest t whose circle passes through it
// with a radius ≥ 0 (in the extended range, if the gradient extends). This
// is the two-point conical gradient of PDF (type 3 shadings), of the HTML
// canvas and of SVG with a focal point.
type RadialGradient struct {
	Gradient
	inv            Matrix // device to gradient space
	x0, y0, r0     float64
	dx, dy, dr, qa float64
	// concentric marks circles with one centre and a start radius of 0,
	// whose parameter is the distance from the centre over r1.
	concentric bool
	ok         bool
}

// Set places the gradient between the circles (x0, y0, r0) and (x1, y1, r1)
// in a space that m maps to device space, and reports whether it can be
// drawn: false for negative radii and singular or non-finite transforms.
// It keeps Ramp, Extend, Outside and Alpha.
func (g *RadialGradient) Set(x0, y0, r0, x1, y1, r1 float64, m Matrix) bool {
	inv, ok := m.Invert()
	g.ok = ok && inv.finite() && r0 >= 0 && r1 >= 0
	for _, v := range [...]float64{x0, y0, r0, x1, y1, r1} {
		g.ok = g.ok && !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	if !g.ok {
		return false
	}
	g.inv = inv
	g.x0, g.y0, g.r0 = x0, y0, r0
	g.dx, g.dy, g.dr = x1-x0, y1-y0, r1-r0
	g.qa = g.dx*g.dx + g.dy*g.dy - g.dr*g.dr
	g.concentric = g.dx == 0 && g.dy == 0 && r0 == 0 && g.qa < 0 && !math.IsInf(g.qa, 0)
	return true
}

// ShadeSpan implements Shader.
func (g *RadialGradient) ShadeSpan(y, x int, dst []uint32) {
	if !g.ok || len(g.Ramp) == 0 || g.Alpha == 0 {
		clear(dst)
		return
	}
	m := &g.inv
	fx, fy := float64(x)+0.5, float64(y)+0.5
	u0 := m[0]*fx + m[2]*fy + m[4] - g.x0
	v0 := m[1]*fx + m[3]*fy + m[5] - g.y0
	// u and v are linear along the span, so finite ends make all of it
	// finite; elsewhere param's b is Inf·0 and the point outside.
	n := float64(len(dst) - 1)
	if g.concentric && finite(u0, v0) && finite(u0+m[0]*n, v0+m[1]*n) {
		for i := range dst {
			u, v := u0+m[0]*float64(i), v0+m[1]*float64(i)
			dst[i] = g.color(g.concentricParam(u, v))
		}
		g.scale(dst)
		return
	}
	for i := range dst {
		u, v := u0+m[0]*float64(i), v0+m[1]*float64(i)
		dst[i] = g.color(g.param(u, v))
	}
	g.scale(dst)
}

// concentricParam is param for concentric circles starting at radius 0.
// There b = 0, so the larger root is √(−qa·c)/−qa ≥ 0 and the smaller one
// has a negative radius; where param would return NaN for t > 1, color
// paints Outside all the same. The operations are param's, so t is equal
// to the bit (up to the sign of zero).
func (g *RadialGradient) concentricParam(u, v float64) float64 {
	nq := -g.qa
	return math.Sqrt(nq*(u*u+v*v)) / nq
}

func finite(u, v float64) bool {
	return math.Abs(u) <= math.MaxFloat64 && math.Abs(v) <= math.MaxFloat64
}

// param returns the parameter of the point (u, v) relative to the first
// centre, or NaN where no circle passes through it.
func (g *RadialGradient) param(u, v float64) float64 {
	// |p − c(t)|² = r(t)²: qa·t² − 2·b·t + c = 0.
	b := u*g.dx + v*g.dy + g.r0*g.dr
	c := u*u + v*v - g.r0*g.r0
	var t1, t2 float64
	if math.Abs(g.qa) < 1e-12*(g.dx*g.dx+g.dy*g.dy+g.dr*g.dr) || g.qa == 0 {
		if b == 0 {
			return math.NaN()
		}
		t1 = c / (2 * b)
		t2 = t1
	} else {
		d := b*b - g.qa*c
		if d < 0 {
			return math.NaN()
		}
		s := math.Sqrt(d)
		t1, t2 = (b+s)/g.qa, (b-s)/g.qa
		if t1 < t2 {
			t1, t2 = t2, t1
		}
	}
	// The larger root wins if its radius is not negative and it lies in
	// the range drawn.
	for _, t := range [2]float64{t1, t2} {
		if g.r0+t*g.dr < 0 {
			continue
		}
		if (t < 0 && !g.Extend[0]) || (t > 1 && !g.Extend[1]) {
			continue
		}
		return t
	}
	return math.NaN()
}
