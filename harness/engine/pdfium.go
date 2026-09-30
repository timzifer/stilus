package engine

import (
	"fmt"
	"image"
	"math"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// PDFium renders with PDFium compiled to WebAssembly (go-pdfium on wazero),
// no cgo: the spec's reference renderer.
type PDFium struct {
	pool pdfium.Pool
	inst pdfium.Pdfium
}

// NewPDFium starts one WebAssembly PDFium instance.
func NewPDFium() (*PDFium, error) {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		return nil, err
	}
	inst, err := pool.GetInstance(time.Minute)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &PDFium{pool: pool, inst: inst}, nil
}

func (*PDFium) Name() string { return "pdfium" }
func (*PDFium) GoHeap() bool { return false }

func (p *PDFium) Close() error {
	p.inst.Close()
	return p.pool.Close()
}

func (p *PDFium) Open(data []byte) (Doc, error) {
	r, err := p.inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, err
	}
	n, err := p.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: r.Document})
	if err != nil {
		p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: r.Document})
		return nil, err
	}
	return &pdfiumDoc{p: p, doc: r.Document, n: n.PageCount}, nil
}

type pdfiumDoc struct {
	p   *PDFium
	doc references.FPDF_DOCUMENT
	n   int
}

func (d *pdfiumDoc) Pages() int { return d.n }

func (d *pdfiumDoc) Close() {
	d.p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.doc})
}

// Render draws with an exact scale. PDFium's own DPI rendering stretches the
// page to the rounded-up bitmap size, which shifts content by up to a pixel
// across a page and would dominate every comparison; here the bitmap is
// sized like go-pdfkit sizes it (rounded up) and the page is drawn with the
// matrix scale(dpi/72) onto white.
func (d *pdfiumDoc) Render(page int, dpi float64) (image.Image, error) {
	in := d.p.inst
	pg, err := in.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: d.doc, Index: page})
	if err != nil {
		return nil, err
	}
	defer in.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: pg.Page})
	ref := requests.Page{ByReference: &pg.Page}
	pw, err := in.FPDF_GetPageWidthF(&requests.FPDF_GetPageWidthF{Page: ref})
	if err != nil {
		return nil, err
	}
	ph, err := in.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: ref})
	if err != nil {
		return nil, err
	}
	s := dpi / 72
	w, h := int(math.Ceil(float64(pw.PageWidth)*s)), int(math.Ceil(float64(ph.PageHeight)*s))
	if w <= 0 || h <= 0 || w*h > 40<<20 {
		return nil, fmt.Errorf("pdfium: page size %d×%d", w, h)
	}
	bm, err := in.FPDFBitmap_Create(&requests.FPDFBitmap_Create{Width: w, Height: h, Alpha: 0})
	if err != nil {
		return nil, err
	}
	defer in.FPDFBitmap_Destroy(&requests.FPDFBitmap_Destroy{Bitmap: bm.Bitmap})
	if _, err := in.FPDFBitmap_FillRect(&requests.FPDFBitmap_FillRect{Bitmap: bm.Bitmap, Width: w, Height: h, Color: 0xffffffff}); err != nil {
		return nil, err
	}
	_, err = in.FPDF_RenderPageBitmapWithMatrix(&requests.FPDF_RenderPageBitmapWithMatrix{
		Bitmap:   bm.Bitmap,
		Page:     ref,
		Matrix:   structs.FPDF_FS_MATRIX{A: float32(s), D: float32(s)},
		Clipping: structs.FPDF_FS_RECTF{Right: float32(w), Bottom: float32(h)},
		Flags:    enums.FPDF_RENDER_FLAG_ANNOT,
	})
	if err != nil {
		return nil, err
	}
	st, err := in.FPDFBitmap_GetStride(&requests.FPDFBitmap_GetStride{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	buf, err := in.FPDFBitmap_GetBuffer(&requests.FPDFBitmap_GetBuffer{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ { // BGRx -> RGBA
		src := buf.Buffer[y*st.Stride:]
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < w; x++ {
			dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = src[4*x+2], src[4*x+1], src[4*x], 255
		}
	}
	return out, nil
}
