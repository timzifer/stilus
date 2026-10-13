package stilus

import (
	"bytes"
	"image"
	"image/color"
	"math/rand/v2"
	"testing"
)

// randomPath returns a path of lines and curves in and around a box of
// the given size, some of them far outside it.
func randomPath(rng *rand.Rand, w, h float64) *Path {
	f := func(n float64) float32 {
		v := rng.Float64()*1.6 - 0.3
		if rng.IntN(12) == 0 {
			v = rng.Float64()*40 - 20 // far away
		}
		return float32(v * n)
	}
	var p Path
	for range 1 + rng.IntN(3) {
		p.MoveTo(f(w), f(h))
		for range 1 + rng.IntN(6) {
			switch rng.IntN(5) {
			case 0:
				p.QuadTo(f(w), f(h), f(w), f(h))
			case 1:
				p.CubicTo(f(w), f(h), f(w), f(h), f(w), f(h))
			case 2:
				// Nearly level, nearly upright.
				x, y := f(w), f(h)
				p.LineTo(x, y)
				if rng.IntN(2) == 0 {
					p.LineTo(f(w), y+float32(rng.Float64()*0.01))
				} else {
					p.LineTo(x+float32(rng.Float64()*0.01), f(h))
				}
			default:
				p.LineTo(f(w), f(h))
			}
		}
		p.Close()
	}
	return &p
}

// tilings are the ways the tests cut a clip: rows, columns, tiles.
func tilings(r image.Rectangle) [][]image.Rectangle {
	var out [][]image.Rectangle
	for _, size := range []image.Point{{r.Dx(), 7}, {r.Dx(), 32}, {r.Dx(), 33}, {5, r.Dy()}, {64, r.Dy()}, {17, 13}, {1, 1}} {
		var tiles []image.Rectangle
		for y := r.Min.Y; y < r.Max.Y; y += size.Y {
			for x := r.Min.X; x < r.Max.X; x += size.X {
				tiles = append(tiles, image.Rect(x, y, x+size.X, y+size.Y).Intersect(r))
			}
		}
		out = append(out, tiles)
	}
	return out
}

// TestFillTiles holds a path filled in tiles against the path filled at
// once: every pixel has the same coverage, whatever the clip.
func TestFillTiles(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	box := image.Rect(-3, 5, 97, 85)
	for it := range 300 {
		p := randomPath(rng, 100, 80)
		m := []Matrix{Identity, {0.7, 0.4, -0.3, 1.2, 5, -3}, {-1, 0, 0, 1, 100, 0}, {3, 0, 0, 3, -100, -80}}[rng.IntN(4)]
		rule := FillRule(rng.IntN(2))
		want := image.NewAlpha(box)
		r := NewRasterizer(box)
		r.Fill(p, m, rule, &MaskBlitter{want})
		for _, tiles := range tilings(box) {
			if it%10 != 0 && tiles[0].Dx() == 1 {
				continue // pixel by pixel is slow
			}
			got := image.NewAlpha(box)
			for _, tile := range tiles {
				r.SetClip(tile)
				r.Fill(p, m, rule, &MaskBlitter{got})
			}
			if mean, max := diff(got, want); max > 0 {
				t.Fatalf("iteration %d, tiles of %v: mean %.4f max %d", it, tiles[0].Size(), mean, max)
			}
		}
	}
}

// hashShader paints every pixel a color of its place alone, opaque or
// not.
type hashShader struct{ opaque bool }

func (h hashShader) ShadeSpan(y, x int, out []uint32) {
	for i := range out {
		v := uint32(x+i)*0x9e3779b1 ^ uint32(y)*0x85ebca6b
		v ^= v >> 15
		v *= 0x2c1b3c6d
		v ^= v >> 12
		c := color.RGBA{uint8(v), uint8(v >> 8), uint8(v >> 16), 255}
		if !h.opaque {
			a := uint32(uint8(v >> 24))
			c = color.RGBA{uint8(uint32(c.R) * a / 255), uint8(uint32(c.G) * a / 255), uint8(uint32(c.B) * a / 255), uint8(a)}
		}
		out[i] = PackRGBA(c)
	}
}

// randomPaint returns an opaque or a translucent color, or a shader.
func randomPaint(rng *rand.Rand) *Paint {
	switch rng.IntN(5) {
	case 0:
		return &Paint{Shader: hashShader{opaque: true}}
	case 1:
		return &Paint{Shader: hashShader{}}
	case 2:
		a := uint32(1 + rng.IntN(254))
		ch := func() uint8 { return uint8(uint32(rng.IntN(256)) * a / 255) }
		return &Paint{Color: color.RGBA{ch(), ch(), ch(), uint8(a)}}
	}
	return &Paint{Color: color.RGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255}}
}

