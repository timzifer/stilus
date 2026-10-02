package stilus

import (
	"image"
	"math/rand/v2"
	"testing"
)

// randomRGBA fills a premultiplied image with random colours, a share of
// them transparent or opaque.
func randomRGBA(rng *rand.Rand, r image.Rectangle) *image.RGBA {
	m := image.NewRGBA(r)
	for i := 0; i < len(m.Pix); i += 4 {
		a := uint8(rng.IntN(256))
		switch rng.IntN(4) {
		case 0:
			a = 0
		case 1:
			a = 255
		}
		for c := range 3 {
			m.Pix[i+c] = uint8(rng.IntN(int(a) + 1))
		}
		m.Pix[i+3] = a
	}
	return m
}

// refLayerPixel is LayerShader on one pixel without its fast paths: blend
// modes in floating point for all but opaque pixels.
func refLayerPixel(bm BlendMode, k uint32, s, d [4]uint8) uint32 {
	r, g, b, a := s[0], s[1], s[2], s[3]
	if k != 255 {
		r, g, b, a = mulByte(r, k), mulByte(g, k), mulByte(b, k), mulByte(a, k)
	}
	if a == 0 {
		return 0
	}
	if bm != BlendNormal && d[3] != 0 {
		if a == 255 && d[3] == 255 {
			r, g, b = blendPixel(bm, r, g, b, a, d[0], d[1], d[2], d[3])
		} else {
			r, g, b = refBlendFloat(bm, r, g, b, a, d[0], d[1], d[2], d[3])
		}
	}
	return pack(r, g, b, a)
}

func refBlendFloat(bm BlendMode, sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b uint8) {
	as, ab := float64(sa)/255, float64(da)/255
	cs := [3]float64{float64(sr) / float64(sa), float64(sg) / float64(sa), float64(sb) / float64(sa)}
	cb := [3]float64{float64(dr) / float64(da), float64(dg) / float64(da), float64(db) / float64(da)}
	for i := range 3 {
		cs[i], cb[i] = min(cs[i], 1), min(cb[i], 1)
	}
	bl := Blend(bm, cb, cs)
	var o [3]uint8
	src := [3]uint8{sr, sg, sb}
	for i := range 3 {
		v := float64(src[i])*(1-ab) + as*ab*bl[i]*255
		o[i] = uint8(min(max(v+0.5, 0), float64(sa)))
	}
	return o[0], o[1], o[2]
}

func TestLayerShaderFastPaths(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	r := image.Rect(3, 5, 3+67, 5+9)
	src, dst := randomRGBA(rng, r), randomRGBA(rng, r)
	mask := image.NewAlpha(image.Rect(10, 6, 50, 12))
	for i := range mask.Pix {
		mask.Pix[i] = uint8(rng.IntN(256))
	}
	// Colours above their alpha take the general path.
	src.Pix[1], src.Pix[3], dst.Pix[0], dst.Pix[3] = 200, 100, 90, 60
	out := make([]uint32, r.Dx())
	for _, bm := range []BlendMode{BlendNormal, BlendMultiply, BlendScreen, BlendOverlay} {
		for _, m := range []*image.Alpha{nil, mask} {
			for alpha := range 256 {
				ls := &LayerShader{Src: src, Dst: dst, Mask: m, Alpha: uint8(alpha), Blend: bm}
				for y := r.Min.Y; y < r.Max.Y; y++ {
					ls.ShadeSpan(y, r.Min.X, out)
					for i, got := range out {
						x := r.Min.X + i
						k := uint32(alpha)
						if m != nil {
							k = div255(k * uint32(m.AlphaAt(x, y).A))
						}
						o := src.PixOffset(x, y)
						if bm == BlendNormal && o == 0 {
							continue // Normal copies colours above their alpha
						}
						want := refLayerPixel(bm, k, [4]uint8(src.Pix[o:o+4]), [4]uint8(dst.Pix[o:o+4]))
						if got != want {
							t.Fatalf("%v alpha %d mask %v (%d, %d): %v, want %v",
								bm, alpha, m != nil, x, y, UnpackRGBA(got), UnpackRGBA(want))
						}
					}
				}
			}
		}
	}
}

