# 0011. Images and patterns sampled by a compositor

- Status: accepted (implemented: `SamplerForm`, `Sampler.Form`,
  `ImageForm`, `ImageShader.Form`, gpu image records)
- Date: 2026-11-13
- Needed by: ADR 0007 (shader paints slower on the GPU than on the CPU)

## Context

After ADR 0009 the gpu target computes gradients itself; `ImageShader`
(images, the tiles of coloured patterns, the stencils of uncoloured ones)
still went through `ShadeSpan` and four uploaded bytes per covered pixel:
images-60 37.7 ms and patterns-200 38.8 ms on the GPU against 20–25 ms
on the CPU canvas (Apple M4, A3 at 150 dpi).

What a `Sampler` reads is set up once by `Setup`/`SetupWrap`: the mip
level (a `*Plane` the texture keeps), the transform from device pixels to
its pixel space, nearest or bilinear, repeating or not. The texture's
planes do not change once made (`Texture`), so a renderer can copy a
plane once and sample it for every pixel.

## Decision

**Core: samplers and image shaders tell what they read.**

```go
type SamplerForm struct {
	Plane    *Plane // the mip level sampled
	Inv      Matrix // device pixel centre → Plane's pixel space
	Bilinear bool
	Wrap     bool
}
func (s *Sampler) Form() (SamplerForm, bool)

type ImageForm struct {
	Image, Mask       SamplerForm
	HasImage, HasMask bool
	Color             uint32
	Alpha             uint8
}
func (s *ImageShader) Form() ImageForm
```

The documentation is the contract: nearest pixels clamped to the edges or
taken modulo the size, bilinear pairs and weights as `split` gives them
(1/256, truncated), the channel mix of `lerp`, alpha and mask by
`mul255`. `TestImageForm` samples from the form alone, over RGBA,
indexed and bit planes, reduced (mip levels), magnified and rotated,
translucent, masked and stencils, repeating or not: nearest exactly
(138 330 pixels), bilinear within 1 per sample (the axis-aligned path
mixes along v first), within 2 for an image and a mask; repeating
textures, in 32.32 fixed point in the sampler and float64 in the test,
on 0 of 203 580 pixels apart.

**gpu: planes are copied, records point at them.** `Target.Blitter`
recognises `*ImageShader` and writes, once per operation and again after
a flush, a record: the colour, alpha, and per sampler the plane's offset
and size, its flags, `Inv`'s linear part in float32, and an integer
origin with the coordinates at its centre, which is the first pixel the
operation draws. The fragment shader computes a pixel's coordinates as
the origin's plus the linear part times the pixel's integer distance from
it, so float32 holds numbers of the size of the operation, not of the
page; repeating coordinates are reduced at the origin in float64. Planes
are copied into the data buffer as RGBA words once per flush (indexed and
bit planes expanded through their palette), keyed by the `*Plane`. An
image whose planes would take more than a quarter of a binding is shaded
on the CPU as before.

## Consequences

| scene (A3, 150 dpi, Apple M4) | CPU canvas | gpu | gpu+read | before |
|---|---:|---:|---:|---:|
| images-60 | 20.3 ms | 2.8 ms | 6.2 ms | 37.7 ms |
| patterns-200 | 24.7 ms | 3.8 ms | 7.5 ms | 38.8 ms |

- Accuracy: images-60 max 2/255, mean 0.016/255; patterns-200 max 3/255,
  mean 0.026/255. `gpu.TestImages` (every kind of plane and sampler, as
  fills and as strokes, on Metal and the software adapter) has no pixel
  beyond 2/255. float32 may put a pixel near a texel border on the other
  texel; for tiles with hard edges that is a whole colour step on that
  pixel, which the scenes do not show.
- Planes are uploaded once per flush, not kept across flushes: a viewer
  redrawing the same images each frame uploads them each frame (the
  texels of the level sampled, about the device pixels the image
  covers). Keeping planes in GPU memory across flushes, keyed by plane,
  is the next lever if that shows.
- `MaskShader` (cached glyph masks) still goes through `ShadeSpan`;
  `MeshShader` followed ([ADR 0012](0012-mesh-forms.md)). Glyph masks are recycled by the cache within a frame, so
  they would need a generation, as clip masks have (ADR 0008).

## Alternatives considered

- *GPU textures and samplers* (`textureSample`): fixed-function filtering
  rounds and weights differently from stilus and has no 1-bit or indexed
  formats; storage-buffer reads keep stilus's arithmetic and need no
  texture per plane in the bind group.
- *Uploading the shaded colours* (as before): the per-pixel CPU cost
  stays.
