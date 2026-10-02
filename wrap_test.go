package stilus

import (
	"image"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// wrapRef samples s's plane at pixel (x, y) the slow way, in floating
// point: coordinates from the pixel's position, floored and reduced with
// math.Floor and %. It also reports whether a coordinate lies so close to
// a texel's edge (or, bilinearly, to a step of the weight) that fixed and
// floating point may round it differently.
func wrapRef(s *Sampler, x, y int) (c uint32, edge bool) {
	p, m := s.p, &s.m
	fx, fy := float64(x)+0.5, float64(y)+0.5
	u := m[0]*fx + m[2]*fy + m[4]
	v := m[1]*fx + m[3]*fy + m[5]
	if s.bilinear {
		u, v = u-0.5, v-0.5
	}
	near := func(f float64) bool {
		if s.bilinear {
			f *= 256
		}
		g := f - math.Floor(f)
		return g < 1e-6 || g > 1-1e-6
	}
	edge = near(u) || near(v)
	mod := func(f float64, n int) int { return wrapIndex(int(math.Floor(f)), n) }
	x0, y0 := mod(u, p.W), mod(v, p.H)
	if !s.bilinear {
		return p.At(x0, y0), edge
	}
	x1, y1 := (x0+1)%p.W, (y0+1)%p.H
	tx := uint32((u - math.Floor(u)) * 256)
	ty := uint32((v - math.Floor(v)) * 256)
	if m[1] == 0 {
		// Axis-aligned spans mix rows first.
		return lerp(lerp(p.At(x0, y0), p.At(x0, y1), ty), lerp(p.At(x1, y0), p.At(x1, y1), ty), tx), edge
	}
	a := lerp(p.At(x0, y0), p.At(x1, y0), tx)
	b := lerp(p.At(x0, y1), p.At(x1, y1), tx)
	return lerp(a, b, ty), edge
}

func wrapPlanes(rng *rand.Rand) []Plane {
	rgba := Plane{Kind: PlaneRGBA, W: 7, H: 5, Stride: 9, Pix32: make([]uint32, 45)}
	for i := range rgba.Pix32 {
		rgba.Pix32[i] = rng.Uint32() | 0xff<<24 // any colour; lerp needs none premultiplied
	}
	index := Plane{Kind: PlaneIndex, W: 6, H: 3, Stride: 6, Pix8: make([]uint8, 18), Pal: AlphaPalette}
	for i := range index.Pix8 {
		index.Pix8[i] = uint8(rng.IntN(256))
	}
	bits := Plane{Kind: PlaneBits, W: 11, H: 4, Stride: 2, Pix8: make([]uint8, 8), Pal: GrayPalette}
	for i := range bits.Pix8 {
		bits.Pix8[i] = uint8(rng.IntN(256))
	}
	return []Plane{rgba, index, bits}
}

func TestSampleWrapMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	mats := []Matrix{
		Translate(3, -2),     // unit copy
		Translate(-17, 40),   // unit copy, far before the tile
		Translate(2.5, 0.25), // nearest, axis-aligned
		Scale(3, 2).Mul(Translate(-1, 0)),
		Scale(-1.5, 1),                    // mirrored
		Rotate(0.5).Mul(Scale(2, 2)),      // rotated
		Rotate(-1.1).Mul(Scale(0.3, 0.4)), // rotated and minified
		{1, 0.3, 0.2, 1, 4, 5},            // skewed
	}
	got := make([]uint32, 64)
	var one [1]uint32
	checked := 0
	defer func() {
		if checked < 10000 {
			t.Errorf("only %d pixels away from texel edges", checked)
		}
	}()
	for _, p := range wrapPlanes(rng) {
		tex := NewTexture(p)
		for mi, m := range mats {
			for _, smooth := range []bool{false, true} {
				var s Sampler
				if !s.SetupWrap(tex, m, smooth) {
					t.Fatalf("matrix %d not invertible", mi)
				}
				for y := -9; y < 20; y += 2 {
					for x := -40; x < 40; x += 13 {
						n := 1 + (x+40)%len(got)
						s.Sample(y, x, got[:n])
						for i, c := range got[:n] {
							// A pixel samples the same wherever its span starts.
							s.Sample(y, x+i, one[:])
							if one[0] != c {
								t.Fatalf("kind %d matrix %d smooth %v (%d, %d)+%d: %x in the span, %x alone",
									p.Kind, mi, smooth, x, y, i, c, one[0])
							}
							want, edge := wrapRef(&s, x+i, y)
							if edge {
								continue
							}
							if c != want {
								t.Fatalf("kind %d matrix %d smooth %v (%d, %d)+%d: %x, want %x",
									p.Kind, mi, smooth, x, y, i, c, want)
							}
							checked++
						}
					}
				}
			}
		}
	}
}

