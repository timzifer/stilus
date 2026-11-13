package gpu_test

import (
	"context"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu"
	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/gpu"
)

// presentSource draws a few shapes with partial coverage onto a w × h
// target.
func presentSource(t *testing.T, d *gpu.Device, w, h int) *gpu.Target {
	t.Helper()
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	tg.Clear(color.RGBA{})
	var c stilus.Canvas
	c.ResetCompositor(tg, tg.Bounds())
	var p stilus.Path
	p.MoveTo(3.3, 2.7)
	p.LineTo(float32(w)-4.1, 5.2)
	p.LineTo(float32(w)/2, float32(h)-3.6)
	p.Close()
	c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{200, 40, 10, 255}})
	var q stilus.Path
	q.MoveTo(0, 0)
	q.LineTo(float32(w), float32(h))
	c.Stroke(&q, stilus.Identity, &stilus.StrokeStyle{Width: 2.5}, &stilus.Paint{Color: color.RGBA{0, 60, 120, 160}})
	return tg
}

// readTexture copies a w × h texture of four bytes per pixel to memory.
func readTexture(t *testing.T, d *gpu.Device, tex *wgpu.Texture, w, h int) []byte {
	t.Helper()
	dev := d.WGPU()
	bpr := (w*4 + 255) &^ 255
	buf, err := dev.CreateBuffer(&wgpu.BufferDescriptor{Size: uint64(bpr * h), Usage: wgpu.BufferUsageCopyDst | wgpu.BufferUsageMapRead})
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Release()
	enc, err := dev.CreateCommandEncoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc.CopyTextureToBuffer(tex, buf, []wgpu.BufferTextureCopy{{
		BufferLayout: wgpu.ImageDataLayout{BytesPerRow: uint32(bpr), RowsPerImage: uint32(h)},
		TextureBase:  wgpu.ImageCopyTexture{Texture: tex},
		Size:         wgpu.Extent3D{Width: uint32(w), Height: uint32(h), DepthOrArrayLayers: 1},
	}})
	cmd, err := enc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dev.Queue().Submit(cmd); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := buf.Map(ctx, wgpu.MapModeRead, 0, uint64(bpr*h)); err != nil {
		t.Fatal(err)
	}
	rng, err := buf.MappedRange(0, uint64(bpr*h))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		copy(out[y*w*4:(y+1)*w*4], rng.Bytes()[y*bpr:])
	}
	_ = buf.Unmap()
	return out
}

