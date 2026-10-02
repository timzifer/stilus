# 0005. Prepared geometry shared by bands

- Status: accepted (implemented: `Shape`, `Canvas.FillShape`)
- Date: 2026-10-02
- Needed by: cera's banded rendering (one canvas per band, every band
  playing the whole display list)

## Context

A page is rendered in horizontal bands, one canvas each, so that workers
never share a destination row. Every band plays the whole operation list,
and each operation redoes per band what does not depend on the band:
the path's bounds, the transform of its points, flattening, dashing, the
stroke outline with its joins and caps, the analytic strip set-up, and for
cached glyphs the key and the map lookup, all before the band's rows can
reject the operation.

Measured on one core (Ryzen 7 5800H, A3 at 150 dpi), the page drawn as n
bands one after another:

| scene | 1 band | 16 bands | 64 bands |
|---|---|---|---|
| hatch-2000-hairline | 45.7 ms | 75.8 ms | 134 ms |
| short-20000-hairline | 38.4 ms | 57.3 ms | 119 ms |
| glyphs-30000-uncached | 61.0 ms | 114 ms | 283 ms |
| text-30000-cached | 18.9 ms | 57.1 ms | 176 ms |
| contours-3000 | 86.9 ms | 98.7 ms | 177 ms |
| gradients, images, layers, patterns | flat | flat | flat |

`BenchmarkScenesParallel` uses two bands per worker, 32 with 16 threads.
The profiles at 64 bands show where the time goes:

- *hatch*: half is still coverage (`solidRows`); the rest is the stroker
  repeating `fastLine` and `strokePolyAt` (transform, `hypot`, segment
  set-up) for lines that cross every band.
- *glyphs-uncached*: `Path.Bounds` alone is 41 %, for culling paths the
  band then rejects.
- *text-cached*: `sigmaMax` (20 %) and the map lookup (30 %) run before
  `FillGlyph` learns that the glyph is outside the band.

Shaders already solved this for themselves: `MeshShader.Set` transforms
and bins its triangles once, and one shader then serves every band,
concurrently. Geometry has no such step. A `Path` cannot cache it
implicitly: its `Verbs` and `Points` are exported and may change between
draws, with nothing to invalidate a cache. The band-sorted display list
measured in the README stroked operations crossing a band border once per
band, which is exactly the cost here.

## Decision

Proposed: geometry gets an explicit preparation step, like `MeshShader`,
built and measured in two stages, each with a go/no-go on the numbers
above.

**A `Shape`: a path prepared for one transform.**

```go
// Shape is a path prepared for drawing under one transform: its device
// bounds, and its edges or strips binned by bands of rows, so that a
// canvas drawing it touches only what crosses its clip's rows. The zero
// value is empty; SetFill and SetStroke (re)build it, keeping storage.
// After that it does not change and may be drawn by several canvases at
// once. It keeps no reference to the path.
type Shape struct { /* unexported */ }

func (s *Shape) SetFill(p *Path, m Matrix, rule FillRule) bool
func (s *Shape) SetStroke(p *Path, m Matrix, st *StrokeStyle) bool
func (s *Shape) Bounds() image.Rectangle // device pixels it can touch

func (c *Canvas) FillShape(s *Shape, paint *Paint)
```

The shape copies what it needs, so a later change to the path does not
reach it; "versioned" means the caller rebuilds it, as with
`MeshShader.Set`, when path, transform or style change (another zoom is
another shape). `SetFill`/`SetStroke` reuse the shape's slices, so a
display list that rebuilds its shapes per page allocates nothing once
warm, as the 0-allocation rule asks.

**What it holds.**

