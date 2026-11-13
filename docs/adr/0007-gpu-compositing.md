# 0007. Compositing on the GPU through gogpu/wgpu

- Status: accepted (implemented: `Compositor`, `Canvas.ResetCompositor`,
  module `gpu`)
- Date: 2026-11-13
- Needed by: viewers and UI toolkits that present pages on the GPU (lux,
  a cera viewer, gogpu/gg as CPU filler behind a GPU surface)

## Context

A caller that shows what stilus draws on a GPU surface today renders into
an `*image.RGBA` on the CPU and uploads the whole image: every pixel of
the page crosses the bus once per frame, also those nothing was drawn
onto, and compositing (the larger half of the time in fill scenes, and
everything in layers) runs on the CPU.

[gogpu/wgpu](https://github.com/gogpu/wgpu) is WebGPU in Go without cgo
(Metal, Vulkan, DX12, GLES, a software adapter, and the browser under
`js/wasm`), with WGSL compiled by gogpu/naga. It runs headless: render to
a texture, copy to a buffer, read back. Its software adapter interprets
SPIR-V on the CPU, which lets CI without a GPU run the shaders.

The constraints of stilus stand: the core module has no dependencies,
builds on every GOOS/GOARCH, does not allocate after warm-up, and its cost
is edges plus covered spans. Its value is the sparse, exact coverage
(analytic strokes, exact-area accumulation, prepared geometry, unions),
which stays on the CPU: a GPU rasterizer of its own (compute binning,
vello-style) would be another library with another accuracy, not a
backend of this one.

What crosses the `Blitter` interface is already the right unit for a GPU:
`BlitRun(y, x0, x1, alpha)` is a constant quad, `BlitCoverage(y, x, cov)`
a row of coverage bytes, both sparse. This is the “sparse strips” split
of vello_hybrid: the CPU produces coverage, the GPU paints and composites
it.

## Decision

**Core: a canvas can draw through a `Compositor`.** One exported
interface and one method, no dependency:

```go
// Compositor composites what a Canvas draws when its destination is not
// an image.RGBA in memory, such as a GPU texture. Blitter returns the
// blitter coverage painted with p goes to; it is called once per drawing
// operation and its result is used only until the next call. The blitter
// must composite source-over, premultiplied, in call order.
type Compositor interface {
	Blitter(p *Paint) Blitter
}

func (c *Canvas) ResetCompositor(dst Compositor, region image.Rectangle)
```

Under a compositor the canvas never touches pixels itself: the fused
solid strip kernel (`segFast.solid`), the destination pre-touch and the
direct glyph mask blit are off, so all coverage leaves through the
blitter chain (clip mask and border fractions still applied on the CPU).
The glyph path then fills the glyph's box with a `MaskShader`, as it does
under a mask clip. Coverage is the same as on an image; only who
composites differs. `ResetPair` (CMYK) is not combined with a compositor.

**Module `github.com/timzifer/stilus/gpu`** (directory `gpu/`, its own
`go.mod` like `bench/` and `harness/`, depends on gogpu/wgpu):

```go
type Device struct{ … }                      // pipeline, shared by targets
func Open(*Options) (*Device, error)          // headless instance, adapter, device
func NewDevice(d *wgpu.Device) (*Device, error) // the caller's device
func (d *Device) Close()

type Target struct{ … }                       // RGBA8Unorm texture, premultiplied
func (d *Device) NewTarget(w, h int) (*Target, error)
func (t *Target) Blitter(p *stilus.Paint) stilus.Blitter // stilus.Compositor
func (t *Target) Clear(c color.RGBA)
func (t *Target) Flush() error                // upload and draw what was recorded
func (t *Target) ReadRGBA(dst *image.RGBA) error
func (t *Target) Texture() *wgpu.Texture
func (t *Target) Close()
```

*Recording.* Every blit becomes one instance of 16 bytes in emission
order: origin (x, y), size (w, h), the premultiplied colour, and a mode
with its datum:

| mode | datum | fragment |
|---|---|---|
| run | alpha | colour × alpha |
| coverage | byte offset into the coverage buffer | colour × cov[x] |
| colours | word offset into the colour buffer | colour[x] |

Coverage bytes are appended to one buffer, packed four to a word. Runs of
one operation with the same `x0`, `x1` and alpha on consecutive rows are
merged into one quad of several rows, so a rectangle or the interior of a
wide fill costs one instance. Within one operation the spans do not
overlap (the `Blitter` contract), and where the opaque stroke path
composites parts twice they are one colour, for which source-over is
commutative; merging is therefore free to move a row into an earlier
quad.

*Paints.* A solid colour is drawn by the GPU from the instance. A
`Paint.Shader` is evaluated on the CPU (`ShadeSpan`, multiplied by the
coverage) and uploaded as colours: correct for every shader that reads
only its own sources (gradients, images, patterns, meshes, glyph masks).
A `LayerShader` reads its backdrop `Dst` from memory and so cannot read
the texture; layers stay on the CPU in this decision. Shaders evaluated
in WGSL from the shader's parameters (gradients first) are a later step:
it needs read access to their set-up from another package, which is an
API decision of its own.

*Drawing.* One pipeline: a quad per instance from `vertex_index`, the
fragment reads its coverage or colour from storage buffers by
`position.x − x0`, and the fixed-function blend is
`One, OneMinusSrcAlpha` – source-over on premultiplied colours, in
primitive order. `Flush` writes the buffers, draws all instances in one
render pass (`LoadOp` clear after `Clear`, load otherwise) and submits;
`ReadRGBA` flushes, copies the texture in a second encoder and maps it.
Recording grows retained slices and does not allocate when warm; the GPU
buffers grow by doubling.

*Accuracy.* The GPU blends in floating point and rounds once when it
stores, where stilus rounds `mul255` and the blend separately. A pixel
may differ by 1/255 per operation composited onto it; the tests bound the
deviation from the CPU canvas over the scenes (max ≤ 4/255 per channel,
mean < 0.05/255), not byte-identity.

## Consequences

- A caller with a GPU surface draws a page with the same canvas calls
  and gets a texture; only spans travel (16 bytes per run quad, 1 byte
  per edge pixel), not the page.
- CPU time drops by the compositing share; rasterization and stroking
  are unchanged. Shader paints still cost their `ShadeSpan` plus four
  bytes per covered pixel uploaded, until they get WGSL counterparts.
- Bands and workers: one target per frame, recorded by one canvas. A
  canvas per band recording into its own target, or one target fed by
  several recorders in band order, is possible later; this decision
  records from one goroutine.
- `LayerShader` (blend modes, soft masks, groups) and CMYK pairs are not
  supported on a target. Pages with them render on the CPU.
- Not byte-identical to the CPU canvas; `TestCanvasTiles`-style
  guarantees hold for the coverage, not for the stored bytes.
- The core gains an interface and a reset; the `gpu` module carries the
  dependency, so `go get github.com/timzifer/stilus` stays
  dependency-free.

## Outcome

Built as decided. `TestCompositorScenes` (core) draws every scene through
a compositor returning stilus's own blitters: byte-identical to the canvas
drawing onto the image, so switching off the fused kernel, the pre-touch
and the direct glyph blit changes no coverage. `gpu.TestScenes` on Metal
(Apple M4): 13 scenes at 72 dpi, max 1/255, mean ≤ 0.0005/255; most are
byte-identical. The layers scene blends Normal and reads only its layers,
so it runs on a target too.

`gpu.BenchmarkScenes`, A3 at 150 dpi, Apple M4, wgpu v0.34.14, one core
recording, `gpu` = record and submit, `gpu+read` = with the readback a
caller without a GPU surface would need:

| scene | CPU canvas | gpu | gpu+read |
|---|---:|---:|---:|
| mixed drawing | 23.4 ms | 15.9 ms | 21.1 ms |
| short-20000-0.35mm | 19.4 ms | 17.1 ms | 23.0 ms |
| short-20000-hairline | 17.2 ms | 15.7 ms | 21.1 ms |
| glyphs-30000-uncached | 31.9 ms | 28.6 ms | 33.7 ms |
| contours-3000 | 39.1 ms | 37.8 ms | 47.3 ms |
| hatch-2000-maskclip | 28.7 ms | 27.1 ms | 39.1 ms |
| hatch-2000-hairline | 23.7 ms | 36.9 ms | 56.2 ms |
| hatch-2000-0.35mm | 28.5 ms | 40.5 ms | 61.3 ms |
| text-30000-cached | 8.2 ms | 9.4 ms | 14.5 ms |
| gradients-200 | 28.1 ms | 37.6 ms | 42.3 ms |
| images-60 | 20.5 ms | 32.7 ms | 36.8 ms |
| patterns-200 | 24.1 ms | 34.5 ms | 39.7 ms |
| layers-24 | 14.3 ms | 17.6 ms | 20.9 ms |

Fills with transparency gain most (−32 %); scattered strokes and glyphs
gain a little. Two kinds of scene lose:

- *Hatching*: a long thin line is a coverage span of three pixels on
  each of ~1 700 rows, 19 bytes recorded per row. The profile is mostly
  in the core: without the fused strip kernel the analytic rows go
  through `emitCoverage` and the border-fraction blitter (48 % and 31 %
  cumulative), the target's recording is 22 %. Drawing the strips whole
  on the GPU ([ADR 0008](0008-gpu-strips.md)) took hatching from 37 to
  4 ms.
- *Shaders*: `ShadeSpan` on the CPU plus four bytes per covered pixel.
  Gradients are computed on the GPU since [ADR 0009](0009-gradient-forms.md)
  (37 → 3 ms), images and patterns since
  [ADR 0011](0011-image-forms.md), meshes since
  [ADR 0012](0012-mesh-forms.md); cached glyph masks still go through
  `ShadeSpan`.

The software adapter (`Options.Fallback`) draws the scenes as Metal
does, max 1/255, in 82 s at 72 dpi: it interprets SPIR-V. An earlier
wgpu (v0.32.1) read storage buffers in the fragment stage as zero and
shaded the shared diagonal of a quad twice; v0.34 fixed both, and the
render pass and the copy of its target no longer conflict in one encoder.
`Open` takes a software adapter only when asked for one, since it is too
slow to be chosen silently. CI, without a GPU, runs the primitives on it;
the scene tests skip without a hardware adapter.

## Alternatives considered

- *Upload the CPU image.* What callers do now; costs the page per frame
  and keeps compositing on the CPU.
- *Rasterize on the GPU* (compute: flatten, bin, tile coverage). Large,
  a different accuracy model, and none of the analytic stroke or union
  work carries over; a separate project if ever.
- *Coverage into an A8 texture, then one full-screen composite per
  paint.* Needs a pass per operation or a texture atlas per paint and
  touches whole tiles, not spans.
- *A `Blitter` of the gpu module plugged into `Rasterizer` directly,
  without core changes.* Loses the canvas: clips, analytic strokes,
  shapes, unions and the glyph cache all live in `Canvas` and write the
  image through internal blitters.
