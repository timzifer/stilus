# 0003. Group compositing: backdrop removal and shape

- Status: accepted
- Date: 2026-10-02
- Needed by: cera ADR 0009 (transparency remainders, items 3 and 4, M8)

## Context

stilus composites layers with `LayerShader`: an isolated group drawn into an
image of its own, with constant alpha, an optional mask and the 16 blend
modes, exact under partial coverage. cera builds knockout groups, soft masks
and group nesting on top of it (`transparency.go` in cera). Two compositing
cases of the transparency model are not covered and are approximated by cera
today:

1. **Non-isolated groups that are themselves blended or knocked out**
   (`non-isolated-blend`): the group is drawn onto a copy of its backdrop,
   so its result includes the backdrop, and compositing that result over the
   backdrop again with a blend mode counts the backdrop twice. PDF 2.0
   11.4.8 removes the backdrop's contribution first:
   C = Cn + (Cn − C0) · (α0 / αgn − α0), with the group's own alpha αgn from
   drawing the group alone.
2. **Alpha is shape** (`alpha-is-shape`): objects whose alpha is shape
   rather than opacity need a shape channel next to colour and alpha inside
   the groups that use it.

Both are rare in cera's corpus; cera implements them only when the corpus
shows real files (cera ADR 0009). This record fixes what stilus provides
then, so the kernels fit the existing compositing code.

## Decision

**Backdrop removal.** `LayerShader` gains optional `Initial` and `Alone`
(`*image.RGBA`, same rectangle as `Src`): the backdrop the group started
from, and the same group drawn without it. When both are set, `Src` is taken
to include `Initial`, and `ShadeSpan` first computes the backdrop-free colour
per pixel by the formula above, with αgn from `Alone`, then blends and
composites it like any layer. The kernel is per pixel and scalar; it runs
only for such groups, so the SWAR and SIMD paths of ordinary layers are
untouched.

**Shape.** `LayerShader` gains an optional `Shape *image.Alpha`, a shape
channel the caller keeps inside groups that use alpha-is-shape. With a shape
the layer is composited as shape × opacity instead of opacity alone, by the
separated compositing formula of the specification for that group. Canvases
do not grow a shape channel: the caller draws shape-only objects into the
`image.Alpha` with an ordinary alpha blitter.

None of the new fields changes output when nil.

As implemented:

- **Backdrop removal** is done in premultiplied colours, where the formula
  is C·αgn = sn·(αgn + (1−αgn)·α0)/αn − (1−αgn)·b0, clamped to [0, αgn]
  (sn, αn the layer's, b0, α0 the backdrop's). The first term's scale is 1
  where αn is the union of α0 and αgn, as in a layer drawn onto its
  backdrop, but 8-bit rounding makes that rare, so it is one 64-bit
  multiplication by a reciprocal from a table of 256, no division. Pixels
  where the group is opaque or empty are copied or cleared; the rest does
  not branch. The backdrop-free pixels are written into the span and the
  usual paths (opacity, mask, every blend mode) run on them, so their
  output is that of a layer holding those pixels; `srcRow` declines such
  layers. Within 1 level of the formula in floating point; a group of
  Normal objects comes out as the group drawn alone, exactly in the tests.
- **Shape is not added.** On a `LayerShader` it would change nothing that
  `Mask` does not: the shader's result is linear in the premultiplied
  layer pixel for a fixed straight colour, so compositing it with shape f
  over the backdrop, (1−f)·D + f·(s + D·(1−αs)), is f·s + D·(1−f·αs), the
  layer masked by f. Shape and opacity differ only where shape replaces
  what is below instead of covering it, i.e. inside knockout groups, and
  cera merges knockout objects itself with a shape plane
  (`koMerge` in cera's `transparency.go`). For `AIS`, cera draws an
  object's shape plane with its soft mask and constant alpha instead of
  opaque, and a group's shape plane by drawing its objects into an
  `image.Alpha`; both are ordinary fills.

## Consequences

- cera's approximations become exact for the two cases, at the cost of one
  extra layer (backdrop removal) or one alpha plane (shape, kept by cera)
  for exactly the groups that need them.
- `BenchmarkBackdropRemoval`: on drawn content (ellipses, half of them
  translucent) a Normal layer with backdrop removal costs about a third of
  a Multiply layer without it; on random pixels, where branches
  mispredict, about the same.
- `LayerShader` keeps one type and one entry point; the slow paths are
  chosen by nil checks once per span.

## Alternatives considered

- **Separate shape and opacity channels in every layer**: exact for both
  cases together, but doubles layer memory and compositing work for every
  group to fix a handful of files.
- **A new `GroupShader` type**: duplicates mask, alpha and blend handling
  that `LayerShader` already has.
