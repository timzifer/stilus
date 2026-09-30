package engine

import (
	"bytes"
	"errors"
	"image"
	"math"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// sceneMarker matches corpus.SceneMarker (not imported to avoid a cycle).
const sceneMarker = "%stilus-scene "

// Stilus draws synthetic drawing pages directly from their scene geometry.
// Until the PDF front end exists (M2/M3) it can render nothing else; it
// measures the rasterizer core against PDFium on identical paths.
type Stilus struct{}

func (Stilus) Name() string { return "stilus" }
func (Stilus) GoHeap() bool { return true }
func (Stilus) Close() error { return nil }

// ErrNoPDF is returned for documents that are not synthetic scene pages.
var ErrNoPDF = errors.New("stilus: no PDF front end yet, only synthetic scene pages")

func (Stilus) Open(data []byte) (Doc, error) {
	head := data[:min(len(data), 256)]
	i := bytes.Index(head, []byte(sceneMarker))
	if i < 0 {
		return nil, ErrNoPDF
	}
	name := head[i+len(sceneMarker):]
	name = name[:bytes.IndexByte(name, '\n')]
	for _, s := range scenes.All() {
		if s.Name == string(name) {
			return &stilusDoc{s: s}, nil
		}
	}
	return nil, ErrNoPDF
}

type stilusDoc struct {
	s   *scenes.Scene
	img *image.RGBA
	c   *stilus.Canvas
}

func (d *stilusDoc) Pages() int { return 1 }
func (d *stilusDoc) Close()     {}

func (d *stilusDoc) Render(page int, dpi float64) (image.Image, error) {
	// Same page size rule as the other engines: round up.
	r := image.Rect(0, 0, int(math.Ceil(scenes.PageW*dpi/72)), int(math.Ceil(scenes.PageH*dpi/72)))
	if d.img == nil || d.img.Rect != r {
		d.img = image.NewRGBA(r)
		d.c = stilus.NewCanvas(d.img)
	}
	for i := range d.img.Pix {
		d.img.Pix[i] = 0xff
	}
	d.c.Reset(d.img, r)
	d.s.Draw(d.c, dpi)
	return d.img, d.c.Err()
}
