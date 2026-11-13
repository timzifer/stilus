package stilus_test

import (
	"image/color"
	"math"
	"math/rand"
	"testing"

	"github.com/timzifer/stilus"
)

// formIndex is clampIndex, or the coordinate modulo n with wrap.
func formIndex(u float64, n int, wrap bool) int {
	if wrap {
		u -= math.Floor(u/float64(n)) * float64(n)
		return min(max(int(u), 0), n-1)
	}
	if !(u > 0) {
		return 0
	}
	if u >= float64(n) {
		return n - 1
	}
	return int(u)
}

// formSplit is split, or its periodic counterpart.
func formSplit(u float64, n int, wrap bool) (int, int, uint32) {
	if wrap {
		u -= math.Floor(u/float64(n)) * float64(n)
		i := min(int(u), n-1)
		return i, (i + 1) % n, uint32((u - float64(i)) * 256)
	}
	if !(u > 0) {
		return 0, 0, 0
	}
	if u >= float64(n-1) {
		return n - 1, n - 1, 0
	}
	i := int(u)
	return i, i + 1, uint32((u - float64(i)) * 256)
}

func formLerp(a, b uint32, t uint32) uint32 {
	ca, cb := stilus.UnpackRGBA(a), stilus.UnpackRGBA(b)
	l := func(x, y uint8) uint8 { return uint8((uint32(x)*(256-t) + uint32(y)*t) >> 8) }
	return stilus.PackRGBA(color.RGBA{l(ca.R, cb.R), l(ca.G, cb.G), l(ca.B, cb.B), l(ca.A, cb.A)})
}

// formSample samples a SamplerForm at device pixel (x, y).
func formSample(f *stilus.SamplerForm, x, y int) uint32 {
	p, m := f.Plane, f.Inv
	fx, fy := float64(x)+0.5, float64(y)+0.5
	u, v := m[0]*fx+m[2]*fy+m[4], m[1]*fx+m[3]*fy+m[5]
	if !f.Bilinear {
		return p.At(formIndex(u, p.W, f.Wrap), formIndex(v, p.H, f.Wrap))
	}
	x0, x1, tx := formSplit(u-0.5, p.W, f.Wrap)
	y0, y1, ty := formSplit(v-0.5, p.H, f.Wrap)
	a := formLerp(p.At(x0, y0), p.At(x1, y0), tx)
	b := formLerp(p.At(x0, y1), p.At(x1, y1), tx)
	return formLerp(a, b, ty)
}

func mulc(c uint32, a uint8) uint32 {
	k := stilus.UnpackRGBA(c)
	m := func(v uint8) uint8 {
		x := uint32(v)*uint32(a) + 128
		return uint8((x + x>>8) >> 8)
	}
	return stilus.PackRGBA(color.RGBA{m(k.R), m(k.G), m(k.B), m(k.A)})
}

// formImage is the colour of an ImageForm at device pixel (x, y).
func formImage(f *stilus.ImageForm, x, y int) uint32 {
	c := f.Color
	if f.HasImage {
		c = formSample(&f.Image, x, y)
		if f.Alpha != 255 {
			c = mulc(c, f.Alpha)
		}
	}
	if f.HasMask {
		switch a := stilus.UnpackRGBA(formSample(&f.Mask, x, y)).R; a {
		case 0:
			c = 0
		case 255:
		default:
			c = mulc(c, a)
		}
	}
	return c
}

