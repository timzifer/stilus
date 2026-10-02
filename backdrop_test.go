package stilus

import (
	"image"
	"math"
	"math/rand/v2"
	"testing"
)

// refRemoveBackdrop is the backdrop removal of PDF 2.0, 11.4.8 in floating
// point: the premultiplied colour C·αgn with C = Cn + (Cn − C0)·(α0/αgn − α0).
func refRemoveBackdrop(s, b0 [4]uint8, g uint8) [4]float64 {
	if g == 0 || s[3] == 0 {
		return [4]float64{}
	}
	an, a0, ag := float64(s[3])/255, float64(b0[3])/255, float64(g)/255
	var o [4]float64
	for c := range 3 {
		cn := float64(s[c]) / 255 / an
		var c0 float64
		if a0 > 0 {
			c0 = float64(b0[c]) / 255 / a0
		}
		v := cn + (cn-c0)*(a0/ag-a0)
		o[c] = min(max(v, 0), 1) * ag * 255
	}
	o[3] = float64(g)
	return o
}

func TestLayerShaderBackdropRemoval(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	r := image.Rect(2, 7, 2+97, 7+31)
	src, init, alone := randomRGBA(rng, r), randomRGBA(rng, r), randomRGBA(rng, r)
	ls := &LayerShader{Src: src, Initial: init, Alone: alone, Alpha: 255}
	out := make([]uint32, r.Dx())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		ls.ShadeSpan(y, r.Min.X, out)
		for i, got := range out {
			o := src.PixOffset(r.Min.X+i, y)
			want := refRemoveBackdrop([4]uint8(src.Pix[o:o+4]), [4]uint8(init.Pix[o:o+4]), alone.Pix[o+3])
			gr, gg, gb, ga := unpack(got)
			for c, v := range [4]uint8{gr, gg, gb, ga} {
				if math.Abs(float64(v)-want[c]) > 1 {
					t.Fatalf("(%d, %d) channel %d: %d, want %.2f (src %v, initial %v, alone %d)",
						r.Min.X+i, y, c, v, want[c], src.Pix[o:o+4], init.Pix[o:o+4], alone.Pix[o+3])
				}
			}
		}
	}
}

// nonIsolated returns a backdrop, a group of Normal objects drawn alone and
// the same group drawn onto a copy of the backdrop.
func nonIsolated(rng *rand.Rand, r image.Rectangle) (backdrop, alone, src *image.RGBA) {
	backdrop, alone = randomRGBA(rng, r), randomRGBA(rng, r)
	src = image.NewRGBA(r)
	bs := words(backdrop.Pix)
	for i, v := range words(alone.Pix) {
		src.Pix[4*i], src.Pix[4*i+1], src.Pix[4*i+2], src.Pix[4*i+3] = unpack(over(v, bs[i]))
	}
	return backdrop, alone, src
}

// words returns the pixels of p packed.
func words(p []uint8) []uint32 {
	w := make([]uint32, len(p)/4)
	for i := range w {
		w[i] = pack(p[4*i], p[4*i+1], p[4*i+2], p[4*i+3])
	}
	return w
}

func TestBackdropRemovalNormalGroup(t *testing.T) {
	// The group's objects are Normal, so without the backdrop the group
	// is what it is drawn alone, up to the rounding of drawing it onto the
	// backdrop.
	rng := rand.New(rand.NewPCG(5, 6))
	r := image.Rect(0, 0, 256, 64)
	backdrop, alone, src := nonIsolated(rng, r)
	ls := &LayerShader{Src: src, Initial: backdrop, Alone: alone, Alpha: 255}
	out := make([]uint32, r.Dx())
	worst := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		ls.ShadeSpan(y, r.Min.X, out)
		for i, got := range out {
			o := alone.PixOffset(i, y)
			gr, gg, gb, ga := unpack(got)
			for c, v := range [4]uint8{gr, gg, gb, ga} {
				d := int(v) - int(alone.Pix[o+c])
				worst = max(worst, d, -d)
			}
		}
	}
	if worst > 1 {
		t.Errorf("largest difference from the group alone %d", worst)
	}
}

func TestBackdropRemovalComposite(t *testing.T) {
	// Composited Normal at full opacity over the backdrop it started from,
	// a non-isolated group is the layer drawn onto that backdrop, also
	// through a canvas (which must not take the layer's row as it is).
	rng := rand.New(rand.NewPCG(9, 10))
	r := image.Rect(0, 0, 128, 16)
	backdrop, alone, src := nonIsolated(rng, r)
	dst := image.NewRGBA(r)
	copy(dst.Pix, backdrop.Pix)
	c := NewCanvas(dst)
	var p Path
	p.Rect(0, 0, float32(r.Dx()), float32(r.Dy()))
	c.Fill(&p, Identity, NonZero, &Paint{Shader: &LayerShader{Src: src, Dst: dst, Initial: backdrop, Alone: alone, Alpha: 255}})
	for i := range dst.Pix {
		if d := int(dst.Pix[i]) - int(src.Pix[i]); d > 2 || d < -2 {
			t.Fatalf("byte %d: %d, want %d", i, dst.Pix[i], src.Pix[i])
		}
	}
}

