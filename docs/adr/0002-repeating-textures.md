# 0002. Repeating textures for tiling patterns

- Status: accepted
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
- **Modulo per span, not per pixel.** Along a row the texture coordinate
  is linear in x. The sampler keeps it in 32.32 fixed point, reduced modulo
  the period: a span starts from the row's coordinate at x = 0 plus x
  steps, multiplied and reduced in 128 bits, and every further pixel adds
  the step and subtracts the period when it passes it. The inner loop
  neither converts nor divides, and because integer sums are exact a pixel
  gets the same coordinate whichever band, tile or span it is drawn in.
  Axis-aligned, unit-scale tiles copy whole runs of a tile row (the
  `srcRow` fast path within a period, otherwise one period copied and then
  doubled in place).
- **Mip levels wrap too.** Downsampling for minified patterns treats the
  texture as periodic. For base sizes that are not a multiple of 2ᵏ,
  level k gets the size W/2ᵏ rounded, and each of its pixels averages the
  base pixels in its share of the period (blocks of 2ᵏ ± 1), so a level
  still repeats with a whole number of pixels and `toDevice` carries the
  non-integer scale; the error is below what minification already blurs.
  Sizes that divide reuse the ordinary levels.
- **Stencils.** A solid colour through a mask (also without wrapping) is
  looked up in a table of the colour times each level of alpha, and masks
  whose colours are levels of alpha are interpolated on one channel.
- **Average colour.** For cells smaller than a device pixel cera paints a
  solid colour; stilus needs nothing for that.

## Consequences

- Rotated, skewed and scaled hatching is one fill with one shader per band,
  exact up to resampling.
- No new texture formats: index, bit and RGBA planes all wrap, so a
  monochrome hatch tile stays one byte (or one bit) a pixel.
- `Setup`, `SetImage` and `SetMask` keep their edge-repeat behaviour and
  output.
- Repeating textures are limited to 2³⁰ pixels a side, so that periods fit
  the fixed point; `SetupWrap` reports false beyond.
- Wrapped sampling costs no more than edge-repeat sampling of the same
  tile (`BenchmarkWrapPattern`: rotated tiles and hatches are faster, as
  their inner loops step integers instead of converting floats).

## Alternatives considered

- **Wrap in cera by building a larger tile** (e.g. 4 × 4 cells) and using
  edge repeat: still wrong outside the larger tile, and 16× the memory.
- **A separate `PatternShader` type**: duplicates the sampler's mip and
  filter code for a difference of one coordinate transform.
