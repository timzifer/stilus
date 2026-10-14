package stilus

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

// rampShader paints a premultiplied colour that changes with x and y, its
// channels scaled by k so that two of them differ.
type rampShader struct{ k uint32 }

func (s rampShader) ShadeSpan(y, x int, dst []uint32) {
	for i := range dst {
		a := uint32(128 + (x+i+y)%128)
		v := (uint32(x+i) * s.k) % (a + 1)
		dst[i] = PackRGBA(color.RGBA{uint8(v), uint8(v / 2), uint8(a - v/3), uint8(a)})
	}
}

// pairScene draws the same scene with the given canvas and a source of
// paints: fills of curves, opaque and translucent strokes (the analytic
// path among them), a dashed stroke, a pixel-aligned rectangle, shader
// fills, glyphs from the cache (blitted and through the mask shader), all
// under a fractional clip rectangle and a clip path.
func pairScene(c *Canvas, gc *GlyphCache, paint func(i int) *Paint) {
	var p Path
	p.MoveTo(10, 10)
	p.CubicTo(60, 0, 90, 80, 30, 70)
	p.Close()
	var clip Path
	clip.MoveTo(5, 5)
	clip.LineTo(115, 12)
	clip.LineTo(100, 95)
	clip.LineTo(8, 88)
	clip.Close()
	c.ClipRect(Rect{X0: 2.5, Y0: 3.25, X1: 117.75, Y1: 96.5}, Identity)
	c.ClipPath(&clip, Identity, NonZero)
	c.Fill(&p, Identity, NonZero, paint(0))
	var line Path
	line.MoveTo(5, 50)
	line.LineTo(115, 60)
	c.Stroke(&line, Identity, &StrokeStyle{Width: 6, MiterLimit: 10}, paint(1))
	c.Stroke(&line, Matrix{1, 0, 0, 1, 0, 15}, &StrokeStyle{Width: 3, MiterLimit: 10}, paint(2))
	c.Stroke(&p, Identity, &StrokeStyle{Width: 2, MiterLimit: 10, Dash: []float64{4, 3}}, paint(3))
	var r Path
	r.Rect(70, 20, 30, 20)
	c.Fill(&r, Identity, NonZero, paint(4))
	c.Fill(&r, Matrix{1, 0, 0, 1, -50, 40}, NonZero, paint(5))
	var g Path
	g.MoveTo(0, 0)
	g.LineTo(0.6, 0)
	g.LineTo(0.3, -0.7)
	g.Close()
	for i := range 6 {
		gc.FillGlyph(c, 1, 7, &g, Matrix{14, 0, 0, 14, float64(15 + 16*i), 90}, paint(i%3))
	}
	c.PopClip()
	c.PopClip()
	// Without a clip: the opaque stroke's analytic rows and the glyph
	// mask's direct blit.
	c.Stroke(&line, Matrix{1, 0, 0, 1, 0, -30}, &StrokeStyle{Width: 5, MiterLimit: 10}, paint(1))
	c.Stroke(&line, Matrix{1, 0, 0, 1, 0, -40}, &StrokeStyle{Width: 4, MiterLimit: 10}, paint(0))
	for i := range 6 {
		gc.FillGlyph(c, 1, 7, &g, Matrix{14, 0, 0, 14, float64(10 + 18*i), 30}, paint([3]int{0, 4, 2}[i%3]))
	}
}

