# 0015. CMYK destinations through two compositors

- Status: accepted (implemented: `Canvas.ResetCompositorPair`)
- Date: 2026-11-13
- Needed by: ADR 0007 (CMYK pages on the GPU)

## Context

`Canvas.ResetPair` draws into two images at once, (C, M, Y, A) and
(K, K, K, A), rasterizing once and compositing into both, each with its
paint (`Paint.Second`). A canvas with a compositor drew into one
destination only, so CMYK pages needed the CPU canvas.

## Decision

```go
func (c *Canvas) ResetCompositorPair(dst, second Compositor, region image.Rectangle)
```

Every operation asks both compositors for a blitter, the second with
`Paint.Second`, and blits the same coverage through both. Where both
blitters take strips (`StripBlitter`), the pair does too, so strips stay
whole on both. Everything that decides on paints by both (`invisible`,
`opaque`, the glyph cache's second mask shader) asks whether the canvas
is paired, by images or by compositors.

With two gpu targets this is a CMYK destination on the GPU; a layer
pair (`LayerShader` with `Subtractive` for the CMY image and `Black` for
the K image) composites each layer onto its own target's backdrop.

## Consequences

- `TestCompositorPair` (core): a pair of image compositors that take
  strips gets the bytes of drawing each image alone, steep strokes'
  strips included.
- `gpu.TestPair`: fills, strokes, a gradient, cached glyphs and a layer
  pair on two targets, byte-identical to `ResetPair` on images (Metal and
  the software adapter).
- With this every paint of stilus and both kinds of destination draw on
  the GPU; callers' own shaders go through `ShadeSpan`.
