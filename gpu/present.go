package gpu

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu"
)

//go:embed present.wgsl
var presentWGSL string

const (
	// slotSize is the uniform data of one draw (present.wgsl, Slot).
	slotSize = 80
	// slotStride is the distance of two slots: dynamic offsets must be
	// multiples of 256 on every backend.
	slotStride = 256
	// DefaultSlots is the number of draws per frame a Presenter makes room
	// for when PresenterOptions give none.
	DefaultSlots = 1024
)

// PresenterOptions configure NewPresenter. The zero value is the default.
type PresenterOptions struct {
	// Slots is the number of draws per frame (DefaultSlots if ≤ 0). Each
	// takes 256 bytes of a uniform buffer.
	Slots int
}

// DrawOptions place a target in a pass. The zero value, or nil, draws the
// whole target 1:1 at the viewport's origin, as drawn.
type DrawOptions struct {
	// X and Y are the top-left corner of the drawn rectangle in pixels of
	// the viewport. They may be fractional.
	X, Y float32
	// W and H are its size in pixels of the viewport; 0 is the size of
	// Src. Other sizes stretch Src, filtered linearly.
	W, H float32
	// Src is the part of the target drawn; empty is the whole target. It
	// must lie within the target's bounds. A stretched draw reads no
	// pixel outside Src.
	Src image.Rectangle
	// Fade fades the target out: its colours are multiplied by 1 − Fade.
	// 0 draws it as it is, 1 draws nothing. Values outside [0, 1] are
	// clamped.
	Fade float32
}

// Presenter draws targets into a render pass that the caller owns, such as
// a window's frame: with gogpu, the pass begun on Context.CommandEncoder()
// over Context.SurfaceView(). Nothing is copied; the target's texture is
// sampled where it lies.
//
// A frame is Begin, any number of Draw calls into one or more passes, and
// Commit before the encoder is submitted. Each draw takes its own slot of
// a uniform buffer through a dynamic offset, so many draws share a pass;
// Commit writes the slots of the frame with one WriteBuffer. The queue
// applies the write before the next submission and after the previous
// one, so a frame in flight never sees the next frame's slots.
//
//	p, _ := dev.NewPresenter(provider.SurfaceFormat(), nil)
//	// per frame, in OnDraw:
//	w, h := ctx.FramebufferSize()
//	pass, _ := ctx.CommandEncoder().BeginRenderPass(&wgpu.RenderPassDescriptor{
//		ColorAttachments: []wgpu.RenderPassColorAttachment{{
//			View: ctx.SurfaceView(), LoadOp: gputypes.LoadOpLoad, StoreOp: gputypes.StoreOpStore,
//		}},
//	})
//	p.Begin(w, h)
//	p.Draw(pass, tile, &gpu.DrawOptions{X: x, Y: y})
//	pass.End()
//	p.Commit()
//
// A Presenter is not safe for concurrent use.
type Presenter struct {
	d        *Device
	format   gputypes.TextureFormat
	pipeline *wgpu.RenderPipeline
	ubuf     *wgpu.Buffer
	bind     *wgpu.BindGroup

	slots  int
	next   int
	vw, vh float32
	stage  []byte // the frame's slots, written in Commit
}

// NewPresenter makes a presenter for render passes whose colour attachment
// has the given format; opts may be nil. The format must not be an sRGB one: a target holds
// sRGB-encoded values, which an sRGB attachment would encode again; use a
// non-sRGB view of such a surface.
func (d *Device) NewPresenter(format gputypes.TextureFormat, opts *PresenterOptions) (*Presenter, error) {
	if format.IsSrgb() {
		return nil, fmt.Errorf("gpu: presenter format %v is sRGB; use its non-sRGB view", format)
	}
	if opts == nil {
		opts = &PresenterOptions{}
	}
	slots := opts.Slots
	if slots <= 0 {
		slots = DefaultSlots
	}
	size := uint64(slots) * slotStride
	if max := uint64(d.dev.Limits().MaxBufferSize); max != 0 && size > max {
		return nil, fmt.Errorf("gpu: %d presenter slots exceed the buffer limit", slots)
	}
	p := &Presenter{d: d, format: format, slots: slots, stage: make([]byte, size)}
	over := gputypes.BlendComponent{
		SrcFactor: gputypes.BlendFactorOne,
		DstFactor: gputypes.BlendFactorOneMinusSrcAlpha,
		Operation: gputypes.BlendOperationAdd,
	}
	var err error
	p.pipeline, err = d.dev.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Label:  "stilus-present",
		Layout: d.present.layout,
		Vertex: wgpu.VertexState{Module: d.present.shader, EntryPoint: "vs_main"},
		Primitive: gputypes.PrimitiveState{
			Topology: gputypes.PrimitiveTopologyTriangleStrip,
		},
		Multisample: gputypes.MultisampleState{Count: 1, Mask: 0xFFFFFFFF},
		Fragment: &wgpu.FragmentState{
			Module:     d.present.shader,
			EntryPoint: "fs_main",
			Targets: []gputypes.ColorTargetState{{
				Format:    format,
				Blend:     &gputypes.BlendState{Color: over, Alpha: over},
				WriteMask: gputypes.ColorWriteMaskAll,
			}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gpu: presenter pipeline for %v: %w", format, err)
	}
	if p.ubuf, err = d.dev.CreateBuffer(&wgpu.BufferDescriptor{
		Label: "stilus-present", Size: size,
		Usage: wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	}); err != nil {
		p.Close()
		return nil, fmt.Errorf("gpu: presenter buffer: %w", err)
	}
	if p.bind, err = d.dev.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:   "stilus-present",
		Layout:  d.present.slots,
		Entries: []wgpu.BindGroupEntry{{Binding: 0, Buffer: p.ubuf, Size: slotSize}},
	}); err != nil {
		p.Close()
		return nil, fmt.Errorf("gpu: presenter bind group: %w", err)
	}
	return p, nil
}

