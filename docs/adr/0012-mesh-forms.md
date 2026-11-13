# 0012. Meshes shaded by a compositor

- Status: accepted (implemented: `MeshForm`, `MeshFormTriangle`,
  `MeshEdge`, `MeshShader.Form`, gpu mesh records, scene meshes-40)
- Date: 2026-11-13
- Needed by: ADR 0007 (shader paints slower on the GPU than on the CPU)

## Context

After ADR 0009 and 0011 the last paint the gpu target still shaded on the
CPU for every pixel was `MeshShader` (Gouraud shadings, PDF types 4–7),
besides cached glyph masks. No scene measured meshes. The new scene
meshes-40 (40 meshes of 384 jittered triangles, vertex colours, ramp
parameters, constant alpha; `Set` per drawing) takes 20.4 ms on the CPU
canvas and 22.6 ms on the GPU (Apple M4, A3 at 150 dpi, readback
included); on the CPU, `ShadeSpan` is 54 % of it, compositing 13 %, `Set`
8 %.

`Set` already does what a renderer needs: it transforms every triangle
into device space and prepares the rows its pixel centres fill (two left
and two right edges from their upper ends, so that triangles sharing an
edge compute the same crossing) and the plane of its values.

## Decision

**Core: a mesh shader shows its prepared triangles.**

```go
type MeshForm struct{ … }             // a view, valid until the next Set
func (s *MeshShader) Form() (MeshForm, bool)
func (f MeshForm) Len() int
func (f MeshForm) Triangle(i int) MeshFormTriangle
func (f MeshForm) Ramp() Ramp         // nil for colours
func (f MeshForm) Alpha() uint8
func (f MeshForm) Bounds() image.Rectangle

type MeshEdge struct{ X, Y, Slope float64 }
type MeshFormTriangle struct {
	R0, R1, C0, C1 int
	L, R           [2]MeshEdge
	X0, Y0         float64
	V, GX, GY      [4]float64
	N              int // 4 colour bytes or 1 ramp index
}
```

A view, not a slice, so that `Form` does not allocate. The documentation
is the contract: the pixels of a row lie between `ceil(lo − ½)` and
`ceil(hi − ½)` of the edges' crossings, the last triangle containing a
pixel wins, its values are the plane's at the pixel centre rounded half
up, colours clamped and premultiplied, times `Alpha`. `TestMeshForm`
(60 meshes of 30 random, overlapping, flat and thin triangles, colour and
ramp, two alphas): the pixels the form's triangles contain are exactly
those the shader paints (137 000 of 571 200), their colours within 1
(the shader steps in 40.24 fixed point), within one ramp step for ramps.

**gpu: a mesh is a record with its own grid.** `Target.Blitter`
recognises `*MeshShader` and writes, per operation, a record: a grid of
cells of 16 pixels or more (at most 4 096 cells over the mesh's bounds),
the triangle ids per cell in stream order, the triangles relative to the
bounds' minimum (so that float32 holds small numbers), and the ramp. The
fragment shader walks the pixel's cell backwards, takes the first
triangle whose rows and columns contain the pixel, and computes its
values as the form says. A record larger than a quarter of a binding is
shaded on the CPU.

## Consequences

| scene (A3, 150 dpi, Apple M4) | CPU canvas | gpu | gpu+read | before |
|---|---:|---:|---:|---:|
| meshes-40 | 20.4 ms | 4.1 ms | 8.3 ms | 22.6 ms |

- Accuracy: meshes-40 max 1/255; `gpu.TestMeshes` (overlapping, flat
  and translucent triangles, colours and ramp, fills, strokes and a mask
  clip, on Metal and the software adapter) max 1/255. float32 edge
  crossings can give a pixel on a shared edge to the other triangle,
  whose values there are almost the same.
- The record is about 30 words per triangle plus the grid, written per
  operation: 46 KB for a mesh of 384 triangles. `Set` stays on the CPU.
- A pixel costs the triangles of its cell walked from the last; meshes
  with many triangles overlapping in few cells cost more per pixel.
- meshes-40 joins the benchmark scenes, so the history chart has it for
  releases from this one on.
- Cached glyph masks (`MaskShader`) followed
  ([ADR 0013](0013-glyph-masks.md)).

## Alternatives considered

- *Rasterizing the triangles with the GPU into a layer, sampled by the
  spans*: a render pass per mesh, a texture per mesh, and the GPU's own
  fill rule and interpolation instead of stilus's; the record keeps one
  pass and stilus's arithmetic.
- *Exporting the triangles as a slice*: an allocation per `Form`, or a
  slice of internal state the caller could change.
