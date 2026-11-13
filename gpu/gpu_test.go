package gpu_test

import (
	"image"
	"image/color"
	"math"
	"os"
	"sync"
	"testing"

	_ "github.com/gogpu/wgpu/hal/allbackends"
	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/gpu"
	"github.com/timzifer/stilus/internal/scenes"
)

var (
	devOnce sync.Once
	dev     *gpu.Device
	devErr  error
)

// device opens one device for the tests and skips the test when there is
// none. STILUS_GPU_FALLBACK=1 requests wgpu's software adapter, which
// runs the shaders by interpreting SPIR-V: the scenes take minutes there,
// the primitives a fraction of a second (CI runs those).
func device(t testing.TB) *gpu.Device {
	devOnce.Do(func() {
		dev, devErr = gpu.Open(&gpu.Options{Fallback: os.Getenv("STILUS_GPU_FALLBACK") == "1"})
	})
	if devErr != nil {
		t.Skipf("no GPU device: %v", devErr)
	}
	return dev
}

var white = color.RGBA{255, 255, 255, 255}

func fill(img *image.RGBA, c color.RGBA) {
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
}

// deviation returns the largest and the mean channel difference.
func deviation(a, b []byte) (int, float64) {
	mx, sum := 0, 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		mx = max(mx, d)
		sum += d
	}
	return mx, float64(sum) / float64(len(a))
}

// cpuOnly lists scenes whose shaders read the destination from memory
// (LayerShader.Dst): they cannot be drawn onto a target. The layers scene
// blends Normal and reads only its layers.
var cpuOnly = map[string]bool{}

// TestScenes draws every scene on the CPU and on a target and bounds the
// deviation.
func TestScenes(t *testing.T) {
	d := device(t)
	const dpi = 72
	size := scenes.Size(dpi)
	tg, err := d.NewTarget(size.Dx(), size.Dy())
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(size)
	got := image.NewRGBA(size)
	var c stilus.Canvas
	for _, s := range scenes.All() {
		if cpuOnly[s.Name] {
			continue
		}
		t.Run(s.Name, func(t *testing.T) {
			fill(want, white)
			c.Reset(want, size)
			s.Draw(&c, dpi)
			tg.Clear(white)
			c.ResetCompositor(tg, tg.Bounds())
			s.Draw(&c, dpi)
			if c.Err() != nil {
				t.Fatal(c.Err())
			}
			if err := tg.ReadRGBA(got); err != nil {
				t.Fatal(err)
			}
			mx, mean := deviation(want.Pix, got.Pix)
			t.Logf("max %d/255, mean %.4f/255", mx, mean)
			if mx > maxDev || mean > 0.05 {
				t.Errorf("deviation max %d/255, mean %.4f/255", mx, mean)
			}
		})
	}
}

// maxDev bounds the deviation of a pixel: one 1/255 rounding per operation
// stacked on it, a few at most in the scenes.
const maxDev = 4