// alphaSteps samples the alphas, including the extremes.
var alphaSteps = []int{1, 2, 3, 17, 64, 127, 128, 129, 200, 254, 255}

// TestBlendIntegerExhaustive checks the integer Multiply and Screen against
// floating point for all premultiplied channel values at sampled alphas.
func TestBlendIntegerExhaustive(t *testing.T) {
	for _, bm := range []BlendMode{BlendMultiply, BlendScreen} {
		for _, sa := range alphaSteps {
			for _, da := range alphaSteps {
				for s := 0; s <= sa; s++ {
					for d := 0; d <= da; d++ {
						r := multiplyInt(uint8(s), uint8(d), uint8(da))
						if bm == BlendScreen {
							r = screenInt(uint8(s), uint8(sa), uint8(d))
						}
						w, _, _ := refBlendFloat(bm, uint8(s), 0, 0, uint8(sa), uint8(d), 0, 0, uint8(da))
						if r != w {
							t.Fatalf("%v s %d/%d d %d/%d: %d, want %d", bm, s, sa, d, da, r, w)
						}
					}
				}
			}
		}
	}
}

// TestBlendIntegerOpaqueTable checks that opaque pixels, which
// shadeSeparable computes with the integer formulas, get the bytes of
// blendTable that blendPixel looks them up in.
func TestBlendIntegerOpaqueTable(t *testing.T) {
	for s := range 256 {
		for d := range 256 {
			m := multiplyInt(uint8(s), uint8(d), 255)
			if w := blendTable(BlendMultiply)[d<<8|s]; m != w {
				t.Fatalf("Multiply s %d d %d: %d, want %d", s, d, m, w)
			}
			sc := screenInt(uint8(s), 255, uint8(d))
			if w := blendTable(BlendScreen)[d<<8|s]; sc != w {
				t.Fatalf("Screen s %d d %d: %d, want %d", s, d, sc, w)
			}
		}
	}
}

func TestMipIndexMatchesRGBA(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	var gray, alpha Palette
	for i := range 256 {
		v, a := uint8(rng.IntN(256)), uint8(rng.IntN(256))
		gray[i], alpha[i] = pack(v, v, v, 255), pack(a, a, a, a)
	}
	for _, pal := range []*Palette{GrayPalette, AlphaPalette, &gray, &alpha} {
		for _, sz := range [][2]int{{1, 1}, {7, 5}, {33, 17}, {64, 64}, {129, 3}} {
			w, h := sz[0], sz[1]
			p := Plane{Kind: PlaneIndex, W: w, H: h, Stride: w + 3, Pix8: make([]uint8, (w+3)*h)}
			p.Pal = pal
			for i := range p.Pix8 {
				p.Pix8[i] = uint8(rng.IntN(256))
			}
			tex := NewTexture(p)
			if !tex.gray && !tex.alpha {
				t.Fatal("palette not recognised")
			}
			ref := &Texture{base: p} // four channels
			for k := 1; k <= tex.Levels(); k++ {
				got, want := tex.Level(k), ref.Level(k)
				for i, v := range got.Pix8 {
					r, _, _, a := unpack(want.Pix32[i])
					if (tex.gray && v != r) || (!tex.gray && v != a) {
						t.Fatalf("%d × %d level %d pixel %d: %d, want %v", w, h, k, i, v, UnpackRGBA(want.Pix32[i]))
					}
				}
			}
		}
	}
}

