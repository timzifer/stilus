package stilus_test

import (
	"image/color"
	"math"
	"math/rand"
	"testing"

	"github.com/timzifer/stilus"
)

// formParam computes a gradient's parameter at the centre of pixel (x, y)
// from its form alone, as GradientForm documents it.
func formParam(f *stilus.GradientForm, ext [2]bool, x, y int) float64 {
	fx, fy := float64(x)+0.5, float64(y)+0.5
	if !f.Radial {
		return f.A*fx + f.B*fy + f.C
	}
	m := f.Inv
	u := m[0]*fx + (m[2]*fy + m[4] - f.X0)
	v := m[1]*fx + (m[3]*fy + m[5] - f.Y0)
	b := float64(u*f.DX) + float64(v*f.DY) + float64(f.R0*f.DR)
	c := float64(u*u) + float64(v*v) - float64(f.R0*f.R0)
	var t1, t2 float64
	if f.LinearEq {
		if b == 0 {
			return math.NaN()
		}
		t1 = c / (2 * b)
		t2 = t1
	} else {
		d := float64(b*b) - float64(f.QA*c)
		if d < 0 {
			return math.NaN()
		}
		s := math.Sqrt(d)
		t1, t2 = (b+s)/f.QA, (b-s)/f.QA
		if t1 < t2 {
			t1, t2 = t2, t1
		}
	}
	for _, t := range [2]float64{t1, t2} {
		if f.R0+t*f.DR < 0 || t < 0 && !ext[0] || t > 1 && !ext[1] {
			continue
		}
		return t
	}
	return math.NaN()
}

// formColor is the colour at t of a gradient with form f.
func formColor(g *stilus.Gradient, f *stilus.GradientForm, t float64) uint32 {
	var c uint32
	switch {
	case t < 0 && !g.Extend[0], t > 1 && !g.Extend[1], math.IsNaN(t):
		c = g.Outside
	default:
		t = min(max(t, 0), 1)
		r := g.Ramp
		if k := f.Knots; k != nil {
			i := 0
			for i+1 < len(k) && k[i+1] <= t {
				i++
			}
			if t <= k[i] || i+1 == len(k) {
				c = r[i]
			} else {
				w := uint32(int64((t-k[i])*(1/(k[i+1]-k[i]))*256 + 0.5))
				a, b := stilus.UnpackRGBA(r[i]), stilus.UnpackRGBA(r[i+1])
				lerp := func(a, b uint8) uint8 { return uint8((uint32(a)*(256-w) + uint32(b)*w + 128) >> 8) }
				c = stilus.PackRGBA(color.RGBA{lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B), lerp(a.A, b.A)})
			}
		} else {
			c = r[int(t*float64(len(r)-1)+0.5)]
		}
	}
	if g.Alpha != 255 {
		k := stilus.UnpackRGBA(c)
		mul := func(v uint8) uint8 {
			x := uint32(v)*uint32(g.Alpha) + 128
			return uint8((x + x>>8) >> 8)
		}
		c = stilus.PackRGBA(color.RGBA{mul(k.R), mul(k.G), mul(k.B), mul(k.A)})
	}
	return c
}

// TestGradientForm: colours computed from Form alone are ShadeSpan's,
// for linear and radial gradients, with and without knots, extended or
// not, under rotated and scaled transforms.
func TestGradientForm(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	ramp := make(stilus.Ramp, 64)
	for i := range ramp {
		ramp[i] = stilus.PackRGBA(color.RGBA{uint8(i * 4), 90, uint8(255 - i*4), 255})
	}
	knots := make([]float32, len(ramp))
	for i := range knots {
		knots[i] = float32(math.Pow(float64(i)/float64(len(knots)-1), 1.7))
	}
	knots[30] = knots[29] // a hard stop
	dst := make([]uint32, 120)
	checked := 0
	for n := 0; n < 300; n++ {
		a := rng.Float64() * math.Pi
		m := stilus.Matrix{math.Cos(a) * 2, math.Sin(a) * 2, -math.Sin(a) * 1.5, math.Cos(a) * 1.5, rng.Float64() * 50, rng.Float64() * 50}
		var g stilus.Gradient
		g.Ramp = ramp
		g.Extend = [2]bool{rng.Intn(2) == 0, rng.Intn(2) == 0}
		g.Outside = stilus.PackRGBA(color.RGBA{10, 0, 0, 40})
		g.Alpha = []uint8{255, 160, 7}[n%3]
		if n%2 == 0 {
			g.Knots = knots
		}
		var sh stilus.Shader
		var f stilus.GradientForm
		var ok bool
		if n%4 < 2 {
			lg := &stilus.LinearGradient{Gradient: g}
			lg.Set(rng.Float64()*40, rng.Float64()*40, 20+rng.Float64()*40, rng.Float64()*40, m)
			sh = lg
			f, ok = lg.Form()
		} else {
			rg := &stilus.RadialGradient{Gradient: g}
			r0 := []float64{0, 3, 12}[n%3]
			rg.Set(10+rng.Float64()*20, 10+rng.Float64()*20, r0, 15+rng.Float64()*20, 15+rng.Float64()*20, 5+rng.Float64()*30, m)
			sh = rg
			f, ok = rg.Form()
		}
		if !ok {
			t.Fatalf("case %d: Form not ok", n)
		}
		for y := 0; y < 100; y += 7 {
			sh.ShadeSpan(y, -10, dst)
			for i, want := range dst {
				got := formColor(&g, &f, formParam(&f, g.Extend, i-10, y))
				if got != want {
					t.Fatalf("case %d (%v), pixel (%d,%d): form %08x, ShadeSpan %08x", n, f.Radial, i-10, y, got, want)
				}
				checked++
			}
		}
	}
	t.Logf("%d pixels", checked)
	var empty stilus.LinearGradient
	if _, ok := empty.Form(); ok {
		t.Fatal("unset gradient has a form")
	}
}
