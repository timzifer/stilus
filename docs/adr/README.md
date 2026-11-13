# Architecture decision records

Each record states one decision: the context that forces it, what was
decided, and what follows from it. Records are numbered and never renumbered;
a record that is replaced keeps its number and says which record supersedes
it. Status is one of *proposed*, *accepted*, *superseded by NNNN* or
*rejected*.

stilus knows nothing about PDF. The records below cover the rasterizer and
compositing work that [cera](https://github.com/timzifer/cera)'s plan for a
feature-complete PDF renderer needs from stilus; the PDF side of each feature
(reading dictionaries, sampling functions, display list, `Stats.Unsupported`
keys) stays in cera's own ADRs, which each record names. A record is done
when its shaders or kernels exist with tests and cera's ADR can use them.

| ADR | title | needed by | status |
|---|---|---|---|
| [0001](0001-shading-shaders.md) | Shaders for shadings: non-uniform ramps, sampled grids, mesh shader | cera ADR 0001 (M7) | accepted |
| [0002](0002-repeating-textures.md) | Repeating textures for tiling patterns | cera ADR 0002 (M7) | accepted |
| [0003](0003-group-compositing.md) | Group compositing: backdrop removal and shape | cera ADR 0009 (M8) | accepted |
| [0004](0004-knockout-compositing.md) | Knockout compositing by shader | cera ADR 0009 (M8) | proposed |
| [0005](0005-prepared-geometry.md) | Prepared geometry shared by bands | cera's banded rendering | accepted |
| [0006](0006-union.md) | Union of many paths in one coverage pass | cera#21 (batched same-paint operations) | accepted |
| [0007](0007-gpu-compositing.md) | Compositing on the GPU through gogpu/wgpu | GPU viewers (lux, cera viewer) | accepted |
| [0008](0008-gpu-strips.md) | Analytic strips drawn whole by a compositor | ADR 0007 (hatching) | accepted |
| [0009](0009-gradient-forms.md) | Gradients computed by a compositor | ADR 0007 (shader paints) | accepted |
| [0010](0010-gpu-present.md) | Drawing targets into a caller's render pass | GPU viewers in a window (#46, #51) | accepted |
| [0011](0011-image-forms.md) | Images and patterns sampled by a compositor | ADR 0007 (shader paints) | accepted |
| [0012](0012-mesh-forms.md) | Meshes shaded by a compositor | ADR 0007 (shader paints) | accepted |
| [0013](0013-glyph-masks.md) | Glyph masks painted by a compositor | ADR 0007 (shader paints) | accepted |
| [0014](0014-gpu-layers.md) | Layers composited by a GPU target | ADR 0007, cera ADR 0009 | accepted |
| [0015](0015-compositor-pairs.md) | CMYK destinations through two compositors | ADR 0007 (CMYK pages) | accepted |
| [0016](0016-texture-store.md) | Texture planes kept across flushes | ADR 0011 (viewers) | accepted |
| [0017](0017-recorders.md) | Several canvases drawing onto one target at once | cera's banded rendering | accepted |
| [0018](0018-gpu-shader-types.md) | Callers' shaders on the GPU | #67 | accepted |

## Template

```markdown
# NNNN. Title

- Status: proposed
- Date: YYYY-MM-DD
- Needed by: cera ADR NNNN (Mx)

## Context

What forces the decision: the caller's need, the constraints of stilus
(pure Go, no dependencies, every GOOS/GOARCH, 0 allocations after warm-up,
cost proportional to edges and covered spans, nothing PDF-specific).

## Decision

What stilus does, in enough detail to implement it: types, functions,
which existing shader or kernel it extends.

## Consequences

What becomes easier or harder for callers, what stays approximated.

## Alternatives considered

What was rejected and why.
```
