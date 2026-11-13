# 0013. Glyph masks painted by a compositor

- Status: accepted (implemented: gpu coverage-mask records)
- Date: 2026-11-13
- Needed by: ADR 0007 (shader paints slower on the GPU than on the CPU)

## Context

Under a compositor, `GlyphCache.FillGlyph` cannot blit a cached mask
into the destination; it fills the mask's box with a `MaskShader` (the
colour through the mask, at the mask's device position). The gpu target
shaded that on the CPU and uploaded four bytes per pixel: text-30000-
cached took 14.4 ms on the GPU against 8.1 ms on the CPU canvas (Apple
M4, A3 at 150 dpi, readback included). After ADR 0012 it is the last of
stilus's paints shaded per pixel on the CPU.

`MaskShader` needs no form: its fields (`Mask`, `X`, `Y`, `Color`) are
exported and say everything. What it does not say is whether the mask's
bytes stay: the cache evicts and recycles masks, so the same
`*image.Alpha` can hold another glyph in the next operation of the same
frame. A copy keyed by the pointer, as planes are (ADR 0011), would need
a generation per mask, as clip masks have (ADR 0008).

## Decision

No core change. The gpu target recognises `*MaskShader` and copies the
mask's bytes into the operation's record, with the colour and the mask's
device rectangle; the record is uploaded with the operation's first span
and again after a flush, from that copy. The fragment shader takes the
byte under the pixel (0 outside the mask) and multiplies the colour as
`mul255` does. A glyph's mask is a few hundred bytes, a quarter of what
its shaded pixels took. A mask larger than a quarter of a binding is
shaded on the CPU.

## Consequences

| scene (A3, 150 dpi, Apple M4) | CPU canvas | gpu | gpu+read | before |
|---|---:|---:|---:|---:|
| text-30000-cached | 8.1 ms | 4.2 ms | 6.8 ms | 14.4 ms |

- text-30000-cached and glyphs-30000-uncached byte-identical to the CPU
  canvas; `gpu.TestGlyphMasks` (300 glyphs at subpixel positions, opaque
  and translucent, under a fractional clip and a mask clip, and a
  `MaskShader` drawn directly) within 1/255, on Metal and the software
  adapter.
- A glyph drawn twice in a frame is copied twice. Copying once per flush
  would need a mask generation in `GlyphCache`; the copies cost less
  than the cache lookups that come before them.
- With this, every paint of stilus is painted on the GPU except
  `LayerShader`, which followed ([ADR 0014](0014-gpu-layers.md)), and
  CMYK pairs; callers' own shaders go through `ShadeSpan`.

## Alternatives considered

- *A glyph atlas on the GPU, keyed by the cache's glyph key*: fewer
  bytes per frame, but the cache would have to tell the compositor about
  evictions; worth it only if copying shows in profiles.
