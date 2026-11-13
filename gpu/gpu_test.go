package gpu_test

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/gogpu/wgpu"
	_ "github.com/gogpu/wgpu/hal/allbackends"
	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/gpu"
	"github.com/timzifer/stilus/internal/scenes"
)

var (
	logAdapter bool
	devOnce    sync.Once
	dev        *gpu.Device
	devErr     error
)

// device opens one device for the tests and skips the test when there is
// none. STILUS_GPU_FALLBACK=1 requests wgpu's software adapter, which
// runs the shaders by interpreting SPIR-V: the scenes take minutes there,
// the primitives a fraction of a second (CI runs those).
func device(t testing.TB) *gpu.Device {
	devOnce.Do(func() {
		opts := &gpu.Options{Fallback: os.Getenv("STILUS_GPU_FALLBACK") == "1"}
		switch os.Getenv("STILUS_GPU_BACKEND") {
		case "vulkan":
			opts.Backends = wgpu.BackendsVulkan
		case "gl":
			opts.Backends = wgpu.BackendsGL
		}
		dev, devErr = gpu.Open(opts)
		if devErr == nil {
			logAdapter = true
		}
	})
	if logAdapter {
		logAdapter = false
		t.Logf("adapter: %s", dev.AdapterName())
	}
	if devErr != nil {
		if os.Getenv("STILUS_GPU_REQUIRE") == "1" {
			t.Fatalf("no GPU device: %v", devErr)
		}
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

// drawGroup draws the content of a layer: translucent and opaque discs
// and a bar, in the cell at (x, y).
func drawGroup(c *stilus.Canvas, x, y float32, i int) {
	var p stilus.Path
	p.Ellipse(x+18, y+18, 14, 12)
	c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{uint8(40 + i*12), 20, 150, 200}})
	var q stilus.Path
	q.Ellipse(x+32, y+24, 12, 13)
	c.Fill(&q, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{230, uint8(200 - i*9), 30, 255}})
	var b stilus.Path
	b.Rect(x+4, y+30, 44, 6)
	c.Fill(&b, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 70, 60, 130}})
}

// drawBackdrop draws what the layers are composited onto.
func drawBackdrop(c *stilus.Canvas, w, h int) {
	for i := 0; i < 12; i++ {
		var p stilus.Path
		p.Rect(float32(i*w/12), 0, float32(w/12)+1, float32(h))
		c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{uint8(i * 20), uint8(240 - i*15), uint8(60 + i*10), 255}})
	}
	var e stilus.Path
	e.Ellipse(float32(w)/2, float32(h)/2, float32(w)/3, float32(h)/4)
	c.Fill(&e, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 0, 0, 90}})
}

type layerCase struct {
	mode        stilus.BlendMode
	alpha       uint8
	mask        bool
	nonIsolated bool
	subtractive bool
	black       bool
}

func layerCases() []layerCase {
	var cs []layerCase
	for m := stilus.BlendNormal; m <= stilus.BlendLuminosity; m++ {
		cs = append(cs, layerCase{mode: m, alpha: []uint8{255, 170}[int(m)%2], mask: m%3 == 1})
	}
	cs = append(cs,
		layerCase{mode: stilus.BlendMultiply, alpha: 255, nonIsolated: true},
		layerCase{mode: stilus.BlendNormal, alpha: 200, nonIsolated: true, mask: true},
		layerCase{mode: stilus.BlendScreen, alpha: 255, subtractive: true},
		layerCase{mode: stilus.BlendColorBurn, alpha: 255, subtractive: true},
		layerCase{mode: stilus.BlendHue, alpha: 255, black: true},
		layerCase{mode: stilus.BlendLuminosity, alpha: 220, black: true, mask: true},
	)
	return cs
}

// TestLayers composites layers with every blend mode, alpha, a mask, a
// non-isolated group's backdrop removed, ink amounts and a CMYK pair's
// black image onto a target, as stilus.LayerShader with images (want:
// the CPU canvas, which reads the backdrop from its image) and as
// gpu.Layer with targets.
func TestLayers(t *testing.T) {
	testLayers(t)
}

