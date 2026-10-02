package stilus

import (
	"math"
	"math/rand"
	"testing"
)

// A hard stop falls exactly where its knot says, not on the nearest entry
// of an evenly spaced ramp.
func TestKnotsHardStop(t *testing.T) {
	red, blue := pack(255, 0, 0, 255), pack(0, 0, 255, 255)
	var g LinearGradient
	g.Ramp, g.Alpha = Ramp{red, red, blue, blue}, 255
	g.Knots = []float32{0, 0.3137, 0.3137, 1}
	if !g.Set(0, 0, 1000, 0, Identity) {
		t.Fatal("set")
	}
	px := make([]uint32, 1000)
	g.ShadeSpan(0, 0, px)
	// The stop is at x = 313.7: centres up to 313.5 are red.
	for x, c := range px {
		want := red
		if float64(x)+0.5 >= float64(float32(0.3137))*1000 {
			want = blue
		}
		if c != want {
			t.Fatalf("x %d: %v", x, UnpackRGBA(c))
		}
	}
	// At the stop itself, the later entry.
	g.Knots = []float32{0, 0.5, 0.5, 1}
	g.Set(0, 0, 100, 0, Identity)
	if c := shade(&g, 0, 49, 2); c[0] != rgba(255, 0, 0, 255) || c[1] != rgba(0, 0, 255, 255) {
		t.Errorf("around 0.5: %v", c)
	}
	g.Set(0, 0, 101, 0, Identity) // the centre of pixel 50 is t = 0.5
	if c := shade(&g, 0, 50, 1)[0]; c != rgba(0, 0, 255, 255) {
		t.Errorf("at 0.5: %v", c)
	}
}

func TestKnotsInterpolate(t *testing.T) {
	var g LinearGradient
	g.Ramp, g.Alpha = Ramp{pack(0, 0, 0, 255), pack(200, 100, 0, 255), pack(255, 255, 255, 255)}, 255
	g.Knots = []float32{0, 0.25, 1}
	g.Outside = pack(1, 2, 3, 4)
	if !g.Set(0, 0, 1000, 0, Identity) {
		t.Fatal("set")
	}
	px := shade(&g, 0, -10, 1020)
	for x := range 1000 {
		tt := (float64(x) + 0.5) / 1000
		var want [2]float64
		if tt < 0.25 {
			want = [2]float64{200 * tt / 0.25, 100 * tt / 0.25}
		} else {
			f := (tt - 0.25) / 0.75
			want = [2]float64{200 + 55*f, 100 + 155*f}
		}
		c := px[x+10]
		if math.Abs(float64(c.R)-want[0]) > 1 || math.Abs(float64(c.G)-want[1]) > 1 || c.A != 255 {
			t.Fatalf("x %d: %v, want %.1f %.1f", x, c, want[0], want[1])
		}
	}
	if px[0] != rgba(1, 2, 3, 4) || px[1019] != rgba(1, 2, 3, 4) {
		t.Errorf("outside: %v %v", px[0], px[1019])
	}
	// Knots that do not begin at 0 or end at 1 hold the end colours.
	g.Knots = []float32{0.4, 0.5, 0.6}
	g.Extend = [2]bool{true, true}
	g.Set(0, 0, 1000, 0, Identity)
	if c := shade(&g, 0, 100, 1)[0]; c != rgba(0, 0, 0, 255) {
		t.Errorf("before the first knot: %v", c)
	}
	if c := shade(&g, 0, 900, 1)[0]; c != rgba(255, 255, 255, 255) {
		t.Errorf("past the last knot: %v", c)
	}
	// Premultiplied colours stay premultiplied.
	g.Ramp = Ramp{pack(255, 0, 0, 255), pack(0, 0, 0, 0), pack(0, 90, 90, 90)}
	g.Set(0, 0, 1000, 0, Identity)
	for _, c := range shade(&g, 0, 0, 1000) {
		if c.R > c.A || c.G > c.A || c.B > c.A {
			t.Fatalf("not premultiplied: %v", c)
		}
	}
}

// The bucket index finds the same interval as a linear search, for knots
// clustered, repeated, and crossing bucket boundaries.
func TestKnotsIndex(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for range 200 {
		n := 2 + rng.Intn(600)
		var g Gradient
		g.Ramp = make(Ramp, n)
		g.Knots = make([]float32, n)
		for i := range g.Ramp {
			g.Ramp[i] = pack(uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255)
			g.Knots[i] = float32(rng.Float64())
			switch rng.Intn(5) {
			case 0:
				g.Knots[i] = float32(rng.Intn(4)) / 4 // on bucket starts
			case 1:
				g.Knots[i] = 0.5
			}
		}
		sortFloat32(g.Knots)
		if !g.prepare() {
			t.Fatal("prepare")
		}
		for range 2000 {
			hint := rng.Intn(n)
			tt := rng.Float64()
			switch rng.Intn(4) {
			case 0:
				tt = float64(g.Knots[rng.Intn(n)])
			case 1:
				tt = float64(rng.Intn(257)) / knotBuckets
			}
			// The last knot at or before t, by a linear search.
			i := 0
			for j, k := range g.Knots {
				if float64(k) <= tt {
					i = j
				}
			}
			var want uint32
			k := g.kt
			switch {
			case tt <= k[i] || i+1 == n:
				want = g.Ramp[i]
			default:
				w := uint64((tt-k[i])/(k[i+1]-k[i])*256 + 0.5)
				a, b := expand(g.Ramp[i]), expand(g.Ramp[i+1])
				want = compact(((a*(256-w) + b*w + 0x0080008000800080) >> 8) & lanes)
			}
			if got, _ := g.knotColor(tt, hint); !nearColor(got, want, 1) {
				t.Fatalf("t %v: %v, want %v", tt, UnpackRGBA(got), UnpackRGBA(want))
			}
		}
		// The span loop gives what knotColor gives.
		ts := make([]float64, 300)
		for j := range ts {
			ts[j] = float64(j)/250 - 0.1 + rng.Float64()*0.01
		}
		got := make([]uint32, len(ts))
		g.Extend = [2]bool{true, true}
		g.knotColors(ts, got)
		for j, tt := range ts {
			want, _ := g.knotColor(min(max(tt, 0), 1), 0)
			if got[j] != want {
				t.Fatalf("knotColors at %v: %v, want %v", tt, UnpackRGBA(got[j]), UnpackRGBA(want))
			}
		}
	}
}

