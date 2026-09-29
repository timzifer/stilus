# harness – M0: measurement harness for the PDF renderer

A separate module (Go ≥ 1.26.4) so the stilus core stays dependency-free.
It renders a corpus with several engines, times them, counts allocations,
compares every page against the reference and splits go-pdfkit's CPU time
into parser, interpreter, paths, text, images, shadings, transparency and
colour.

| engine | what | role |
|---|---|---|
| `pdfium` | PDFium compiled to WebAssembly (go-pdfium on wazero, no cgo) | reference |
| `pdfkit` | go-pdfkit/render with go-gfx | today's baseline |
| `stilus` | stilus core, drawing synthetic pages from their geometry | rasterizer accuracy/speed until the PDF front end exists |

## Use

```sh
cd harness
go run ./cmd/pdfbench fetch  -dir corpus     # public corpus, SHA-256 verified
go run ./cmd/pdfbench scenes -dir corpus     # synthetic A3 drawings (scenes of the core)
go run ./cmd/pdfbench run -corpus corpus,/path/to/customer/drawings -dpi 150 -out report
```

`run` flags: `-engines pdfkit,stilus,pdfium`, `-ref pdfium`, `-runs 3`
(timed renders after one warm-up; min and median are reported), `-pages 5`
(per document, 0 = all), `-match substring`, `-images` (write every
rendering), `-profiles dir` (raw go-pdfkit CPU profiles for `go tool pprof`),
`-split=false`.

Output in `-out`: `report.md` (speed, allocations, accuracy per category,
time split, per-page table), `results.csv`, and `diffs/*.png` for every page
that misses the accuracy target (reference as faint grey; red where the
engine is darker, blue where it is lighter, saturating at 64/255).

## Metrics

Pages are compared as grey levels composited over white, over the common
area. Target of the spec: mean deviation < 1/255 and < 0.5 % of pixels
deviating by more than 32/255. SSIM (8×8 windows) is reported alongside.

The reference is rendered with `FPDF_RenderPageBitmapWithMatrix` and an
exact scale into a bitmap sized like go-pdfkit sizes pages (rounded up).
PDFium's own DPI rendering stretches the page to the rounded bitmap size,
which shifts content by up to a pixel across a page and made every engine
look 10–40/255 off.

## Corpus

`corpus/manifest.json`: 33 public files, pinned by URL and SHA-256 —
pdf.js test suite at commit `18e8a26` (text and font kinds, transparency
groups, blend modes, knockout, soft masks, function/radial shadings, tiling
patterns, CMYK JPEG, DeviceN, JPEG 2000, CCITT, JBIG2, annotations, clipping)
and four arXiv papers. Nothing is committed; `fetch` downloads and verifies.
`scenes` adds the eight synthetic drawings (hatching, short strokes, contours,
glyph outlines, a mixed drawing with transparency, a mask clip). Customer
drawings are passed as another `-corpus` directory and are reported as
category `local`.