// TestPrimitives checks runs, merged runs, coverage, shaders and clipping
// at the target's edges against the CPU canvas.
func TestPrimitives(t *testing.T) {
	d := device(t)
	const w, h = 97, 61
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	var p, q, e stilus.Path
	p.Rect(-10, 3, 60, 40) // pixel-aligned, crosses the left edge: merged runs
	q.MoveTo(20, -5)
	q.LineTo(110, 30)
	q.LineTo(40, 70)
	q.Close()
	e.Ellipse(50, 30, 30, 18)
	var g stilus.LinearGradient
	g.Ramp = []uint32{stilus.PackRGBA(color.RGBA{200, 0, 0, 255}), stilus.PackRGBA(color.RGBA{0, 0, 120, 160})}
	g.Alpha = 255
	g.Extend = [2]bool{true, true}
	g.Set(0, 0, w, h, stilus.Identity)
	ops := func(c *stilus.Canvas) {
		c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 90, 0, 255}})
		c.Fill(&q, stilus.Identity, stilus.EvenOdd, &stilus.Paint{Color: color.RGBA{60, 0, 0, 128}})
		c.Stroke(&e, stilus.Identity, &stilus.StrokeStyle{Width: 3, Dash: []float64{7, 3}}, &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}})
		c.ClipPath(&e, stilus.Identity, stilus.NonZero)
		c.Fill(&q, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: &g})
		c.PopClip()
	}
	fill(want, white)
	c := stilus.NewCanvas(want)
	ops(c)
	tg.Clear(white)
	c.ResetCompositor(tg, tg.Bounds())
	ops(c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	mx, mean := deviation(want.Pix, got.Pix)
	t.Logf("max %d/255, mean %.4f/255", mx, mean)
	if mx > 2 {
		t.Errorf("deviation max %d/255, mean %.4f/255", mx, mean)
	}
	// A sub-rectangle reads the same pixels.
	sub := image.NewRGBA(image.Rect(13, 7, 50, 33))
	if err := tg.ReadRGBA(sub); err != nil {
		t.Fatal(err)
	}
	for y := sub.Rect.Min.Y; y < sub.Rect.Max.Y; y++ {
		for x := sub.Rect.Min.X; x < sub.Rect.Max.X; x++ {
			if sub.RGBAAt(x, y) != got.RGBAAt(x, y) {
				t.Fatalf("sub-rectangle differs at (%d,%d)", x, y)
			}
		}
	}
}