// TestLayerStrips is TestLayers with a layer buffer too small for any
// layer's copies at once: every layer is drawn in strips of a few rows.
func TestLayerStrips(t *testing.T) {
	defer gpu.SetLayerLimit(16 << 10)()
	testLayers(t)
}

func testLayers(t *testing.T) {
	d := device(t)
	const cw, ch = 52, 44
	cases := layerCases()
	cols := 6
	w, h := cols*cw, (len(cases)+cols-1)/cols*ch
	bounds := image.Rect(0, 0, w, h)
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	want := image.NewRGBA(bounds)
	var c stilus.Canvas
	c.Reset(want, bounds)
	drawBackdrop(&c, w, h)
	mask := image.NewAlpha(bounds)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mask.Pix[mask.PixOffset(x, y)] = uint8((x*5 + y*3) % 256)
		}
	}
	// The layers in images, one per cell, and the shaders for the CPU.
	shaders := make([]*stilus.LayerShader, len(cases))
	cells := make([]image.Rectangle, len(cases))
	for i, lc := range cases {
		x, y := (i%cols)*cw, (i/cols)*ch
		r := image.Rect(x, y, x+cw, y+ch)
		cells[i] = r
		src := image.NewRGBA(r)
		sh := &stilus.LayerShader{Src: src, Dst: want, Alpha: lc.alpha, Blend: lc.mode, Subtractive: lc.subtractive, Black: lc.black}
		if lc.nonIsolated {
			// The group drawn onto a copy of its backdrop, and alone.
			copyRect(src, want, r)
			initial := image.NewRGBA(r)
			copyRect(initial, want, r)
			alone := image.NewRGBA(r)
			var g stilus.Canvas
			g.Reset(alone, r)
			drawGroup(&g, float32(x), float32(y), i)
			sh.Initial, sh.Alone = initial, alone
		}
		var g stilus.Canvas
		g.Reset(src, r)
		drawGroup(&g, float32(x), float32(y), i)
		if lc.mask {
			sh.Mask = mask
		}
		shaders[i] = sh
	}
	ops := func(c *stilus.Canvas, paints []stilus.Shader) {
		for i := range cases {
			var p stilus.Path
			r := cells[i]
			// The fill reaches past half pixels: partial coverage at the
			// cell's edges blends exactly.
			p.Rect(float32(r.Min.X)+2.5, float32(r.Min.Y)+1.5, float32(r.Dx())-4, float32(r.Dy())-3)
			c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: paints[i]})
		}
	}
	cpu := make([]stilus.Shader, len(cases))
	for i := range shaders {
		cpu[i] = shaders[i]
	}
	ops(&c, cpu)

	var recs []*gpu.Recorder
	check := func(name string, paints []stilus.Shader) {
		t.Helper()
		worst := 0
		defer func() { t.Logf("%s: max %d/255 over %d cases", name, worst, len(cases)) }()
		got := image.NewRGBA(bounds)
		tg.Clear(color.RGBA{})
		if recs == nil {
			c.ResetCompositor(tg, tg.Bounds())
			drawBackdrop(&c, w, h)
			ops(&c, paints)
		} else {
			// Bands at once, cutting the layers' cells.
			var wg sync.WaitGroup
			for i, r := range recs {
				band := image.Rect(0, h*i/len(recs), w, h*(i+1)/len(recs))
				wg.Add(1)
				go func() {
					defer wg.Done()
					var c stilus.Canvas
					c.ResetCompositor(r, band)
					drawBackdrop(&c, w, h)
					ops(&c, paints)
				}()
			}
			wg.Wait()
			for _, r := range recs {
				if r.Err() != nil {
					t.Fatal(r.Err())
				}
			}
		}
		if err := tg.ReadRGBA(got); err != nil {
			t.Fatal(err)
		}
		if tg.Err() != nil {
			t.Fatal(tg.Err())
		}
		for i := range cases {
			r := cells[i]
			mx, sum, n := 0, 0, 0
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					a, b := want.RGBAAt(x, y), got.RGBAAt(x, y)
					dd := max(absd(a.R, b.R), absd(a.G, b.G), absd(a.B, b.B), absd(a.A, b.A))
					mx = max(mx, dd)
					sum += dd
					n++
				}
			}
			mean := float64(sum) / float64(n)
			worst = max(worst, mx)
			if mx > 3 || mean > 0.2 {
				t.Errorf("%s case %d %+v: max %d/255, mean %.3f/255", name, i, cases[i], mx, mean)
			}
		}
	}
	check("LayerShader", cpu)

	// The same layers drawn into targets.
	gpuPaints := make([]stilus.Shader, len(cases))
	var keep []*gpu.Target
	newTarget := func(img *image.RGBA) *gpu.Target {
		lt, err := d.NewTarget(img.Rect.Dx(), img.Rect.Dy())
		if err != nil {
			t.Fatal(err)
		}
		keep = append(keep, lt)
		var ic stilus.ImageShader
		tex := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: img.Rect.Dx(), H: img.Rect.Dy(), Stride: img.Rect.Dx(), Pix32: pixels32(img)})
		ic.SetImage(tex, stilus.Identity, false, 255)
		var lc stilus.Canvas
		lc.ResetCompositor(lt, lt.Bounds())
		var p stilus.Path
		p.Rect(0, 0, float32(img.Rect.Dx()), float32(img.Rect.Dy()))
		lt.Clear(color.RGBA{})
		lc.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: &ic})
		return lt
	}
	for i, sh := range shaders {
		r := cells[i]
		l := &gpu.Layer{Src: newTarget(sh.Src), X: r.Min.X, Y: r.Min.Y, Alpha: sh.Alpha, Blend: sh.Blend, Subtractive: sh.Subtractive, Black: sh.Black}
		if sh.Mask != nil {
			// The mask's alpha over the cell, in a target at (X, Y).
			img := image.NewRGBA(r)
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					img.Pix[img.PixOffset(x, y)+3] = mask.Pix[mask.PixOffset(x, y)]
				}
			}
			l.Mask = newTarget(img)
		}
		if sh.Initial != nil {
			l.Initial, l.Alone = newTarget(sh.Initial), newTarget(sh.Alone)
		}
		gpuPaints[i] = l
	}
	check("Layer", gpuPaints)

	// The same through five recorders at once, whose bands cut the cells.
	for range 5 {
		r, err := tg.NewRecorder()
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	check("LayerShader, recorders", cpu)
	check("Layer, recorders", gpuPaints)
	defer func() {
		for _, k := range keep {
			k.Close()
		}
	}()
}

