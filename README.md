# stilus

A sparse CPU rasterizer for 2D vector graphics in pure Go: no cgo, no
dependencies, all GOOS/GOARCH including `js/wasm`, MIT licensed.

The cost of a path is **edge length in pixels plus covered spans** – never
the area of its bounding box or the width of the canvas. After warm-up,
drawing performs **zero allocations**.

stilus is the rasterizer core (milestone M1) of the PDF renderer described
in the spec *“PDF-Renderer für Go (Testballon)”*. It knows nothing about PDF,
fonts or color spaces, so it can equally serve lux, figure or act as the CPU
filler behind gogpu/gg.

## API in three layers

```go
// 1. Spans – plug into any compositor
type Blitter interface {
    BlitRun(y, x0, x1 int, alpha uint8)   // interior: constant runs, no pixel loop
    BlitCoverage(y, x int, cov []uint8)   // antialiased edge pixels
}
r := stilus.NewRasterizer(clip)
r.Fill(path, matrix, stilus.NonZero, blitter)    // or AddLine/AddPath + Rasterize

// 2. Geometry – strokes in user space, exact under anisotropic CTMs
var s stilus.Stroker
s.Stroke(r, path, matrix, &stilus.StrokeStyle{Width: 1, Cap: stilus.RoundCap, Dash: []float64{3, 2}})
r.Rasterize(stilus.NonZero, blitter)

// 3. Canvas – the display-list device: Fill/Stroke/ClipPath/ClipRect/PopClip
c := stilus.NewCanvas(img)                        // *image.RGBA owned by the caller
c.Reset(img, band)                                // tile/band/viewport rendering
c.ClipRect(stilus.Rect{X0: 0, Y0: 0, X1: 842, Y1: 595}, ctm)
c.Stroke(path, ctm, style, &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}})
c.Fill(path, ctm, stilus.EvenOdd, &stilus.Paint{Shader: myShader}) // per-span callback
c.PopClip()
```

Integration points for other renderers: implement `Blitter` (own pixel
format, GPU upload, coverage masks), `Shader` (gradients, images – called per
span, never per pixel) or consume `Stroker` output through a `LineSink`.

## How it is fast

- **Band accumulation**: edges are accumulated as area/cover into a
  32-row band of cells (24.8 fixed point), not a page-sized buffer.
  Paths are clipped analytically first; curves are flattened in device space
  (0.1 px) after culling against the clip.
- **Sparse sweep**: narrow rows (the common case: strokes, glyphs) are
  integrated over their touched cell range; wide rows use a dirty bitset
  and emit the constant stretches between edges as runs. The sweep zeroes
  what it reads – there is no clearing pass.
- **Hairlines** (thinner than one device pixel) are drawn analytically as
  coverage rows, without building a polygon.
- **Rectangle clips** (91 % of real PDF clips) cost nothing per pixel;
  other clips are rasterized once into a mask that is never cleared as a
  whole and keeps fully opaque runs as runs.
- **Compositing** on premultiplied RGBA8 with SWAR arithmetic (four
  channels per 64-bit multiply, no divisions), memmove-speed opaque fills.

## Numbers

A3 landscape at 150 dpi (2481×1754 px), rectangle page clip, cloud Xeon
@ 2.1 GHz, `go run ./cmd/stilus-scenes` (minimum of 15 runs):

| scene | 1 core | 4 cores (bands) | spec target (1 core) |
|---|---:|---:|---:|
| 2 000 long hatch lines, hairline | 67 ms | 16 ms | ≤ 60 ms |
| 2 000 long hatch lines, 0.35 mm | 167 ms | 43 ms | – |
| 20 000 short strokes, 0.35 mm | 50 ms | 16 ms | ≤ 40 ms |
| 20 000 short strokes, hairline | 24 ms | 8 ms | – |
| 30 000 glyph outlines, uncached | 64 ms | 26 ms | – |
| mixed drawing (fills with alpha, dashes, curves) | 50 ms | 15 ms | – |
| 2 000 hatch lines through an elliptic mask clip | 50 ms | 20 ms | – |

Allocations per rendered scene after warm-up: **0**.

Same machine, same scenes, other pure-Go rasterizers (`bench/`, separate
module):

| scene | stilus | x/image/vector¹ | gogpu/gg v0.52.5 (CPU)² |
|---|---:|---:|---:|
| 2 000 hatch lines | 70 ms | 33 400 ms | 14 800 ms |
| 20 000 short strokes | 55 ms | 111 ms | 2 090 ms |

¹ stilus stroker outlines, filled with one `vector.Rasterizer` sized to each
outline's bounding box. ² No clip; gg's rectangle clip is slower still.

## Accuracy and robustness

- Coverage is exact-area accumulation (like FreeType, AGG/PDFium, Skia);
  tests compare against a supersampling reference integrating the winding
  number (mean < 0.5/255, max ≤ 16/255 on random polygons).
- Stroker: AGG-style outlines (inner corners at the offset intersection, no
  overlap on ordinary polylines), butt/round/square caps, miter/round/bevel
  joins with miter limit, dashes with phase and zero-length dots. Strokes
  below one device pixel are drawn one pixel wide, like PDFium.
- NaN/Inf rejected, huge coordinates clipped analytically, budgets for
  edges per path, curve/arc subdivision, dash counts and clip depth. Canvas
  never panics; errors are reported through `Err()`.
- Fuzz targets: `FuzzFill`, `FuzzStroke`.

## Development

```sh
go test ./...                                  # tests, reference comparisons, alloc gates
go test -run XXX -bench BenchmarkScenes .      # scene benchmarks at 72/150/300 dpi
go run ./cmd/stilus-scenes -dpi 150 -out /tmp/scenes   # timings + PNGs
go run ./cmd/stilus-scenes -threads 4          # band-parallel rendering
(cd bench && go test -bench .)                 # comparison with x/image/vector and gg
go test -fuzz FuzzStroke                       # fuzzing
```

## Not in this milestone

Blend modes, transparency groups and soft masks (M6), shadings and images
(M5/M7), glyph cache (M4), display list and PDF interpretation (M3). The
`Blitter`/`Shader` interfaces are where these plug in.

Next speed levers for thin strokes: an analytic path for straight segments
wider than a hairline, and SIMD for the sweep and compositing loops.