// TestFlushes: drawing across several flushes composites onto what is
// there, and Clear starts over.
func TestFlushes(t *testing.T) {
	d := device(t)
	tg, err := d.NewTarget(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	var c stilus.Canvas
	var p stilus.Path
	p.Rect(0, 0, 8, 8)
	half := &stilus.Paint{Color: color.RGBA{0, 0, 128, 128}}
	c.ResetCompositor(tg, tg.Bounds())
	c.Fill(&p, stilus.Identity, stilus.NonZero, half)
	if err := tg.Flush(); err != nil {
		t.Fatal(err)
	}
	c.Fill(&p, stilus.Identity, stilus.NonZero, half)
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if err := tg.ReadRGBA(img); err != nil {
		t.Fatal(err)
	}
	// 128 + 128·127/255 = 191.75
	if a := img.RGBAAt(3, 3); a.A < 191 || a.A > 192 || a.B != a.A {
		t.Fatalf("two halves: %v", a)
	}
	tg.Clear(color.RGBA{})
	if err := tg.ReadRGBA(img); err != nil {
		t.Fatal(err)
	}
	if a := img.RGBAAt(3, 3); a != (color.RGBA{}) {
		t.Fatalf("after Clear: %v", a)
	}
}

// BenchmarkScenes compares drawing a scene at 150 dpi onto an image with
// recording it onto a target and flushing it, and with reading it back.
func BenchmarkScenes(b *testing.B) {
	d := device(b)
	const dpi = 150
	size := scenes.Size(dpi)
	tg, err := d.NewTarget(size.Dx(), size.Dy())
	if err != nil {
		b.Fatal(err)
	}
	defer tg.Close()
	img := image.NewRGBA(size)
	var c stilus.Canvas
	for _, s := range scenes.All() {
		b.Run(s.Name+"/cpu", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				fill(img, white)
				c.Reset(img, size)
				s.Draw(&c, dpi)
			}
		})
		b.Run(s.Name+"/gpu", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				tg.Clear(white)
				c.ResetCompositor(tg, tg.Bounds())
				s.Draw(&c, dpi)
				if err := tg.Flush(); err != nil {
					b.Fatal(err)
				}
			}
			// Waits for the GPU once, outside the timing.
			b.StopTimer()
			_ = tg.ReadRGBA(image.NewRGBA(image.Rect(0, 0, 1, 1)))
			b.StartTimer()
		})
		b.Run(s.Name+"/gpu+read", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				tg.Clear(white)
				c.ResetCompositor(tg, tg.Bounds())
				s.Draw(&c, dpi)
				if err := tg.ReadRGBA(img); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestStrips draws lines at many angles and widths, opaque and
// translucent, under a clip with fractional borders and within an
// elliptic mask: the strips whose coverage the GPU computes against the
// canvas's bytes.
func TestStrips(t *testing.T) {
	for _, masked := range []bool{false, true} {
		t.Run(map[bool]string{false: "rect", true: "mask"}[masked], func(t *testing.T) { testStrips(t, masked) })
	}
}

func testStrips(t *testing.T, masked bool) {
	d := device(t)
	const w, h = 160, 120
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	paints := []*stilus.Paint{
		{Color: color.RGBA{0, 0, 0, 255}},
		{Color: color.RGBA{0, 40, 140, 255}},
		{Color: color.RGBA{90, 0, 0, 160}},
	}
	ops := func(c *stilus.Canvas) {
		c.ClipRect(stilus.Rect{X0: 3.4, Y0: 2.7, X1: 151.6, Y1: 113.3}, stilus.Identity)
		if masked {
			var e stilus.Path
			e.Ellipse(82, 58, 70, 47)
			c.ClipPath(&e, stilus.Identity, stilus.NonZero)
		}
		for i := 0; i < 48; i++ {
			a := float64(i) * math.Pi / 48
			cx, cy := 80+float64(i%7)*3, 60+float64(i%5)*2
			var p stilus.Path
			p.MoveTo(float32(cx-90*math.Cos(a)), float32(cy-90*math.Sin(a)))
			p.LineTo(float32(cx+90*math.Cos(a)), float32(cy+90*math.Sin(a)))
			st := &stilus.StrokeStyle{Width: 0.3 + float64(i%6)*0.7}
			c.Stroke(&p, stilus.Identity, st, paints[i%len(paints)])
		}
		if masked {
			c.PopClip()
		}
		c.PopClip()
	}
	fill(want, white)
	c := stilus.NewCanvas(want)
	ops(c)
	tg.Clear(white)
	c.ResetCompositor(tg, tg.Bounds())
	ops(c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	mx, mean := deviation(want.Pix, got.Pix)
	t.Logf("max %d/255, mean %.4f/255", mx, mean)
	if mx > 2 || mean > 0.05 {
		t.Errorf("deviation max %d/255, mean %.4f/255", mx, mean)
	}
}

// TestGradients draws linear and radial gradients the GPU computes, with
// and without knots, extended or not, translucent, as fills, as strokes
// (strips) and within a mask, against the canvas's bytes.
func TestGradients(t *testing.T) {
	d := device(t)
	const w, h = 180, 140
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	ramp := make(stilus.Ramp, 32)
	for i := range ramp {
		ramp[i] = stilus.PackRGBA(color.RGBA{uint8(i * 8), uint8(200 - i*5), 90, 255})
	}
	knots := make([]float32, len(ramp))
	for i := range knots {
		knots[i] = float32(i*i) / float32((len(knots)-1)*(len(knots)-1))
	}
	knots[11] = knots[10]
	var shaders []stilus.Shader
	for i := 0; i < 8; i++ {
		var g stilus.Gradient
		g.Ramp, g.Alpha = ramp, []uint8{255, 170}[i%2]
		g.Extend = [2]bool{i%4 != 3, i%4 != 2}
		g.Outside = stilus.PackRGBA(color.RGBA{0, 0, 60, 60})
		if i%3 == 0 {
			g.Knots = knots
		}
		m := stilus.Matrix{1.3, 0.4, -0.3, 1.1, 5, 3}
		if i < 4 {
			lg := &stilus.LinearGradient{Gradient: g}
			lg.Set(10, 10, 60+float64(i)*10, 40, m)
			shaders = append(shaders, lg)
		} else {
			rg := &stilus.RadialGradient{Gradient: g}
			rg.Set(40+float64(i)*3, 40, float64(i%2)*5, 60, 50, 30+float64(i)*4, m)
			shaders = append(shaders, rg)
		}
	}
	ops := func(c *stilus.Canvas) {
		for i, sh := range shaders {
			p := &stilus.Paint{Shader: sh}
			var e stilus.Path
			x, y := float32(10+(i%4)*42), float32(10+(i/4)*62)
			e.Ellipse(x+20, y+25, 21, 26)
			c.Fill(&e, stilus.Identity, stilus.NonZero, p)
			var l stilus.Path
			l.MoveTo(x, y+55)
			l.LineTo(x+40, y)
			c.Stroke(&l, stilus.Identity, &stilus.StrokeStyle{Width: 2.5}, p)
		}
		var mask stilus.Path
		mask.Ellipse(90, 70, 70, 50)
		c.ClipPath(&mask, stilus.Identity, stilus.NonZero)
		var r stilus.Path
		r.Rect(0, 0, w, h)
		c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: shaders[5]})
		c.PopClip()
	}
	fill(want, white)
	c := stilus.NewCanvas(want)
	ops(c)
	tg.Clear(white)
	c.ResetCompositor(tg, tg.Bounds())
	ops(c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	mx, mean := deviation(want.Pix, got.Pix)
	t.Logf("max %d/255, mean %.4f/255", mx, mean)
	// float32 puts a pixel whose parameter lies at the boundary of two
	// ramp entries now and then on the other entry: one ramp step (8/255
	// here) on a few pixels, at most 2/255 elsewhere.
	steps := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a, b := want.RGBAAt(x, y), got.RGBAAt(x, y)
			if max(absd(a.R, b.R), absd(a.G, b.G), absd(a.B, b.B), absd(a.A, b.A)) > 2 {
				steps++
			}
		}
	}
	t.Logf("%d pixels a ramp step off", steps)
	if mx > 8 || steps > w*h/1000 || mean > 0.05 {
		t.Errorf("deviation max %d/255, mean %.4f/255, %d pixels beyond 2/255", mx, mean, steps)
	}
}

