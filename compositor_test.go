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

// stripBlitter takes strips whole and blits their rows, as Strip.Row
// gives them.
type stripBlitter struct {
	stilus.Blitter
	cov    []uint8
	strips *int
	masked *int
}

func (b *stripBlitter) BlitStrip(s *stilus.Strip) {
	*b.strips++
	if s.Mask != nil {
		*b.masked++
	}
	for j := s.Y0; j < s.Y1; j++ {
		var x int
		x, b.cov = s.Row(j, b.cov[:0])
		if len(b.cov) > 0 {
			b.Blitter.BlitCoverage(j, x, b.cov)
		}
	}
}

type stripCompositor struct {
	imageCompositor
	sb     stripBlitter
	strips int
	masked int
}

func (c *stripCompositor) Blitter(p *stilus.Paint) stilus.Blitter {
	c.sb.Blitter, c.sb.strips, c.sb.masked = c.imageCompositor.Blitter(p), &c.strips, &c.masked
	return &c.sb
}

// TestStripBlitterScenes: strips handed to a StripBlitter and drawn row
// by row with Strip.Row give the bytes of the canvas, on the scenes and
// under a rectangle clip with fractional borders, also within an elliptic
// mask.
func TestStripBlitterScenes(t *testing.T) {
	const dpi = 72
	size := scenes.Size(dpi)
	frac := &stilus.Rect{X0: 40.3, Y0: 30.6, X1: 1100.45, Y1: 800.2}
	var ell stilus.Path
	ell.Ellipse(600, 420, 520, 330)
	type clip struct {
		rect *stilus.Rect
		mask *stilus.Path
	}
	clips := map[string]clip{"": {}, "frac": {rect: frac}, "mask": {rect: frac, mask: &ell}}
	total, masked := 0, 0
	for _, s := range scenes.All() {
		for cn, clip := range clips {
			t.Run(s.Name+"/"+cn, func(t *testing.T) {
				m := scenes.Matrix(dpi)
				draw := func(c *stilus.Canvas) {
					if clip.rect != nil {
						c.ClipRect(*clip.rect, m)
					}
					if clip.mask != nil {
						c.ClipPath(clip.mask, m, stilus.NonZero)
					}
					s.Draw(c, dpi)
					if clip.mask != nil {
						c.PopClip()
					}
					if clip.rect != nil {
						c.PopClip()
					}
				}
				want, got := image.NewRGBA(size), image.NewRGBA(size)
				c := stilus.NewCanvas(want)
				draw(c)
				comp := &stripCompositor{imageCompositor: imageCompositor{dst: got}}
				c.ResetCompositor(comp, size)
				draw(c)
				if c.Err() != nil {
					t.Fatal(c.Err())
				}
				total += comp.strips
				masked += comp.masked
				if !bytes.Equal(want.Pix, got.Pix) {
					t.Fatalf("%d bytes differ (%d strips)", diffBytes(want.Pix, got.Pix), comp.strips)
				}
			})
		}
	}
	if total == 0 || masked == 0 {
		t.Fatalf("%d strips handed over, %d masked", total, masked)
	}
}
