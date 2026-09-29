// Package metrics compares rendered pages: mean and peak grey-level
// deviation, the share of clearly different pixels, SSIM, and a diff image.
//
// Pages are compared as grey levels composited over white, the way they are
// seen on paper; the thresholds of the spec are in 1/255 steps.
package metrics

import (
	"image"
	"image/color"
	"math"
)

// Spec thresholds against the reference renderer.
const (
	MaxMean   = 1.0   // mean deviation, 1/255 steps
	MaxOver32 = 0.005 // share of pixels deviating by more than 32/255
)

// Result of comparing two renderings of the same page.
type Result struct {
	SizeA, SizeB image.Point // rendered sizes; only the common area is compared
	Mean         float64     // mean absolute deviation, 0–255
	Over32       float64     // share of pixels deviating by more than 32
	Max          int         // largest deviation
	SSIM         float64     // structural similarity, 1 = identical
}

// Pass reports whether r meets the spec's accuracy target.
func (r Result) Pass() bool { return r.Mean < MaxMean && r.Over32 < MaxOver32 }

// Gray converts img to grey levels composited over white.
func Gray(img image.Image) *image.Gray {
	b := img.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	switch m := img.(type) {
	case *image.RGBA: // premultiplied
		for y := 0; y < b.Dy(); y++ {
			row := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			out := g.Pix[y*g.Stride:]
			for x := 0; x < b.Dx(); x++ {
				p := row[4*x : 4*x+4]
				inv := 255 - uint32(p[3])
				out[x] = luma(uint32(p[0])+inv, uint32(p[1])+inv, uint32(p[2])+inv)
			}
		}
	case *image.NRGBA: // straight alpha
		for y := 0; y < b.Dy(); y++ {
			row := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			out := g.Pix[y*g.Stride:]
			for x := 0; x < b.Dx(); x++ {
				p := row[4*x : 4*x+4]
				a, inv := uint32(p[3]), 255-uint32(p[3])
				out[x] = luma((uint32(p[0])*a+127)/255+inv, (uint32(p[1])*a+127)/255+inv, (uint32(p[2])*a+127)/255+inv)
			}
		}
	default:
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				r, gg, bb, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				inv := 0xffff - a
				g.Pix[y*g.Stride+x] = luma((r+inv)>>8, (gg+inv)>>8, (bb+inv)>>8)
			}
		}
	}
	return g
}

// luma is Rec. 601 luminance of 8-bit channels.
func luma(r, g, b uint32) uint8 {
	v := (299*r + 587*g + 114*b + 500) / 1000
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

// Compare compares two grey images over their common area.
func Compare(a, b *image.Gray) Result {
	r := Result{SizeA: a.Rect.Size(), SizeB: b.Rect.Size()}
	w, h := min(a.Rect.Dx(), b.Rect.Dx()), min(a.Rect.Dy(), b.Rect.Dy())
	if w == 0 || h == 0 {
		r.Mean, r.Over32, r.Max = 255, 1, 255
		return r
	}
	var sum, over int
	for y := 0; y < h; y++ {
		ra, rb := a.Pix[y*a.Stride:y*a.Stride+w], b.Pix[y*b.Stride:y*b.Stride+w]
		for x := range ra {
			d := int(ra[x]) - int(rb[x])
			if d < 0 {
				d = -d
			}
			sum += d
			if d > 32 {
				over++
			}
			r.Max = max(r.Max, d)
		}
	}
	n := float64(w * h)
	r.Mean = float64(sum) / n
	r.Over32 = float64(over) / n
	r.SSIM = ssim(a, b, w, h)
	return r
}

// ssim is the mean structural similarity over 8×8 windows with stride 4.
func ssim(a, b *image.Gray, w, h int) float64 {
	const win, step = 8, 4
	const c1, c2 = (0.01 * 255) * (0.01 * 255), (0.03 * 255) * (0.03 * 255)
	var total float64
	n := 0
	for y := 0; y+win <= h; y += step {
		for x := 0; x+win <= w; x += step {
			var sa, sb, saa, sbb, sab float64
			for j := 0; j < win; j++ {
				ra := a.Pix[(y+j)*a.Stride+x:]
				rb := b.Pix[(y+j)*b.Stride+x:]
				for i := 0; i < win; i++ {
					va, vb := float64(ra[i]), float64(rb[i])
					sa += va
					sb += vb
					saa += va * va
					sbb += vb * vb
					sab += va * vb
				}
			}
			const k = win * win
			ma, mb := sa/k, sb/k
			va, vb := saa/k-ma*ma, sbb/k-mb*mb
			cov := sab/k - ma*mb
			total += ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return total / float64(n)
}

// Diff renders the deviation of b from reference a: the reference as a
// faint grey drawing, deviations above 4/255 in red (b darker) or blue
// (b lighter), saturating at 64/255.
func Diff(a, b *image.Gray) *image.RGBA {
	w, h := min(a.Rect.Dx(), b.Rect.Dx()), min(a.Rect.Dy(), b.Rect.Dy())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			va, vb := int(a.Pix[y*a.Stride+x]), int(b.Pix[y*b.Stride+x])
			base := uint8(255 - (255-va)/5)
			c := color.RGBA{base, base, base, 255}
			if d := vb - va; d < -4 || d > 4 {
				s := uint8(math.Min(float64(abs(d))*4, 255))
				if d < 0 {
					c = color.RGBA{255, 255 - s, 255 - s, 255}
				} else {
					c = color.RGBA{255 - s, 255 - s, 255, 255}
				}
			}
			out.SetRGBA(x, y, c)
		}
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
