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
| [0003](0003-group-compositing.md) | Group compositing: backdrop removal and shape | cera ADR 0009 (M8) | proposed |

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
