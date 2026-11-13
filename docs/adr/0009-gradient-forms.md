# 0009. Gradients computed by a compositor

- Status: accepted (implemented: `GradientForm`, `LinearGradient.Form`,
  `RadialGradient.Form`, gpu gradient records)
- Date: 2026-11-13
- Needed by: ADR 0007 (shader paints slower on the GPU than on the CPU)

## Context

A compositor (ADR 0007) gets a paint's `Shader` and can only call
`ShadeSpan`: the gpu target evaluated every shader on the CPU and
uploaded four bytes per covered pixel. Gradients took 37 ms on the GPU
against 28 ms on the CPU canvas (gradients-200, Apple M4, 150 dpi).

A gradient's colour at a pixel needs little: its parameter t, a linear
function of the pixel centre for a linear gradient, the root of a
quadratic for a radial one, and a lookup in the ramp (nearest, or a blend
between knots). The ramp, knots, extension, outside colour and alpha are
exported fields of `Gradient` already; what `Set` derives (the inverse
transform folded into coefficients, the circles relative to the first,
the knots as validated) is not, and the gpu package cannot read it.

## Decision

**Core: a gradient tells how it computes t.**

```go
type GradientForm struct {
	Radial bool
	A, B, C float64                     // linear: t = A·x + B·y + C
	Inv Matrix                          // radial: device → gradient space
	X0, Y0, R0, DX, DY, DR, QA float64
	LinearEq bool
	Knots []float64                     // as Set read them, nil if none
}
func (g *LinearGradient) Form() (GradientForm, bool)
func (g *RadialGradient) Form() (GradientForm, bool) // false: paints nothing
```

Its documentation is the contract: the roots, which one wins (radius not
negative, within the range drawn), and the colour from t through the
`Gradient` fields. `TestGradientForm` computes 540 000 pixels from the
form and the exported fields alone, linear and radial, with and without
knots (a hard stop among them), extended or not, translucent, under
rotated and scaled transforms, and gets `ShadeSpan`'s bits.

Only stilus's own gradients have a form; `ImageShader`, `MeshShader`,
`LayerShader` and the caller's shaders stay `ShadeSpan` for a compositor.

**gpu: a gradient is a record, its spans point at it.** `Target.Blitter`
recognises `*LinearGradient` and `*RadialGradient` with a form and
returns a blitter that writes the gradient's record into the data buffer
(flags, the form's numbers as float32, the ramp, the knots), once per
operation and again after a flush. Its spans are those of a solid colour
– runs, coverage, strips, masked strips – with bit 29 of the datum set
and the record's offset in place of the colour; the fragment shader
computes t at the pixel centre and the colour from it with stilus's
integer arithmetic (rounded index, knot blend in 1/256, alpha by
div255). A gradient that paints nothing gets a blitter that records
nothing.

## Consequences

| scene (A3, 150 dpi, Apple M4) | CPU canvas | gpu | gpu+read | before |
|---|---:|---:|---:|---:|
| gradients-200 | 25.3 ms | 2.8 ms | 5.9 ms | 37.1 ms |

- The gradient shading moves off the CPU entirely, with the four bytes
  per pixel it uploaded.
- Accuracy: t in float32 lies at times on the other side of the boundary
  between two ramp entries than in float64, and the pixel takes the
  neighbouring entry: one ramp step. On the scene (ramp steps of 1/255)
  max 2/255, mean 0.003/255; `gpu.TestGradients` (steps of 8/255,
  knots, outside colour, strokes, a mask) 3 pixels of 25 200 one step
  off, the rest within 2/255.
- Records cost 17 words plus one or two per ramp entry per operation;
  1 KB for a 256-entry ramp.
- `Form` exposes `Set`'s derived numbers as API: changing how a gradient
  computes t now means changing `GradientForm` and the gpu shader with
  it. The equality test guards the first, the gpu tests the second.
- Images and patterns followed on the same pattern
  ([ADR 0011](0011-image-forms.md)).

## Alternatives considered

- *A GPU interface in the core* (`Shader` with a WGSL method): the core
  would know about WGSL and one GPU API; a form is plain numbers any
  renderer can use.
- *Exported fields instead of `Form`*: `Set`'s derived state would become
  mutable from outside, and its invariants (`ok`, knots validated) the
  caller's problem.
- *Evaluating `ShadeSpan` into a texture per gradient*: the CPU cost
  stays, only the upload changes shape.
