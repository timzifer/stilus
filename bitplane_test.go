package stilus

import (
	"image"
	"math/rand/v2"
	"slices"
	"testing"
)

// nearestRef is Sampler.Sample for nearest sampling as it was before the
// column and row caches: every pixel mapped through floats on its own.
func nearestRef(s *Sampler, y, x int, dst []uint32) {
	p, m := s.p, &s.m
	fy := float64(y) + 0.5
	u0 := m[2]*fy + m[4] + m[0]*0.5
	v0 := m[3]*fy + m[5] + m[1]*0.5
	for i := range dst {
		fx := float64(x + i)
		dst[i] = p.At(clampIndex(u0+m[0]*fx, p.W), clampIndex(v0+m[1]*fx, p.H))
	}
}

func randomPlanes(rng *rand.Rand) []Plane {
	pal := new(Palette)
	for i := range pal {
		pal[i] = pack(uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255)
	}
	rgba := Plane{Kind: PlaneRGBA, W: 19, H: 11, Stride: 21, Pix32: make([]uint32, 21*11)}
	for i := range rgba.Pix32 {
		rgba.Pix32[i] = rng.Uint32() | 0xff<<alphaShift
	}
	index := Plane{Kind: PlaneIndex, W: 17, H: 13, Stride: 17, Pix8: make([]uint8, 17*13), Pal: pal}
	bits := Plane{Kind: PlaneBits, W: 45, H: 9, Stride: 7, Pix8: make([]uint8, 7*9), Pal: pal}
	for _, p := range []*Plane{&index, &bits} {
		for i := range p.Pix8 {
			p.Pix8[i] = uint8(rng.IntN(256))
			if p.Kind == PlaneBits && rng.IntN(3) == 0 {
				p.Pix8[i] = uint8(rng.IntN(2)) * 0xff // whole bytes of one colour
			}
		}
	}
	return []Plane{rgba, index, bits}
}

// TestNearestCachesMatchPerPixel checks that the cached columns and rows
// of nearest axis-aligned sampling, and the unit path of one-bit planes,
// pick exactly the texels each pixel picks on its own, whatever the order
// and extent of the spans.
func TestNearestCachesMatchPerPixel(t *testing.T) {
	rng := rand.New(rand.NewPCG(34, 35))
	mats := []Matrix{
		Translate(3, 5),
		Translate(-13, 2),
		Translate(0.5, 0.25),
		Scale(2.1, 2.1).Mul(Translate(1.3, -0.7)),
		Scale(8.3, 8.3),
		Scale(7.9, 7.9).Mul(Translate(-2.6, 0.1)),
		Scale(1/0.3, 1/0.7).Mul(Translate(4.5, 1)),
		Scale(-3.3, 2).Mul(Translate(-20, 0)),
		Scale(2.5, -4).Mul(Translate(0, -10)),
		Scale(3, 0.8),  // rows do not repeat
		Scale(0.95, 5), // columns reduced, rows magnified
		Scale(1, 1).Mul(Translate(7, 0)),
		Scale(2, 2).Mul(Rotate(0.3)), // not cached
		{2, 0, 0.7, 2, 1, 1},         // sheared: u depends on y
	}
	got, want := make([]uint32, 300), make([]uint32, 300)
	for _, p := range randomPlanes(rng) {
		tex := NewTexture(p)
		for _, m := range mats {
			var s Sampler
			if !s.Setup(tex, m, false) {
				t.Fatalf("%v: not drawable", m)
			}
			if s.bilinear {
				continue
			}
			for range 400 {
				y := rng.IntN(140) - 30
				x := rng.IntN(260) - 60
				n := 1 + rng.IntN(len(got)-1)
				if rng.IntN(3) == 0 {
					n = 1 + rng.IntN(4)
				}
				s.Sample(y, x, got[:n])
				nearestRef(&s, y, x, want[:n])
				if !slices.Equal(got[:n], want[:n]) {
					i := 0
					for got[i] == want[i] {
						i++
					}
					t.Fatalf("kind %d %v (%d, %d)+%d: %x, want %x", p.Kind, m, x, y, i, got[i], want[i])
				}
			}
		}
	}
}

// TestSampleUnitBits checks the unit path of one-bit planes at every
// offset against the bits read one by one.
func TestSampleUnitBits(t *testing.T) {
	rng := rand.New(rand.NewPCG(36, 37))
	p := randomPlanes(rng)[2]
	row := p.Pix8[2*p.Stride:][:(p.W+7)/8]
	got, want := make([]uint32, 80), make([]uint32, 80)
	for off := -90; off < 90; off++ {
		for _, n := range []int{0, 1, 7, 8, 9, 17, 80} {
			sampleUnitBits(got[:n], row, p.W, p.Pal, off)
			for i := range want[:n] {
				k := min(max(off+i, 0), p.W-1)
				want[i] = p.Pal[row[k>>3]>>(7-uint(k)&7)&1]
			}
			if !slices.Equal(got[:n], want[:n]) {
				t.Fatalf("off %d n %d: %x, want %x", off, n, got[:n], want[:n])
			}
		}
	}
}