// Format is the colour format of the passes the presenter draws into.
func (p *Presenter) Format() gputypes.TextureFormat { return p.format }

// Begin starts a frame on a viewport of w × h pixels, the size of the
// pass's attachment. Slots are reused from the first one.
func (p *Presenter) Begin(w, h int) {
	p.next = 0
	p.vw, p.vh = float32(w), float32(h)
}

// Draw flushes t and records it into pass as placed by o, which may be
// nil. Placed 1:1 at whole pixels, the pixels drawn are those of the
// target. pass must have one colour attachment of the presenter's format.
// A draw that fades the target out completely or has no area records
// nothing and takes no slot.
func (p *Presenter) Draw(pass *wgpu.RenderPassEncoder, t *Target, o *DrawOptions) error {
	if t.d != p.d {
		return errors.New("gpu: target of another device")
	}
	if p.vw <= 0 || p.vh <= 0 {
		return errors.New("gpu: Draw before Begin")
	}
	if o == nil {
		o = &DrawOptions{}
	}
	src := o.Src
	if src.Empty() {
		src = t.Bounds()
	} else if !src.In(t.Bounds()) {
		return fmt.Errorf("gpu: Draw source %v outside the target %v", src, t.Bounds())
	}
	w, h := o.W, o.H
	if w == 0 {
		w = float32(src.Dx())
	}
	if h == 0 {
		h = float32(src.Dy())
	}
	alpha := 1 - min(max(o.Fade, 0), 1)
	if alpha == 0 || w == 0 || h == 0 {
		return nil
	}
	if p.next >= p.slots {
		return fmt.Errorf("gpu: more than %d draws in one frame", p.slots)
	}
	bg, err := t.presentBind()
	if err != nil {
		return err
	}
	if err := t.Flush(); err != nil {
		return err
	}
	off := p.next * slotStride
	p.next++
	tw, th := float32(t.w), float32(t.h)
	u0, v0 := float32(src.Min.X)/tw, float32(src.Min.Y)/th
	u1, v1 := float32(src.Max.X)/tw, float32(src.Max.Y)/th
	s := p.stage[off : off+slotSize]
	for i, v := range [20]float32{
		o.X, o.Y, o.X + w, o.Y + h, // rect
		p.vw, p.vh, 0, 0, // view
		u0, v0, u1, v1, // uv
		// the texel centres at the edges of src, which keep the linear
		// filter inside it
		(float32(src.Min.X) + 0.5) / tw, (float32(src.Min.Y) + 0.5) / th,
		(float32(src.Max.X) - 0.5) / tw, (float32(src.Max.Y) - 0.5) / th,
		alpha, 0, 0, 0,
	} {
		binary.LittleEndian.PutUint32(s[4*i:], math.Float32bits(v))
	}
	pass.SetPipeline(p.pipeline)
	pass.SetBindGroup(0, p.bind, []uint32{uint32(off)})
	pass.SetBindGroup(1, bg, nil)
	pass.Draw(gputypes.DrawArgs{VertexCount: 4, InstanceCount: 1})
	return nil
}

