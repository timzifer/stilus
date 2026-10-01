package stilus

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestTextureMipLevels(t *testing.T) {
	// Odd sizes: edge blocks are partial and averaged over what they hold.
	p := Plane{Kind: PlaneRGBA, W: 5, H: 3, Stride: 5, Pix32: make([]uint32, 15)}
	for i := range p.Pix32 {
		p.Pix32[i] = pack(uint8(10*i), 0, 0, 255)
	}
	tex := NewTexture(p)
	if tex.Levels() != 3 {
		t.Errorf("levels %d", tex.Levels())
	}
	l1 := tex.Level(1)
	if l1.W != 3 || l1.H != 2 {
		t.Fatalf("level 1 is %d × %d", l1.W, l1.H)
	}
	// Top-left block: samples 0, 1, 5, 6 → 30; bottom-right: sample 14.
	if r, _, _, _ := unpack(l1.At(0, 0)); r != 30 {
		t.Errorf("level 1 (0, 0) red %d", r)
	}
	if r, _, _, _ := unpack(l1.At(2, 1)); r != 140 {
		t.Errorf("level 1 (2, 1) red %d", r)
	}
	if tex.Level(1) != l1 || tex.Level(9) != tex.Level(3) {
		t.Error("levels are not kept")
	}

	// Bit planes count ones, at byte and sub-byte block sizes.
	bitsPlane := Plane{Kind: PlaneBits, W: 20, H: 4, Stride: 3, Pix8: []byte{
		0xff, 0x00, 0xf0, 0xff, 0x00, 0xf0, 0xff, 0x00, 0xf0, 0xff, 0x00, 0xf0,
	}, Pal: GrayPalette}
	bt := NewTexture(bitsPlane)
	if !bt.gray {
		t.Error("a grey palette makes grey levels")
	}
	l3 := bt.Level(3) // blocks of 8: all ones, all zeros, the last 4 ones
	for x, want := range []uint8{1, 0, 1} {
		if got := l3.Pix8[x]; got != want {
			t.Errorf("level 3 [%d] = %d, want %d", x, got, want)
		}
	}
	l2 := bt.Level(2)
	if l2.W != 5 || l2.Pix8[1] != 1 || l2.Pix8[2] != 0 || l2.Pix8[4] != 1 {
		t.Errorf("level 2 %v", l2.Pix8[:l2.W])
	}
}