func TestSampleUnitMatchesNearest(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	p := Plane{Kind: PlaneRGBA, W: 13, H: 4, Stride: 15, Pix32: make([]uint32, 60)}
	for i := range p.Pix32 {
		p.Pix32[i] = rng.Uint32()
	}
	tex := NewTexture(p)
	got, want := make([]uint32, 40), make([]uint32, 40)
	for tx := -30; tx <= 30; tx++ {
		var s Sampler
		if !s.Setup(tex, Translate(float64(tx), 2), false) || !s.unit {
			t.Fatalf("translation by %d not a unit sampler", tx)
		}
		for x := -20; x <= 20; x += 3 {
			for y := -1; y < 8; y++ {
				s.Sample(y, x, got)
				s.unit = false
				s.Sample(y, x, want)
				s.unit = true
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("tx %d (%d, %d)+%d: %x, want %x", tx, x, y, i, got[i], want[i])
					}
				}
			}
		}
	}
	var s Sampler
	for _, m := range []Matrix{Translate(0.5, 0), Scale(2, 2), Translate(3, 0).Mul(Scale(-1, 1))} {
		if s.Setup(tex, m, false); s.unit {
			t.Errorf("%v taken as a unit translation", m)
		}
	}
	if s.Setup(tex, Translate(1, 1), true); s.unit {
		t.Error("smooth sampling taken as a copy")
	}
}

// TestBilinearSpanIndependent checks that an axis-aligned bilinear span
// gives every pixel the colour it gets sampled alone: the texel pair a
// span reuses changes from (0, 0) to (0, 1) at the left clamp although
// its first texel stays the same.
func TestBilinearSpanIndependent(t *testing.T) {
	pal := &Palette{0: 0xff000000, 1: 0xffffffff}
	planes := map[string]Plane{
		"rgba":  {Kind: PlaneRGBA, W: 2, H: 1, Stride: 2, Pix32: []uint32{0xff000000, 0xffffffff}},
		"index": {Kind: PlaneIndex, W: 2, H: 1, Stride: 2, Pix8: []uint8{0, 1}, Pal: pal},
		"bits":  {Kind: PlaneBits, W: 2, H: 1, Stride: 1, Pix8: []uint8{0x40}, Pal: pal},
	}
	for name, p := range planes {
		var s Sampler
		if !s.Setup(NewTexture(p), Scale(10, 10), true) {
			t.Fatalf("%s: setup failed", name)
		}
		span, one := make([]uint32, 30), make([]uint32, 1)
		for y := 0; y < 10; y++ {
			s.Sample(y, -5, span)
			for i, got := range span {
				s.Sample(y, i-5, one)
				if got != one[0] {
					t.Fatalf("%s (%d, %d): %x in a span, %x alone", name, i-5, y, got, one[0])
				}
			}
		}
	}

	// Random pictures at scales that step by less than, about and more
	// than one texel, where a span moves its pair on by one column or
	// jumps.
	rng := rand.New(rand.NewPCG(11, 12))
	rpal := new(Palette)
	for i := range rpal {
		a := uint8(rng.IntN(256))
		rpal[i] = pack(uint8(rng.IntN(int(a)+1)), uint8(rng.IntN(int(a)+1)), uint8(rng.IntN(int(a)+1)), a)
	}
	rnd := map[string]Plane{
		"rgba":  {Kind: PlaneRGBA, W: 19, H: 7, Stride: 21, Pix32: make([]uint32, 21*7)},
		"index": {Kind: PlaneIndex, W: 19, H: 7, Stride: 19, Pix8: make([]uint8, 19*7), Pal: rpal},
		"bits":  {Kind: PlaneBits, W: 19, H: 7, Stride: 3, Pix8: make([]uint8, 3*7), Pal: rpal},
	}
	for name, p := range rnd {
		for i := range p.Pix32 {
			p.Pix32[i] = rpal[rng.IntN(256)]
		}
		for i := range p.Pix8 {
			p.Pix8[i] = uint8(rng.IntN(256))
		}
		for _, sx := range []float64{7.3, 2.5, 1.3, 1, 0.8, 0.6} {
			var s Sampler
			if !s.Setup(NewTexture(p), Scale(sx, 1.7).Mul(Translate(-3.4, 0.6)), true) {
				t.Fatalf("%s: setup failed", name)
			}
			span, one := make([]uint32, 180), make([]uint32, 1)
			for y := 0; y < 14; y++ {
				s.Sample(y, -12, span)
				for i, got := range span {
					s.Sample(y, i-12, one)
					if got != one[0] {
						t.Fatalf("%s scale %g (%d, %d): %x in a span, %x alone", name, sx, i-12, y, got, one[0])
					}
				}
			}
		}
	}
}

