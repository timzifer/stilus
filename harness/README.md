# harness – M0: measurement harness for the PDF renderer

A separate module (Go ≥ 1.26.4) so the stilus core stays dependency-free.
It renders a corpus with several engines, times them, counts allocations
and compares every page against the reference.

| engine | what | role |
|---|---|---|
| `pdfium` | PDFium compiled to WebAssembly (go-pdfium on wazero, no cgo) | reference |
| `stilus` | stilus core, drawing synthetic pages from their geometry | rasterizer accuracy/speed until the PDF front end exists |
| `reference` | each operation of a synthetic page with its coverage 8×8 supersampled | reference for synthetic pages, not timed |

## Use

```sh
cd harness
go run ./cmd/pdfbench fetch  -dir corpus     # public corpus, SHA-256 verified
go run ./cmd/pdfbench scenes -dir corpus     # synthetic A3 drawings (scenes of the core)
go run ./cmd/pdfbench run -corpus corpus,/path/to/customer/drawings -dpi 150 -out report
```

`run` flags: `-engines stilus,pdfium`, `-ref pdfium`, `-scene-ref reference`
(reference for synthetic pages; empty: `-ref`), `-runs 3`
(timed renders after one warm-up; min and median are reported), `-pages 5`
(per document, 0 = all), `-match substring`, `-images` (write every
rendering).

Output in `-out`: `report.md` (speed, allocations, accuracy per category,
per-page table), `results.csv`, and `diffs/*.png` for every page
that misses the accuracy target (reference as faint grey; red where the
engine is darker, blue where it is lighter, saturating at 64/255).

## Metrics

Pages are compared as grey levels composited over white, over the common
area. Target of the spec: mean deviation < 1/255 and < 0.5 % of pixels
deviating by more than 32/255. SSIM (8×8 windows) is reported alongside.

PDFium is rendered with `FPDF_RenderPageBitmapWithMatrix` and an
exact scale into a bitmap of the page size rounded up to whole pixels.
PDFium's own DPI rendering stretches the page to the rounded bitmap size,
which shifts content by up to a pixel across a page and made every engine
look 10–40/255 off.

Synthetic pages are compared against `reference`, not PDFium, and PDFium
is measured against it too. PDFium is biased at edges: it over-inks
partially covered pixels of diagonal lines by 0.5–0.8 % and loses about
0.9 % of the area of small curves, so an exact rasterizer misses the
target against it on dense hairlines and glyphs. `reference` rasterizes
every operation at 8× the resolution, sums its coverage into the page's
pixels and composites it at the page's resolution. Supersampling the whole
page would not do: coverage-based antialiasing composites each operation
with its own coverage, so two shapes sharing a pixel give 1−(1−a)(1−b)
where a supersampled page gives a+b, about 19/255 on dense hairlines.
Strokes thinner than a pixel are drawn one pixel wide, as both engines
draw them.

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