// TestPresent draws a target 1:1 at an offset into textures of both
// colour orders, once opaque and once at half opacity, over a background,
// and compares with the target's own pixels.
func TestPresent(t *testing.T) {
	d := device(t)
	const sw, sh, dw, dh, ox, oy = 37, 29, 64, 48, 11, 7
	src := presentSource(t, d, sw, sh)
	defer src.Close()
	want := image.NewRGBA(src.Bounds())
	if err := src.ReadRGBA(want); err != nil {
		t.Fatal(err)
	}
	partial := 0
	for i := 3; i < len(want.Pix); i += 4 {
		if a := want.Pix[i]; a != 0 && a != 255 {
			partial++
		}
	}
	if partial == 0 {
		t.Fatal("source has no partial coverage")
	}
	bg := color.RGBA{20, 120, 220, 255}
	for _, format := range []gputypes.TextureFormat{gpu.Format, gputypes.TextureFormatBGRA8Unorm} {
		for _, alpha := range []float32{1, 0.5} {
			p, err := d.NewPresenter(format, &gpu.PresenterOptions{Slots: 4})
			if err != nil {
				t.Fatal(err)
			}
			dev := d.WGPU()
			tex, err := dev.CreateTexture(&wgpu.TextureDescriptor{
				Size:          wgpu.Extent3D{Width: dw, Height: dh, DepthOrArrayLayers: 1},
				MipLevelCount: 1, SampleCount: 1,
				Dimension: gputypes.TextureDimension2D,
				Format:    format,
				Usage:     gputypes.TextureUsageRenderAttachment | gputypes.TextureUsageCopySrc,
			})
			if err != nil {
				t.Fatal(err)
			}
			view, err := dev.CreateTextureView(tex, nil)
			if err != nil {
				t.Fatal(err)
			}
			enc, err := dev.CreateCommandEncoder(nil)
			if err != nil {
				t.Fatal(err)
			}
			pass, err := enc.BeginRenderPass(&wgpu.RenderPassDescriptor{ColorAttachments: []wgpu.RenderPassColorAttachment{{
				View: view, LoadOp: gputypes.LoadOpClear, StoreOp: gputypes.StoreOpStore,
				ClearValue: gputypes.Color{R: float64(bg.R) / 255, G: float64(bg.G) / 255, B: float64(bg.B) / 255, A: 1},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			p.Begin(dw, dh)
			if err := p.Draw(pass, src, &gpu.DrawOptions{X: ox, Y: oy, Fade: 1 - alpha}); err != nil {
				t.Fatal(err)
			}
			if err := pass.End(); err != nil {
				t.Fatal(err)
			}
			if err := p.Commit(); err != nil {
				t.Fatal(err)
			}
			cmd, err := enc.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := dev.Queue().Submit(cmd); err != nil {
				t.Fatal(err)
			}
			got := readTexture(t, d, tex, dw, dh)
			bgra := format == gputypes.TextureFormatBGRA8Unorm
			mx := 0
			for y := 0; y < dh; y++ {
				for x := 0; x < dw; x++ {
					g := got[(y*dw+x)*4:][:4]
					if bgra {
						g = []byte{g[2], g[1], g[0], g[3]}
					}
					e := []byte{bg.R, bg.G, bg.B, bg.A}
					if image.Pt(x, y).In(image.Rect(ox, oy, ox+sw, oy+sh)) {
						s := want.Pix[want.PixOffset(x-ox, y-oy):][:4]
						for i := range e {
							v := float32(s[i])*alpha + float32(e[i])*(1-float32(s[3])*alpha/255)
							e[i] = uint8(v + 0.5)
						}
					}
					for i := range e {
						mx = max(mx, absd(g[i], e[i]))
					}
				}
			}
			// 1:1, the target's pixels; with alpha one rounding more.
			lim := 0
			if alpha != 1 {
				lim = 1
			}
			if mx > lim {
				t.Errorf("format %v alpha %v: max deviation %d", format, alpha, mx)
			}
			view.Release()
			tex.Release()
			p.Close()
		}
	}
}

func TestPresenterLimits(t *testing.T) {
	d := device(t)
	if _, err := d.NewPresenter(gputypes.TextureFormatBGRA8UnormSrgb, nil); err == nil {
		t.Error("sRGB format accepted")
	}
	p, err := d.NewPresenter(gpu.Format, &gpu.PresenterOptions{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	src := presentSource(t, d, 8, 8)
	defer src.Close()
	dst, err := d.NewTarget(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.Flush(); err != nil {
		t.Fatal(err)
	}
	enc, err := d.WGPU().CreateCommandEncoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := enc.BeginRenderPass(&wgpu.RenderPassDescriptor{ColorAttachments: []wgpu.RenderPassColorAttachment{{
		View: dst.View(), LoadOp: gputypes.LoadOpLoad, StoreOp: gputypes.StoreOpStore,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Draw(pass, src, nil); err == nil {
		t.Error("Draw before Begin accepted")
	}
	p.Begin(8, 8)
	if err := p.Draw(pass, src, &gpu.DrawOptions{Src: image.Rect(4, 4, 9, 9)}); err == nil {
		t.Error("source outside the target accepted")
	}
	if err := p.Draw(pass, src, &gpu.DrawOptions{Fade: 1}); err != nil {
		t.Fatal(err) // draws nothing, takes no slot
	}
	if err := p.Draw(pass, src, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Draw(pass, src, nil); err == nil {
		t.Error("draw past the slots accepted")
	}
	if err := pass.End(); err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(); err != nil {
		t.Fatal(err)
	}
	cmd, err := enc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.WGPU().Queue().Submit(cmd); err != nil {
		t.Fatal(err)
	}
	got := image.NewRGBA(dst.Bounds())
	want := image.NewRGBA(src.Bounds())
	if err := dst.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	if err := src.ReadRGBA(want); err != nil {
		t.Fatal(err)
	}
	if mx, _ := deviation(got.Pix, want.Pix); mx != 0 {
		t.Errorf("target into target: max deviation %d", mx)
	}
}

// presentInto draws with draw into a fresh w × h RGBA8 target cleared to
// transparent and returns its pixels.
func presentInto(t *testing.T, d *gpu.Device, w, h int, draw func(*gpu.Presenter, *wgpu.RenderPassEncoder)) *image.RGBA {
	t.Helper()
	p, err := d.NewPresenter(gpu.Format, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	dst, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.Flush(); err != nil {
		t.Fatal(err)
	}
	enc, err := d.WGPU().CreateCommandEncoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := enc.BeginRenderPass(&wgpu.RenderPassDescriptor{ColorAttachments: []wgpu.RenderPassColorAttachment{{
		View: dst.View(), LoadOp: gputypes.LoadOpLoad, StoreOp: gputypes.StoreOpStore,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	p.Begin(w, h)
	draw(p, pass)
	if err := pass.End(); err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(); err != nil {
		t.Fatal(err)
	}
	cmd, err := enc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.WGPU().Queue().Submit(cmd); err != nil {
		t.Fatal(err)
	}
	got := image.NewRGBA(dst.Bounds())
	if err := dst.ReadRGBA(got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestPresentSource draws parts of a target: 1:1 they are its pixels,
// stretched they read nothing outside the part.
func TestPresentSource(t *testing.T) {
	d := device(t)
	src := presentSource(t, d, 37, 29)
	defer src.Close()
	all := image.NewRGBA(src.Bounds())
	if err := src.ReadRGBA(all); err != nil {
		t.Fatal(err)
	}
	part := image.Rect(5, 3, 30, 21)
	got := presentInto(t, d, 40, 30, func(p *gpu.Presenter, pass *wgpu.RenderPassEncoder) {
		if err := p.Draw(pass, src, &gpu.DrawOptions{X: 2, Y: 4, Src: part}); err != nil {
			t.Fatal(err)
		}
	})
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			var want []byte
			if q := image.Pt(x-2, y-4).Add(part.Min); q.In(part) {
				want = all.Pix[all.PixOffset(q.X, q.Y):][:4]
			} else {
				want = []byte{0, 0, 0, 0}
			}
			if g := got.Pix[got.PixOffset(x, y):][:4]; string(g) != string(want) {
				t.Fatalf("1:1 at (%d, %d): got %v, want %v", x, y, g, want)
			}
		}
	}

	// Left half red, right half blue; the left half stretched must stay red.
	halves, err := d.NewTarget(8, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer halves.Close()
	halves.Clear(color.RGBA{})
	var c stilus.Canvas
	c.ResetCompositor(halves, halves.Bounds())
	var l, r stilus.Path
	l.MoveTo(0, 0)
	l.LineTo(4, 0)
	l.LineTo(4, 4)
	l.LineTo(0, 4)
	l.Close()
	r.MoveTo(4, 0)
	r.LineTo(8, 0)
	r.LineTo(8, 4)
	r.LineTo(4, 4)
	r.Close()
	c.Fill(&l, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{255, 0, 0, 255}})
	c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 0, 255, 255}})
	got = presentInto(t, d, 30, 20, func(p *gpu.Presenter, pass *wgpu.RenderPassEncoder) {
		if err := p.Draw(pass, halves, &gpu.DrawOptions{W: 30, H: 20, Src: image.Rect(0, 0, 4, 4)}); err != nil {
			t.Fatal(err)
		}
	})
	for i := 0; i < len(got.Pix); i += 4 {
		if g := got.Pix[i : i+4]; g[0] != 255 || g[1] != 0 || g[2] != 0 || g[3] != 255 {
			t.Fatalf("stretched part at pixel %d: %v, bled outside the part", i/4, g)
		}
	}
}
