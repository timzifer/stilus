package engine

import (
	"image"
	"time"

	"github.com/go-pdfkit/reader"
	"github.com/go-pdfkit/render"
)

// PDFKit renders with github.com/go-pdfkit/render (go-gfx rasterizer): the
// "today" baseline of the spec.
type PDFKit struct{}

func (PDFKit) Name() string { return "pdfkit" }
func (PDFKit) GoHeap() bool { return true }
func (PDFKit) Close() error { return nil }

func (PDFKit) Open(data []byte) (Doc, error) {
	d, err := reader.Open(data)
	if err != nil {
		return nil, err
	}
	return pdfkitDoc{d}, nil
}

type pdfkitDoc struct{ d *reader.Document }

func (d pdfkitDoc) Pages() int { return d.d.PageCount() }
func (d pdfkitDoc) Close()     {}

func (d pdfkitDoc) Render(page int, dpi float64) (image.Image, error) {
	img, err := render.Page(d.d, page+1, render.Options{DPI: dpi, MaxDuration: time.Minute})
	if img == nil {
		return nil, err
	}
	// go-gfx images are densely packed, straight-alpha RGBA.
	return &image.NRGBA{Pix: img.Pix, Stride: 4 * img.W, Rect: image.Rect(0, 0, img.W, img.H)}, err
}
