# 0008. Analytic strips drawn whole by a compositor

- Status: accepted (implemented: `Strip`, `StripBlitter`, gpu strip spans)
- Date: 2026-11-13
- Needed by: ADR 0007 (GPU compositing slower than the CPU on hatching)

## Context

ADR 0007 composites the canvas's spans on the GPU. Strokes lost: a long
thin line is a coverage span of a few pixels on each of ~1 700 rows, and
the CPU still computed every pixel's coverage (two half-plane areas,
`trapezoid.area`) and recorded 19 bytes per row. Hatching took 37 ms on
the GPU against 24 ms on the CPU canvas (Apple M4, A3 at 150 dpi); the
profile was in the core's row emission, not in the GPU.

The middle of a stroke segment, between its corners, is already
described in closed form: the analytic stroke path (`segFast`) has, per
strip, the two parallel lines (unit normal, two distances), the row
range and the x of the lines at each row (`rowCtx`), and computes every
row from them. That description is a few numbers, whatever the strip's
length.

## Decision

**Core: strips leave whole when the blitter can take them.**

```go
type Strip struct {
	NX, NY, K1, K2 float64 // pixels between K2 <= NX·x + NY·y <= K1
	XL, XR, SL     float64 // row j: columns [floor(XL+j·SL), floor(XR+j·SL)]
	Y0, Y1, X0, X1 int     // rows and the clip's columns
}
func (s *Strip) Area(u float64) float64          // the half-plane area
func (s *Strip) Row(j int, cov []uint8) (int, []uint8) // the canvas's bytes

type StripBlitter interface {
	Blitter
	BlitStrip(s *Strip)
}
```

Under a compositor whose blitter is a `StripBlitter`, with no mask clip,
`segFast.strip` hands the thin strips (those without an interior run) to
`BlitStrip` instead of emitting their rows. Rows on a clip border with
fractional coverage, first or last row and rows reaching a border
column, still go through the blitter chain as `solidRows` sends them, so
a strip is cut into runs of rows around them. `Strip.Row` is the
contract: blitting each row's `Row` gives the canvas's bytes, which
`TestStripBlitterScenes` checks over the scenes, under the page clip and
under a clip with fractional borders on all four sides.

The same hook serves `Canvas.Stroke` and `FillShape`, which both draw
through `segFast.strip`; it needs no opaque paint, since without a mask
the chain only scales the clip's borders, which stay on the CPU.

**gpu: a strip is one instance.** Mode 3 of a span: its rectangle is the
strip's rows and the clip's columns, its datum the offset of eight words
(the normal, both distances, the lines' x at the first row, the slope,
an integer origin). The vertex shader draws the parallelogram around the
strip's rows, two pixels wider on each side than the columns at each
row's centre; the fragment shader tests the row's columns, one more on
each side, and computes the coverage as stilus does, quantized the same
way, in float32 relative to the origin (first row, floor of the left
line's x there), so that the magnitudes stay small.

## Consequences

`gpu.BenchmarkScenes`, A3 at 150 dpi, Apple M4, wgpu v0.34.14:

| scene | CPU canvas | gpu | gpu+read | gpu before (0007) |
|---|---:|---:|---:|---:|
| hatch-2000-hairline | 24.4 ms | 4.0 ms | 8.0 ms | 36.9 ms |
| hatch-2000-0.35mm | 27.7 ms | 4.2 ms | 8.4 ms | 40.5 ms |
| short-20000-0.35mm | 19.4 ms | 9.2 ms | 13.1 ms | 17.1 ms |
| short-20000-hairline | 16.5 ms | 8.3 ms | 11.7 ms | 15.7 ms |
| contours-3000 | 39.1 ms | 21.5 ms | 25.8 ms | 37.8 ms |
| mixed drawing | 23.0 ms | 13.1 ms | 16.8 ms | 15.9 ms |
| hatch-2000-maskclip | 28.8 ms | 27.4 ms | 38.3 ms | 27.1 ms |
| glyphs-30000-uncached | 31.5 ms | 28.3 ms | 33.8 ms | 28.6 ms |

- Stroke scenes are now 2–6× faster than the CPU canvas, readback
  included. What remains on the CPU is the stroker, the corners through
  the accumulator and the strips' set-up.
- Under a mask clip the strips went row by row at first: the mask
  multiplied each row on the CPU. See *Masks* below.
- Accuracy: float32 instead of float64 moves a pixel's coverage across a
  quantization step at times; the scenes stay within 1/255 (most pixels
  identical), `gpu.TestStrips` (48 lines at every angle and six widths,
  opaque and translucent, fractional clip) within 2/255, which it shows
  without strips as well, from stacked translucency.
- The CPU canvas is unchanged: the hook is one more branch per strip,
  not per row; `BenchmarkScenes` before and after, interleaved eight
  times, within noise (medians −5.6…+4.7 %, minima −2.7…+1.0 %).
- The software adapter computes the strips too, correctly and more
  slowly than rows (its interpreter shades the parallelogram's spare
  pixels): hatching 48 s at 72 dpi. CI runs `TestStrips` on it.

## Masks

Added after the first measurements. A strip under a mask clip carries the
mask, `Strip.Mask`, a view of the canvas's clip mask:

```go
type ClipMask struct{ … }
func (m *ClipMask) Bounds() image.Rectangle
func (m *ClipMask) Gen() uint64               // same Gen, same content
func (m *ClipMask) Row(y int, dst []uint8) []uint8
```

A pixel's coverage is then round(cov·mask/255), as `maskBlitter` has it,
and `Strip.Row` applies the mask too, so the contract stays the canvas's
bytes (`TestStripBlitterScenes` adds an elliptic mask within the
fractional clip). The canvas reuses its masks for later clips; `Gen`, a
counter shared by all canvases, names a mask's content, so that a
compositor can keep a copy. The gpu target copies each mask it meets
into the data buffer once per flush (cleared outside each row's valid
range, which the canvas never clears as a whole) and the strip's
parameters say where; a mask too large for a binding sends the strip's
rows instead.

hatch-2000-maskclip, Apple M4, 150 dpi: CPU canvas 27.7 ms, gpu 2.9 ms,
with readback 5.7 ms (27.4 ms before). Within 1/255 on the scene,
`gpu.TestStrips/mask` within 2/255 as without the mask.

## Alternatives considered

- *Merge the rows of a strip into coverage blocks* (one instance for
  several rows of bytes): keeps the CPU's per-pixel areas, the larger
  half of the cost.
- *A compositor entry point for `solidRows`'s rows*: the same per-pixel
  work on the CPU, only a cheaper recording.
- *Exact arithmetic on the GPU in float64*: WGSL has no f64 in fragment
  shaders on most backends; the relative origin keeps float32 within a
  quantization step.