func TestImageShader(t *testing.T) {
	// A 2 × 2 texture scaled to 20 × 20 at (10, 10), nearest, through a
	// mask that hides its right half.
	p := Plane{Kind: PlaneRGBA, W: 2, H: 2, Stride: 2, Pix32: []uint32{
		PackRGBA(rgba(255, 0, 0, 255)), PackRGBA(rgba(0, 255, 0, 255)),
		PackRGBA(rgba(0, 0, 255, 255)), PackRGBA(rgba(255, 255, 255, 255)),
	}}
	m := Plane{Kind: PlaneIndex, W: 2, H: 1, Stride: 2, Pix8: []uint8{255, 0}, Pal: AlphaPalette}
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	c := NewCanvas(img)
	var s ImageShader
	toDevice := Matrix{10, 0, 0, 10, 10, 10}
	if !s.SetImage(NewTexture(p), toDevice, false, 255) || !s.SetMask(NewTexture(m), toDevice.Mul(Scale(1, 2)), false) {
		t.Fatal("setup failed")
	}
	var r Path
	r.Rect(10, 10, 20, 20)
	c.Fill(&r, Identity, NonZero, &Paint{Shader: &s})
	for _, tc := range []struct {
		x, y int
		want color.RGBA
	}{
		{12, 12, rgba(255, 0, 0, 255)},
		{12, 25, rgba(0, 0, 255, 255)},
		{25, 12, rgba(0, 0, 0, 0)},
		{5, 5, rgba(0, 0, 0, 0)},
	} {
		if got := img.RGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("(%d, %d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	// Constant alpha and a solid colour.
	s.Reset()
	s.SetColor(PackRGBA(rgba(0, 0, 128, 128)))
	img2 := image.NewRGBA(img.Rect)
	c.Reset(img2, img2.Rect)
	c.Fill(&r, Identity, NonZero, &Paint{Shader: &s})
	if got := img2.RGBAAt(20, 20); got != rgba(0, 0, 128, 128) {
		t.Errorf("solid: %v", got)
	}
}

func TestBlendFunctions(t *testing.T) {
	cb, cs := [3]float64{0.2, 0.4, 0.6}, [3]float64{0.9, 0.5, 0.1}
	for _, c := range []struct {
		bm   BlendMode
		want [3]float64
	}{
		{BlendNormal, cs},
		{BlendMultiply, [3]float64{0.18, 0.2, 0.06}},
		{BlendScreen, [3]float64{0.92, 0.7, 0.64}},
		{BlendOverlay, [3]float64{0.36, 0.4, 0.28}},
		{BlendHardLight, [3]float64{0.84, 0.4, 0.12}},
		{BlendColorDodge, [3]float64{1, 0.8, 0.6 / 0.9}},
		{BlendColorBurn, [3]float64{1 - 0.8/0.9, 0, 0}},
		{BlendExclusion, [3]float64{0.2 + 0.9 - 0.36, 0.5, 0.6 + 0.1 - 0.12}},
		{BlendColor, setLum(cs, lum(cb))},
	} {
		got := Blend(c.bm, cb, cs)
		for i := range got {
			if math.Abs(got[i]-c.want[i]) > 1e-9 {
				t.Errorf("%v: got %v, want %v", c.bm, got, c.want)
				break
			}
		}
	}
	// Non-separable modes keep the luminosity they are asked for.
	for _, bm := range []BlendMode{BlendHue, BlendSaturation, BlendColor, BlendLuminosity} {
		got := Blend(bm, cb, cs)
		want := lum(cb)
		if bm == BlendLuminosity {
			want = lum(cs)
		}
		if math.Abs(lum(got)-want) > 1e-9 {
			t.Errorf("%v: luminosity %v, want %v", bm, lum(got), want)
		}
	}
	if s := sat(setSat(cb, 0.3)); math.Abs(s-0.3) > 1e-9 {
		t.Errorf("setSat: saturation %v", s)
	}
	if BlendHue.String() != "Hue" || BlendMode(99).String() != "BlendMode(?)" {
		t.Error("names")
	}
}

func TestLayerShader(t *testing.T) {
	r := image.Rect(0, 0, 8, 1)
	dst := image.NewRGBA(r)
	src := image.NewRGBA(r)
	for x := range 8 {
		dst.SetRGBA(x, 0, rgba(0, 0, 255, 255))
		src.SetRGBA(x, 0, rgba(255, 0, 0, 255))
	}
	mask := image.NewAlpha(image.Rect(2, 0, 6, 1))
	for i := range mask.Pix {
		mask.Pix[i] = 255
	}
	mask.Pix[0] = 0
	c := NewCanvas(dst)
	ls := &LayerShader{Src: src, Dst: dst, Mask: mask, Alpha: 255, Blend: BlendMultiply}
	var p Path
	p.Rect(0, 0, 8, 1)
	c.Fill(&p, Identity, NonZero, &Paint{Shader: ls})
	// Outside the mask and at its zero: the backdrop; inside: red × blue.
	for x, want := range []color.RGBA{
		rgba(0, 0, 255, 255), rgba(0, 0, 255, 255), rgba(0, 0, 255, 255),
		rgba(0, 0, 0, 255), rgba(0, 0, 0, 255), rgba(0, 0, 0, 255),
		rgba(0, 0, 255, 255), rgba(0, 0, 255, 255),
	} {
		if got := dst.RGBAAt(x, 0); got != want {
			t.Errorf("x %d: %v, want %v", x, got, want)
		}
	}
}

func TestGlyphCacheMatchesFill(t *testing.T) {
	var g Path
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.CubicTo(0.9, 0.3, 0.7, 0.8, 0.3, 0.7)
	g.Close()
	r := image.Rect(0, 0, 120, 40)
	want, got := image.NewRGBA(r), image.NewRGBA(r)
	cw, cg := NewCanvas(want), NewCanvas(got)
	var gc GlyphCache
	paint := &Paint{Color: rgba(0, 0, 0, 255)}
	for i := range 10 {
		// Positions at whole quarter pixels, so the cached mask is exact.
		m := Matrix{20, 0, 0, -20, 3 + 10.25*float64(i), 30.5}
		cw.Fill(&g, m, NonZero, paint)
		gc.FillGlyph(cg, 7, 1, &g, m, paint)
	}
	if len(gc.masks) != 4 { // one per quarter-pixel phase
		t.Errorf("%d masks cached, want 4", len(gc.masks))
	}
	for i := range want.Pix {
		if d := int(want.Pix[i]) - int(got.Pix[i]); d > 1 || d < -1 {
			t.Fatalf("byte %d: %d vs %d", i, got.Pix[i], want.Pix[i])
		}
	}
	allocs := testing.AllocsPerRun(10, func() {
		gc.FillGlyph(cg, 7, 1, &g, Matrix{20, 0, 0, -20, 3, 30.5}, paint)
	})
	if allocs != 0 {
		t.Errorf("%v allocations per cached glyph", allocs)
	}
}

func shade(s Shader, y, x, n int) []color.RGBA {
	buf := make([]uint32, n)
	s.ShadeSpan(y, x, buf)
	out := make([]color.RGBA, n)
	for i, c := range buf {
		out[i] = UnpackRGBA(c)
	}
	return out
}

// grayRamp runs from black to white in n steps.
func grayRamp(n int) Ramp {
	r := make(Ramp, n)
	for i := range r {
		v := uint8(i * 255 / (n - 1))
		r[i] = pack(v, v, v, 255)
	}
	return r
}

func TestLinearGradient(t *testing.T) {
	var g LinearGradient
	g.Ramp, g.Alpha = grayRamp(256), 255
	g.Outside = pack(255, 0, 0, 255)
	// From x = 10 to x = 110 in a space scaled by 2: device 20 … 220.
	if !g.Set(10, 0, 110, 0, Scale(2, 2)) {
		t.Fatal("set")
	}
	px := shade(&g, 5, 0, 240)
	if px[10] != rgba(255, 0, 0, 255) || px[230] != rgba(255, 0, 0, 255) {
		t.Errorf("outside: %v %v", px[10], px[230])
	}
	if v := px[119].R; v < 125 || v > 130 { // t = (119.5−20)/200
		t.Errorf("middle %v", px[119])
	}
	g.Extend = [2]bool{true, true}
	px = shade(&g, 5, 0, 240)
	if px[10] != rgba(0, 0, 0, 255) || px[230] != rgba(255, 255, 255, 255) {
		t.Errorf("extended: %v %v", px[10], px[230])
	}
	// Constant along the perpendicular, under rotation.
	g.Set(0, 0, 0, 100, Rotate(math.Pi/2))
	a, b := shade(&g, 3, -50, 1)[0], shade(&g, 90, -50, 1)[0]
	if a != b {
		t.Errorf("rotated: %v vs %v", a, b)
	}
	g.Alpha = 128
	if c := shade(&g, 3, -99, 1)[0]; c.A != 128 {
		t.Errorf("alpha: %v", c)
	}
	if g.Set(1, 1, 1, 1, Identity) {
		t.Error("coincident points accepted")
	}
	if c := shade(&g, 0, 0, 1)[0]; c.A != 0 {
		t.Error("unset gradient paints")
	}
}

func TestRadialGradient(t *testing.T) {
	var g RadialGradient
	g.Ramp, g.Alpha = grayRamp(256), 255
	// Concentric: t is the distance from (50, 50) between radius 10 and 50.
	if !g.Set(50, 50, 10, 50, 50, 50, Identity) {
		t.Fatal("set")
	}
	px := shade(&g, 49, 0, 100) // centres at y = 49.5
	if out := shade(&g, 0, 0, 1)[0]; px[50].A != 0 || out.A != 0 {
		t.Errorf("not extended: %v %v", px[50], out)
	}
	if v := px[79].R; v < 120 || v > 136 { // distance 29.5 → t ≈ 0.49
		t.Errorf("middle %v", px[79])
	}
	g.Extend = [2]bool{true, true}
	px = shade(&g, 49, 0, 100)
	if out := shade(&g, 0, 0, 1)[0]; px[50] != rgba(0, 0, 0, 255) || out != rgba(255, 255, 255, 255) {
		t.Errorf("extended: %v %v", px[50], out)
	}
	// A cone: the circle grows from a point at (0, 50) to radius 50 at
	// (100, 50); points behind the start are outside even when extended
	// (negative radii).
	g.Extend = [2]bool{false, false}
	g.Set(0, 50, 0, 100, 50, 50, Identity)
	if c := shade(&g, 49, 60, 1)[0]; c.A == 0 {
		t.Error("inside the cone is not painted")
	}
	if c := shade(&g, 0, 2, 1)[0]; c.A != 0 {
		t.Errorf("outside the cone: %v", c)
	}
	if g.Set(0, 0, -1, 1, 1, 1, Identity) {
		t.Error("negative radius accepted")
	}
}

func TestFillMeshNoSeams(t *testing.T) {
	// A square of two triangles sharing a diagonal, in a fan of eight
	// around a centre: every pixel inside is drawn exactly once.
	r := image.Rect(0, 0, 64, 64)
	count := make([]int, 64*64)
	var tris []MeshTriangle
	cx, cy := float32(31.3), float32(30.7)
	for k := range 8 {
		a0 := float64(k) * math.Pi / 4
		a1 := float64(k+1) * math.Pi / 4
		tris = append(tris, MeshTriangle{
			{X: cx, Y: cy, C: pack(1, 1, 1, 1)},
			{X: cx + float32(25*math.Cos(a0)), Y: cy + float32(25*math.Sin(a0)), C: pack(1, 1, 1, 1)},
			{X: cx + float32(25*math.Cos(a1)), Y: cy + float32(25*math.Sin(a1)), C: pack(1, 1, 1, 1)},
		})
	}
	for _, tri := range tris {
		img := image.NewRGBA(r)
		FillMesh(img, r, []MeshTriangle{tri}, Identity, nil)
		for i := range count {
			if img.Pix[4*i+3] != 0 {
				count[i]++
			}
		}
	}
	inside := 0
	for y := range 64 {
		for x := range 64 {
			n := count[y*64+x]
			if n > 1 {
				t.Fatalf("pixel (%d, %d) drawn %d times", x, y, n)
			}
			d := math.Hypot(float64(x)+0.5-float64(cx), float64(y)+0.5-float64(cy))
			if d < 22 && n != 1 {
				t.Fatalf("pixel (%d, %d) inside not drawn", x, y)
			}
			inside += n
		}
	}
	// The octagon of circumradius 25 has area 2·√2·25².
	if a := 2 * math.Sqrt2 * 625; math.Abs(float64(inside)-a) > 60 {
		t.Errorf("area %d, want about %.0f", inside, a)
	}
}

func TestFillMeshInterpolates(t *testing.T) {
	r := image.Rect(0, 0, 101, 10)
	img := image.NewRGBA(r)
	tris := []MeshTriangle{
		{{X: 0, Y: -100, C: pack(0, 0, 0, 255), T: 0}, {X: 100, Y: -100, C: pack(200, 0, 0, 255), T: 1}, {X: 0, Y: 100, C: pack(0, 0, 0, 255), T: 0}},
		{{X: 100, Y: -100, C: pack(200, 0, 0, 255), T: 1}, {X: 100, Y: 100, C: pack(200, 0, 0, 255), T: 1}, {X: 0, Y: 100, C: pack(0, 0, 0, 255), T: 0}},
	}
	FillMesh(img, r, tris, Identity, nil)
	for _, x := range []int{0, 25, 50, 99} {
		want := 2 * (float64(x) + 0.5)
		if got := float64(img.RGBAAt(x, 5).R); math.Abs(got-want) > 1 {
			t.Errorf("x %d: red %v, want %.1f", x, got, want)
		}
	}
	if img.RGBAAt(100, 5).A != 0 {
		t.Error("pixel past the mesh drawn")
	}
	// Through a ramp.
	FillMesh(img, r, tris, Identity, grayRamp(101))
	if g := img.RGBAAt(50, 5).G; g < 126 || g > 130 {
		t.Errorf("ramp: %d", g)
	}
	allocs := testing.AllocsPerRun(10, func() { FillMesh(img, r, tris, Identity, nil) })
	if allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
}

func TestClipStroke(t *testing.T) {
	// Filling through a stroke's clip paints what the stroke paints.
	r := image.Rect(0, 0, 80, 60)
	var p Path
	p.MoveTo(10, 10)
	p.LineTo(70, 20)
	p.LineTo(30, 50)
	for _, st := range []StrokeStyle{
		{Width: 6, Join: RoundJoin, Cap: RoundCap, MiterLimit: 10},
		{Width: 0.3, MiterLimit: 10},
		{Width: 4, MiterLimit: 10, Dash: []float64{5, 3}},
	} {
		want, got := image.NewRGBA(r), image.NewRGBA(r)
		NewCanvas(want).Stroke(&p, Identity, &st, white)
		c := NewCanvas(got)
		c.ClipStroke(&p, Identity, &st)
		var all Path
		all.Rect(0, 0, 80, 60)
		c.Fill(&all, Identity, NonZero, white)
		c.PopClip()
		if mean, max := diff(alphaOfRGBA(got), alphaOfRGBA(want)); mean > 0.05 || max > 2 {
			t.Errorf("%+v: mean %.3f max %d", st, mean, max)
		}
		if c.ClipDepth() != 0 || c.Err() != nil {
			t.Errorf("depth %d err %v", c.ClipDepth(), c.Err())
		}
	}
}