func copyRect(dst, src *image.RGBA, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(dst.Pix[dst.PixOffset(r.Min.X, y):dst.PixOffset(r.Max.X, y)], src.Pix[src.PixOffset(r.Min.X, y):src.PixOffset(r.Max.X, y)])
	}
}

// pixels32 returns img's pixels as PackRGBA words.
func pixels32(img *image.RGBA) []uint32 {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	p := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)
			p[y*w+x] = stilus.PackRGBA(c)
		}
	}
	return p
}

// TestPair draws a CMYK destination, two targets of (C, M, Y, A) and
// (K, K, K, A), through one canvas: fills, strokes (strips), a gradient,
// cached glyphs and a layer pair blended on ink amounts, against
// Canvas.ResetPair on images.
func TestPair(t *testing.T) {
	d := device(t)
	const w, h = 160, 110
	bounds := image.Rect(0, 0, w, h)
	ta, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer ta.Close()
	tb, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tb.Close()
	wa, wb := image.NewRGBA(bounds), image.NewRGBA(bounds)
	ink := func(c, m, y, k, a uint8) *stilus.Paint {
		return &stilus.Paint{Color: color.RGBA{c, m, y, a}, Second: &stilus.Paint{Color: color.RGBA{k, k, k, a}}}
	}
	ramp := stilus.Ramp{stilus.PackRGBA(color.RGBA{200, 0, 40, 255}), stilus.PackRGBA(color.RGBA{0, 160, 90, 255})}
	kramp := stilus.Ramp{stilus.PackRGBA(color.RGBA{10, 10, 10, 255}), stilus.PackRGBA(color.RGBA{120, 120, 120, 255})}
	var g1, g2 stilus.LinearGradient
	g1.Ramp, g1.Alpha, g1.Extend = ramp, 255, [2]bool{true, true}
	g2.Ramp, g2.Alpha, g2.Extend = kramp, 255, [2]bool{true, true}
	g1.Set(0, 0, w, 0, stilus.Identity)
	g2.Set(0, 0, w, 0, stilus.Identity)
	// A layer pair: the CMY layer on ink amounts, the K layer as the
	// black image, both Multiply.
	lr := image.Rect(60, 20, 140, 90)
	la, lb := image.NewRGBA(lr), image.NewRGBA(lr)
	var lc stilus.Canvas
	lc.ResetPair(la, lb, lr)
	var e stilus.Path
	e.Ellipse(100, 55, 35, 30)
	lc.Fill(&e, stilus.Identity, stilus.NonZero, ink(30, 160, 200, 40, 220))
	ops := func(c *stilus.Canvas, dsta, dstb *image.RGBA) {
		var r stilus.Path
		r.Rect(5, 5, 150, 100)
		c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: &g1, Second: &stilus.Paint{Shader: &g2}})
		for i := 0; i < 10; i++ {
			var l stilus.Path
			l.MoveTo(float32(10+i*14), 4)
			l.LineTo(float32(20+i*14), 106)
			c.Stroke(&l, stilus.Identity, &stilus.StrokeStyle{Width: 0.5 + float64(i)*0.4}, ink(uint8(i*20), 200, 0, uint8(i*25), 255))
		}
		var gc stilus.GlyphCache
		var g stilus.Path
		g.MoveTo(0, 0)
		g.LineTo(6, 0)
		g.LineTo(3, -8)
		g.Close()
		for i := 0; i < 20; i++ {
			gc.FillGlyph(c, 1, 1, &g, stilus.Matrix{1, 0, 0, 1, 8 + float64(i)*7.3, 100}, ink(0, 0, 0, 255, 255))
		}
		ls := &stilus.LayerShader{Src: la, Dst: dsta, Alpha: 255, Blend: stilus.BlendMultiply, Subtractive: true}
		lk := &stilus.LayerShader{Src: lb, Dst: dstb, Alpha: 255, Blend: stilus.BlendMultiply, Black: true}
		var lp stilus.Path
		lp.Rect(float32(lr.Min.X), float32(lr.Min.Y), float32(lr.Dx()), float32(lr.Dy()))
		c.Fill(&lp, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: ls, Second: &stilus.Paint{Shader: lk}})
	}
	var c stilus.Canvas
	c.ResetPair(wa, wb, bounds)
	ops(&c, wa, wb)
	ta.Clear(color.RGBA{})
	tb.Clear(color.RGBA{})
	c.ResetCompositorPair(ta, tb, bounds)
	ops(&c, nil, nil)
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	for i, p := range [2]struct {
		tg   *gpu.Target
		want *image.RGBA
	}{{ta, wa}, {tb, wb}} {
		got := image.NewRGBA(bounds)
		if err := p.tg.ReadRGBA(got); err != nil {
			t.Fatal(err)
		}
		if p.tg.Err() != nil {
			t.Fatal(p.tg.Err())
		}
		mx, mean := deviation(p.want.Pix, got.Pix)
		drawn := 0
		for k := 3; k < len(got.Pix); k += 4 {
			if got.Pix[k] != 0 {
				drawn++
			}
		}
		if drawn < w*h/2 {
			t.Errorf("image %d: only %d pixels drawn", i, drawn)
		}
		t.Logf("image %d: max %d/255, mean %.4f/255, %d pixels drawn", i, mx, mean, drawn)
		if mx > 3 || mean > 0.05 {
			t.Errorf("image %d: deviation max %d/255, mean %.4f/255", i, mx, mean)
		}
	}
}