func sortFloat32(a []float32) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func nearColor(a, b uint32, tol int) bool {
	ca, cb := UnpackRGBA(a), UnpackRGBA(b)
	d := func(x, y uint8) bool { return int(x)-int(y) <= tol && int(y)-int(x) <= tol }
	return d(ca.R, cb.R) && d(ca.G, cb.G) && d(ca.B, cb.B) && d(ca.A, cb.A)
}

func TestKnotsRejected(t *testing.T) {
	r := Ramp{1, 2, 3}
	for _, k := range [][]float32{
		{0, 1},
		{0, 0.6, 0.5},
		{-0.1, 0.5, 1},
		{0, 0.5, 1.1},
		{0, float32(math.NaN()), 1},
	} {
		var lin LinearGradient
		lin.Ramp, lin.Alpha, lin.Knots = r, 255, k
		if lin.Set(0, 0, 1, 0, Identity) {
			t.Errorf("linear accepts %v", k)
		}
		var rad RadialGradient
		rad.Ramp, rad.Alpha, rad.Knots = r, 255, k
		if rad.Set(0, 0, 0, 0, 0, 1, Identity) {
			t.Errorf("radial accepts %v", k)
		}
		if c := shade(&rad, 0, 0, 1)[0]; c.A != 0 {
			t.Errorf("rejected knots paint %v", c)
		}
	}
	// Without knots again, the ramp is evenly spaced.
	var g LinearGradient
	g.Ramp, g.Alpha, g.Knots = grayRamp(256), 255, []float32{0, 1}
	g.Set(0, 0, 1, 0, Identity)
	g.Knots = nil
	if !g.Set(0, 0, 256, 0, Identity) || shade(&g, 0, 100, 1)[0].R != 100 {
		t.Error("knots kept after they were removed")
	}
}

func TestKnotsSpanIndependentNoAllocs(t *testing.T) {
	ramp := Ramp{pack(255, 0, 0, 255), pack(0, 255, 0, 255), pack(0, 0, 255, 255), pack(9, 9, 9, 9)}
	knots := []float32{0, 0.37, 0.37, 1}
	var lin LinearGradient
	lin.Ramp, lin.Alpha, lin.Extend, lin.Knots = ramp, 200, [2]bool{true, true}, knots
	lin.Set(0, 0, 31, 7, Identity)
	var rad RadialGradient
	rad.Ramp, rad.Alpha, rad.Extend, rad.Knots = ramp, 255, [2]bool{true, true}, knots
	rad.Set(3.3, 1.7, 0, 3.3, 1.7, 31, Identity)
	for name, s := range map[string]Shader{"linear": &lin, "radial": &rad} {
		one := make([]uint32, 1)
		span := make([]uint32, 140)
		for _, x0 := range []int{-100, -37, -3} {
			s.ShadeSpan(5, x0, span)
			for i := range span {
				s.ShadeSpan(5, x0+i, one)
				if one[0] != span[i] {
					t.Fatalf("%s: pixel %d differs alone", name, x0+i)
				}
			}
		}
	}
	span := make([]uint32, 64)
	allocs := testing.AllocsPerRun(10, func() {
		lin.Set(0, 0, 31, 7, Identity)
		lin.ShadeSpan(5, 0, span)
		rad.Set(3.3, 1.7, 0, 3.3, 1.7, 31, Identity)
	})
	if allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
}

func BenchmarkGradientKnots(b *testing.B) {
	ramp := make(Ramp, 64)
	knots := make([]float32, 64)
	for i := range ramp {
		v := uint8(i * 4)
		ramp[i] = pack(v, 255-v, v/2, 255)
		knots[i] = float32(math.Sqrt(float64(i) / 63))
	}
	for _, withKnots := range []bool{false, true} {
		b.Run(map[bool]string{false: "even", true: "knots"}[withKnots], func(b *testing.B) {
			var g LinearGradient
			g.Ramp, g.Alpha, g.Extend = ramp, 255, [2]bool{true, true}
			if withKnots {
				g.Knots = knots
			}
			g.Set(0, 0, 900, 300, Identity)
			dst := make([]uint32, 1024)
			b.SetBytes(4 * 1024)
			for b.Loop() {
				g.ShadeSpan(17, 0, dst)
			}
		})
	}
}