func chanDiff(a, b uint32) int {
	ca, cb := stilus.UnpackRGBA(a), stilus.UnpackRGBA(b)
	d := 0
	for _, p := range [4][2]uint8{{ca.R, cb.R}, {ca.G, cb.G}, {ca.B, cb.B}, {ca.A, cb.A}} {
		d = max(d, absDiff(p[0], p[1]))
	}
	return d
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// TestImageForm: colours sampled from Form alone are ShadeSpan's: exact
// for nearest sampling, within 1 for bilinear (the order of the two
// mixes; 2 for an image and a mask), and for repeating textures exact but where float64 and the
// sampler's fixed point put a pixel on either side of a texel border.
func TestImageForm(t *testing.T) {
	rng := rand.New(rand.NewSource(10))
	const n = 37
	pix := make([]uint32, n*n)
	idx := make([]uint8, n*n)
	bitsRow := (n + 7) / 8
	bits := make([]uint8, bitsRow*n)
	for i := range pix {
		pix[i] = stilus.PackRGBA(color.RGBA{uint8(rng.Intn(256)) / 2, uint8(rng.Intn(128)), 40, 200})
		idx[i] = uint8(rng.Intn(256))
	}
	for i := range bits {
		bits[i] = uint8(rng.Intn(256))
	}
	var pal stilus.Palette
	for i := range pal {
		pal[i] = stilus.PackRGBA(color.RGBA{uint8(i / 2), uint8(255 - i), 30, 255})
	}
	texs := []*stilus.Texture{
		stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: pix}),
		stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneIndex, W: n, H: n, Stride: n, Pix8: idx, Pal: &pal}),
		stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneBits, W: n, H: n, Stride: bitsRow, Pix8: bits, Pal: &pal}),
	}
	mask := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneIndex, W: n, H: n, Stride: n, Pix8: idx, Pal: stilus.AlphaPalette})
	dst := make([]uint32, 90)
	var exact, near, wrapOff, wrapAll int
	for c := 0; c < 240; c++ {
		s := []float64{0.3, 0.9, 1, 2.5, 7}[c%5]
		a := 0.0
		if c%3 == 1 {
			a = rng.Float64()
		}
		toDev := stilus.Scale(s, s*1.2).Mul(stilus.Rotate(a)).Mul(stilus.Translate(3+rng.Float64()*10, 2+rng.Float64()*10))
		smooth, wrap := c%2 == 0, c%7 < 3
		var sh stilus.ImageShader
		tex := texs[c%3]
		switch c % 4 {
		case 0, 1:
			if wrap {
				sh.SetImageWrap(tex, toDev, smooth, []uint8{255, 160}[c%2])
			} else {
				sh.SetImage(tex, toDev, smooth, []uint8{255, 160}[c%2])
			}
		case 2:
			sh.SetImage(tex, toDev, smooth, 255)
			sh.SetMask(mask, toDev.Mul(stilus.Translate(1.5, 0.5)), !smooth)
		default:
			sh.SetColor(stilus.PackRGBA(color.RGBA{0, 30, 110, 255}))
			if wrap {
				sh.SetMaskWrap(mask, toDev, smooth)
			} else {
				sh.SetMask(mask, toDev, smooth)
			}
		}
		f := sh.Form()
		bil := f.HasImage && f.Image.Bilinear || f.HasMask && f.Mask.Bilinear
		wr := f.HasImage && f.Image.Wrap || f.HasMask && f.Mask.Wrap
		for y := -5; y < 80; y += 3 {
			sh.ShadeSpan(y, -5, dst)
			for i, want := range dst {
				got := formImage(&f, i-5, y)
				d := chanDiff(got, want)
				switch {
				case wr:
					wrapAll++
					if d > 1 {
						wrapOff++
					}
				case bil:
					// 1 per bilinear sample, an image and a mask both.
					if d > 2 {
						t.Fatalf("case %d bilinear (%d,%d): form %08x, ShadeSpan %08x", c, i-5, y, got, want)
					}
					near++
				default:
					if d != 0 {
						t.Fatalf("case %d nearest (%d,%d): form %08x, ShadeSpan %08x", c, i-5, y, got, want)
					}
					exact++
				}
			}
		}
	}
	t.Logf("nearest %d exact, bilinear %d within 2, repeating %d of %d off", exact, near, wrapOff, wrapAll)
	if wrapOff > wrapAll/200 {
		t.Fatalf("repeating: %d of %d pixels off", wrapOff, wrapAll)
	}
}
