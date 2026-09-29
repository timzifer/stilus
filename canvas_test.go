package stilus

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// alphaCanvas renders with a white paint onto a transparent RGBA image and
// returns the alpha channel.
func alphaOfRGBA(img *image.RGBA) *image.Alpha {
	a := image.NewAlpha(img.Rect)
	for i := range a.Pix {
		a.Pix[i] = img.Pix[4*i+3]
	}
	return a
}

var white = &Paint{Color: color.RGBA{255, 255, 255, 255}}

func mulMasks(a, b *image.Alpha) *image.Alpha {
	out := image.NewAlpha(a.Rect)
	for i := range out.Pix {
		out.Pix[i] = uint8(div255(uint32(a.Pix[i]) * uint32(b.Pix[i])))
	}
	return out
}

func TestCanvasRectClip(t *testing.T) {
	clip := image.Rect(0, 0, 64, 48)
	var shape Path
	shape.Ellipse(30, 24, 26, 20)
	for _, r := range []Rect{
		{10.25, 8.5, 50.75, 40.125},
		{10, 8, 50, 40},
		{20.3, 10.2, 20.8, 30.9}, // narrower than a pixel
		{-100, -100, 1000, 1000},
		{70, 0, 80, 10}, // outside
	} {
		img := image.NewRGBA(clip)
		c := NewCanvas(img)
		c.ClipRect(r, Identity)
		c.Fill(&shape, Identity, NonZero, white)
		got := alphaOfRGBA(img)

		var rp Path
		rp.Rect(float32(r.X0), float32(r.Y0), float32(r.X1-r.X0), float32(r.Y1-r.Y0))
		want := mulMasks(renderMask(&shape, Identity, clip, NonZero), renderMask(&rp, Identity, clip, NonZero))
		if mean, max := diff(got, want); mean > 0.1 || max > 3 {
			t.Errorf("rect clip %v: mean %.3f max %d", r, mean, max)
		}
	}
}

func TestCanvasMaskClip(t *testing.T) {
	clip := image.Rect(0, 0, 64, 48)
	var a, b, shape Path
	a.Ellipse(24, 24, 20, 16)
	b.Ellipse(40, 20, 18, 14)
	shape.MoveTo(0, 0)
	shape.LineTo(64, 10)
	shape.LineTo(30, 48)
	shape.Close()
	rot := Translate(-32, -24).Mul(Rotate(0.4)).Mul(Translate(32, 24))

	img := image.NewRGBA(clip)
	c := NewCanvas(img)
	c.ClipPath(&a, Identity, NonZero)
	c.ClipRect(Rect{8.5, 6.25, 50, 44}, rot) // rotated: mask
	c.ClipPath(&b, Identity, EvenOdd)
	c.ClipRect(Rect{12.5, 4, 60, 40.5}, Identity) // axis aligned on a mask
	c.Fill(&shape, Identity, NonZero, white)
	if c.ClipDepth() != 4 || c.Err() != nil {
		t.Fatalf("depth %d err %v", c.ClipDepth(), c.Err())
	}
	got := alphaOfRGBA(img)

	var rr, r2 Path
	rr.Rect(8.5, 6.25, 50-8.5, 44-6.25)
	r2.Rect(12.5, 4, 60-12.5, 40.5-4)
	want := renderMask(&a, Identity, clip, NonZero)
	want = mulMasks(want, renderMask(&rr, rot, clip, NonZero))
	want = mulMasks(want, renderMask(&b, Identity, clip, EvenOdd))
	want = mulMasks(want, renderMask(&r2, Identity, clip, NonZero))
	want = mulMasks(want, renderMask(&shape, Identity, clip, NonZero))
	if mean, max := diff(got, want); mean > 0.2 || max > 4 {
		t.Errorf("mask clip: mean %.3f max %d", mean, max)
	}

	// Pop everything: drawing is unclipped again.
	for c.ClipDepth() > 0 {
		c.PopClip()
	}
	img2 := image.NewRGBA(clip)
	c.Reset(img2, img2.Bounds())
	c.Fill(&shape, Identity, NonZero, white)
	if mean, max := diff(alphaOfRGBA(img2), renderMask(&shape, Identity, clip, NonZero)); mean > 0 || max > 0 {
		t.Errorf("unclipped: mean %.3f max %d", mean, max)
	}
}