// BenchmarkPhotoFrames draws a 2400 × 1600 photo reduced onto a target,
// frame after frame, as a viewer redrawing its page does.
func BenchmarkPhotoFrames(b *testing.B) {
	d := device(b)
	const pw, ph = 2400, 1600
	pix := make([]uint32, pw*ph)
	for i := range pix {
		pix[i] = stilus.PackRGBA(color.RGBA{uint8(i), uint8(i >> 8), uint8(i >> 16), 255})
	}
	tex := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: pw, H: ph, Stride: pw, Pix32: pix})
	tg, err := d.NewTarget(1200, 800)
	if err != nil {
		b.Fatal(err)
	}
	defer tg.Close()
	var sh stilus.ImageShader
	sh.SetImage(tex, stilus.Scale(0.5, 0.5), true, 255)
	var r stilus.Path
	r.Rect(0, 0, 1200, 800)
	var c stilus.Canvas
	px := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for i := 0; i < b.N; i++ {
		tg.Clear(white)
		c.ResetCompositor(tg, tg.Bounds())
		c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: &sh})
		if err := tg.ReadRGBA(px); err != nil {
			b.Fatal(err)
		}
	}
}

// TestTextureStore draws more texture planes than the store holds, in
// one frame and over several: the store is flushed and emptied when full,
// and planes drawn again in a later frame are found there.
func TestTextureStore(t *testing.T) {
	d := device(t)
	defer gpu.SetStoreLimit(1 << 20)()
	const w, h = 240, 160
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	var texs []*stilus.Texture
	for k := 0; k < 8; k++ {
		const n = 300 // 360 KB a plane: three fit
		pix := make([]uint32, n*n)
		for i := range pix {
			pix[i] = stilus.PackRGBA(color.RGBA{uint8(i*(k+1) + k*40), uint8(i >> 7), uint8(k * 30), 255})
		}
		texs = append(texs, stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: pix}))
	}
	ops := func(c *stilus.Canvas, order []int) {
		for j, k := range order {
			var sh stilus.ImageShader
			x, y := float64(j%4)*60, float64(j/4)*80
			sh.SetImage(texs[k], stilus.Translate(x-float64(k*7), y-float64(k*5)), false, 255)
			var r stilus.Path
			r.Rect(float32(x), float32(y), 60, 80)
			c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: &sh})
		}
	}
	for frame, order := range [][]int{{0, 1, 2, 3, 4, 5, 6, 7}, {7, 6, 5, 4, 3, 2, 1, 0}, {0, 0, 1, 1, 2, 2, 3, 3}} {
		want := image.NewRGBA(image.Rect(0, 0, w, h))
		got := image.NewRGBA(want.Rect)
		c := stilus.NewCanvas(want)
		ops(c, order)
		tg.Clear(color.RGBA{})
		c.ResetCompositor(tg, tg.Bounds())
		ops(c, order)
		if err := tg.ReadRGBA(got); err != nil {
			t.Fatal(err)
		}
		if tg.Err() != nil {
			t.Fatal(tg.Err())
		}
		if mx, mean := deviation(want.Pix, got.Pix); mx > 0 {
			t.Errorf("frame %d: max %d/255, mean %.4f/255", frame, mx, mean)
		}
	}
}