// TestOpaqueImageShadedInPlace checks that shading an opaque texture
// straight into the destination gives the bytes of compositing its shaded
// span, for every kind of plane, mip level and wrap, and that the shortcut
// is refused where it would not.
func TestOpaqueImageShadedInPlace(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	var pal Palette
	for i := range pal {
		pal[i] = pack(uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255)
	}
	rgba := Plane{Kind: PlaneRGBA, W: 37, H: 23, Stride: 40, Pix32: make([]uint32, 40*23)}
	for i := range rgba.Pix32 {
		rgba.Pix32[i] = rng.Uint32() | 0xff<<alphaShift
	}
	index := Plane{Kind: PlaneIndex, W: 29, H: 31, Stride: 29, Pix8: make([]uint8, 29*31), Pal: &pal}
	bits := Plane{Kind: PlaneBits, W: 21, H: 17, Stride: 3, Pix8: make([]uint8, 3*17), Pal: &pal}
	for _, p := range []*Plane{&index, &bits} {
		for i := range p.Pix8 {
			p.Pix8[i] = uint8(rng.IntN(256))
		}
	}
	r := image.Rect(-8, -4, 120, 90)
	for _, p := range []Plane{rgba, index, bits} {
		tex := NewTexture(p)
		for _, m := range []Matrix{
			Translate(3, 5),
			Scale(3.7, 2.1).Mul(Translate(-4.3, 1.6)),
			Scale(0.3, 0.45).Mul(Translate(10.5, 2.25)),
			Scale(0.07, 0.09),
			Scale(2, 2).Mul(Rotate(0.4)).Mul(Translate(30, -10)),
		} {
			for _, wrap := range []bool{false, true} {
				for _, smooth := range []bool{false, true} {
					var sh ImageShader
					if wrap {
						sh.SetImageWrap(tex, m, smooth, 255)
					} else {
						sh.SetImage(tex, m, smooth, 255)
					}
					dst, want := randomRGBA(rng, r), image.NewRGBA(r)
					copy(want.Pix, dst.Pix)
					fused, plain := NewShaderBlitter(dst, &sh), NewShaderBlitter(want, &sh)
					if fused.os == nil {
						t.Fatal("ImageShader is no opaqueSource")
					}
					if !sh.opaqueSpan(fused.t.row(0, 0, 1)) {
						t.Fatalf("kind %d: opaque texture not taken as opaque", p.Kind)
					}
					plain.os = nil
					for y := r.Min.Y; y < r.Max.Y; y++ {
						x0 := r.Min.X + rng.IntN(r.Dx())
						x1 := x0 + 1 + rng.IntN(r.Max.X-x0)
						fused.BlitRun(y, x0, x1, 255)
						plain.BlitRun(y, x0, x1, 255)
					}
					for i := range dst.Pix {
						if dst.Pix[i] != want.Pix[i] {
							t.Fatalf("kind %d %v wrap %v smooth %v: byte %d: %d, want %d",
								p.Kind, m, wrap, smooth, i, dst.Pix[i], want.Pix[i])
						}
					}
				}
			}
		}
	}

	var sh ImageShader
	d := make([]uint32, 4)
	sh.SetImage(NewTexture(rgba), Identity, false, 200)
	if sh.opaqueSpan(d) {
		t.Error("constant alpha 200 taken as opaque")
	}
	translucent := rgba
	translucent.Pix32 = append([]uint32(nil), rgba.Pix32...)
	translucent.Pix32[40*22+36] &^= 1 << alphaShift
	sh.SetImage(NewTexture(translucent), Identity, false, 255)
	if sh.opaqueSpan(d) {
		t.Error("texture with a translucent pixel taken as opaque")
	}
	sh.SetImage(NewTexture(rgba), Identity, false, 255)
	if sh.opaqueSpan(rgba.Pix32[100:110]) {
		t.Error("shaded in place over its own pixels")
	}
	sh.SetMask(NewTexture(index), Identity, false)
	if sh.opaqueSpan(d) {
		t.Error("masked texture taken as opaque")
	}
}

