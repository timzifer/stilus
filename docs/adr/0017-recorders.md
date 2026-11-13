# 0017. Several canvases drawing onto one target at once

- Status: accepted (implemented: `Recorder`, `Target.NewRecorder`)
- Date: 2026-11-13
- Needed by: cera's banded rendering on the GPU (one canvas per band)

## Context

On the CPU a page is drawn in bands, one canvas and goroutine each
(ADR 0005). A gpu target records from one goroutine: its spans, data and
store are one canvas's, and it flushes when its buffers fill, which
touches the GPU. With the GPU compositing, what remains per operation is
CPU work (rasterizing, stroking, set-up), which bands would spread over
cores.

## Decision

```go
func (t *Target) NewRecorder() (*Recorder, error)
type Recorder struct{ … }            // a stilus.Compositor
func (r *Recorder) Blitter(p *stilus.Paint) stilus.Blitter
func (r *Recorder) Err() error
func (r *Recorder) Close()
```

A recorder is a target without a texture of its own: it records into its
own spans, data and store, and draws onto its target's texture. While
canvases record, a recorder touches neither the GPU nor its target: its
buffers grow instead of flushing, and the planes it uploads are kept as
writes for later. The target's `Flush` uploads its own spans and every
recorder's, then draws them in one render pass, its own first, then the
recorders' in the order they were made, each with its bind group.

Recorders draw disjoint parts, as bands do. Two kinds of operation need
what was drawn before them on the GPU and so a flush in the middle of a
band, which a recorder cannot do: layers (ADR 0014) are reported by
`Err` (`ErrLayer`) and not drawn, so pages with layers draw them through
the target; a texture larger than the recorder's store is shaded on the
CPU. Spans beyond the device's storage binding are dropped and reported
(`ErrRecorderFull`).

## Consequences

`gpu.BenchmarkScenesBands`, A3 at 150 dpi, Apple M4 under a load of
about 3.5 from other processes (absolute times above earlier tables),
with readback, one canvas per band on its goroutine:

| scene | 1 band | 4 bands | 8 bands |
|---|---:|---:|---:|
| glyphs-30000-uncached | 34.6 ms | 18.0 ms | 17.5 ms |
| contours-3000 | 27.7 ms | 15.0 ms | 14.4 ms |
| mixed drawing | 18.3 ms | 10.7 ms | 10.2 ms |
| short-20000-0.35mm | 13.8 ms | 8.0 ms | 7.9 ms |
| hatch-2000-0.35mm | 13.6 ms | 8.3 ms | 10.7 ms |
| images-60 | 8.6 ms | 7.8 ms | 8.4 ms |
| meshes-40 | 12.4 ms | 14.2 ms | 15.7 ms |

- Scenes whose time is CPU work scale to about twice as fast at four
  bands; those bound by the GPU and the readback (images, gradients,
  patterns) stay flat.
- Meshes get slower: every band wrote the whole mesh's record, and the
  scenes set the shader up per band. See *Mesh records once per page*.
- `gpu.TestRecorders` draws every scene but the layered one in four
  bands at once, twice, against the CPU canvas of the whole page, also
  under the race detector.

## Mesh records once per page

Added after the first measurements. `MeshForm.Gen` names a shader's
triangles: a counter shared by all mesh shaders, taken by each `Set`.
A target and its recorders share a cache of mesh records by generation,
under a lock: the first band that draws a mesh makes its record, the
others copy it, and later frames find it until the shader's next `Set`
(Alpha, which may change without a `Set`, is written into the copy). The
cache is bounded (32 MB) and emptied when full.

`gpu.TestMeshCache`: 12 meshes in four bands over four frames, records
made once per mesh and again after a `Set`, an Alpha changed without
`Set` drawn, within 1/255 of the CPU canvas, also under the race
detector. `gpu.BenchmarkMeshBands` (40 meshes of 384 triangles set up
once per page, A3 at 150 dpi, with readback), Apple M4, the cache off
and on: 1 band 6.6–8.4 / 6.7–7.3 ms, 4 bands 7.3–7.5 / 7.2–7.6 ms,
8 bands 8.3–8.8 / 7.2–7.5 ms. Making the records was the smaller part:
what remains is the meshes' `Set` on the CPU, once per page, and their
pixels on the GPU, which bands do not divide.

The meshes-40 scene then set its shaders up in every drawing, so every
frame made new records. It now sets each shader up once per resolution,
under a lock, so that bands may draw one copy of the scene at once, and
later frames find the records: `BenchmarkScenesBands` meshes-40 with
1, 4 and 8 bands 13.9 / 21.6 / 35.1 → 10.4 / 7.4 / 6.5 ms. On the CPU
canvas the scene no longer times the set-up: 14.5 → 13.4 ms at 150 dpi
(the benchmark history measures every release with the scene of the
working tree, so its line moves for all of them).
