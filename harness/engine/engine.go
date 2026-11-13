// Package engine puts PDF renderers behind one interface for the harness.
package engine

import (
	"fmt"
	"image"
	"strings"
)

// Engine opens documents.
type Engine interface {
	Name() string
	// GoHeap reports whether the engine allocates on the Go heap, so that
	// allocation counts are meaningful (PDFium runs inside WebAssembly).
	GoHeap() bool
	Open(data []byte) (Doc, error)
	Close() error
}

// Doc is an open document.
type Doc interface {
	Pages() int
	// Render draws page (0-based) at dpi on white.
	Render(page int, dpi float64) (image.Image, error)
	Close()
}

// New returns the engine with the given name.
func New(name string) (Engine, error) {
	switch strings.ToLower(name) {
	case "pdfium":
		return NewPDFium()
	case "stilus":
		return Stilus{}, nil
	case "reference":
		return Reference{}, nil
	}
	return nil, fmt.Errorf("unknown engine %q (pdfium, stilus, reference)", name)
}