func BenchmarkLayerShader(b *testing.B) {
	rng := rand.New(rand.NewPCG(7, 8))
	r := image.Rect(0, 0, 512, 64)
	src, dst := randomRGBA(rng, r), randomRGBA(rng, r)
	// Translucent gradients: colours and alphas change slowly, as in
	// drawn content, rather than at random.
	smooth := func(m *image.RGBA, phase int) {
		for i := 0; i < len(m.Pix); i += 4 {
			x := i/4 + phase
			a := uint8(40 + x%200)
			m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = a/2, a/3, a, a
		}
	}
	gsrc, gdst := image.NewRGBA(r), image.NewRGBA(r)
	smooth(gsrc, 0)
	smooth(gdst, 77)
	out := make([]uint32, r.Dx())
	for _, c := range []struct {
		name  string
		bm    BlendMode
		alpha uint8
	}{
		{"Normal", BlendNormal, 255}, {"NormalAlpha", BlendNormal, 128},
		{"Multiply", BlendMultiply, 200}, {"Screen", BlendScreen, 200},
		{"MultiplyGradient", BlendMultiply, 255}, {"ScreenGradient", BlendScreen, 255},
	} {
		ls := &LayerShader{Src: src, Dst: dst, Alpha: c.alpha, Blend: c.bm}
		if c.alpha == 255 && c.bm != BlendNormal {
			ls.Src, ls.Dst = gsrc, gdst
		}
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(4 * r.Dx() * r.Dy()))
			for b.Loop() {
				for y := range r.Dy() {
					ls.ShadeSpan(y, 0, out)
				}
			}
		})
	}
}

func BenchmarkMipIndex(b *testing.B) {
	p := Plane{Kind: PlaneIndex, W: 1024, H: 1024, Stride: 1024, Pix8: make([]uint8, 1<<20), Pal: GrayPalette}
	for i := range p.Pix8 {
		p.Pix8[i] = uint8(i * 31)
	}
	b.SetBytes(int64(len(p.Pix8)))
	for b.Loop() {
		NewTexture(p).Level(2)
	}
}

func BenchmarkSampleUnit(b *testing.B) {
	p := Plane{Kind: PlaneRGBA, W: 512, H: 64, Stride: 512, Pix32: make([]uint32, 512*64)}
	var s Sampler
	s.Setup(NewTexture(p), Translate(7, 3), false)
	dst := make([]uint32, 480)
	b.SetBytes(int64(4 * len(dst) * 64))
	for b.Loop() {
		for y := range 64 {
			s.Sample(y, 10, dst)
		}
	}
}

func TestFastPathsDoNotAllocate(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	r := image.Rect(0, 0, 64, 32)
	dst := randomRGBA(rng, r)
	c := NewCanvas(dst)
	var p Path
	p.Rect(0, 0, 64, 32)
	p2 := Plane{Kind: PlaneRGBA, W: 64, H: 32, Stride: 64, Pix32: make([]uint32, 64*32)}
	var is ImageShader
	is.SetImage(NewTexture(p2), Translate(0, 0), false, 255)
	for _, sh := range []Shader{
		&LayerShader{Src: randomRGBA(rng, r), Dst: dst, Alpha: 255},
		&LayerShader{Src: randomRGBA(rng, r), Dst: dst, Alpha: 128, Blend: BlendScreen},
		&is,
	} {
		paint := &Paint{Shader: sh}
		c.Fill(&p, Identity, NonZero, paint)
		if a := testing.AllocsPerRun(10, func() { c.Fill(&p, Identity, NonZero, paint) }); a != 0 {
			t.Errorf("%T: %v allocations per fill", sh, a)
		}
	}
}
