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
- Meshes get slower: every band writes the whole mesh's record and the
  scenes set the shader up per band. Preparing the record once per page,
  as `Shape` prepares geometry, would remove that.
- `gpu.TestRecorders` draws every scene but the layered one in four
  bands at once, twice, against the CPU canvas of the whole page, also
  under the race detector.