func TestCanvasStrokeClip(t *testing.T) {
	clip := image.Rect(0, 0, 64, 48)
	var p, e Path
	p.MoveTo(2, 2)
	p.LineTo(60, 44)
	p.LineTo(5, 40)
	e.Ellipse(32, 24, 20, 15)
	for _, w := range []float64{0, 3} {
		img := image.NewRGBA(clip)
		c := NewCanvas(img)
		c.ClipPath(&e, Identity, NonZero)
		c.Stroke(&p, Identity, &StrokeStyle{Width: w}, white)
		got := alphaOfRGBA(img)
		want := mulMasks(strokeMask(&p, Identity, &StrokeStyle{Width: w}, clip), renderMask(&e, Identity, clip, NonZero))
		if mean, max := diff(got, want); mean > 0.1 || max > 3 {
			t.Errorf("stroke %v clip: mean %.3f max %d", w, mean, max)
		}
	}
}

func TestBlend(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 1))
	for i := range img.Pix {
		img.Pix[i] = []uint8{40, 80, 120, 200}[i%4]
	}
	c := NewCanvas(img)
	var p Path
	p.Rect(0, 0, 4, 1)
	col := color.RGBA{100, 50, 0, 128} // premultiplied
	c.Fill(&p, Identity, NonZero, &Paint{Color: col})
	for ch, d := range []float64{40, 80, 120, 200} {
		s := float64([]uint8{100, 50, 0, 128}[ch])
		want := s + d*(1-128.0/255)
		if got := float64(img.Pix[ch]); math.Abs(got-want) > 1 {
			t.Errorf("channel %d: %v want %.1f", ch, got, want)
		}
	}
}

type gradient struct{}

func (gradient) ShadeSpan(y, x int, dst []uint32) {
	for i := range dst {
		v := uint8((x + i) * 4)
		dst[i] = PackRGBA(color.RGBA{v, v, v, 255})
	}
}

func TestShader(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 8))
	c := NewCanvas(img)
	var p Path
	p.Rect(0, 0, 64, 8)
	c.Fill(&p, Identity, NonZero, &Paint{Shader: gradient{}})
	for x := 0; x < 64; x++ {
		if got := img.RGBAAt(x, 3).R; got != uint8(x*4) {
			t.Fatalf("pixel %d: %d want %d", x, got, x*4)
		}
	}
}

func TestCanvasNoAllocs(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	c := NewCanvas(img)
	var p, e Path
	p.MoveTo(10, 10)
	for i := 0; i < 20; i++ {
		p.CubicTo(float32(10+i*18), 280, float32(15+i*18), 20, float32(20+i*18), 150)
	}
	e.Ellipse(200, 150, 150, 100)
	paint := &Paint{Color: color.RGBA{0, 0, 128, 255}}
	thick := &StrokeStyle{Width: 3, Join: RoundJoin}
	hair := &StrokeStyle{Width: 0}
	run := func() {
		c.ClipRect(Rect{20.5, 20.5, 380.5, 280.5}, Identity)
		c.ClipPath(&e, Identity, NonZero)
		c.Fill(&p, Identity, EvenOdd, paint)
		c.Stroke(&p, Identity, thick, paint)
		c.Stroke(&p, Identity, hair, paint)
		c.PopClip()
		c.PopClip()
	}
	run()
	if allocs := testing.AllocsPerRun(10, run); allocs != 0 {
		t.Fatalf("canvas allocates %.1f times per run", allocs)
	}
}
