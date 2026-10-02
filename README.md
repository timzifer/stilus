# stilus

[![CI](https://github.com/timzifer/stilus/actions/workflows/ci.yml/badge.svg)](https://github.com/timzifer/stilus/actions/workflows/ci.yml)
[![Coverage](https://raw.githubusercontent.com/timzifer/stilus/badges/.badges/main/coverage.svg)](https://github.com/timzifer/stilus/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/timzifer/stilus.svg)](https://pkg.go.dev/github.com/timzifer/stilus)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

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

// Prepared geometry for banded rendering: set once per page, drawn by
// every band's canvas (concurrently), each replaying only its own rows.
var sh stilus.Shape
sh.SetStroke(path, ctm, style)                    // or SetFill(path, ctm, rule)
c.FillShape(&sh, paint)                           // same bytes as c.Stroke(path, ctm, style, paint)
```

Integration points for other renderers: implement `Blitter` (own pixel
format, GPU upload, coverage masks), `Shader` (called per span, never per
pixel) or consume `Stroker` output through a `LineSink`.

## Shaders

Paints beyond a solid colour, each a `Shader` for `Paint.Shader`; they
know nothing of PDF either, and serve cera as well as any 2D library:

```go
// Images: one byte, one bit or four bytes a pixel, mip levels made on
// first use, nearest or bilinear, with an optional alpha mask.
tex := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneIndex, W: w, H: h, Stride: w, Pix8: pix, Pal: stilus.GrayPalette})
var img stilus.ImageShader
img.SetImage(tex, toDevice, false, 255)        // toDevice: texture pixels → device

// Tiling patterns: a tile repeated under any affine transform; an alpha
// tile with SetColor is the stencil of an uncoloured pattern.
img.SetImageWrap(tile, toDevice, false, 255)   // or SetColor(c) + SetMaskWrap(alphaTile, …)

// Gradients: a ramp of premultiplied colours over t in [0, 1], evenly
// spaced or at knots (interpolated, with exact hard stops).
var g stilus.LinearGradient                    // or RadialGradient (two circles)
g.Ramp, g.Alpha, g.Extend = ramp, 255, [2]bool{true, true}
g.Knots = knots                                // optional, one per ramp entry
g.Set(x0, y0, x1, y1, m)

// Gouraud meshes, without seams: a shader for the mesh's outline or clip,
// set once for all bands, or drawn into a layer.
var mesh stilus.MeshShader
mesh.Alpha = 255
mesh.Set(triangles, m, nil)
c.Fill(outline, m, stilus.NonZero, &stilus.Paint{Shader: &mesh})
stilus.FillMesh(layer, region, triangles, m, nil)

// Layers with opacity, mask and the 16 W3C/PDF blend modes, exact at
// antialiased edges; a non-isolated group drawn onto a copy of its backdrop
// sets Initial (that backdrop) and Alone (the group drawn alone), so that
// the backdrop is removed before blending. Glyph coverage masks per size
// and quarter pixel.
var gc stilus.GlyphCache
gc.FillGlyph(c, fontID, glyphID, outline, m, paint)
```

`Canvas.ClipStroke` clips to the area a stroke paints, for strokes and
stroked text painted with a shader.

## How it is fast

- **Band accumulation**: edges are accumulated as area/cover into a
  32-row band of cells (24.8 fixed point), not a page-sized buffer.
  Paths are clipped analytically first; curves are flattened in device space
  (0.1 px) after culling against the clip.
- **Sparse sweep**: narrow rows (the common case: strokes, glyphs) are
  integrated over their touched cell range; wide rows use a dirty bitset
  and emit the constant stretches between edges as runs. The sweep zeroes
  what it reads – there is no clearing pass.
- **Hairlines** (thinner than one device pixel) are stroked one pixel wide
  in device space by the same stroker, with exact area coverage.
- **Analytic strokes**: between the corners of a polyline (joins, caps,
  inner notches), each segment is bounded by two parallel lines, so each
  pixel's area is the difference of two closed-form half-plane areas – no
  cells, no sweep. Only the bands of rows around corners go through the
  accumulator, with the stroke's real outline edges, so corners, caps,
  round joins and segments meeting end to end are exactly as on the outline
  path. Covers polylines, flattened curves, closed shapes and straight
  dashes; within 2/255 of the outline path (`TestPolylineFastPath`).
- **Prepared geometry** (`Shape`, [ADR 0005](docs/adr/0005-prepared-geometry.md)):
  a page rendered in bands plays every operation in every band, and an
  operation crossing many bands would be transformed, flattened, dashed
  and stroked once per band. A `Shape` records what the rasterizer and the
  analytic strips receive – device edges with their row limits, strips with
  their set-up done – binned by rows; a band replays the records that
  reach it. They are made by the first band that draws the shape, so the
  work stays in the workers. Byte-identical to `Fill`/`Stroke`; 2 000
  hatch lines drawn as 64 bands on one core: 87 → 54 ms (one band: 46 ms).
- **Rectangle clips** (91 % of real PDF clips) cost nothing per pixel;
  other clips are rasterized once into a mask that is never cleared as a
  whole and keeps fully opaque runs as runs.
- **Compositing** on premultiplied RGBA8 with SWAR arithmetic (four
  channels per 64-bit multiply, no divisions), memmove-speed opaque fills.
- **Destination pre-touch**: scattered short strokes are bound by memory
  latency – every row is a cold cache line, and the arithmetic between two
  rows fills the out-of-order window, so the misses are taken one after
  another. Before compositing, `Canvas.Stroke` loads the destination under
  the first 64 rows of each segment up to 256 rows tall: independent loads
  whose misses overlap. Only the rows that fit in the reorder buffer
  together overlap, so the loop is kept to a few instructions per row
  (running sums for the row's x and offset, one load at each end of the
  stretch). Longer segments are left to the hardware prefetcher. Output is
  unchanged; 20 000 short strokes −13…18 %, contours −15 %, mixed drawing
  −7 %, hatching unchanged (Ryzen 7 5800H, min of 60). On a cloud Xeon
  with 4 KB pages the touch itself is 14 % of the short-stroke scene: every
  row of the destination is another page, and the page walks are what the
  loads wait for.

## Numbers

A3 landscape at 150 dpi (2481×1754 px), rectangle page clip, cloud Xeon
@ 2.1 GHz, `go run ./cmd/stilus-scenes` (minimum of 15 runs):

| scene | 1 core | 4 cores (bands) | spec target (1 core) |
|---|---:|---:|---:|
| 2 000 long hatch lines, hairline | 53 ms | 15 ms | ≤ 60 ms ✓ |
| 2 000 long hatch lines, 0.35 mm | 67 ms | 19 ms | – |
| 20 000 short strokes, 0.35 mm | 41 ms | 12 ms | ≤ 40 ms ✗ |
| 20 000 short strokes, hairline | 34 ms | 11 ms | – |
| 30 000 glyph outlines, uncached | 55 ms | 18 ms | – |
| mixed drawing (fills with alpha, dashes, curves) | 49 ms | 15 ms | – |
| 3 000 polylines + 600 circles, 0.35 mm | 84 ms | 24 ms | – |
| 2 000 hatch lines through an elliptic mask clip | 54 ms | 19 ms | – |

Hairlines are drawn one device pixel wide with exact area coverage, like
PDFium (an earlier approximation was faster but 14/255 off on dense
hatching). Against PDFium on the synthetic drawings of the harness: mean
deviation 0.3–1.5/255, SSIM ≥ 0.998 (see `harness/`).

Allocations per rendered scene after warm-up: **0**.

### History

`BenchmarkScenes` at 150 dpi on one core, for every release tag and for
`main`, rendered with [figure](https://github.com/timzifer/figure) by the
[Benchmarks workflow](.github/workflows/bench.yml). Timings from different
machines do not compare, so each run measures all of them anew on one GitHub
runner, round-robin, and replaces the chart: its level can move from one run
to the next, the relations within it are what it shows.

![BenchmarkScenes at 150 dpi per release](https://raw.githubusercontent.com/timzifer/stilus/badges/bench/bench.svg)

Same machine, same scenes, other pure-Go rasterizers (`bench/`, separate
module):

| scene | stilus | x/image/vector¹ | gogpu/gg v0.52.5 (CPU)² |
|---|---:|---:|---:|
| 2 000 hatch lines | 70 ms | 33 400 ms | 14 800 ms |
| 20 000 short strokes | 55 ms | 111 ms | 2 090 ms |

¹ stilus stroker outlines, filled with one `vector.Rasterizer` sized to each
outline's bounding box. ² No clip; gg's rectangle clip is slower still.

## SIMD (experimental, Go 1.26+ `simd/archsimd`)

Built with `GOEXPERIMENT=simd` on amd64, the compositing kernels
(`covOpaque`, `covOver`, `runOver`) use AVX2 (4 px per iteration) or
AVX-512 with VBMI (8 px, `VPMOVWB` pack, `VPERMB` coverage broadcast),
chosen at runtime. Results are bit-identical to the scalar path
(`TestSpanKernels`). Any other build uses the scalar kernels unchanged.

```sh
GOEXPERIMENT=simd go test ./...                # Go 1.26 or 1.27
GOEXPERIMENT=simd go test -run XXX -bench SpanKernels .
STILUS_SIMD=0 | 256   # at run time: scalar only | AVX2 only
```

Kernel throughput, ns per span (same Xeon):

| span | kernel | scalar | AVX2 | AVX-512 |
|---:|---|---:|---:|---:|
| 4 px | opaque coverage | 13 | 16 | 17 |
| 16 px | opaque coverage | 36 | 16 | 14 |
| 64 px | coverage over (alpha) | 282 | 71 | 35 |
| 256 px | run with alpha | 517 | 196 | 74 |
| 1024 px | opaque coverage | 2423 | 846 | 483 |

Spans shorter than 16 px stay scalar: one vector block plus `VZEROUPPER`
costs more than a few scalar pixels. On the scenes this means: fills with
transparency (mixed drawing) −15…20 %, stroke-dominated scenes unchanged,
because their spans are 2–6 px wide. Same picture on a Ryzen 7 5800H
(AVX2 only) with Go 1.27.1: mixed drawing −21 %, contours −8 %, the rest
unchanged.

The experiment's API changed in Go 1.27 (loads and stores take slices:
`LoadUint8x16(s)`, `Store(s)`, `TruncToUint8`). The kernels call small
wrappers in `simd_api_go126.go` / `simd_api_go127.go`, selected by the
toolchain's own `go1.27` release tag, so no extra flag is needed; the
wrappers inline and the generated loops are the same on both. Without
`GOEXPERIMENT=simd` any Go ≥ 1.24 builds the scalar path.

Pitfalls found on the way (Go 1.26, the first still true in 1.27), all
avoided in `simd_amd64.go`:

- The compiler emits no `VZEROUPPER`; without `archsimd.ClearAVXUpperBits()`
  at the end of each kernel, the rasterizer's legacy-SSE float code ran 3–5×
  slower afterwards.
- `ShiftAllRight` with a count compiles to a legacy-SSE `MOVQ` inside the
  AVX loop; division by 255 therefore uses `MulHigh` (`(x+128)·257 >> 16`).
- Vectors inside a struct are copied with legacy-SSE `MOVUPS`; constants are
  kept in plain local variables.

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
  Dash patterns beyond the subdivision budget skip or truncate the affected
  subpath and report `ErrDashBudget`; `Stroker.Truncated()` exposes the same
  state when using the stroker directly. Patterns too dense to resolve (a
  device period of at most 1/4 px, or more than 32 entries per pixel) are
  drawn as a solid stroke at the pattern's mean coverage, caps included, so
  their cost is that of a solid stroke; `Stroker.Coverage()` reports the
  factor. Walking a pattern dash by dash is bounded by 32 transitions per
  device pixel of the subpath.
- Fuzz targets: `FuzzFill`, `FuzzStroke`.

## Development

```sh
go test ./...                                  # tests, reference comparisons, alloc gates
go test -run XXX -bench BenchmarkScenes .      # scene benchmarks at 72/150/300 dpi
go run ./cmd/stilus-scenes -dpi 150 -out /tmp/scenes   # timings + PNGs
go run ./cmd/stilus-scenes -threads 4          # band-parallel rendering
(cd bench && go test -bench .)                 # comparison with x/image/vector and gg
bench/scripts/bench-refs.sh h.json v0.2.0 main   # measure refs side by side, then:
(cd bench && go run ./cmd/benchchart render -db ../h.json -o ../h.svg)
go test -fuzz FuzzStroke                       # fuzzing
```

The benchmark history (`BenchmarkScenes` at 150 dpi per release, charted
by CI on the `badges` branch) always uses the scenes of the working tree:
`bench-refs.sh` builds every ref with the current `internal/scenes`. A
scene added now is therefore charted over past releases as well. Files of
`internal/scenes` that do not compile against a release, because they use
API it lacks, are left out of that release, so the scene's line starts at
the first release that can draw it.

To add a scene, put it in a file of its own in `internal/scenes` that
appends its constructor to `more` in `init`. `scenes.go` holds the
framework and must keep compiling against every release. Then chart it
before committing, with `@` standing for the working tree:

```sh
bench/scripts/bench-refs.sh h.json $(git tag -l 'v*') dev=@
(cd bench && go run ./cmd/benchchart render -db ../h.json -o ../h.svg)
```

## Not here

Display lists, PDF interpretation, colour spaces, transparency groups as
PDF defines them (knockout, soft masks from groups) and caches across
documents belong to the renderer (cera). The `Blitter`/`Shader`
interfaces are where they plug in.

Speed levers tried and measured (Ryzen 7 5800H, A3 at 150 dpi):

- *Band-sorted display list* (record once, play band by band so the band
  stays in cache): slower on every stroke scene, 1 and 4–8 threads, at
  band heights 16–256. Operations crossing a band border are stroked once
  per band, which costs more than the cache misses saved; only uncached
  glyphs gained (−25 % with 8 workers). Still the right structure for M3,
  but not a speed lever on its own. Prepared geometry (`Shape`) removes
  the per-band stroking for operations spanning three bands or more; for
  one crossing a single border, drawing it twice stays cheaper.
- *Deferred span compositing* (spans appended to per-band streams,
  composited band by band): bit-identical, −10…17 % on scattered strokes,
  but +70 % on long hatch lines (recording every row), and deciding per
  operation which to defer needed fragile heuristics. Pre-touching the
  destination (above) gets the same gain without either.
- *Vectorizing the sweep*: on a page-sized fill the integration loop is
  about 13 % of the time and its dirty groups are 4–8 cells, too short for
  a vector prefix sum; the rest is interior runs and edge compositing
  (`BenchmarkWideFill`). Not pursued.
- *Piecewise-quadratic strip coverage*: the difference of the two
  trapezoid distributions along a row is a quadratic on each of up to nine
  pieces, so a row could walk the pieces instead of evaluating two branchy
  half-plane areas per pixel. Bit-identical, but slower on thin strokes:
  a piece ends at nearly every pixel of a 1–2 px strip, and the piece
  changes cost more than the areas. Hairline hatching +13 %. Not pursued.
- *A row kernel of its own for the analytic strip* (so that its loop runs
  from registers): the call per row costs more than the spills it saves on
  rows of three pixels. What helped instead was keeping fewer values live
  across the pixel loop (the clip's border columns and the paint are read
  through pointers, the border rows are taken out of the loop) and
  keeping the per-row helpers small enough to inline: hatching −7…8 %.
- *Fewer branches in the half-plane area* (`area` from |u|: one test for
  the linear middle, then the quadratic of max(h − |u|, 0), mirrored by
  the sign of u): bit-identical, but slower everywhere, measured
  interleaved: hatching +33…42 %, short strokes +9 %. The cases of
  `area` follow the pixels of a strip in a pattern the branch predictor
  learns; the longer dependency chain costs more than the mispredictions
  it saves. Choosing it only for strips of few rows: short strokes
  +7…8 %. There is little to skip either: 95 % of the pixels a hairline
  hatch row visits are partially covered, none fully, 5 % not at all.
  Not pursued.

What remains in stroke scenes is arithmetic: the analytic strip's
per-pixel half-plane areas (`trapezoid.area`, branchy) and the stroker's
outline and band setup; on scattered short strokes, the memory latency of
the destination rows.