func TestFixedAxis(t *testing.T) {
	for _, c := range []struct {
		d float64
		n int
	}{{1, 7}, {-1, 7}, {0.3, 5}, {-2.75, 3}, {1e9 + 0.1, 11}, {0, 4}, {-1e-12, 4}} {
		a := newFixedAxis(c.d, c.n)
		if a.step >= a.period {
			t.Fatalf("d %v n %d: step %x outside the period", c.d, c.n, a.step)
		}
		for _, f := range []float64{0, 0.5, -3.25, 1e7, -1e12, 6.999999} {
			// at(f, x) is at(f, x0) advanced by x-x0 steps, from any x0.
			for _, x0 := range []int{-1000, -1, 0, 3} {
				u := a.at(f, x0)
				for x := x0; x < x0+50; x++ {
					if got := a.at(f, x); got != u {
						t.Fatalf("d %v n %d f %v: at x %d = %x, stepped from %d %x", c.d, c.n, f, x, got, x0, u)
					}
					if u += a.step; u >= a.period {
						u -= a.period
					}
				}
			}
		}
	}
	a := newFixedAxis(1, 6)
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if u := a.at(f, 0); u != 0 {
			t.Errorf("at(%v) = %x", f, u)
		}
	}
	if u := a.at(-0.25, 0); u != 6<<32-1<<30 {
		t.Errorf("at(-0.25) = %x", u)
	}
}

func TestSampleUnitWrap(t *testing.T) {
	for _, w := range []int{1, 2, 3, 8} {
		row := make([]uint32, w)
		for i := range row {
			row[i] = uint32(i + 1)
		}
		for _, n := range []int{0, 1, 2, 5, 17, 100} {
			dst := make([]uint32, n)
			for off := -12; off < 12; off++ {
				sampleUnitWrap(dst, row, off)
				for i, c := range dst {
					if want := row[wrapIndex(off+i, w)]; c != want {
						t.Fatalf("w %d n %d off %d [%d] = %d, want %d", w, n, off, i, c, want)
					}
				}
			}
		}
	}
}

func TestWrapLevels(t *testing.T) {
	// Sizes that divide share Level's planes.
	p8 := Plane{Kind: PlaneRGBA, W: 8, H: 4, Stride: 8, Pix32: make([]uint32, 32)}
	t8 := NewTexture(p8)
	if t8.wrapLevel(2) != t8.Level(2) || t8.wrapLevel(0) != t8.Base() {
		t.Error("dividing sizes make separate wrap levels")
	}
	// A row of 6 at level 2 is two blocks of three, not 4 + 2.
	p := Plane{Kind: PlaneIndex, W: 6, H: 1, Stride: 6, Pix8: []uint8{0, 30, 60, 90, 120, 150}, Pal: GrayPalette}
	tex := NewTexture(p)
	l := tex.wrapLevel(2)
	if l.W != 2 || l.H != 1 || l.Pix8[0] != 30 || l.Pix8[1] != 120 {
		t.Errorf("level 2: %d × %d %v", l.W, l.H, l.Pix8)
	}
	if tex.wrapLevel(2) != l {
		t.Error("wrap levels are not kept")
	}
	// A periodic texture of one colour stays that colour at every level:
	// no partial block at the edge is averaged with anything else.
	q := Plane{Kind: PlaneRGBA, W: 13, H: 7, Stride: 13, Pix32: make([]uint32, 91)}
	c := pack(10, 20, 30, 255)
	for i := range q.Pix32 {
		q.Pix32[i] = c
	}
	qt := NewTexture(q)
	for k := 1; k <= qt.Levels(); k++ {
		l := qt.wrapLevel(k)
		if l.W != wrapSize(13, k) || l.H != wrapSize(7, k) {
			t.Errorf("level %d is %d × %d", k, l.W, l.H)
		}
		for _, v := range l.Pix32 {
			if v != c {
				t.Fatalf("level %d: %x, want %x", k, v, c)
			}
		}
	}
	// Minified, the wrap sampler reads a wrap level.
	var s Sampler
	s.SetupWrap(tex, Scale(0.2, 0.2), false)
	if s.p != l || !s.bilinear {
		t.Error("minified wrap sampler does not read the wrap level")
	}
}