// TestRecorders draws every scene in four bands at once, one canvas and recorder per band on its own goroutine, and gets
// what the CPU canvas draws of the whole page; frames are drawn twice, so
// that the recorders' stores are found again.
func TestRecorders(t *testing.T) {
	d := device(t)
	const dpi, bands = 72, 4
	size := scenes.Size(dpi)
	tg, err := d.NewTarget(size.Dx(), size.Dy())
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	recs := make([]*gpu.Recorder, bands)
	for i := range recs {
		if recs[i], err = tg.NewRecorder(); err != nil {
			t.Fatal(err)
		}
	}
	want := image.NewRGBA(size)
	got := image.NewRGBA(size)
	// The scenes set their shaders up as they draw: one set per band, as
	// BenchmarkScenesParallel has it.
	sets := make([][]*scenes.Scene, bands)
	for i := range sets {
		sets[i] = scenes.All()
	}
	for k, s := range scenes.All() {
		// The software adapter interprets the shaders: two scenes there.
		if os.Getenv("STILUS_GPU_FALLBACK") == "1" && s.Name != "text-30000-cached" && s.Name != "layers-24" {
			continue
		}
		t.Run(s.Name, func(t *testing.T) {
			fill(want, white)
			var c stilus.Canvas
			c.Reset(want, size)
			s.Draw(&c, dpi)
			for frame := 0; frame < 2; frame++ {
				tg.Clear(white)
				var wg sync.WaitGroup
				for i, r := range recs {
					band := image.Rect(0, size.Dy()*i/bands, size.Dx(), size.Dy()*(i+1)/bands)
					wg.Add(1)
					go func() {
						defer wg.Done()
						var c stilus.Canvas
						c.ResetCompositor(r, band)
						sets[i][k].Draw(&c, dpi)
					}()
				}
				wg.Wait()
				if err := tg.ReadRGBA(got); err != nil {
					t.Fatal(err)
				}
				for _, r := range recs {
					if r.Err() != nil {
						t.Fatal(r.Err())
					}
				}
				mx, mean := deviation(want.Pix, got.Pix)
				if mx > 4 || mean > 0.05 {
					t.Errorf("frame %d: max %d/255, mean %.4f/255", frame, mx, mean)
				}
			}
		})
	}
}

