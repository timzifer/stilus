package stilus_test

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// imageCompositor composites onto an image with the exported blitters, as
// an outside compositor would.
type imageCompositor struct {
	dst    *image.RGBA
	solid  stilus.SolidBlitter
	shader stilus.ShaderBlitter
	calls  int
}

func (c *imageCompositor) Blitter(p *stilus.Paint) stilus.Blitter {
	c.calls++
	if p.Shader != nil {
		c.shader.Reset(c.dst, p.Shader)
		return &c.shader
	}
	c.solid.Reset(c.dst, p.Color)
	return &c.solid
}

// TestCompositorScenes: a canvas drawing through a compositor gives the
// bytes of the canvas drawing onto the image itself. The fused strip
// kernel, the pre-touch and the direct glyph blit are off under a
// compositor, and each is an optimization of the blitter path.
func TestCompositorScenes(t *testing.T) {
	const dpi = 72
	size := scenes.Size(dpi)
	for _, s := range scenes.All() {
		t.Run(s.Name, func(t *testing.T) {
			want := image.NewRGBA(size)
			got := image.NewRGBA(size)
			for _, img := range []*image.RGBA{want, got} {
				for i := range img.Pix {
					img.Pix[i] = 255
				}
			}
			c := stilus.NewCanvas(want)
			s.Draw(c, dpi)
			if c.Err() != nil {
				t.Fatal(c.Err())
			}
			comp := &imageCompositor{dst: got}
			c.ResetCompositor(comp, size)
			s.Draw(c, dpi)
			if c.Err() != nil {
				t.Fatal(c.Err())
			}
			if comp.calls == 0 {
				t.Fatal("compositor not used")
			}
			if !bytes.Equal(want.Pix, got.Pix) {
				t.Fatalf("%d bytes differ", diffBytes(want.Pix, got.Pix))
			}
		})
	}
}

func diffBytes(a, b []byte) int {
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// TestCompositorRegion: the region limits the canvas like a clip, and
// Reset afterwards draws onto the image again.
func TestCompositorRegion(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	comp := &imageCompositor{dst: img}
	c := stilus.NewCanvas(img)
	c.ResetCompositor(comp, image.Rect(10, 10, 20, 20))
	var p stilus.Path
	p.Rect(0, 0, 40, 40)
	c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}})
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			in := image.Pt(x, y).In(image.Rect(10, 10, 20, 20))
			if a := img.RGBAAt(x, y).A; (a == 255) != in {
				t.Fatalf("(%d,%d) alpha %d", x, y, a)
			}
		}
	}
	c.Reset(img, img.Bounds())
	calls := comp.calls
	c.Fill(&p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}})
	if comp.calls != calls || img.RGBAAt(0, 0).A != 255 {
		t.Fatal("Reset did not leave the compositor")
	}
}