// Commit writes the frame's slots. Call it after the frame's last Draw and
// before the encoder is submitted.
func (p *Presenter) Commit() error {
	if p.next == 0 {
		return nil
	}
	n := (p.next-1)*slotStride + slotSize
	if err := p.d.queue.WriteBuffer(p.ubuf, 0, p.stage[:n]); err != nil {
		return fmt.Errorf("gpu: presenter write: %w", err)
	}
	return nil
}

// Close releases the presenter's GPU resources. The passes it recorded
// into must have been submitted.
func (p *Presenter) Close() {
	if p.bind != nil {
		p.bind.Release()
		p.bind = nil
	}
	if p.ubuf != nil {
		p.ubuf.Release()
		p.ubuf = nil
	}
	if p.pipeline != nil {
		p.pipeline.Release()
		p.pipeline = nil
	}
}

// presentRes is what presenters of a device share: the shader, the layouts
// of the slot group (0) and the texture group (1), and the sampler.
type presentRes struct {
	shader  *wgpu.ShaderModule
	slots   *wgpu.BindGroupLayout
	texture *wgpu.BindGroupLayout
	layout  *wgpu.PipelineLayout
	sampler *wgpu.Sampler
}

func (r *presentRes) init(dev *wgpu.Device) error {
	var err error
	if r.shader, err = dev.CreateShaderModule(&wgpu.ShaderModuleDescriptor{Label: "stilus-present", WGSL: presentWGSL}); err != nil {
		return fmt.Errorf("gpu: present shader: %w", err)
	}
	vf := gputypes.ShaderStageVertex | gputypes.ShaderStageFragment
	if r.slots, err = dev.CreateBindGroupLayout(&wgpu.BindGroupLayoutDescriptor{
		Label: "stilus-present-slots",
		Entries: []gputypes.BindGroupLayoutEntry{{
			Binding: 0, Visibility: vf,
			Buffer: &gputypes.BufferBindingLayout{Type: gputypes.BufferBindingTypeUniform, HasDynamicOffset: true, MinBindingSize: slotSize},
		}},
	}); err != nil {
		return fmt.Errorf("gpu: present layout: %w", err)
	}
	if r.texture, err = dev.CreateBindGroupLayout(&wgpu.BindGroupLayoutDescriptor{
		Label: "stilus-present-texture",
		Entries: []gputypes.BindGroupLayoutEntry{
			{Binding: 0, Visibility: gputypes.ShaderStageFragment,
				Texture: &gputypes.TextureBindingLayout{SampleType: gputypes.TextureSampleTypeFloat, ViewDimension: gputypes.TextureViewDimension2D}},
			{Binding: 1, Visibility: gputypes.ShaderStageFragment,
				Sampler: &gputypes.SamplerBindingLayout{Type: gputypes.SamplerBindingTypeFiltering}},
		},
	}); err != nil {
		return fmt.Errorf("gpu: present layout: %w", err)
	}
	if r.layout, err = dev.CreatePipelineLayout(&wgpu.PipelineLayoutDescriptor{
		Label:            "stilus-present",
		BindGroupLayouts: []*wgpu.BindGroupLayout{r.slots, r.texture},
	}); err != nil {
		return fmt.Errorf("gpu: present pipeline layout: %w", err)
	}
	if r.sampler, err = dev.CreateSampler(&wgpu.SamplerDescriptor{
		Label:        "stilus-present",
		AddressModeU: gputypes.AddressModeClampToEdge,
		AddressModeV: gputypes.AddressModeClampToEdge,
		AddressModeW: gputypes.AddressModeClampToEdge,
		MagFilter:    gputypes.FilterModeLinear,
		MinFilter:    gputypes.FilterModeLinear,
	}); err != nil {
		return fmt.Errorf("gpu: present sampler: %w", err)
	}
	return nil
}

func (r *presentRes) release() {
	if r.sampler != nil {
		r.sampler.Release()
	}
	if r.layout != nil {
		r.layout.Release()
	}
	if r.texture != nil {
		r.texture.Release()
	}
	if r.slots != nil {
		r.slots.Release()
	}
	if r.shader != nil {
		r.shader.Release()
	}
	*r = presentRes{}
}

// presentBind returns the target's texture group, made on first use.
func (t *Target) presentBind() (*wgpu.BindGroup, error) {
	if t.pbind != nil {
		return t.pbind, nil
	}
	if t.view == nil {
		return nil, errors.New("gpu: target closed")
	}
	bg, err := t.d.dev.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "stilus-present-texture",
		Layout: t.d.present.texture,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, TextureView: t.view},
			{Binding: 1, Sampler: t.d.present.sampler},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gpu: present bind group: %w", err)
	}
	t.pbind = bg
	return bg, nil
}