// BenchmarkScenesBands draws the scenes at 150 dpi in bands at once, one
// canvas and recorder per band, with readback.
func BenchmarkScenesBands(b *testing.B) {
	d := device(b)
	const dpi = 150
	size := scenes.Size(dpi)
	tg, err := d.NewTarget(size.Dx(), size.Dy())
	if err != nil {
		b.Fatal(err)
	}
	defer tg.Close()
	img := image.NewRGBA(size)
	for _, bands := range []int{1, 4, 8} {
		recs := make([]*gpu.Recorder, bands)
		sets := make([][]*scenes.Scene, bands)
		for i := range recs {
			recs[i], _ = tg.NewRecorder()
			sets[i] = scenes.All()
		}
		canvases := make([]stilus.Canvas, bands)
		for k, s := range sets[0] {
			b.Run(fmt.Sprintf("%s/%d-bands", s.Name, bands), func(b *testing.B) {
				for n := 0; n < b.N; n++ {
					tg.Clear(white)
					var wg sync.WaitGroup
					for i, r := range recs {
						band := image.Rect(0, size.Dy()*i/bands, size.Dx(), size.Dy()*(i+1)/bands)
						wg.Add(1)
						go func() {
							defer wg.Done()
							canvases[i].ResetCompositor(r, band)
							sets[i][k].Draw(&canvases[i], dpi)
						}()
					}
					wg.Wait()
					if err := tg.ReadRGBA(img); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
		for _, r := range recs {
			r.Close()
		}
	}
}

// meshPage returns n meshes of 384 triangles over a w × h page, set up
// once, as a page prepares its shadings.
func meshPage(n, w, h int) ([]*stilus.MeshShader, []stilus.Path) {
	shaders := make([]*stilus.MeshShader, n)
	outlines := make([]stilus.Path, n)
	for i := range shaders {
		x0, y0 := float32((i*97)%(w-120)), float32((i*61)%(h-90))
		const nx, ny = 16, 12
		var tris []stilus.MeshTriangle
		vert := func(r, c int) stilus.MeshVertex {
			jx, jy := float32((r*7+c*13+i)%5)-2, float32((r*11+c*3+i)%5)-2
			if r == 0 || r == ny || c == 0 || c == nx {
				jx, jy = 0, 0
			}
			return stilus.MeshVertex{
				X: x0 + float32(c)*7 + jx, Y: y0 + float32(r)*7 + jy,
				C: stilus.PackRGBA(color.RGBA{uint8(c * 15), uint8(r * 20), uint8(i * 9), 255}),
			}
		}
		for r := 0; r < ny; r++ {
			for c := 0; c < nx; c++ {
				a, b, cc, d := vert(r, c), vert(r, c+1), vert(r+1, c), vert(r+1, c+1)
				tris = append(tris, stilus.MeshTriangle{a, b, d}, stilus.MeshTriangle{a, d, cc})
			}
		}
		sh := &stilus.MeshShader{Alpha: 255}
		sh.Set(tris, stilus.Identity, nil)
		shaders[i] = sh
		outlines[i].Rect(x0, y0, nx*7, ny*7)
	}
	return shaders, outlines
}

// TestMeshCache draws meshes set up once in four bands at once over two
// frames: each mesh's record is made once; an Alpha changed without Set
// is drawn; a Set makes a new record.
func TestMeshCache(t *testing.T) {
	d := device(t)
	const w, h, bands = 400, 300, 4
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	recs := make([]*gpu.Recorder, bands)
	for i := range recs {
		recs[i], _ = tg.NewRecorder()
	}
	shaders, outlines := meshPage(12, w, h)
	draw := func(c *stilus.Canvas) {
		for i, sh := range shaders {
			c.Fill(&outlines[i], stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: sh})
		}
	}
	frame := func(name string, wantBuilds int) {
		t.Helper()
		want := image.NewRGBA(image.Rect(0, 0, w, h))
		got := image.NewRGBA(want.Rect)
		c := stilus.NewCanvas(want)
		draw(c)
		tg.Clear(color.RGBA{})
		var wg sync.WaitGroup
		for i, r := range recs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var c stilus.Canvas
				c.ResetCompositor(r, image.Rect(0, h*i/bands, w, h*(i+1)/bands))
				draw(&c)
			}()
		}
		wg.Wait()
		if err := tg.ReadRGBA(got); err != nil {
			t.Fatal(err)
		}
		if mx, _ := deviation(want.Pix, got.Pix); mx > 1 {
			t.Errorf("%s: max %d/255", name, mx)
		}
		if n := gpu.MeshBuilds(tg); n != wantBuilds {
			t.Errorf("%s: %d mesh records made, want %d", name, n, wantBuilds)
		}
	}
	frame("first frame", len(shaders))
	frame("second frame", len(shaders))
	shaders[3].Alpha = 120
	frame("alpha changed", len(shaders))
	shaders, outlines = meshPage(12, w, h) // Set again
	frame("set again", 2*len(shaders))
}

// BenchmarkMeshBands draws 40 meshes set up once per page in bands, one
// canvas and recorder per band, with readback.
func BenchmarkMeshBands(b *testing.B) {
	d := device(b)
	if os.Getenv("STILUS_MESH_CACHE") == "0" {
		defer gpu.SetMeshCache(false)()
	}
	const w, h = 2400, 1700
	tg, err := d.NewTarget(w, h)
	if err != nil {
		b.Fatal(err)
	}
	defer tg.Close()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for _, bands := range []int{1, 4, 8} {
		recs := make([]*gpu.Recorder, bands)
		for i := range recs {
			recs[i], _ = tg.NewRecorder()
		}
		b.Run(fmt.Sprintf("%d-bands", bands), func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				// A new page: the meshes are set up again, once.
				shaders, outlines := meshPage(40, w, h)
				tg.Clear(white)
				var wg sync.WaitGroup
				for i, r := range recs {
					wg.Add(1)
					go func() {
						defer wg.Done()
						var c stilus.Canvas
						c.ResetCompositor(r, image.Rect(0, h*i/bands, w, h*(i+1)/bands))
						for k, sh := range shaders {
							c.Fill(&outlines[k], stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: sh})
						}
					}()
				}
				wg.Wait()
				if err := tg.ReadRGBA(img); err != nil {
					b.Fatal(err)
				}
			}
		})
		for _, r := range recs {
			r.Close()
		}
	}
}

