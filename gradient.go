package stilus

import "math"

// Gradients are shaders that compute a parameter t per device pixel and
// look its colour up in a Ramp. t is computed at the pixel's centre from
// the pixel's own position, so a pixel gets the same colour whichever band,
// tile or span it is drawn in.

// Ramp is a colour ramp: premultiplied colours (PackRGBA layout) at evenly
// spaced parameters from 0 to 1, or at the parameters a gradient's Knots
// give. A gradient with an empty ramp paints nothing.
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
	// Knots, if not empty, are the parameters of the Ramp's entries: as
	// many as entries, non-decreasing, within [0, 1]. Colours are then
	// interpolated linearly between the two entries around t, and a knot
	// given twice is a hard stop exactly there (t at the stop takes the
	// later entry); t before the first knot or past the last takes the
	// colour of the first or last entry. Without knots the entries are
	// evenly spaced and looked up nearest. Set reads the knots: call it
	// again after changing them.
	Knots []float32

	// From Knots, built by prepare: the knots, the inverse width of each
	// interval, and for each of knotBuckets uniform buckets of t the last
	// knot at or before the bucket's start.
	kt   []float64
	kinv []float64
	kidx []int32
}

// knotBuckets is the number of uniform buckets that index the knots.
const knotBuckets = 256

// prepare builds the knot index, keeping its buffers, and reports whether
// the knots are usable.
func (g *Gradient) prepare() bool {
	g.kt, g.kinv = g.kt[:0], g.kinv[:0]
	k := g.Knots
	if len(k) == 0 {
		return true
	}
	if len(k) != len(g.Ramp) {
		return false
	}
	prev := 0.0
	for _, v := range k {
		f := float64(v)
		if !(f >= prev && f <= 1) {
			return false
		}
		g.kt = append(g.kt, f)
		prev = f
	}
	for i := range len(k) - 1 {
		inv := 0.0
		if d := g.kt[i+1] - g.kt[i]; d > 0 {
			inv = 1 / d
		}
		g.kinv = append(g.kinv, inv)
	}
	if cap(g.kidx) < knotBuckets+1 {
		g.kidx = make([]int32, knotBuckets+1)
	}
	g.kidx = g.kidx[:knotBuckets+1]
	i := 0
	for j := range g.kidx {
		b := float64(j) / knotBuckets
		for i+1 < len(g.kt) && g.kt[i+1] <= b {
			i++
		}
		g.kidx[j] = int32(i)
	}
	return true
}

// knotColor returns the colour at t in [0, 1] between the knots, those
// of the interval that starts at the last knot at or before t. i is a
// guess of that interval, which neighbouring pixels mostly share; the
// interval found is returned for the next pixel.
func (g *Gradient) knotColor(t float64, i int) (uint32, int) {
	k := g.kt
	if !(k[i] <= t && (i+1 == len(k) || t < k[i+1])) {
		i = int(g.kidx[int(t*knotBuckets)])
		for i+1 < len(k) && k[i+1] <= t {
			i++
		}
	}
	if t <= k[i] || i+1 == len(k) {
		return g.Ramp[i], i
	}
	// k[i] < t < k[i+1]: blend with a weight of 0 to 256.
	w := uint64(int64((t-k[i])*g.kinv[i]*256 + 0.5))
	a, b := expand(g.Ramp[i]), expand(g.Ramp[i+1])
	return compact(((a*(256-w) + b*w + 0x0080008000800080) >> 8) & lanes), i
}

// knotColors sets dst to the colours at the parameters ts, with knots.
func (g *Gradient) knotColors(ts []float64, dst []uint32) {
	dst = dst[:len(ts)]
	k, inv, idx, r := g.kt, g.kinv, g.kidx, g.Ramp
	i := 0
	for j, t := range ts {
		switch {
		case t < 0:
			if !g.Extend[0] {
				dst[j] = g.Outside
				continue
			}
			t = 0
		case t > 1:
			if !g.Extend[1] {
				dst[j] = g.Outside
				continue
			}
			t = 1
		case !(t >= 0): // NaN
			dst[j] = g.Outside
			continue
		}
		// knotColor, inlined.
		if !(k[i] <= t && (i+1 == len(k) || t < k[i+1])) {
			i = int(idx[int(t*knotBuckets)])
			for i+1 < len(k) && k[i+1] <= t {
				i++
			}
		}
		if t <= k[i] || i+1 >= len(k) || i+1 >= len(r) || i >= len(inv) {
			dst[j] = r[i]
			continue
		}
		w := uint64(int64((t-k[i])*inv[i]*256 + 0.5))
		a, b := expand(r[i]), expand(r[i+1])
		dst[j] = compact(((a*(256-w) + b*w + 0x0080008000800080) >> 8) & lanes)
	}
}

// knotted reports whether colours are looked up between knots: Set found
// them usable, and the ramp has not changed length since.
func (g *Gradient) knotted() bool { return len(g.kt) > 0 && len(g.kt) == len(g.Ramp) }

