# 0014. Layers composited by a GPU target

- Status: accepted (implemented: `Layer`, layer records, `Target.Err`)
- Date: 2026-11-13
- Needed by: ADR 0007 (layers and groups on the GPU), cera's transparency
  groups (cera ADR 0009) drawn by a GPU viewer

## Context

`LayerShader` composites an isolated group, drawn into an image of its
own, onto the backdrop: the layer's pixels times `Alpha` and a `Mask`,
blended with the backdrop by one of the 16 blend modes, the backdrop of
a non-isolated group removed first (`Initial`, `Alone`), on ink amounts
for CMYK (`Subtractive`, `Black`). It reads the backdrop from `Dst`, the
image the canvas draws onto, and returns the colour `s` that the canvas
then composites source-over with the coverage, which makes the blend
exact under partial coverage.

On a gpu target the backdrop is the texture being drawn. A render pass
cannot read its own attachment, so the backdrop has to be copied before
the layer's operation; and a page drawn on the GPU draws its groups on
the GPU too, into targets, not images. So two sources of layers:

1. `*stilus.LayerShader` with images (groups drawn on the CPU): `Src`,
   `Mask`, `Initial`, `Alone` in memory; `Dst` ignored, the backdrop is
   the target.
2. Groups drawn into gpu targets, composited onto another target.

## Decision

**A layer operation snapshots what it reads.** When `Target.Blitter`
gets a layer paint it:

1. flushes the target, and the source targets of a GPU layer, so that
   their textures hold everything drawn before;
2. copies into a *layer buffer* (a fourth storage binding) the region R
   of the layer (its rectangle within the target): the backdrop
   (`CopyTextureToBuffer`, when the mode reads it), the layer, mask,
   initial backdrop and group alone (images by `WriteBuffer`, targets by
   `CopyTextureToBuffer`), as RGBA words in rows aligned to 256 bytes, a
   mask's alpha in the word's top byte, zero outside the mask;
3. records the operation's spans as for any paint, with a layer record:
   mode, flags, alpha, R, and the offset and stride of each copy.

The spans are drawn at the next flush, which the next layer operation
forces before it overwrites the layer buffer; the queue orders the copies
and writes before that flush. One layer operation thus costs one render
pass and its copies.

**The fragment shader computes `s` as `LayerShader.ShadeSpan` does**,
from the copies: the backdrop removal in integers, Normal with alpha and
mask by `mul255`, Multiply and Screen in integers on premultiplied
colours, the other separable modes on opaque pixels as `blendTable`
(rounded) and the general case and the non-separable modes in float32
where stilus uses float64; `Subtractive` complements and `Black`'s mode
choice as there. The hardware then blends `s` over the target with the
coverage.

**gpu-native layers:**

```go
// Layer composites the target Src, offset by (X, Y), onto the target it
// is drawn onto: LayerShader's composition with targets for images.
type Layer struct {
	Src            *Target
	X, Y           int
	Mask           *Target // its alpha multiplies the layer; nil none
	Alpha          uint8
	Blend          stilus.BlendMode
	Initial, Alone *Target // non-isolated groups, as LayerShader's
	Subtractive    bool
	Black          bool
}
func (l *Layer) ShadeSpan(y, x int, dst []uint32) // transparent: GPU only
```

`Layer` is a `stilus.Shader` so that it can be a `Paint.Shader`; drawn
onto anything but a target it paints nothing.

## Consequences

- Layers on the GPU, whether drawn on the CPU or the GPU, with the
  canvas's clip and antialiasing as for any fill.
- A layer operation flushes: layered pages take a render pass and a copy
  per layer.
- The layer buffer holds up to five copies of R; it is bounded by the
  device's storage binding size (128 MB on Metal; a page at 150 dpi is
  17 MB per copy). A layer that does not fit is not drawn and the
  target's `Err` reports it.
- Not byte-identical: float32 where stilus computes float64.

## Outcome

Built as decided. `gpu.TestLayers` composites 22 layers onto a backdrop
of opaque and translucent fills, filled with partial coverage at their
edges: every blend mode, alpha 255 and 170, masks, two non-isolated
groups (one masked), Screen and ColorBurn on ink amounts, Hue and
Luminosity as a CMYK pair's black image. Against the CPU canvas with
`LayerShader` reading its backdrop from the image: max 2/255 as
`LayerShader` with images on the target, max 2/255 as `Layer` with every
image drawn into a target first; the same on the software adapter.

layers-24 (24 layers of 90 × 70 mm in images, Normal, alpha and soft
mask), Apple M4, A3 at 150 dpi: CPU canvas 19.7 ms, gpu 16.0 ms, with
readback 18.1 ms (CPU shading before: 17.5 / 22.1 ms). The layers are
images here, written into the layer buffer per operation, about 1 MB
each; groups drawn into targets are copied on the GPU instead.

## Alternatives considered

- *Textures and samplers for the copies*: `textureLoad` would do, but
  the bind group would need a view per layer; buffers keep one binding
  and stilus's byte layout.
- *Ping-pong targets* (draw the layer into a second texture that becomes
  the target): no copy of the backdrop, but every later operation would
  move to the other texture, and spans of other paints would have to
  wait for it.