// TestGradientsRandom draws linear and radial gradients of random
// geometry, whose pixels do not fall on exact ties between two ramp
// entries: away from ties float32 takes the entry float64 takes.
func TestGradientsRandom(t *testing.T) {
	d := device(t)
	const w, h = 320, 240
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	rng := rand.New(rand.NewSource(65))
	ramp := make(stilus.Ramp, 64)
	for i := range ramp {
		ramp[i] = stilus.PackRGBA(color.RGBA{uint8(i * 4), uint8(255 - i*4), 60, 255})
	}
	var paints []*stilus.Paint
	for i := 0; i < 24; i++ {
		m := stilus.Matrix{0.8 + rng.Float64(), rng.Float64() - 0.5, rng.Float64() - 0.5, 0.8 + rng.Float64(), rng.Float64() * 30, rng.Float64() * 30}
		var g stilus.Gradient
		g.Ramp, g.Alpha, g.Extend = ramp, 255, [2]bool{true, true}
		if i%2 == 0 {
			lg := &stilus.LinearGradient{Gradient: g}
			lg.Set(rng.Float64()*100, rng.Float64()*100, 100+rng.Float64()*150, rng.Float64()*150, m)
			paints = append(paints, &stilus.Paint{Shader: lg})
		} else {
			rg := &stilus.RadialGradient{Gradient: g}
			rg.Set(rng.Float64()*150, rng.Float64()*120, rng.Float64()*10, rng.Float64()*150, rng.Float64()*120, 40+rng.Float64()*80, m)
			paints = append(paints, &stilus.Paint{Shader: rg})
		}
	}
	ops := func(c *stilus.Canvas) {
		for i, p := range paints {
			var r stilus.Path
			r.Rect(float32((i%6)*53), float32((i/6)*60), 53, 60)
			c.Fill(&r, stilus.Identity, stilus.NonZero, p)
		}
	}
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	ops(stilus.NewCanvas(want))
	tg.Clear(color.RGBA{})
	var c stilus.Canvas
	c.ResetCompositor(tg, tg.Bounds())
	ops(&c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	steps := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a, b := want.RGBAAt(x, y), got.RGBAAt(x, y)
			if max(absd(a.R, b.R), absd(a.G, b.G), absd(a.B, b.B), absd(a.A, b.A)) > 1 {
				steps++
			}
		}
	}
	mx, mean := deviation(want.Pix, got.Pix)
	t.Logf("max %d/255, mean %.4f/255, %d of %d pixels a ramp step off", mx, mean, steps, w*h)
	if steps > w*h/20000 {
		t.Errorf("%d pixels a ramp step off", steps)
	}
}