func absd(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// TestImages draws images the GPU samples: RGBA, indexed and bit planes,
// nearest and bilinear, magnified, reduced (mip levels) and rotated,
// repeating tiles, translucent, through a mask and as stencils, as fills
// and strokes, against the canvas's bytes. float32 coordinates put a
// pixel near a texel border on the other texel now and then.
func TestImages(t *testing.T) {
	d := device(t)
	const w, h = 200, 150
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	const n = 24
	pix := make([]uint32, n*n)
	idx := make([]uint8, n*n)
	bits := make([]uint8, 3*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			pix[y*n+x] = stilus.PackRGBA(color.RGBA{uint8(x * 10), uint8(y * 10), 120, 255})
			idx[y*n+x] = uint8((x*11 + y*7) % 256)
		}
		bits[y*3] = 0xa5
		bits[y*3+1] = uint8(y * 9)
	}
	var pal stilus.Palette
	for i := range pal {
		pal[i] = stilus.PackRGBA(color.RGBA{uint8(i), 80, uint8(255 - i), 255})
	}
	texs := []*stilus.Texture{
		stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: pix}),
		stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneIndex, W: n, H: n, Stride: n, Pix8: idx, Pal: &pal}),
		stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneBits, W: n, H: n, Stride: 3, Pix8: bits, Pal: &pal}),
	}
	mask := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneIndex, W: n, H: n, Stride: n, Pix8: idx, Pal: stilus.AlphaPalette})
	shaders := make([]*stilus.ImageShader, 12)
	for i := range shaders {
		s := []float64{0.4, 1.3, 2.7}[i%3]
		x, y := float64(5+(i%4)*48), float64(5+(i/4)*48)
		m := stilus.Scale(s, s).Mul(stilus.Translate(x, y))
		if i%5 == 2 {
			m = stilus.Scale(s, s).Mul(stilus.Rotate(0.4)).Mul(stilus.Translate(x+10, y))
		}
		sh := &stilus.ImageShader{}
		tex := texs[i%3]
		switch i % 4 {
		case 0:
			sh.SetImage(tex, m, i%2 == 0, []uint8{255, 150}[i/4%2])
		case 1:
			sh.SetImageWrap(tex, m, i%3 == 1, 255)
		case 2:
			sh.SetImage(tex, m, true, 255)
			sh.SetMask(mask, m, false)
		default:
			sh.SetColor(stilus.PackRGBA(color.RGBA{0, 30, 110, 255}))
			sh.SetMaskWrap(mask, m, i%2 == 1)
		}
		shaders[i] = sh
	}
	ops := func(c *stilus.Canvas) {
		for i, sh := range shaders {
			p := &stilus.Paint{Shader: sh}
			var r stilus.Path
			x, y := float32(5+(i%4)*48), float32(5+(i/4)*48)
			r.Rect(x, y, 44, 44)
			c.Fill(&r, stilus.Identity, stilus.NonZero, p)
			var l stilus.Path
			l.MoveTo(x, y+44)
			l.LineTo(x+44, y)
			c.Stroke(&l, stilus.Identity, &stilus.StrokeStyle{Width: 3}, &stilus.Paint{Shader: shaders[(i+1)%len(shaders)]})
		}
	}
	fill(want, white)
	c := stilus.NewCanvas(want)
	ops(c)
	tg.Clear(white)
	c.ResetCompositor(tg, tg.Bounds())
	ops(c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	mx, mean := deviation(want.Pix, got.Pix)
	off := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a, b := want.RGBAAt(x, y), got.RGBAAt(x, y)
			if max(absd(a.R, b.R), absd(a.G, b.G), absd(a.B, b.B), absd(a.A, b.A)) > 2 {
				off++
			}
		}
	}
	t.Logf("max %d/255, mean %.4f/255, %d pixels beyond 2/255", mx, mean, off)
	if off > w*h/500 || mean > 0.1 {
		t.Errorf("deviation max %d/255, mean %.4f/255, %d pixels beyond 2/255", mx, mean, off)
	}
}