// knotChunk is the number of parameters computed at a time for knots.
const knotChunk = 64

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
	if g.knotted() {
		c, _ := g.knotColor(t, 0)
		return c
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
// coincident points, singular or non-finite transforms and unusable Knots.
// It keeps Ramp, Extend, Outside, Alpha and Knots.
func (g *LinearGradient) Set(x0, y0, x1, y1 float64, m Matrix) bool {
	g.ok = false
	if !g.prepare() {
		return false
	}
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
	// t is computed from each pixel's own position, not stepped along the
	// span, so a pixel's colour does not depend on where its span starts
	// (fx counts exactly: it stays far below 2^52).
	ty := g.b*(float64(y)+0.5) + g.c
	switch {
	case g.a == 0:
		fill32(dst, g.color(ty))
	case g.knotted():
		var ts [knotChunk]float64
		fx := float64(x) + 0.5
		for o := 0; o < len(dst); o += knotChunk {
			t := ts[:min(knotChunk, len(dst)-o)]
			for j := range t {
				t[j] = g.a*fx + ty
				fx++
			}
			g.knotColors(t, dst[o:])
		}
	default:
		fx := float64(x) + 0.5
		for i := range dst {
			dst[i] = g.color(g.a*fx + ty)
			fx++
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
// drawn: false for negative radii, singular or non-finite transforms and
// unusable Knots. It keeps Ramp, Extend, Outside, Alpha and Knots.
func (g *RadialGradient) Set(x0, y0, r0, x1, y1, r1 float64, m Matrix) bool {
	inv, ok := m.Invert()
	g.ok = ok && inv.finite() && r0 >= 0 && r1 >= 0 && g.prepare()
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
	// u and v are computed from each pixel's own position, not stepped
	// along the span, so a pixel's colour does not depend on where its
	// span starts (fx counts exactly: it stays far below 2^52).
	fy := float64(y) + 0.5
	cu, cv := m[2]*fy+m[4]-g.x0, m[3]*fy+m[5]-g.y0
	fx0, fx1 := float64(x)+0.5, float64(x+len(dst)-1)+0.5
	// u and v are linear along the span, so finite ends make all of it
	// finite; elsewhere param's b is Inf·0 and the point outside.
	concentric := g.concentric && finite(m[0]*fx0+cu, m[1]*fx0+cv) && finite(m[0]*fx1+cu, m[1]*fx1+cv)
	if g.knotted() {
		var ts [knotChunk]float64
		fx := fx0
		for o := 0; o < len(dst); o += knotChunk {
			t := ts[:min(knotChunk, len(dst)-o)]
			for j := range t {
				if concentric {
					t[j] = g.concentricParam(m[0]*fx+cu, m[1]*fx+cv)
				} else {
					t[j] = g.param(m[0]*fx+cu, m[1]*fx+cv)
				}
				fx++
			}
			g.knotColors(t, dst[o:])
		}
		g.scale(dst)
		return
	}
	if concentric {
		fx := fx0
		for i := range dst {
			dst[i] = g.color(g.concentricParam(m[0]*fx+cu, m[1]*fx+cv))
			fx++
		}
		g.scale(dst)
		return
	}
	fx := fx0
	for i := range dst {
		dst[i] = g.color(g.param(m[0]*fx+cu, m[1]*fx+cv))
		fx++
	}
	g.scale(dst)
}

// concentricParam is param for concentric circles starting at radius 0.
// There b = 0, so the larger root is √(−qa·c)/−qa ≥ 0 and the smaller one
// has a negative radius; where param would return NaN for t > 1, color
// paints Outside all the same. The operations are param's, rounded as
// there, so t is equal to the bit (up to the sign of zero).
func (g *RadialGradient) concentricParam(u, v float64) float64 {
	nq := -g.qa
	c := float64(u*u) + float64(v*v)
	return math.Sqrt(float64(nq*c)) / nq
}

func finite(u, v float64) bool {
	return math.Abs(u) <= math.MaxFloat64 && math.Abs(v) <= math.MaxFloat64
}

// param returns the parameter of the point (u, v) relative to the first
// centre, or NaN where no circle passes through it.
func (g *RadialGradient) param(u, v float64) float64 {
	// |p − c(t)|² = r(t)²: qa·t² − 2·b·t + c = 0. The conversions round
	// every product, so that no architecture fuses them into FMAs: t is
	// the same everywhere, and concentricParam can match it.
	b := float64(u*g.dx) + float64(v*g.dy) + float64(g.r0*g.dr)
	c := float64(u*u) + float64(v*v) - float64(g.r0*g.r0)
	var t1, t2 float64
	if math.Abs(g.qa) < 1e-12*(g.dx*g.dx+g.dy*g.dy+g.dr*g.dr) || g.qa == 0 {
		if b == 0 {
			return math.NaN()
		}
		t1 = c / (2 * b)
		t2 = t1
	} else {
		d := float64(b*b) - float64(g.qa*c)
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