// TestGradientsFar is TestGradientsRandom 3000 pixels from the origin,
// as on a page at 300 dpi.
func TestGradientsFar(t *testing.T) {
	d := device(t)
	const w, h = 3400, 240
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	rng := rand.New(rand.NewSource(65))
	ramp := make(stilus.Ramp, 64)
	for i := range ramp {
		ramp[i] = stilus.PackRGBA(color.RGBA{uint8(i * 4), uint8(255 - i*4), 60, 255})
	}
	var paints []*stilus.Paint
	for i := 0; i < 24; i++ {
		m := stilus.Matrix{0.8 + rng.Float64(), rng.Float64() - 0.5, rng.Float64() - 0.5, 0.8 + rng.Float64(), 3000 + rng.Float64()*30, rng.Float64() * 30}
		var g stilus.Gradient
		g.Ramp, g.Alpha, g.Extend = ramp, 255, [2]bool{true, true}
		if i%2 == 0 {
			lg := &stilus.LinearGradient{Gradient: g}
			lg.Set(rng.Float64()*100, rng.Float64()*100, 100+rng.Float64()*150, rng.Float64()*150, m)
			paints = append(paints, &stilus.Paint{Shader: lg})
		} else {
			rg := &stilus.RadialGradient{Gradient: g}
			rg.Set(rng.Float64()*150, rng.Float64()*120, rng.Float64()*10, rng.Float64()*150, rng.Float64()*120, 40+rng.Float64()*80, m)
			paints = append(paints, &stilus.Paint{Shader: rg})
		}
	}
	ops := func(c *stilus.Canvas) {
		for i, p := range paints {
			var r stilus.Path
			r.Rect(3000+float32((i%6)*53), float32((i/6)*60), 53, 60)
			c.Fill(&r, stilus.Identity, stilus.NonZero, p)
		}
	}
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(want.Rect)
	ops(stilus.NewCanvas(want))
	tg.Clear(color.RGBA{})
	var c stilus.Canvas
	c.ResetCompositor(tg, tg.Bounds())
	ops(&c)
	if err := tg.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	steps := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a, b := want.RGBAAt(x, y), got.RGBAAt(x, y)
			if max(absd(a.R, b.R), absd(a.G, b.G), absd(a.B, b.B), absd(a.A, b.A)) > 1 {
				steps++
			}
		}
	}
	mx, mean := deviation(want.Pix, got.Pix)
	t.Logf("max %d/255, mean %.4f/255, %d of %d pixels a ramp step off", mx, mean, steps, w*h)
	if steps > w*h/20000 {
		t.Errorf("%d pixels a ramp step off", steps)
	}
}