// TestMeshes draws Gouraud meshes the GPU shades: vertex colours and ramp
// parameters, overlapping, flat and thin triangles, translucent, as fills,
// as strokes and within a mask, against the canvas's bytes.
func TestMeshes(t *testing.T) {
	d := device(t)
	const w, h = 200, 150
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	ramp := make(stilus.Ramp, 64)
	for i := range ramp {
		ramp[i] = stilus.PackRGBA(color.RGBA{uint8(i * 4), 60, uint8(255 - i*4), 255})
	}
	shaders := make([]*stilus.MeshShader, 4)
	for i := range shaders {
		var tris []stilus.MeshTriangle
		ox, oy := float32(10+(i%2)*95), float32(8+(i/2)*72)
		for k := 0; k < 40; k++ {
			var tri stilus.MeshTriangle
			cx, cy := ox+float32((k*37)%80), oy+float32((k*23)%60)
			for v := range tri {
				a := uint8(150 + (k*v*13)%106)
				tri[v] = stilus.MeshVertex{
					X: cx + float32((k*7+v*19)%30) - 15, Y: cy + float32((k*11+v*29)%26) - 13,
					C: stilus.PackRGBA(color.RGBA{uint8(int(a) * v / 2), uint8(int(a) * (2 - v) / 2), a / 3, a}),
					T: float32(v) / 2,
				}
			}
			if k%9 == 0 {
				tri[1].C, tri[2].C = tri[0].C, tri[0].C
			}
			tris = append(tris, tri)
		}
		var rp stilus.Ramp
		if i%2 == 1 {
			rp = ramp
		}
		sh := &stilus.MeshShader{Alpha: []uint8{255, 255, 190, 120}[i]}
		sh.Set(tris, stilus.Matrix{1.05, 0.1, -0.08, 0.98, 0, 0}, rp)
		shaders[i] = sh
	}
	ops := func(c *stilus.Canvas) {
		for i, sh := range shaders {
			p := &stilus.Paint{Shader: sh}
			var r stilus.Path
			x, y := float32(5+(i%2)*95), float32(3+(i/2)*72)
			r.Rect(x, y, 92, 70)
			if i == 3 {
				var e stilus.Path
				e.Ellipse(x+46, y+35, 40, 30)
				c.ClipPath(&e, stilus.Identity, stilus.NonZero)
			}
			c.Fill(&r, stilus.Identity, stilus.NonZero, p)
			if i == 3 {
				c.PopClip()
			}
			var l stilus.Path
			l.MoveTo(x, y+70)
			l.LineTo(x+92, y)
			c.Stroke(&l, stilus.Identity, &stilus.StrokeStyle{Width: 4}, &stilus.Paint{Shader: shaders[(i+1)%4]})
		}
	}
	fill(want, white)
	c := stilus.NewCanvas(want)
	ops(c)
	tg.Clear(white)
	c.ResetCompositor(tg, tg.Bounds())
	ops(c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	mx, mean := deviation(want.Pix, got.Pix)
	off := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a, b := want.RGBAAt(x, y), got.RGBAAt(x, y)
			if max(absd(a.R, b.R), absd(a.G, b.G), absd(a.B, b.B), absd(a.A, b.A)) > 2 {
				off++
			}
		}
	}
	t.Logf("max %d/255, mean %.4f/255, %d pixels beyond 2/255", mx, mean, off)
	if off > w*h/500 || mean > 0.1 {
		t.Errorf("deviation max %d/255, mean %.4f/255, %d pixels beyond 2/255", mx, mean, off)
	}
}

