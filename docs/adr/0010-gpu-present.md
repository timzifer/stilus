# 0010. Drawing targets into a caller's render pass

- Status: accepted (implemented: `Device.NewPresenter`, `Presenter`)
- Date: 2026-11-13
- Needed by: GPU viewers in a window (morsus map window, issues #46, #51)

## Context

A `gpu.Target` (ADR 0007) leaves a texture. A window has to show it, and
the window's toolkit owns the frame: with gogpu, `Context.SurfaceView()`
is the swapchain view and `Context.CommandEncoder()` the frame's encoder,
submitted once at frame end; the device is shared through
`App.DeviceProvider().Device()` and `gpu.NewDevice`.

`gogpu.Texture` has no constructor from a `*wgpu.Texture`, so a target
cannot be drawn with `Context.DrawTexture*`. The detour through a gogpu
texture (`CopyTextureToTexture` per tile) costs a second texture, a
zero-filled upload and a copy, and was slower than the CPU upload in
morsus (#46: 8.3 vs 7.6 ms per frame, `DrawTexture` 0.42 vs 0.23 ms).
Sampling the target's texture in a pass over the surface needs only
public wgpu API, but a pipeline every caller would write by hand.

## Decision

`stilus/gpu` draws targets into a pass the caller began, starting from
the blitter morsus runs (#51):

```go
type PresenterOptions struct{ Slots int }  // 0: DefaultSlots
type DrawOptions struct {
	X, Y  float32         // top-left corner in viewport pixels
	W, H  float32         // 0: the size of Src
	Src   image.Rectangle // empty: the whole target
	Fade  float32         // colours × (1 − Fade); 0 as drawn, 1 nothing
}

func (d *Device) NewPresenter(format gputypes.TextureFormat, opts *PresenterOptions) (*Presenter, error)
func (p *Presenter) Begin(w, h int)        // viewport of the pass, starts the frame
func (p *Presenter) Draw(pass *wgpu.RenderPassEncoder, t *Target, o *DrawOptions) error
func (p *Presenter) Commit() error         // after the last Draw, before submit
func (p *Presenter) Close()
```

- *Options structs, not parameter lists.* What a draw needs grows
  (source rectangles now; transforms, filters later), and a positional
  signature breaks every caller each time. Every field's zero value is
  the default, and nil options are the zero value: `Draw(pass, t, nil)`
  draws the target 1:1 at the origin. That is why the opacity is `Fade`
  and not `Alpha`: a forgotten `Alpha` would be 0 and draw nothing.
- *Source rectangles.* `Src` selects a part of the target (an atlas, a
  sub-tile). The fragment clamps its texture coordinate to the texel
  centres at the edges of `Src`, so a stretched draw filters only
  pixels inside it.

- *One format per presenter.* The pipeline is made in `NewPresenter` for
  the attachment's format; a caller with two formats makes two
  presenters. `Draw` needs no pipeline argument. sRGB formats are
  refused: a target holds sRGB-encoded bytes, an sRGB attachment would
  encode them again.
- *Slots.* Each draw takes a 256-byte slot of a uniform buffer through a
  dynamic offset (rectangle, viewport, source and its clamp, alpha), so many draws share one
  pass. `Commit` writes the frame's slots with one `WriteBuffer`; the
  queue applies it before the frame's submission and after the previous
  one. More draws than slots in a frame is an error; a draw that fades
  out completely or has no area takes no slot.
- *Bind groups.* Shader, layouts and the linear clamp sampler live in the
  `Device`; group 0 is the presenter's slot buffer, group 1 the target's
  texture and the sampler, made on the first `Draw` and cached in the
  `Target` (released by `Target.Close`). A target can be drawn by every
  presenter of its device.
- *Flush.* `Draw` flushes the target. Its commands are submitted at once,
  before the frame's encoder, so the frame samples what was recorded.
- *Blend.* Premultiplied source-over, scaled by `1 − Fade`. Placed 1:1 at
  whole pixels, the samples hit texel centres and return the target's
  bytes; stretched, the sampler filters linearly.

`gpu.NewDevice` keeps taking `*wgpu.Device`. `gpucontext.DeviceProvider`
returns an opaque `gpucontext.Device` around an `unsafe.Pointer`;
accepting it would mean an unchecked cast, and gogpu's own
`DeviceProvider().Device()` already returns `*wgpu.Device`.

## Consequences

- A window draws tiles straight from their targets: no copy, no second
  texture, no upload per tile. morsus measured a 500 px tile at about
  0.15 ms and pixel-identical to the CPU image.
- `TestPresent` draws a target 1:1 into RGBA8 and BGRA8 textures over a
  background: byte-identical opaque, within 1/255 at half opacity.
  `TestPresentSource` draws a part 1:1 (byte-identical) and stretched
  7.5× next to a contrasting colour (nothing bleeds in; without the clamp
  it does).
- The caller owns the pass, its load op and its encoder; the presenter
  only records draws. Resizing the surface is a new `Begin(w, h)`.

## Alternatives considered

- *Positional parameters* (`Draw(pass, t, x0, y0, x1, y1, alpha)`, as
  in the morsus code): shorter, but adding the source rectangle would
  already have broken every caller.

- *`Target.Blit(pass, …)`*: needs the format and a pipeline cache in the
  device, and the per-frame slots somewhere; the slots are a frame's
  state, not a target's. A separate type keeps that state out of
  `Target` and `Device`.
- *Calling it `Blitter`*: `stilus.Blitter` and `Target.Blitter` already
  mean coverage sinks.
- *One uniform buffer per target*: the same target drawn twice in a
  frame (a wrapped map) would see only the last parameters.
- *`firstInstance` and a storage buffer of parameters*: `instance_index`
  does not include the base instance on every backend (DX12).
- *Texture interop in gogpu* (a `gogpu.Texture` from a `*wgpu.Texture`):
  not needed for this path; worth asking gogpu for, but not blocking.
