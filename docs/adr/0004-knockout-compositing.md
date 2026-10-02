# 0004. Knockout compositing by shader

- Status: proposed
- Date: 2026-10-02
- Needed by: cera ADR 0009 (transparency remainders, item 4, M8) and
  cera's knockout groups (`transparency.go`)

## Context

In a knockout group (PDF 2.0, 11.4.6.2) every object is composited with
the group's initial backdrop B0 rather than with what earlier objects
drew, and replaces the group's result K in proportion to its shape f:

K' = (1−f)·K + f·X,   X = the object with opacity q = α/f over B0.

stilus composites only by *over*: the canvas puts the shader's colour s
onto the destination D with coverage a as a·s + D·(1−a·αs). Knockout is
not expressible that way. The weight of K would have to be (1−a)
whatever the colour, which needs αs = 1, while the alpha channel of the
result needs αs = αX. Both hold only where B0 is opaque. For the same
reason a `Shape` on `LayerShader`, as ADR 0003 first proposed, would equal
`Mask`, and was not added: shape and opacity differ only where shape
replaces instead of covering.

cera therefore merges knockout objects itself. Per object it:

1. copies B0 into a scratch image over the object's bounding box and
   clears a shape image there (`koBegin`);
2. draws the object with its paint into the scratch image;
3. draws the object a second time, opaque, into the shape image
   (`koShape`);
4. merges scratch and shape into K with a scalar loop over the bounding
   box (`koMerge`).

Each knockout group holds two extra RGBA images of its area: the scratch
and the shape, which uses one of its four channels. Two cases stay
approximated:

- **A group inside a knockout group** takes its shape from its alpha
  divided by the greatest opacity drawn into it (`ink`), because the
  group's own shape fgn is not kept.
- **Alpha is shape** (`AIS`), where a soft mask or constant alpha is shape
  rather than opacity, is ignored (cera's `alpha-is-shape`).

Knockout groups are rare in cera's corpus. Flat knockout groups are
exact today; nested groups and `AIS` are not.

## Decision

Proposed, to be implemented when the corpus shows files with nested
groups in knockout groups or `AIS`, or knockout groups large enough that
the two extra images matter.

**A Source operator in the canvas.** `Paint` gains an operator:

```go
// Op is how a paint is composited onto the destination with coverage a.
type Op uint8

const (
	// OpOver composites s over d: a·s + d·(1−a·αs). The default.
	OpOver Op = iota
	// OpSource replaces d by s in proportion to coverage: a·s + d·(1−a).
	// Pixels with zero coverage, outside the path or the clip, are kept.
	OpSource
)

type Paint struct {
	Color  color.RGBA
	Shader Shader
	Op     Op
}
```

This is Porter-Duff Source bounded by the mask, as Cairo's
`OPERATOR_SOURCE` with a mask. It is not specific to PDF. Solid colours
already have the kernel: `lerpx` blends opaque colours by coverage. The
work is in the shader blitter: `BlitRun` and `BlitCoverage` with
`OpSource`, and the `srcRow` fast path (a row copied where coverage is
full). The `OpOver` paths and their output do not change.

**`KnockoutShader`.** It wraps the object's own shader:

```go
// KnockoutShader paints an object of a knockout group: Inner composited
// with blend mode Blend over the group's initial backdrop Initial, mixed
// with the group's current result Dst by the object's shape. Fill with it
// through a Canvas with OpSource, so that coverage and clip are shape too.
type KnockoutShader struct {
	Inner   Shader      // the object's paint: colour, gradient, image or layer
	Initial *image.RGBA // B0; nil for an isolated knockout group
	Dst     *image.RGBA // K, the destination the canvas draws onto
	Blend   BlendMode
	// Shape, when non-nil, is the object's shape f beyond coverage: the
	// shape plane of a nested group, or the soft mask of an AIS object.
	// Inner's alpha is then taken as f·q: X uses opacity q = α/f.
	Shape *image.Alpha
}
```

For every pixel, ShadeSpan returns S = f·X + (1−f)·K, with f taken from
`Shape` (1 without it). The canvas with `OpSource` then writes
a·S + (1−a)·K = a·f·X + (1−a·f)·K. That is the knockout formula with shape
a·f: geometric coverage, clip and `Shape` multiply, as shapes do in the
specification. X is computed like a `LayerShader` pixel: Inner's colour
with opacity q, blended with B0 by `Blend`, over B0.

**What cera does with it.** An object in a knockout group is one fill
onto K: no scratch image, no shape image, no second rasterization, no
merge loop. For a nested group, cera keeps the group's shape plane fgn in
an `image.Alpha`. It draws every object of the group into that plane with
an ordinary alpha fill, opaque or, for `AIS` objects, with their mask and
constant alpha. It then fills the group as one object with
`Inner = &LayerShader{…}` and `Shape` = that plane.

## Consequences

- Nested groups in knockout groups and `AIS` become exact. Only for these
  cases is a shape separate from opacity needed.
- Knockout groups need no images of their own beyond K. A nested group in
  one costs one `image.Alpha`, a quarter of today's RGBA shape image.
- Each object is rasterized once, and only covered spans are touched,
  instead of three passes over its bounding box. The gain grows with many
  small objects in a large group; it has not been measured.
- `OpSource` is a second composite path through the shader blitter, its
  fast paths and SIMD kernels. This is the main cost, and it must be held
  to the rule that `OpOver` output is unchanged.
- `OpSource` is useful beyond knockout: for example, writing a shader's
  output into a layer without compositing, where callers now clear first.

## Alternatives considered

- **`Shape` on `LayerShader` alone** (ADR 0003 as proposed): equal to
  `Mask` under over-compositing, so it fixes nothing.
- **Keep the merge in cera**, with an `image.Alpha` shape plane and an
  exact nested shape: exact, but it keeps the scratch image, the second
  rasterization and the bounding-box passes per object.
- **A knockout layer type in stilus** (scratch and merge moved from cera
  into stilus): moves the cost without removing it.
- **`OpSource` by a clear followed by an over fill**: wrong under partial
  coverage, as antialiased edges would lose the backdrop instead of mixing
  with it.