// TestMagnifiedBitsDrawn draws magnified one-bit images, opaque and at
// constant alpha, through shapes with antialiased edges and a clip, and
// compares them with the same shader hidden behind an interface (no
// expanded rows read directly, no shading into the destination).
func TestMagnifiedBitsDrawn(t *testing.T) {
	rng := rand.New(rand.NewPCG(38, 39))
	bits := randomPlanes(rng)[2]
	tex := NewTexture(bits)
	r := image.Rect(0, 0, 160, 96)
	var ring Path
	ring.Ellipse(80, 48, 70, 40)
	ring.Ellipse(80, 48, 20, 12)
	var shapes [2]Path
	shapes[0].Rect(0, 0, 160, 96)
	shapes[1].Rect(3.5, 2.25, 150.5, 80.6)
	for _, m := range []Matrix{Scale(3.3, 8.3).Mul(Translate(1.5, 0.2)), Scale(-2.1, 2.1).Mul(Translate(-70, 3))} {
		for _, alpha := range []uint8{255, 140} {
			for si := range shapes {
				for _, masked := range []bool{false, true} {
					backdrop := randomRGBA(rng, r)
					draw := func(direct bool) *image.RGBA {
						dst := image.NewRGBA(r)
						copy(dst.Pix, backdrop.Pix)
						var is ImageShader
						if !is.SetImage(tex, m, false, alpha) || !is.col.rows {
							t.Fatalf("%v: not sampled through expanded rows", m)
						}
						var sh Shader = &is
						if !direct {
							sh = struct{ Shader }{sh}
						}
						c := NewCanvas(dst)
						if masked {
							c.ClipPath(&ring, Identity, EvenOdd)
						}
						c.Fill(&shapes[si], Identity, NonZero, &Paint{Shader: sh})
						return dst
					}
					if !slices.Equal(draw(true).Pix, draw(false).Pix) {
						t.Fatalf("%v alpha %d shape %d mask %v: direct differs from shaded", m, alpha, si, masked)
					}
				}
			}
		}
	}
}

func TestMagnifiedBitsDoNotAllocate(t *testing.T) {
	rng := rand.New(rand.NewPCG(40, 41))
	tex := NewTexture(randomPlanes(rng)[2])
	dst := image.NewRGBA(image.Rect(0, 0, 200, 100))
	c := NewCanvas(dst)
	var p Path
	p.Rect(0, 0, 200, 100)
	var is ImageShader
	paint := &Paint{Shader: &is}
	fill := func() {
		is.Reset()
		is.SetImage(tex, Scale(4.4, 11.1), false, 255)
		c.Fill(&p, Identity, NonZero, paint)
	}
	fill()
	if a := testing.AllocsPerRun(10, fill); a != 0 {
		t.Errorf("%v allocations per fill", a)
	}
}

// BenchmarkMagnifiedBits draws one-bit planes magnified as the scan pages
// of cera's corpus draw them.
func BenchmarkMagnifiedBits(b *testing.B) {
	for _, bc := range []struct {
		name string
		w, h int
		dw   float64
		dh   float64
	}{
		{"2.1x", 399, 400, 831, 833},
		{"8.3x", 81, 26, 675, 217},
		{"7.9x", 132, 14, 1042, 110},
		{"1x", 800, 600, 800, 600},
	} {
		b.Run(bc.name, func(b *testing.B) {
			rng := rand.New(rand.NewPCG(1, 2))
			pal := &Palette{pack(255, 255, 255, 255), pack(0, 0, 0, 255)}
			stride := (bc.w + 7) / 8
			p := Plane{Kind: PlaneBits, W: bc.w, H: bc.h, Stride: stride, Pix8: make([]uint8, stride*bc.h), Pal: pal}
			for i := range p.Pix8 {
				p.Pix8[i] = uint8(rng.IntN(256))
			}
			tex := NewTexture(p)
			dst := image.NewRGBA(image.Rect(0, 0, int(bc.dw)+2, int(bc.dh)+2))
			c := NewCanvas(dst)
			var path Path
			path.Rect(0.5, 0.5, float32(bc.dw+0.5), float32(bc.dh+0.5))
			m := Scale(bc.dw/float64(bc.w), bc.dh/float64(bc.h)).Mul(Translate(0.5, 0.5))
			var is ImageShader
			paint := &Paint{Shader: &is}
			b.SetBytes(int64(4 * bc.dw * bc.dh))
			for b.Loop() {
				is.Reset()
				is.SetImage(tex, m, false, 255)
				c.Fill(&path, Identity, NonZero, paint)
			}
		})
	}
}
