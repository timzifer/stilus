package gpu_test

import (
	"image"
	"image/color"
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