- *Fill*: the device-space edges `Rasterizer.AddPath` makes (flattened,
  culled to nothing outside the path's own bounds), sorted by their first
  row, with a start index per band of 2^k rows like `meshBins`. The
  canvas hands the edges of the bands its clip crosses to the rasterizer
  through a new entry point that takes edges instead of a path; `fillRect`
  is decided at `SetFill` (pixel-aligned rectangle) and kept as a flag.
- *Stroke*: what the stroker emits before rasterization, in its two
  forms: the outline edges (dashed, joined, capped) for the general path,
  and for the analytic fast path the strip descriptors `segFast.middle`
  takes (the line, its parallel, the end bands), binned the same way.
  Which form a draw uses still depends on the paint and the clip (opaque
  paint, masks, fractional borders, `mayOverlap`), so both are kept and
  `FillShape` makes the decisions `Canvas.Stroke` makes today, from the
  same inputs. A dense dash keeps its mean coverage as a scalar.

`FillShape` must give the bytes of `Fill`/`Stroke` with the same path,
transform and style: the same functions run on the same values, only
earlier. Tests compare the two over the scenes and over random paths and
clips, band by band.

**Stage 1: prototype the stroke strips, unexported.** The hatch scenes are
where repeated set-up is largest and the stroke fast path the most
involved. A package-internal `Shape` with stroke strips only, measured
with a benchmark that draws the scenes as n bands on one core (the table
above; to be added as `BenchmarkSceneBands`), decides whether the API is
worth it: go if hatch at 32 bands recovers at least half of its banding
overhead without slowing one band.

**Stage 2: the API above, fill edges included,** and cera prepares the
operations whose device bounds span more than one band; the others stay
`Fill`/`Stroke`, which costs nothing extra.

**Cached glyphs** need no `Shape`: a glyph is small and lies in one or two
bands. The cost is in `FillGlyph` doing key work before culling. The
caller can know a text run's box (font bounding box, advances) and cull
whole runs against the band before calling `FillGlyph`; stilus documents
that on `FillGlyph`, and cera's text drawing should be checked for it. A
per-glyph early reject inside `FillGlyph` would need the outline's bounds,
which is the `Path.Bounds` cost again.

## Consequences

- Operations spanning many bands pay their set-up once per page instead
  of once per band; per band they cost a bin lookup and their own rows.
- Memory: the prepared geometry of the operations cera chooses to
  prepare lives as long as the page. A stroke strip is ~100 bytes; a
  flattened fill edge ~40 bytes. Long hatch lines are cheap; preparing
  30 000 uncached glyph outlines would take tens of MB, so glyphs go
  through `GlyphCache` and are not prepared.
- A second drawing path for strokes must stay byte-identical to
  `Canvas.Stroke` as both evolve. The decision logic is shared code, not
  copied; the equality tests guard the rest.
- Workers read shapes concurrently. As built, a shape's records are made
  by the first `FillShape` that needs them, under the shape's lock, and
  only read afterwards (see Outcome); `Set*` must not run while a shape is
  drawn. The race detector runs over a parallel band test
  (`TestShapeConcurrent`).

## Outcome

Both stages are implemented (`shape.go`); stage 1's criterion was met by
a wide margin, so the API went in as proposed, with these differences:

- *Records made on first use.* `SetFill`/`SetStroke` copy the path and
  the style and compute the bounds and the stroke's paint-independent
  decisions; the records are made by the first `FillShape` that needs
  them, with that canvas's stroker, under the shape's lock. A stroke has
  two forms (analytic and outline) and a draw takes one, decided by the
  paint and the clip; making both up front cost a second stroker run per
  shape, which made contours at 16 bands slower than drawing directly.
  Made lazily, only the form in use is built, and the work runs in the
  band workers instead of a serial preparation pass.
- *What a fill records.* `Rasterizer.AddPath` replaces a curve outside the
  clip by its chord; a shape flattens every curve once. Inside the clip
  both cover the same pixels (the difference is a closed loop outside it),
  and the bytes are the same.
- *Exactness.* `FillShape` gives the bytes of `Fill` exactly, and of
  `Stroke` exactly except where `Stroke`'s culling against its clip
  replaces curves outside it by their chords: the chord changes the
  polyline, and so which rows are analytic. `TestFillShapeEqualsDirect`
  (600 random fills and strokes, all caps, joins, dashes, dense dashes,
  mirrored and sheared transforms, opaque, translucent and shader paint,
  fractional rectangle and mask clips, bands of 1 to 150 rows) and
  `TestSceneBandsShapes` (every scene, every operation a shape, 1, 7 and
  24 bands) compare against `Stroke` with that culling off. This makes a
  shape *more* consistent across bands than `Stroke`: drawn in bands, a
  shape stays within 3/255 of itself drawn whole (`TestFillShapeBands`),
  while `Stroke` per band differed from `Stroke` on the whole page by up
  to 63/255 on random curved strokes.
- *Pre-touch.* Replaying a scattered stroke is bound by destination
  latency like stroking it; `FillShape` loads the rows under the strips
  first, then adds the edges, then composites the strips.
- *Bins* are rows of 2^k ≥ 16, at most one per record and four entries
  per record; forms under 16 records or 64 rows are filtered linearly.

Measured on one core (cloud Xeon @ 2.1 GHz, 4 vCPUs, A3 at 150 dpi,
`BenchmarkSceneBands`, median of 6; this machine varies by ±10 %).
*direct* plays every operation in every band; *culled* skips the
operations whose device rows miss the band, as a display list with
bounds does; *shapes* is *culled* with every operation spanning three
bands or more prepared once per page (set-up included):

| scene | bands | direct | culled | shapes |
|---|---:|---:|---:|---:|
| hatch-2000-hairline | 1 | 46.1 ms | 46.9 ms | 49.0 ms |
|  | 16 | 53.6 ms | 58.1 ms | 46.2 ms |
|  | 32 | 68.7 ms | 68.0 ms | 53.0 ms |
|  | 64 | 86.9 ms | 88.9 ms | 54.1 ms |
| short-20000-hairline | 1 | 30.4 ms | 30.7 ms | 33.9 ms |
|  | 16 | 51.2 ms | 34.7 ms | 36.8 ms |
|  | 32 | 67.4 ms | 40.4 ms | 39.4 ms |
|  | 64 | 104.2 ms | 46.0 ms | 50.9 ms |
| glyphs-30000-uncached | 1 | 52.0 ms | 51.4 ms | 49.8 ms |
|  | 16 | 96.1 ms | 54.3 ms | 52.0 ms |
|  | 32 | 142.3 ms | 55.7 ms | 56.4 ms |
|  | 64 | 233.3 ms | 60.3 ms | 60.4 ms |
| mixed-drawing | 1 | 48.4 ms | 45.6 ms | 47.2 ms |
|  | 16 | 57.0 ms | 60.2 ms | 55.5 ms |
|  | 32 | 71.6 ms | 73.0 ms | 56.7 ms |
|  | 64 | 107.7 ms | 96.4 ms | 59.5 ms |
| contours-3000 | 1 | 79.1 ms | 74.1 ms | 76.0 ms |
|  | 16 | 98.0 ms | 96.6 ms | 99.1 ms |
|  | 32 | 118.5 ms | 111.4 ms | 108.9 ms |
|  | 64 | 157.4 ms | 146.7 ms | 113.6 ms |
| text-30000-cached | 1 | 11.4 ms | 11.4 ms | 10.7 ms |
|  | 16 | 50.6 ms | 49.9 ms | 52.0 ms |
|  | 32 | 90.7 ms | 83.9 ms | 92.6 ms |
|  | 64 | 178.3 ms | 177.8 ms | 172.9 ms |

Stage 1's criterion, hatch at 32 bands recovering half its banding
overhead without slowing one band: 69 % recovered (81 % at 64 bands);
`BenchmarkScenes` on one band is unchanged against `main` (geomean
−0.4 %, no scene significant over 6 interleaved runs). Cached text gains
nothing here because the scene's glyphs carry no bounds to cull by; the
culling cera's text drawing should do is the lever for it.

Conclusions for cera:

- The cheapest lever is the one this ADR listed as an alternative's
  side note: skip, per band, the operations whose device box misses it.
  For scattered short strokes and glyphs it removes nearly all of the
  banding overhead; `Stroke` and `Fill` cull too, but only after
  `sigmaMax`, `Path.Bounds` and a transform per operation per band.
- Prepare as shapes the operations spanning **three bands or more**. An
  operation crossing a single band border is cheaper drawn twice: its
  shape's records are cold again by the time the second band draws it
  (in isolation, cache-hot, the shape is 10–15 % faster for such a
  stroke, and 25 % slower for a small curved fill, whose curves outside
  each band `Fill` reduces to chords).
- Cached glyphs: as decided, no `Shape`; `FillGlyph` now documents that
  text runs should be culled against the band before their glyphs are
  drawn.

## Alternatives considered

- *Caching bounds or edges inside `Path`*: the exported fields can change
  without notice; a cache would need a version every caller bumps, which
  is the explicit step under another name, minus thread safety.
- *A band-sorted display list in stilus*: measured slower (README), it
  belongs to cera, and it does not remove the per-band set-up of
  operations that cross bands.
- *Fewer, taller bands* (one per worker instead of two): free, and
  halves the repeated set-up, at the price of load balance on pages with
  uneven content. Worth measuring in cera first, as the cheapest lever.
- *Binning every operation into tiles up front* (a full two-pass
  renderer): the largest gain on paper, but a rewrite of the canvas, and
  most of it is cera's display list's job.