// TestCanvasPair draws into two images at once, each with its paint, and
// gets the bytes of drawing them one by one.
func TestCanvasPair(t *testing.T) {
	rect := image.Rect(0, 0, 120, 100)
	first := []*Paint{
		{Color: color.RGBA{200, 10, 30, 255}}, {Color: color.RGBA{0, 90, 0, 255}}, {Color: color.RGBA{40, 40, 0, 128}},
		{Color: color.RGBA{0, 0, 255, 255}}, {Color: color.RGBA{9, 8, 7, 255}}, {Shader: rampShader{3}},
	}
	second := []*Paint{
		{Color: color.RGBA{60, 60, 60, 255}}, {Shader: rampShader{5}}, {Color: color.RGBA{20, 20, 20, 128}},
		{Color: color.RGBA{255, 255, 255, 255}}, {Color: color.RGBA{1, 1, 1, 255}}, {Color: color.RGBA{0, 0, 0, 0}},
	}
	one := func(ps []*Paint) *image.RGBA {
		img := image.NewRGBA(rect)
		c := NewCanvas(img)
		gc := GlyphCache{MaxBytes: 1 << 20}
		pairScene(c, &gc, func(i int) *Paint { return ps[i] })
		return img
	}
	want1, want2 := one(first), one(second)

	a, b := image.NewRGBA(rect), image.NewRGBA(rect)
	c := NewCanvas(a)
	c.ResetPair(a, b, rect)
	gc := GlyphCache{MaxBytes: 1 << 20}
	pairScene(c, &gc, func(i int) *Paint {
		p := *first[i]
		p.Second = second[i]
		return &p
	})
	if !bytes.Equal(a.Pix, want1.Pix) {
		t.Error("the first image is not what it is drawn alone")
	}
	if !bytes.Equal(b.Pix, want2.Pix) {
		t.Error("the second image is not what it is drawn alone")
	}
	if c.Err() != nil {
		t.Error(c.Err())
	}

	// Without a second paint the same is painted into both; after Reset
	// the canvas draws into one image again.
	a, b = image.NewRGBA(rect), image.NewRGBA(rect)
	c.ResetPair(a, b, rect)
	pairScene(c, &gc, func(i int) *Paint { return first[i] })
	if !bytes.Equal(a.Pix, want1.Pix) || !bytes.Equal(b.Pix, want1.Pix) {
		t.Error("one paint for both images")
	}
	c.Reset(a, rect)
	if c.second != nil {
		t.Error("Reset keeps the second image")
	}
}

// randomPremul returns a premultiplied pixel, opaque one time in four.
func randomPremul(rng *rand.Rand) [4]uint8 {
	a := uint8(rng.IntN(256))
	if rng.IntN(4) == 0 {
		a = 255
	}
	var p [4]uint8
	for i := range 3 {
		p[i] = uint8(rng.IntN(int(a) + 1))
	}
	p[3] = a
	return p
}

func layerImages(px [][4]uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, len(px), 1))
	for i, p := range px {
		copy(img.Pix[4*i:], p[:])
	}
	return img
}

// TestLayerShaderSubtractive blends ink amounts on their complements: each
// mode within a level of B(cb, cs) = 1 − B(1 − cb, 1 − cs) computed in
// floating point, Normal as it is, and the black image of a pair with K of
// the backdrop (Hue, Saturation, Color) or the source (Luminosity).
func TestLayerShaderSubtractive(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	const n = 400
	src, dst := make([][4]uint8, n), make([][4]uint8, n)
	for i := range n {
		src[i], dst[i] = randomPremul(rng), randomPremul(rng)
	}
	s, d := layerImages(src), layerImages(dst)
	out := make([]uint32, n)
	for bm := BlendNormal; bm <= BlendLuminosity; bm++ {
		for _, black := range []bool{false, true} {
			if black && bm < BlendHue {
				continue
			}
			ls := LayerShader{Src: s, Dst: d, Alpha: 255, Blend: bm, Subtractive: true, Black: black}
			ls.ShadeSpan(0, 0, out)
			for i := range n {
				got := UnpackRGBA(out[i])
				sp, dp := src[i], dst[i]
				want := subtractiveRef(bm, black, sp, dp)
				g := [3]uint8{got.R, got.G, got.B}
				for k := range 3 {
					if math.Abs(float64(g[k])-want[k]) > 1 || got.A != sp[3] {
						t.Fatalf("%v black %v: pixel %d: %v over %v gives %v, want %v", bm, black, i, sp, dp, got, want)
					}
				}
			}
		}
	}
}

// subtractiveRef is the colour the shader returns, s·(1−αb) + αs·αb·B, of
// premultiplied ink amounts, in floating point.
func subtractiveRef(bm BlendMode, black bool, sp, dp [4]uint8) [3]float64 {
	as, ab := float64(sp[3])/255, float64(dp[3])/255
	var cs, cb [3]float64
	for k := range 3 {
		if sp[3] > 0 {
			cs[k] = min(float64(sp[k])/float64(sp[3]), 1)
		}
		if dp[3] > 0 {
			cb[k] = min(float64(dp[k])/float64(dp[3]), 1)
		}
	}
	var bl [3]float64
	switch {
	case bm == BlendNormal || black && bm == BlendLuminosity:
		bl = cs
	case black:
		bl = cb
	default:
		var ics, icb [3]float64
		for k := range 3 {
			ics[k], icb[k] = 1-cs[k], 1-cb[k]
		}
		r := Blend(bm, icb, ics)
		for k := range 3 {
			bl[k] = 1 - r[k]
		}
	}
	var o [3]float64
	for k := range 3 {
		if dp[3] == 0 {
			o[k] = float64(sp[k])
			continue
		}
		o[k] = float64(sp[k])*(1-ab) + as*ab*bl[k]*255
	}
	return o
}