// TestCanvasTiles holds scenes drawn in tiles against the scenes drawn at
// once: fills and strokes of all kinds, in colors and shaders, under
// clips, over a backdrop that is not plain. A viewer that draws a page in
// tiles, or in bands by several workers, gets the pixels of the page.
func TestCanvasTiles(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	box := image.Rect(-3, 5, 97, 85)
	backdrop := image.NewRGBA(box)
	hashShader{opaque: true}.ShadeSpan(0, 0, pixels(backdrop))
	for it := range 400 {
		var ops []func(c *Canvas)
		m := []Matrix{Identity, {0.7, 0.4, -0.3, 1.2, 5, -3}, {-1, 0, 0, 1, 100, 0}, {3, 0, 0, 3, -100, -80}, {1.5, 0, 0, -1.5, 0, 90}}[rng.IntN(5)]
		clips := 0
		for range 1 + rng.IntN(4) {
			p := randomPath(rng, 100, 80)
			if rng.IntN(3) == 0 {
				// A box, as most of what a page draws.
				x, y := float32(rng.Float64()*60), float32(rng.Float64()*40)
				w, h := float32(rng.Float64()*70), float32(rng.Float64()*50)
				p = &Path{}
				p.MoveTo(x, y)
				p.LineTo(x+w, y)
				p.LineTo(x+w, y+h)
				p.LineTo(x, y+h)
				p.Close()
			}
			st := &StrokeStyle{
				Width:      []float64{0, 0.3, 0.75, 1, 2.83, 9}[rng.IntN(6)],
				Cap:        Cap(rng.IntN(3)),
				Join:       Join(rng.IntN(3)),
				MiterLimit: []float64{1, 4, 20}[rng.IntN(3)],
			}
			if rng.IntN(4) == 0 {
				st.Dash = [][]float64{{7, 5}, {1, 1}, {0.2, 0.3}, {12, 3, 2, 3}}[rng.IntN(4)]
				st.DashPhase = rng.Float64() * 5
			}
			rule := FillRule(rng.IntN(2))
			paint := randomPaint(rng)
			switch k := rng.IntN(10); {
			case k < 4:
				ops = append(ops, func(c *Canvas) { c.Fill(p, m, rule, paint) })
			case k < 8:
				ops = append(ops, func(c *Canvas) { c.Stroke(p, m, st, paint) })
			case k == 8:
				clips++
				ops = append(ops, func(c *Canvas) { c.ClipPath(p, m, rule) })
			default:
				clips++
				r := Rect{rng.Float64() * 40, rng.Float64() * 30, 40 + rng.Float64()*60, 30 + rng.Float64()*50}
				ops = append(ops, func(c *Canvas) { c.ClipRect(r, m) })
			}
		}
		draw := func(dst *image.RGBA, region image.Rectangle) {
			c := NewCanvas(dst)
			c.Reset(dst, region)
			for _, op := range ops {
				op(c)
			}
			for range clips {
				c.PopClip()
			}
			if err := c.Err(); err != nil {
				t.Fatalf("iteration %d: %v", it, err)
			}
		}
		want := image.NewRGBA(box)
		copy(want.Pix, backdrop.Pix)
		draw(want, box)
		for _, tiles := range tilings(box) {
			if it%10 != 0 && tiles[0].Dx() == 1 {
				continue
			}
			got := image.NewRGBA(box)
			copy(got.Pix, backdrop.Pix)
			for _, tile := range tiles {
				draw(got, tile)
			}
			if !bytes.Equal(got.Pix, want.Pix) {
				n, at := 0, image.Point{}
				for i := 0; i < len(got.Pix); i += 4 {
					if !bytes.Equal(got.Pix[i:i+4], want.Pix[i:i+4]) {
						if n == 0 {
							at = image.Pt(box.Min.X+(i/4)%box.Dx(), box.Min.Y+(i/4)/box.Dx())
						}
						n++
					}
				}
				t.Errorf("iteration %d, tiles of %v: %d pixels differ, the first at %v: %v, want %v", it, tiles[0].Size(), n, at, got.RGBAAt(at.X, at.Y), want.RGBAAt(at.X, at.Y))
				break
			}
		}
	}
}

// TestRunLikeCoverage holds a run of constant coverage against the same
// coverage given pixel by pixel: the rasterizer cuts a row into runs and
// coverage as the clip and the shape have it, so the two must blend
// alike.
func TestRunLikeCoverage(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 32))
	const n = 41
	rect := image.Rect(0, 0, n, 1)
	for _, tt := range []struct {
		name string
		new  func(dst *image.RGBA) Blitter
	}{
		{"opaque color", func(dst *image.RGBA) Blitter { return NewSolidBlitter(dst, color.RGBA{10, 128, 250, 255}) }},
		{"translucent color", func(dst *image.RGBA) Blitter { return NewSolidBlitter(dst, color.RGBA{10, 100, 90, 130}) }},
		{"opaque shader", func(dst *image.RGBA) Blitter { return NewShaderBlitter(dst, hashShader{opaque: true}) }},
		{"translucent shader", func(dst *image.RGBA) Blitter { return NewShaderBlitter(dst, hashShader{}) }},
	} {
		for alpha := 0; alpha < 256; alpha++ {
			run, cov := image.NewRGBA(rect), image.NewRGBA(rect)
			for i := range run.Pix {
				run.Pix[i] = uint8(rng.IntN(256))
			}
			for i := 3; i < len(run.Pix); i += 4 {
				run.Pix[i] = 255 // an opaque backdrop, as a page is
			}
			copy(cov.Pix, run.Pix)
			for _, span := range [][2]int{{0, n}, {3, 9}, {7, 8}} {
				tt.new(run).BlitRun(0, span[0], span[1], uint8(alpha))
				c := make([]uint8, span[1]-span[0])
				for i := range c {
					c[i] = uint8(alpha)
				}
				tt.new(cov).BlitCoverage(0, span[0], c)
			}
			if !bytes.Equal(run.Pix, cov.Pix) {
				t.Errorf("%s, coverage %d: a run blends unlike coverage", tt.name, alpha)
				break
			}
		}
	}
}
