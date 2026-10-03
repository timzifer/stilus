# 0006. Union of many paths in one coverage pass

- Status: accepted (implemented: `Union`, `Canvas.FillUnion`)
- Date: 2026-10-03
- Needed by: timzifer/stilus#28; cera's batching of consecutive same-paint
  operations (timzifer/cera#21)

## Context

Opaque shapes of one paint that overlap or abut along antialiased edges
leave light seams when they are drawn one after another. Each fill
composites its own partial coverage over the last, so two edges that
together cover a pixel give 1 − (1 − a)(1 − b) < 1 instead of 1. This is
the conflation artifact of exact-area renderers. cera compares its output
with an exact rendering, and on a hatch of 2.07 px strokes 1.75 px apart,
which cover the area completely, stilus drew 0.971× the ink, with 24.9 %
of the pixels more than 16/255 off. Poppler and Ghostscript are much
closer, because they union a path's geometry before they integrate it.

Within one path, stilus has no such seams: the subpaths' areas are summed
in the accumulator and the NonZero rule applied to the sum. A union of
many paths only needs the same thing, provided that their windings do not
cancel.

## Decision

**Outlines wind in one direction.** The `Stroker`'s outlines (left side
forward, end cap, right side backward, start cap; two loops for a closed
polyline) wind the same way everywhere they cover. That includes inner
corners routed through their vertex, round joins, caps and dashes. The
direction is that of a stroke under the identity unless the transform
mirrors and the stroke is not a hairline (hairlines are outlined in device
space). This held already; it is now documented on `LineSink` and tested
over 600 random strokes with all caps, joins, dashes, curves and mirrored
transforms (`TestStrokeOutlineWinding`). No normalisation of the output
was needed.

**A `Union` collects the elements, and `Canvas.FillUnion` draws them:**

```go
type Union struct { /* unexported */ }

func (u *Union) Reset()
func (u *Union) Len() int
func (u *Union) Fill(p *Path, m Matrix, rule FillRule)
func (u *Union) Stroke(p *Path, m Matrix, st *StrokeStyle)
func (u *Union) Shape(s *Shape)

func (c *Canvas) FillUnion(u *Union, paint *Paint)
```

A `Union` keeps references, like a display list: adding an element only
stores the path, the transform and the style, with what can be derived
once per page rather than once per band: the device box, a stroke's
`strokePrep` and pad, and the orientation. `FillUnion` only reads the
union, so the canvases of all bands can draw one union at the same time,
and it allocates nothing once warm.

`FillUnion` puts every element into the canvas's rasterizer and calls
`Rasterize(NonZero)` once, through the clip and paint chain of a `Fill`.
For every element:

- *Orientation.* Elements that wind the other way are reversed after they
  are added (`Rasterizer.reverse` negates the direction of their edges).
  A stroke is reversed under a mirroring transform unless it is a
  hairline. A fill is reversed when its signed area (exact for curves:
  the integral of x dy − y dx in closed form) has the sign opposite to a
  stroke's outline. Holes are kept, because the whole path is reversed,
  not each subpath.
- *Culling.* Elements whose device box misses the clip are skipped
  before any work, by the box computed once when they were added.
- *Strokes take the analytic path.* `Canvas.Stroke` fills the rows between
  a segment's ends in closed form and composites them directly. In a
  union those rows must be summed with everything else. So
  `Rasterizer.addStrip` records the strip and `Rasterize` adds its pixels'
  areas into the accumulator band by band, as differences of neighbouring
  pixels like the edges' cells. They are sorted by band like the edges.
  The rows around corners and caps still go to the accumulator as outline
  edges. Since everything is summed, the analytic path is valid for any
  paint and any clip here, not only for opaque paint.
- *Shapes* contribute their recorded edges and strips (the analytic
  form), binned by rows. A union of shapes drawn in bands does the
  stroker's work once per page.
