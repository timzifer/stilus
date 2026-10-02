package stilus

import (
	"image"
	"math"
	"math/rand"
	"slices"
	"testing"
)

// The glyph cache composites masks directly under rectangle clips, away
// from fractional borders, and through a MaskShader fill under masked
// clips. A mask clip that covers the whole canvas takes the second path
// without changing coverage, so both must give the same bytes.
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
	// Images at the origin and off it, with and without a region limit
	// and a rectangle clip, whole-pixel or with fractional borders, some
	// of them cut off by the region.
	for _, tc := range []struct {
		img, region image.Rectangle
		clip        Rect
	}{
		{image.Rect(0, 0, 96, 64), image.Rect(0, 0, 96, 64), Rect{5, 3, 80, 60}},
		{image.Rect(-10, -10, 128, 96), image.Rect(-5, -5, 120, 90), Rect{-100, -100, 500, 500}},
		{image.Rect(-10, -10, 128, 96), image.Rect(-10, -10, 128, 96), Rect{3, 2, 96, 84}},
		{image.Rect(0, 0, 96, 64), image.Rect(0, 0, 96, 64), Rect{5.3, 3.7, 80.5, 59.2}},
		{image.Rect(0, 0, 96, 64), image.Rect(0, 0, 96, 64), Rect{5, 3.25, 80.75, 60}},
		{image.Rect(-10, -10, 128, 96), image.Rect(-5, -5, 120, 90), Rect{-20.5, 2.5, 110.5, 200.5}},
		{image.Rect(0, 0, 96, 64), image.Rect(0, 0, 96, 64), Rect{40.2, 30.6, 40.9, 31.1}},
	} {
		direct, shaded := image.NewRGBA(tc.img), image.NewRGBA(tc.img)
		for i := 0; i < len(direct.Pix); i += 4 { // premultiplied
			a := rng.Intn(256)
			px := []uint8{uint8(rng.Intn(a + 1)), uint8(rng.Intn(a + 1)), uint8(rng.Intn(a + 1)), uint8(a)}
			copy(direct.Pix[i:], px)
			copy(shaded.Pix[i:], px)
		}
		cd, cs := NewCanvas(direct), NewCanvas(shaded)
		cd.Reset(direct, tc.region)
		cs.Reset(shaded, tc.region)
		cd.ClipRect(tc.clip, Identity)
		cs.ClipRect(tc.clip, Identity)
		cs.ClipPath(&cover, Identity, NonZero)
		if cs.top().mask == nil {
			t.Fatal("cover clip took the rectangle path")
		}
		var gd, gs GlyphCache
		for range 400 {
			a := []uint8{0, 1, 64, 128, 255, 255, uint8(rng.Intn(256))}[rng.Intn(7)]
			paint := &Paint{Color: rgba(uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a)}
			s := 8 + 30*rng.Float64()
			m := Matrix{s, 0, 0, -s, rng.Float64()*150 - 30, rng.Float64()*110 - 10}
			gid := int32(rng.Intn(3))
			gd.FillGlyph(cd, 1, gid, &g, m, paint)
			gs.FillGlyph(cs, 1, gid, &g, m, paint)
		}
		if cd.Err() != nil || cs.Err() != nil {
			t.Fatal(cd.Err(), cs.Err())
		}
		for i := range direct.Pix {
			if direct.Pix[i] != shaded.Pix[i] {
				t.Fatalf("%v: pixel %d channel %d: %d vs %d", tc.img, i/4, i%4, direct.Pix[i], shaded.Pix[i])
			}
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

// Where u or v overflows, param's b is Inf·0 = NaN and the point is
// outside; the concentric path must not paint it as t = +Inf.
func TestConcentricRadialOverflow(t *testing.T) {
	var g RadialGradient
	g.Ramp, g.Alpha = grayRamp(256), 255
	g.Extend = [2]bool{true, true}
	g.Outside = pack(1, 2, 3, 4)
	if !g.Set(0, 0, 0, 0, 0, 1, Matrix{1e-300, 0, 0, 1e300, 0, 0}) || !g.concentric {
		t.Fatal("set")
	}
	fast, slow := make([]uint32, 8), make([]uint32, 8)
	for _, x := range []int{0, 1 << 20, -1 << 30} {
		g.ShadeSpan(5, x, fast)
		g.concentric = false
		g.ShadeSpan(5, x, slow)
		g.concentric = true
		if !slices.Equal(fast, slow) {
			t.Fatalf("x %d: %08x, want %08x", x, fast, slow)
		}
	}
}

// The direct glyph blit must match the rectangle fill, which with an edge
// budget of 1 leaves the rasterizer path and reports truncation.
func TestGlyphBlitEdgeBudget(t *testing.T) {
	g := Path{}
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.LineTo(0.3, 0.7)
	g.Close()
	r := image.Rect(0, 0, 32, 32)
	var out [2]*image.RGBA
	var errs [2]error
	for i := range out {
		out[i] = image.NewRGBA(r)
		c := NewCanvas(out[i])
		var gc GlyphCache
		paint := &Paint{Color: rgba(10, 20, 30, 255)}
		m := Matrix{20, 0, 0, -20, 4, 24}
		gc.FillGlyph(c, 1, 1, &g, m, paint) // fills the cache
		clear(out[i].Pix)
		c.r.MaxEdges = 1
		if i == 1 {
			var cover Path
			cover.MoveTo(-10, -10)
			cover.LineTo(100, -10)
			cover.LineTo(-10, 100)
			cover.Close()
			c.r.MaxEdges = 0
			c.ClipPath(&cover, Identity, NonZero)
			c.r.MaxEdges = 1
		}
		gc.FillGlyph(c, 1, 1, &g, m, paint)
		errs[i] = c.Err()
	}
	if !slices.Equal(out[0].Pix, out[1].Pix) || (errs[0] == nil) != (errs[1] == nil) {
		t.Errorf("direct %v, shaded %v", errs[0], errs[1])
	}
}
