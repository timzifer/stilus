package stilus

import (
	"image"
	"math"
	"math/rand"
	"testing"
)

// The glyph cache composites masks directly under whole-pixel rectangle
// clips and through a MaskShader fill under masked clips. A mask clip that
// covers the whole canvas takes the second path without changing coverage,
// so both must give the same bytes.
func TestGlyphBlitMatchesShaderPath(t *testing.T) {
	var g Path
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.CubicTo(0.9, 0.3, 0.7, 0.8, 0.3, 0.7)
	g.Close()
	var cover Path // a triangle around the canvas: an all-opaque mask
	cover.MoveTo(-10, -10)
	cover.LineTo(400, -10)
	cover.LineTo(-10, 400)
	cover.Close()
	rng := rand.New(rand.NewSource(1))
	r := image.Rect(0, 0, 96, 64)
	direct, shaded := image.NewRGBA(r), image.NewRGBA(r)
	for i := range direct.Pix {
		v := uint8(rng.Intn(256))
		direct.Pix[i], shaded.Pix[i] = v, v
	}
	for i := 0; i < len(direct.Pix); i += 4 { // keep premultiplied
		a := direct.Pix[i+3]
		for k := range 3 {
			v := min(direct.Pix[i+k], a)
			direct.Pix[i+k], shaded.Pix[i+k] = v, v
		}
	}
	cd, cs := NewCanvas(direct), NewCanvas(shaded)
	cd.ClipRect(Rect{5, 3, 80, 60}, Identity)
	cs.ClipRect(Rect{5, 3, 80, 60}, Identity)
	cs.ClipPath(&cover, Identity, NonZero)
	if cs.top().mask == nil {
		t.Fatal("cover clip took the rectangle path")
	}
	var gd, gs GlyphCache
	for range 400 {
		a := uint8(rng.Intn(256))
		if rng.Intn(3) == 0 {
			a = 255
		}
		paint := &Paint{Color: rgba(uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a)}
		s := 8 + 30*rng.Float64()
		m := Matrix{s, 0, 0, -s, rng.Float64()*100 - 10, rng.Float64()*70 + 5}
		gid := int32(rng.Intn(3))
		gd.FillGlyph(cd, 1, gid, &g, m, paint)
		gs.FillGlyph(cs, 1, gid, &g, m, paint)
	}
	for i := range direct.Pix {
		if direct.Pix[i] != shaded.Pix[i] {
			t.Fatalf("pixel %d channel %d: %d vs %d", i/4, i%4, direct.Pix[i], shaded.Pix[i])
		}
	}
}

// The concentric path must give param's colours byte for byte.
func TestConcentricRadialMatchesGeneral(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	// A ramp of distinct entries, fine enough that a difference in the
	// last bits of t changes the colour; Alpha 255 leaves them unscaled.
	ramp := make(Ramp, 1<<20)
	for i := range ramp {
		ramp[i] = uint32(i)
	}
	fast, slow := make([]uint32, 300), make([]uint32, 300)
	for n := range 2000 {
		var g RadialGradient
		g.Ramp, g.Alpha = ramp, 255
		g.Outside = 1 << 30
		g.Extend = [2]bool{rng.Intn(2) == 0, rng.Intn(2) == 0}
		m := Matrix{rng.NormFloat64() * 3, rng.NormFloat64(), rng.NormFloat64(), rng.NormFloat64() * 3, rng.NormFloat64() * 100, rng.NormFloat64() * 100}
		if n%3 == 0 {
			m = Translate(rng.Float64()*200, rng.Float64()*200)
		}
		cx, cy := rng.NormFloat64()*50, rng.NormFloat64()*50
		r1 := math.Exp(rng.NormFloat64() * 4)
		if n == 0 {
			r1 = 1e-200 // r1² underflows: no circle, general path
		}
		if !g.Set(cx, cy, 0, cx, cy, r1, m) {
			continue
		}
		if !g.concentric && n != 0 {
			t.Fatalf("not concentric: r1 %v", r1)
		}
		for k := 0; g.concentric && k < 200; k++ {
			u := rng.NormFloat64() * r1 * math.Exp(rng.NormFloat64())
			v := rng.NormFloat64() * r1 * math.Exp(rng.NormFloat64())
			want, got := g.param(u, v), g.concentricParam(u, v)
			if math.IsNaN(want) {
				want = got // t > 1, not extended: color paints Outside
				if !(got > 1 && !g.Extend[1]) {
					t.Fatalf("param NaN at t %v", got)
				}
			}
			if got != want {
				t.Fatalf("r1 %v (%v, %v): t %v, want %v", r1, u, v, got, want)
			}
		}
		y, x := rng.Intn(400)-200, rng.Intn(400)-200
		g.ShadeSpan(y, x, fast)
		g.concentric = false
		g.ShadeSpan(y, x, slow)
		for i := range fast {
			if fast[i] != slow[i] {
				t.Fatalf("case %d pixel %d: %08x vs %08x", n, i, fast[i], slow[i])
			}
		}
	}
}
