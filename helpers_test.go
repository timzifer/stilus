package stilus

import (
	"image/color"
	"testing"
)

func rgba(r, g, b, a uint8) color.RGBA { return color.RGBA{r, g, b, a} }

func TestPackMatchesMemoryLayout(t *testing.T) {
	for _, c := range []color.RGBA{{1, 2, 3, 4}, {255, 0, 128, 255}, {0, 0, 0, 0}} {
		if got, want := pack(c.R, c.G, c.B, c.A), PackRGBA(c); got != want {
			t.Fatalf("pack(%v) = %#08x, want %#08x", c, got, want)
		}
		if r, g, b, a := unpack(PackRGBA(c)); (color.RGBA{r, g, b, a}) != c {
			t.Fatalf("unpack(PackRGBA(%v)) = %v", c, color.RGBA{r, g, b, a})
		}
	}
}