- *What cannot be summed* is drawn on its own after the union, as
  `Fill`, `Stroke` or `FillShape` would draw it: EvenOdd fills, and strokes
  whose dash pattern is too dense to resolve (drawn at the pattern's mean
  coverage, which is a scale of the coverage and not an area).

## Consequences

- The issue's hatch (2.07 px strokes, 1.75 px apart) goes from 0.971× ink
  with 23.4 % of the pixels more than 16/255 off to 1.000× and 0 %.
  Against the supersampled union (`TestFillUnionRef`, with overlapping and
  abutting rectangles of both orientations, rotated and mirrored, and dense
  hatches of strokes and hairlines) the result is within the reference's
  usual tolerance. Where edges of different elements cross inside a
  pixel, the sum over-covers that pixel, as it does within one path. That
  is the exact-area accumulation semantics, not the exact union.
- A union of one element draws the bytes of `Fill` for a fill. For a
  stroke it draws within 1/255 of `Stroke`'s outline path
  (`TestFillUnionOne`). Unions of shapes draw the bytes of unions of their
  paths, whole and in bands (`TestFillUnionShapes`).
- Translucent paint is applied once where elements overlap. That is what
  a union means, but it is not the result of drawing the elements one
  after another.
- Abutting shapes of *different* paints still conflate. That needs
  per-sample coverage (an MSAA-like mask mode) and is out of scope.
- A fill whose subpaths wind both ways where they do not overlap (two
  separate squares, one clockwise and one counter-clockwise) keeps parts
  that cancel where they overlap other elements of the union.

**Cost.** A union composites in a second pass: the accumulator is swept
row by row and composited through the blitter chain. `Stroke`'s analytic
rows compute coverage and composite in one fused loop. The coverage work
is the same in both. Measured on one core (cloud Xeon @ 2.1 GHz, A3 at
150 dpi, `BenchmarkUnion`, median of 6; this machine varies by ±10 %).
*separate* is `Stroke` per operation. *union* adds every operation to one
`Union` per page. *shapes* adds them as shapes prepared once per page,
set-up included:

| scene | bands | separate | union | shapes |
|---|---:|---:|---:|---:|
| hatch-2000-hairline | 1 | 50.3 ms | 65.1 ms | 65.4 ms |
|  | 16 | 62.4 ms | 74.8 ms | 68.3 ms |
| hatch-2000-0.35mm | 1 | 61.0 ms | 69.6 ms | 69.4 ms |
|  | 16 | 68.2 ms | 77.2 ms | 66.5 ms |
| short-20000-0.35mm | 1 | 34.8 ms | 44.2 ms | 52.6 ms |
|  | 16 | 60.0 ms | 49.5 ms | 55.4 ms |

`BenchmarkScenes`, which does not use unions, is unchanged against `main`
(geomean +0.6 %, no scene significant over 6 interleaved runs).

For cera:

- Batch the operations where seams can appear: dense hatches, abutting
  fills, strokes along the edges of fills of the same paint. Isolated
  shapes gain nothing from a union and cost the second pass.
- Banded, prefer `Union.Shape` for operations that span several bands,
  as with `FillShape` (ADR 0005).

## Alternatives considered

- *Per-element coverage, unioned in a coverage buffer* (saturating add of
  each element's 8-bit coverage, composited once): it supports every fill
  rule per element, but it costs one sweep per element plus a page-sized
  buffer, it rounds each element to 8 bits before summing, and a stroke
  composited in two parts (analytic rows and corner bands) would be
  counted twice where they meet.
- *A fill rule "covered if any subpath winds ≠ 0"*: the accumulator only
  holds the sum of the windings, so this cannot be evaluated in one pass.
  With consistent orientation it is not needed.
- *Strokes from their outlines only* (no analytic rows in the
  accumulator): simpler, but the edge walk made the hatch union 1.65×
  slower than separate strokes (83 ms against 50 ms), against 1.3× with
  the strips.
- *`FillUnion(paths []*Path, m Matrix, paint *Paint)`*, as the issue
  first proposed: it cannot hold strokes, per-element transforms or
  shapes, and cera's display list mixes them.
