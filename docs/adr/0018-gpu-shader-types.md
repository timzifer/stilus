# 0018. Callers' shaders on the GPU

- Status: accepted (implemented: `ShaderType`, `Device.NewShaderType`,
  `Shader`, `Record`)
- Date: 2026-11-13
- Needed by: #67; renderers with paints of their own (cera's shadings
  not mapped to stilus's, UI toolkits)

## Context

After ADR 0009–0014 the gpu target computes every paint of stilus
itself; a caller's own `stilus.Shader` is still evaluated on the CPU
(`ShadeSpan`) and uploaded, four bytes per covered pixel. Decided with
the user (#67): one shader for all types, a caller's code reads its
record and textures, types are registered per device.

## Decision

```go
func (d *Device) NewShaderType(name, wgsl string) (*ShaderType, error)

type Shader interface {
	stilus.Shader                     // ShadeSpan: the same colours on the CPU
	Type(d *Device) *ShaderType       // nil: shaded on the CPU on d
	Samplers() []stilus.SamplerForm   // textures it samples
	AppendRecord(dst []byte, r *Record) []byte
}
type Record struct{ X, Y int; … }    // the operation's first pixel
func (r *Record) AppendSampler(dst []byte, i int) []byte
```

**One shader.** The span shader is compiled from `spans.wgsl`, every
registered type's source, and a generated `customColor(id, rec, p)` that
switches on the type's id. `NewShaderType` compiles the whole again and
replaces the pipeline (the old one is released with the device), so a
type that does not compile, or whose names clash with the span shader's,
is refused with the compiler's error naming the type. Spans keep one
pipeline and one draw call a pass; registering is for set-up time, not
while targets record.

**Records.** A caller's shader is painted like stilus's gradients: its
spans (runs, coverage, strips, masked strips) point at a record whose
word 0 is `customKind | id`, the caller's bytes following from word 1,
which its WGSL gets as `rec`. The record is made when the operation's
first span is uploaded, so that `Record.X, Y` is the operation's origin,
for coordinates relative to it.

**Textures.** `Samplers` names the textures; their planes are made room
for together in the target's store (ADR 0016) before `AppendRecord`, and
`AppendSampler` writes a sampler block, the one `ImageShader` records
use, which the WGSL samples with `sampleAt(q, p)` (nearest or bilinear,
repeating or not, stilus's indices and weights).

**Helpers** of the span shader a type may call: `rword`, `rfloat`,
`sampleAt`, `ch`, `pack4`, `mul8`, `lerp8`, `div255`,
`unpack4x8unorm`.

## Consequences

- `gpu.TestShaderTypes`: a procedural shader (stripes) and one sampling
  a texture and tinting it, as fills and strokes, through a target and
  three recorders, against their `ShadeSpan` on the CPU canvas: max
  1/255, Metal and the software adapter; a shader without a type on the
  device is shaded on the CPU; a type registered twice, and one that
  does not compile, are refused.
- The caller keeps two implementations in step, `ShadeSpan` and WGSL;
  the test pattern above (draw both, compare) is how to check them.
  Colours must be premultiplied: a colour above its alpha composites
  differently on the CPU (SWAR) and the GPU (clamped blend).
- Every type makes the span shader larger; their code runs only for
  their spans.