// TestGlyphMasks draws cached glyphs, whose masks the GPU paints, at
// subpixel positions, in opaque and translucent colours, through a mask
// clip and a fractional clip, against the canvas's bytes; and a mask
// shader drawn directly.
func TestGlyphMasks(t *testing.T) {
	d := device(t)
	const w, h = 220, 120
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	var glyphs [5]stilus.Path
	for i := range glyphs {
		g := &glyphs[i]
		g.MoveTo(0, 0)
		g.LineTo(6+float32(i), 0)
		g.LineTo(3, -9-float32(i))
		g.Close()
		g.Ellipse(3, -4, 2, 3)
	}
	paints := []*stilus.Paint{
		{Color: color.RGBA{0, 0, 0, 255}},
		{Color: color.RGBA{0, 40, 140, 255}},
		{Color: color.RGBA{90, 0, 0, 150}},
	}
	mask := image.NewAlpha(image.Rect(0, 0, 20, 14))
	for i := range mask.Pix {
		mask.Pix[i] = uint8(i * 7)
	}
	ops := func(c *stilus.Canvas) {
		var gc stilus.GlyphCache
		c.ClipRect(stilus.Rect{X0: 2.5, Y0: 1.7, X1: 217.3, Y1: 118.6}, stilus.Identity)
		for i := 0; i < 300; i++ {
			x, y := 4+float64(i%30)*7.13, 14+float64(i/30)*10.37
			if i == 150 {
				var e stilus.Path
				e.Ellipse(110, 80, 90, 30)
				c.ClipPath(&e, stilus.Identity, stilus.NonZero)
			}
			gc.FillGlyph(c, 1, int32(i%5), &glyphs[i%5], stilus.Matrix{1, 0, 0, 1, x, y}, paints[i%3])
		}
		c.PopClip()
		ms := &stilus.MaskShader{Mask: mask, X: 30, Y: 40, Color: stilus.PackRGBA(color.RGBA{0, 90, 0, 200})}
		var r stilus.Path
		r.Rect(30, 40, 20, 14)
		c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: ms})
		c.PopClip()
	}
	fill(want, white)
	c := stilus.NewCanvas(want)
	ops(c)
	tg.Clear(white)
	c.ResetCompositor(tg, tg.Bounds())
	ops(c)
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	mx, mean := deviation(want.Pix, got.Pix)
	t.Logf("max %d/255, mean %.4f/255", mx, mean)
	if mx > 2 || mean > 0.05 {
		t.Errorf("deviation max %d/255, mean %.4f/255", mx, mean)
	}
}
