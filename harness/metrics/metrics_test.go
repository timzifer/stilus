package metrics

import (
	"image"
	"image/color"
	"testing"
)

func TestCompare(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 64, 64))
	b := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range a.Pix {
		a.Pix[i] = 255
		b.Pix[i] = 255
	}
	ga, gb := Gray(a), Gray(b)
	if r := Compare(ga, gb); r.Mean != 0 || r.Max != 0 || r.SSIM < 0.9999 || !r.Pass() {
		t.Fatalf("identical: %+v", r)
	}
	// Transparent pixels count as white paper.
	b.SetNRGBA(3, 3, color.NRGBA{0, 0, 0, 0})
	if r := Compare(ga, Gray(b)); r.Max != 0 {
		t.Fatalf("transparent: %+v", r)
	}
	for y := 0; y < 64; y++ {
		b.SetNRGBA(10, y, color.NRGBA{0, 0, 0, 255})
	}
	r := Compare(ga, Gray(b))
	if r.Max != 255 || r.Over32 != 1.0/64 || r.Pass() {
		t.Fatalf("line: %+v", r)
	}
	if d := Diff(ga, Gray(b)); d.RGBAAt(10, 5).R != 255 || d.RGBAAt(10, 5).G == 255 {
		t.Fatalf("diff colour %v", d.RGBAAt(10, 5))
	}
}

// A correct upper quarter of an otherwise missing page must not pass.
func TestCompareSizeMismatch(t *testing.T) {
	full := image.NewGray(image.Rect(0, 0, 64, 64))
	for i := range full.Pix {
		full.Pix[i] = 255
	}
	quarter := image.NewGray(image.Rect(0, 0, 64, 16))
	copy(quarter.Pix, full.Pix)
	r := Compare(full, quarter)
	if r.Pass() || !r.SizeMismatch() {
		t.Fatalf("cropped rendering passes: %+v", r)
	}
}
