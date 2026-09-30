package stilus_test

// TestGoldenHashes prints a hash of every scene's output, so that a
// refactoring can be checked for bit-identical results:
//
//	STILUS_GOLDEN=1 go test -run TestGoldenHashes -v . > before.txt
//	... change ...
//	STILUS_GOLDEN=1 go test -run TestGoldenHashes -v . | diff before.txt -
//
// It asserts nothing (output legitimately changes with accuracy work) and
// is skipped unless STILUS_GOLDEN is set.

import (
	"crypto/sha256"
	"fmt"
	"image"
	"os"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

func TestGoldenHashes(t *testing.T) {
	if os.Getenv("STILUS_GOLDEN") == "" {
		t.Skip()
	}
	for _, dpi := range []float64{72, 150} {
		for _, s := range scenes.All() {
			img := image.NewRGBA(scenes.Size(dpi))
			for i := range img.Pix {
				img.Pix[i] = uint8(i * 7)
			}
			c := stilus.NewCanvas(img)
			s.Draw(c, dpi)
			fmt.Printf("%-24s %4g %x\n", s.Name, dpi, sha256.Sum256(img.Pix))
		}
	}
	// Band-parallel style: a region not at the origin.
	for _, s := range scenes.All() {
		img := image.NewRGBA(image.Rect(-30, 17, 800, 600))
		c := stilus.NewCanvas(img)
		c.Reset(img, image.Rect(0, 100, 700, 500))
		s.Draw(c, 72)
		fmt.Printf("%-24s band %x\n", s.Name, sha256.Sum256(img.Pix))
	}
}
