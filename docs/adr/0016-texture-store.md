# 0016. Texture planes kept across flushes

- Status: accepted (implemented: the gpu target's store, `DropTextures`)
- Date: 2026-11-13
- Needed by: ADR 0011 (viewers redrawing their pages)

## Context

ADR 0011 copied a texture's planes into the data buffer once per flush.
A viewer redraws its page every frame, so every frame uploaded every
image again: a 2400 × 1600 photo drawn at half size (its 1200 × 800 mip
level, 3.8 MB) took 5.5 ms a frame on an Apple M4, most of it the upload.
A texture's planes do not change once made (`stilus.Texture`), so there
is nothing to upload a second time.

## Decision

A target keeps a *store*, a fifth storage binding that outlives flushes,
and a map from `*stilus.Plane` to the plane's place in it. A plane not
in the store is converted to RGBA words and written there once
(`Queue.WriteBuffer`, ordered before the flush that draws it); image
records point into the store. The map keeps the planes alive; when the
planes of an operation do not fit, the target flushes (the spans read
the planes where they are), empties the store and starts again, growing
it up to `min(storage binding, 256 MB)`. `Target.DropTextures` empties it
on request, after a flush.

Layers' images and clip and glyph masks stay per operation or flush:
they change.

## Consequences

- The photo, frame after frame: 5.5 → 1.8 ms. images-60 and
  patterns-200, drawn once per frame with readback: 6.2 → 5.6 ms and
  7.5 → 7.6 ms (their textures are small).
- Memory: the store holds what was drawn until it is full; a viewer that
  changes documents calls `DropTextures`.
- `gpu.TestTextureStore` draws eight planes of 360 KB into a store of
  1 MB, in one frame and over three, emptying it several times: byte for
  byte the CPU canvas.
- A plane must not change while it is in the store, as stilus already
  requires of textures.