// TestWrapShaderBandsAndRows draws a repeating tile with and without the
// direct row path, as a whole and in bands: all must agree with each
// other and with the tile.
func TestWrapShaderBandsAndRows(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	tile := Plane{Kind: PlaneRGBA, W: 5, H: 3, Stride: 5, Pix32: make([]uint32, 15)}
	for i := range tile.Pix32 {
		tile.Pix32[i] = PackRGBA(rgba(uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255))
	}
	tex := NewTexture(tile)
	r := image.Rect(0, 0, 61, 23)
	var path Path
	path.Rect(1, 2, 57, 19)
	for _, m := range []Matrix{Translate(-3, 7), Translate(2, 1), Rotate(0.3)} {
		var s ImageShader
		if !s.SetImageWrap(tex, m, false, 255) {
			t.Fatal("setup failed")
		}
		draw := func(sh Shader, band int) *image.RGBA {
			dst := image.NewRGBA(r)
			c := NewCanvas(dst)
			for y := 0; y < r.Dy(); y += band {
				c.Reset(dst, image.Rect(0, y, r.Dx(), y+band))
				c.Fill(&path, Identity, NonZero, &Paint{Shader: sh})
			}
			return dst
		}
		ref := draw(struct{ Shader }{&s}, r.Dy())
		for _, band := range []int{1, 4, 7} {
			if !slices.Equal(draw(&s, band).Pix, ref.Pix) {
				t.Fatalf("matrix %v band %d: direct rows differ from shaded spans", m, band)
			}
		}
		if m[1] == 0 {
			inv, _ := m.Invert()
			for y := 3; y < 18; y++ {
				for x := 2; x < 56; x++ {
					u, v := inv.Apply(float64(x)+0.5, float64(y)+0.5)
					want := UnpackRGBA(tile.At(wrapIndex(int(math.Floor(u)), 5), wrapIndex(int(math.Floor(v)), 3)))
					if got := ref.RGBAAt(x, y); got != want {
						t.Fatalf("matrix %v (%d, %d) = %v, want %v", m, x, y, got, want)
					}
				}
			}
		}
	}
}

func TestWrapStencilNoAllocs(t *testing.T) {
	tile := Plane{Kind: PlaneIndex, W: 8, H: 8, Stride: 8, Pix8: make([]uint8, 64), Pal: AlphaPalette}
	for i := range 8 {
		tile.Pix8[i*8+i] = 255
	}
	tex := NewTexture(tile)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 100))
	c := NewCanvas(dst)
	var s ImageShader
	s.SetColor(PackRGBA(rgba(0, 0, 0, 255)))
	var p Path
	p.Rect(3, 3, 190, 90)
	paint := &Paint{Shader: &s}
	draw := func() {
		s.SetMaskWrap(tex, Rotate(0.4).Mul(Scale(0.7, 0.7)), true)
		c.Fill(&p, Identity, NonZero, paint)
	}
	draw()
	if n := testing.AllocsPerRun(20, draw); n != 0 {
		t.Errorf("%v allocations per fill", n)
	}
	// The diagonal hatch covers some pixels and leaves others.
	var on, off int
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] > 128 {
			on++
		} else if dst.Pix[i] == 0 {
			off++
		}
	}
	if on == 0 || off == 0 {
		t.Errorf("hatch: %d pixels on, %d off", on, off)
	}
}

