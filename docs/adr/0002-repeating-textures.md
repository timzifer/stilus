# 0002. Repeating textures for tiling patterns

- Status: proposed
- Date: 2026-10-02
- Needed by: cera ADR 0002 (patterns, M7)

## Context

cera draws most tiling patterns by rasterizing one cell into a tile at
device resolution and filling the painted area with a shader that maps each
device pixel through the inverse pattern matrix and wraps it into the tile
(cera ADR 0002, strategy 1). Hatching in CAD drawings — tiny cells, often
rotated — is the main case. Uncoloured patterns are stencils: one alpha tile
serves every colour.

`ImageShader` and `Sampler` already map device pixels into a texture under
any affine transform, sample nearest or bilinearly, take an alpha mask and a
solid colour (`SetColor` + `SetMask` is a stencil), and pick mip levels when
minifying. Outside the texture they **repeat the edge pixels**; there is no
wrap-around.

## Decision

**Wrap mode on `Sampler`.**

```go
// SetupWrap is Setup for a texture that repeats in both directions with
// the period of its base size: texture coordinates are taken modulo W and
// H before sampling, and bilinear samples at the border read the opposite
// edge.
func (s *Sampler) SetupWrap(t *Texture, toDevice Matrix, smooth bool) bool
```

`ImageShader` gets `SetImageWrap` and `SetMaskWrap` alongside `SetImage` and
`SetMask`. A coloured pattern is `SetImageWrap(tile, …)`; an uncoloured one
is `SetColor(c)` + `SetMaskWrap(alphaTile, …)`.

- **Period.** The period is the texture's base size. cera rasterizes the
  tile so that it is exactly one `XStep` × `YStep` step of the pattern at
  device resolution (cells larger than a step are drawn into the tile at
  their wrapped offsets by cera); non-integer steps are carried by
  `toDevice`, not by the texture.
- **Modulo per span, not per pixel.** Along a span the texture coordinate
  is linear; the sampler reduces the first coordinate modulo the period and
  advances incrementally, subtracting the period when it passes it, so the
  inner loop has no division. Axis-aligned, unit-scale tiles copy whole
  runs of a tile row (the `srcRow` fast path, wrapped).
- **Mip levels wrap too.** Downsampling for minified patterns treats the
  texture as periodic, so a level's edges are averaged with the opposite
  edge. For base sizes that are not a multiple of 2ᵏ, level k gets a
  rounded size and the sampler carries the period as a float; the error is
  below what minification already blurs.
- **Average colour.** For cells smaller than a device pixel cera paints a
  solid colour; stilus needs nothing for that.

## Consequences

- Rotated, skewed and scaled hatching is one fill with one shader per band,
  exact up to resampling.
- No new texture formats: index, bit and RGBA planes all wrap, so a
  monochrome hatch tile stays one byte (or one bit) a pixel.
- `Setup`, `SetImage` and `SetMask` keep their edge-repeat behaviour and
  output.

## Alternatives considered

- **Wrap in cera by building a larger tile** (e.g. 4 × 4 cells) and using
  edge repeat: still wrong outside the larger tile, and 16× the memory.
- **A separate `PatternShader` type**: duplicates the sampler's mip and
  filter code for a difference of one coordinate transform.
