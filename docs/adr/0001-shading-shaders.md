# 0001. Shaders for shadings: non-uniform ramps, sampled grids, mesh shader

- Status: proposed
- Date: 2026-10-02
- Needed by: cera ADR 0001 (shadings, M7)

## Context

cera compiles PDF shadings once per document into colour tables and
triangles and never evaluates a PDF function in a raster worker (cera
ADR 0001). Drawing them is stilus's part. stilus already has:

- `LinearGradient` and `RadialGradient` (the two-circle form of PDF type 3,
  larger root first, with `Extend`) over a `Ramp`: premultiplied colours at
  **evenly spaced** parameters, looked up **nearest**.
- `ImageShader` with bilinear sampling of RGBA textures.
- `FillMesh`: Gouraud triangles written into a layer by pixel centre and the
  top-left rule, without antialiasing, then composited with `LayerShader`.

What cera's plan needs beyond that:

1. **Hard stops at exact positions.** Stitching functions put colour
   breaks at arbitrary t. With an evenly spaced, nearest ramp a break moves
   by up to half an entry — about one device pixel for a 1024-entry ramp
   over 2000 px — and smooth parts get stairs one entry wide.
2. **Type 1 (function-based) shadings** as a grid sampled in the function's
   domain, interpolated bilinearly.
3. **Meshes without a layer per band.** `FillMesh` needs an RGBA layer the
   size of the region and a second pass to composite it; every band a
   full-page mesh covers pays a band of layer memory and traffic.

## Decision

**Non-uniform ramps.** `Gradient` gains an optional `Knots []float32`: the
parameter of each `Ramp` entry, non-decreasing from 0 to 1, the same length
as `Ramp`. With knots, colours are interpolated linearly between the two
entries around t; a knot that appears twice with different colours is a
hard stop exactly there. Lookup stays O(1) per pixel: `Set` builds, into a
buffer kept by the gradient, an index of 256 uniform buckets to the first
knot of each bucket; a lookup reads the bucket and scans forward over the
knots inside it. Without knots, behaviour and output are unchanged (nearest
entry, evenly spaced), so existing callers and golden tests are unaffected.

**Sampled grids need no new shader.** A type 1 shading is an RGBA
`Texture` of the sampled grid (64 × 64 up to 256 × 256, built by cera)
drawn with `ImageShader.SetImage(tex, domainToDevice, true, alpha)`. The
caller clips the fill to the domain's rectangle, so the edge repetition of
`Sampler` is never visible.

**`MeshShader`.** A shader that draws Gouraud triangles span by span,
without a layer:

```go
// MeshShader paints triangles, transformed into device space,
// interpolating their vertex colours (or ramp parameters) barycentrically.
// Pixels covered by no triangle are transparent. Fill the mesh's outline,
// the caller's path or the clip with it through a Canvas, so that the
// outer edge is antialiased and clipped like any fill.
type MeshShader struct {
	Alpha uint8
	// unexported: device-space triangles, ramp, bucket grid, scratch
}

// Set prepares the shader for tris, which m maps to device space; with a
// non-empty ramp the vertices' T is looked up in it. It reports false for
// singular or non-finite transforms, and keeps its buffers.
func (s *MeshShader) Set(tris []MeshTriangle, m Matrix, ramp Ramp) bool
```

`Set` transforms the triangles to device space once and bins them into a
uniform grid of buckets (about 32 × 32 device pixels each, bounded at 4096
buckets) by bounding box. `ShadeSpan` walks the span's buckets; for each
pixel centre it tests the bucket's triangles with the same edge functions
and top-left rule as `FillMesh`, takes the last triangle in stream order
that covers it (as `FillMesh` does by overwriting), and interpolates. Inside
one triangle the barycentric weights are linear along the span, so a run of
pixels in one triangle costs additions only. Buffers are kept between `Set`
calls: no allocations after warm-up.

`FillMesh` stays: it is the reference the shader is tested against
(identical output for every pixel inside the mesh) and remains useful for
callers that want the mesh as an image.

## Consequences

- cera draws axial, radial, function-based and mesh shadings with stilus as
  it is plus `Knots` and `MeshShader`; nothing in stilus learns about PDF
  shading types.
- No seams: triangles that share an edge never both cover a pixel centre,
  and no pixel inside the mesh is antialiased against another triangle. The
  outline is antialiased by the fill that carries the shader.
- Hard colour edges between triangles inside a mesh stay aliased, as in
  `FillMesh`, PDFium and MuPDF.
- A mesh costs one fill per band instead of a layer, a mesh pass and a
  composite; the bucket grid is built once per `Set`, so cera keeps one
  shader per shading and worker.

## Alternatives considered

- **Every triangle as an antialiased fill**: seams where antialiased edges
  meet, and one rasterizer pass per triangle.
- **Keep only `FillMesh`**: correct, but costs a band-sized layer and an
  extra composite for every band a mesh touches.
- **Longer evenly spaced ramps instead of knots** (e.g. 16 384 entries):
  64 KB per gradient, still not exact, and cache misses on every lookup.