func TestBackdropRemovalBlend(t *testing.T) {
	// Removal composes with masks, opacity and blend modes: the result is
	// that of a layer holding the backdrop-free pixels.
	rng := rand.New(rand.NewPCG(11, 12))
	r := image.Rect(1, 1, 1+64, 1+8)
	src, init, alone, dst := randomRGBA(rng, r), randomRGBA(rng, r), randomRGBA(rng, r), randomRGBA(rng, r)
	mask := image.NewAlpha(image.Rect(10, 2, 50, 7))
	for i := range mask.Pix {
		mask.Pix[i] = uint8(rng.IntN(256))
	}
	free := image.NewRGBA(r)
	out := make([]uint32, r.Dx())
	rm := &LayerShader{Src: src, Initial: init, Alone: alone, Alpha: 255}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		rm.ShadeSpan(y, r.Min.X, out)
		for i, v := range out {
			o := free.PixOffset(r.Min.X+i, y)
			free.Pix[o], free.Pix[o+1], free.Pix[o+2], free.Pix[o+3] = unpack(v)
		}
	}
	want := make([]uint32, r.Dx())
	for _, bm := range []BlendMode{BlendNormal, BlendMultiply, BlendScreen, BlendOverlay, BlendHue} {
		for _, m := range []*image.Alpha{nil, mask} {
			for _, alpha := range []uint8{255, 128, 3} {
				got := &LayerShader{Src: src, Dst: dst, Mask: m, Alpha: alpha, Blend: bm, Initial: init, Alone: alone}
				ref := &LayerShader{Src: free, Dst: dst, Mask: m, Alpha: alpha, Blend: bm}
				for y := r.Min.Y; y < r.Max.Y; y++ {
					got.ShadeSpan(y, r.Min.X, out)
					ref.ShadeSpan(y, r.Min.X, want)
					for i := range out {
						if out[i] != want[i] {
							t.Fatalf("%v alpha %d mask %v (%d, %d): %v, want %v",
								bm, alpha, m != nil, r.Min.X+i, y, UnpackRGBA(out[i]), UnpackRGBA(want[i]))
						}
					}
				}
			}
		}
	}
}

// drawnGroup returns a backdrop and a group drawn as in a document: a
// smooth backdrop and ellipses, some translucent, antialiased at their
// edges.
func drawnGroup(r image.Rectangle) (backdrop, alone, src *image.RGBA) {
	backdrop, alone = image.NewRGBA(r), image.NewRGBA(r)
	for i := 0; i < len(backdrop.Pix); i += 4 {
		x := uint8(i / 4)
		backdrop.Pix[i], backdrop.Pix[i+1], backdrop.Pix[i+2], backdrop.Pix[i+3] = x/2, 90, 255-x, 255
	}
	c := NewCanvas(alone)
	rng := rand.New(rand.NewPCG(13, 14))
	for range 24 {
		var p Path
		p.Ellipse(float32(rng.IntN(r.Dx())), float32(rng.IntN(r.Dy())), float32(8+rng.IntN(40)), float32(8+rng.IntN(24)))
		a := uint8(255)
		if rng.IntN(2) == 0 {
			a = uint8(60 + rng.IntN(180))
		}
		c.Fill(&p, Identity, NonZero, &Paint{Color: rgba(uint8(rng.IntN(int(a)+1)), uint8(rng.IntN(int(a)+1)), 0, a)})
	}
	src = image.NewRGBA(r)
	bs := words(backdrop.Pix)
	for i, v := range words(alone.Pix) {
		src.Pix[4*i], src.Pix[4*i+1], src.Pix[4*i+2], src.Pix[4*i+3] = unpack(over(v, bs[i]))
	}
	return backdrop, alone, src
}

func BenchmarkBackdropRemoval(b *testing.B) {
	rng := rand.New(rand.NewPCG(13, 14))
	r := image.Rect(0, 0, 512, 64)
	out := make([]uint32, r.Dx())
	for _, data := range []string{"Random", "Drawn"} {
		backdrop, alone, src := nonIsolated(rng, r)
		if data == "Drawn" {
			backdrop, alone, src = drawnGroup(r)
		}
		dst := randomRGBA(rng, r)
		for _, c := range []struct {
			name string
			bm   BlendMode
		}{{"Normal", BlendNormal}, {"Multiply", BlendMultiply}} {
			ls := &LayerShader{Src: src, Dst: dst, Initial: backdrop, Alone: alone, Alpha: 255, Blend: c.bm}
			b.Run(data+c.name, func(b *testing.B) {
				b.SetBytes(int64(4 * r.Dx() * r.Dy()))
				for b.Loop() {
					for y := range r.Dy() {
						ls.ShadeSpan(y, 0, out)
					}
				}
			})
		}
	}
}