// BenchmarkWrapPattern fills a page-sized area with pattern tiles the way
// cera draws tiling patterns. The edge-repeat cases sample the same tile
// with Setup, as the cost wrapping is measured against.
func BenchmarkWrapPattern(b *testing.B) {
	hatch := Plane{Kind: PlaneIndex, W: 8, H: 8, Stride: 8, Pix8: make([]uint8, 64), Pal: AlphaPalette}
	for i := range 8 {
		hatch.Pix8[i*8+i] = 255
	}
	htex := NewTexture(hatch)
	rng := rand.New(rand.NewPCG(15, 16))
	tile := Plane{Kind: PlaneRGBA, W: 24, H: 24, Stride: 24, Pix32: make([]uint32, 24*24)}
	for i := range tile.Pix32 {
		tile.Pix32[i] = PackRGBA(rgba(uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255))
	}
	ttex := NewTexture(tile)
	r := image.Rect(0, 0, 512, 512)
	var full Path
	full.Rect(0, 0, 512, 512)
	rot := Rotate(0.5)
	for _, c := range []struct {
		name string
		set  func(*ImageShader)
	}{
		{"hatch-axis", func(s *ImageShader) { s.SetColor(0xff000000); s.SetMaskWrap(htex, Scale(1.5, 1.5), false) }},
		{"hatch-rotated", func(s *ImageShader) { s.SetColor(0xff000000); s.SetMaskWrap(htex, rot, false) }},
		{"hatch-rotated-smooth", func(s *ImageShader) { s.SetColor(0xff000000); s.SetMaskWrap(htex, rot, true) }},
		{"hatch-rotated-edge", func(s *ImageShader) { s.SetColor(0xff000000); s.SetMask(htex, rot, false) }},
		{"tile-unit", func(s *ImageShader) { s.SetImageWrap(ttex, Translate(5, 3), false, 255) }},
		{"tile-scaled-smooth", func(s *ImageShader) { s.SetImageWrap(ttex, Scale(2.5, 2.5), true, 255) }},
		{"tile-rotated", func(s *ImageShader) { s.SetImageWrap(ttex, rot, false, 255) }},
		{"tile-rotated-edge", func(s *ImageShader) { s.SetImage(ttex, rot, false, 255) }},
	} {
		b.Run(c.name, func(b *testing.B) {
			dst := image.NewRGBA(r)
			cv := NewCanvas(dst)
			var s ImageShader
			c.set(&s)
			paint := &Paint{Shader: &s}
			b.SetBytes(int64(4 * r.Dx() * r.Dy()))
			b.ReportAllocs()
			for b.Loop() {
				cv.Fill(&full, Identity, NonZero, paint)
			}
		})
	}
}

// TestStencilTable checks that a solid colour through a mask, looked up in
// the shader's table, is the colour times the mask's level.
func TestStencilTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 18))
	mask := Plane{Kind: PlaneIndex, W: 16, H: 16, Stride: 16, Pix8: make([]uint8, 256), Pal: AlphaPalette}
	for i := range mask.Pix8 {
		mask.Pix8[i] = uint8(i)
	}
	tex := NewTexture(mask)
	var s ImageShader
	got, m := make([]uint32, 300), make([]uint32, 300)
	for range 20 {
		c := PackRGBA(rgba(uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255))
		c = mul255(c, uint32(rng.IntN(256)))
		s.SetColor(c)
		s.SetMaskWrap(tex, Rotate(rng.Float64()), true)
		for y := range 10 {
			s.ShadeSpan(y, -7, got)
			s.mask.Sample(y, -7, m)
			for i, a := range m {
				if want := mul255(c, a&0xff); got[i] != want {
					t.Fatalf("colour %x level %d: %x, want %x", c, a&0xff, got[i], want)
				}
			}
		}
	}
}
