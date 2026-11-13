// Package gpu composites what a stilus.Canvas draws onto a GPU texture
// through gogpu/wgpu (ADR 0007).
//
// stilus still rasterizes on the CPU: paths, strokes, clips, shapes,
// unions and glyphs give the same coverage as on an image. A Target is a
// stilus.Compositor; it records the canvas's spans (constant runs and rows
// of coverage bytes) and draws them with one render pass per Flush,
// source-over on premultiplied RGBA8. The GPU paints solid colours,
// computes the thin strips of strokes (ADR 0008), gradients (0009),
// images and patterns (0011), meshes (0012) and glyph masks (0013), and
// composites layers from a copy of the backdrop (0014), LayerShader's
// with images and Layer's with targets. A caller's shader runs on the GPU
// when it is a Shader whose WGSL is registered on the device (0018);
// others are evaluated on the CPU and their colours uploaded.
//
//	dev, _ := gpu.Open(nil)
//	t, _ := dev.NewTarget(w, h)
//	t.Clear(color.RGBA{255, 255, 255, 255})
//	c := new(stilus.Canvas)
//	c.ResetCompositor(t, t.Bounds())
//	c.Fill(path, m, stilus.NonZero, paint)
//	t.ReadRGBA(img) // or t.Flush() and sample t.Texture()
//
// In a window, a Presenter draws targets into the frame's render pass
// without copying them (ADR 0010).
//
// The GPU blends in floating point and rounds once, so a pixel may differ
// from the CPU canvas by 1/255 per operation composited onto it; float32
// moves a gradient's or an image's sample to its neighbour now and then.
// A CMYK destination is two targets drawn through one canvas
// (Canvas.ResetCompositorPair, ADR 0015).
//
// Texture planes stay in a target's store from flush to flush (ADR 0016).
// Several canvases draw onto one target at once through its recorders,
// one per band and goroutine, layers included (NewRecorder, ADR 0017).
package gpu

import (
	_ "embed"
	"errors"
	"fmt"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu"
)

//go:embed spans.wgsl
var spansWGSL string

// Format is the texture format of every target: premultiplied RGBA8.
const Format = gputypes.TextureFormatRGBA8Unorm

// Device holds the pipeline that targets share. It is safe to create
// targets from several goroutines only as far as the wgpu device is.
type Device struct {
	dev      *wgpu.Device
	queue    *wgpu.Queue
	layout   *wgpu.BindGroupLayout
	playout  *wgpu.PipelineLayout
	shader   *wgpu.ShaderModule
	pipeline *wgpu.RenderPipeline
	// types are the callers' shader types (custom.go); retired are the
	// modules and pipelines they replaced, released with the device.
	types   []*ShaderType
	retired []func()
	maxBind uint64 // largest storage binding
	present presentRes

	// own, when the device was made by Open.
	instance *wgpu.Instance
	adapter  *wgpu.Adapter
}

// Options select the adapter Open requests.
type Options struct {
	// Fallback requests the software adapter. Without it, Open fails
	// rather than return a software adapter when there is no GPU.
	Fallback bool
	// LowPower prefers an integrated GPU.
	LowPower bool
	// Backends, when not zero, limits the adapter to these backends, and
	// Open takes the backend's adapter even if it runs on the CPU (such
	// as Mesa's lavapipe for Vulkan), but not wgpu's software adapter.
	Backends gputypes.Backends
}

// Open creates an instance, adapter and device of their own, headless.
// The adapter's backends must be registered, for example by importing
// github.com/gogpu/wgpu/hal/allbackends.
func Open(opts *Options) (*Device, error) {
	if opts == nil {
		opts = &Options{}
	}
	var idesc *wgpu.InstanceDescriptor
	if opts.Backends != 0 {
		idesc = &wgpu.InstanceDescriptor{Backends: opts.Backends}
	}
	inst, err := wgpu.CreateInstance(idesc)
	if err != nil {
		return nil, fmt.Errorf("gpu: instance: %w", err)
	}
	pp := wgpu.PowerPreferenceHighPerformance
	if opts.LowPower {
		pp = wgpu.PowerPreferenceLowPower
	}
	ad, err := inst.RequestAdapter(&wgpu.RequestAdapterOptions{PowerPreference: pp, ForceFallbackAdapter: opts.Fallback})
	if err != nil {
		inst.Release()
		return nil, fmt.Errorf("gpu: adapter: %w", err)
	}
	info := ad.Info()
	switch {
	case opts.Backends != 0 && (info.Backend == gputypes.BackendEmpty || !opts.Backends.Contains(info.Backend)):
		ad.Release()
		inst.Release()
		return nil, fmt.Errorf("gpu: no adapter of the backends asked for (only %q, %v)", info.Name, info.Backend)
	case opts.Backends == 0 && info.DeviceType == gputypes.DeviceTypeCPU && !opts.Fallback:
		ad.Release()
		inst.Release()
		return nil, fmt.Errorf("gpu: no hardware adapter (only %q)", info.Name)
	}
	dev, err := ad.RequestDevice(nil)
	if err != nil {
		ad.Release()
		inst.Release()
		return nil, fmt.Errorf("gpu: device: %w", err)
	}
	d, err := NewDevice(dev)
	if err != nil {
		dev.Release()
		ad.Release()
		inst.Release()
		return nil, err
	}
	d.instance, d.adapter = inst, ad
	return d, nil
}

// NewDevice builds the pipeline on the caller's device, which stays the
// caller's: Close does not release it.
func NewDevice(dev *wgpu.Device) (*Device, error) {
	if dev == nil {
		return nil, errors.New("gpu: nil device")
	}
	d := &Device{dev: dev, queue: dev.Queue()}
	d.maxBind = uint64(dev.Limits().MaxStorageBufferBindingSize)
	if d.maxBind == 0 {
		d.maxBind = 128 << 20
	}
	var err error
	vf := gputypes.ShaderStageVertex | gputypes.ShaderStageFragment
	d.layout, err = dev.CreateBindGroupLayout(&wgpu.BindGroupLayoutDescriptor{
		Label: "stilus-spans",
		Entries: []gputypes.BindGroupLayoutEntry{
			{Binding: 0, Visibility: gputypes.ShaderStageVertex, Buffer: &gputypes.BufferBindingLayout{Type: gputypes.BufferBindingTypeUniform}},
			{Binding: 1, Visibility: vf, Buffer: &gputypes.BufferBindingLayout{Type: gputypes.BufferBindingTypeReadOnlyStorage}},
			{Binding: 2, Visibility: vf, Buffer: &gputypes.BufferBindingLayout{Type: gputypes.BufferBindingTypeReadOnlyStorage}},
			{Binding: 3, Visibility: gputypes.ShaderStageFragment, Buffer: &gputypes.BufferBindingLayout{Type: gputypes.BufferBindingTypeReadOnlyStorage}},
			{Binding: 4, Visibility: gputypes.ShaderStageFragment, Buffer: &gputypes.BufferBindingLayout{Type: gputypes.BufferBindingTypeReadOnlyStorage}},
		},
	})
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("gpu: bind group layout: %w", err)
	}
	d.playout, err = dev.CreatePipelineLayout(&wgpu.PipelineLayoutDescriptor{
		Label:            "stilus-spans",
		BindGroupLayouts: []*wgpu.BindGroupLayout{d.layout},
	})
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("gpu: pipeline layout: %w", err)
	}
	if d.shader, d.pipeline, err = d.buildPipeline(d.spansSource()); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.present.init(dev); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// AdapterName names the adapter and backend of a device Open made, and is
// empty for one the caller made.
func (d *Device) AdapterName() string {
	if d.adapter == nil {
		return ""
	}
	info := d.adapter.Info()
	return fmt.Sprintf("%s (%v, %v)", info.Name, info.Backend, info.DeviceType)
}

// buildPipeline compiles the span shader from source and makes its
// pipeline.
func (d *Device) buildPipeline(source string) (*wgpu.ShaderModule, *wgpu.RenderPipeline, error) {
	shader, err := d.dev.CreateShaderModule(&wgpu.ShaderModuleDescriptor{Label: "stilus-spans", WGSL: source})
	if err != nil {
		return nil, nil, fmt.Errorf("gpu: shader: %w", err)
	}
	over := gputypes.BlendComponent{
		SrcFactor: gputypes.BlendFactorOne,
		DstFactor: gputypes.BlendFactorOneMinusSrcAlpha,
		Operation: gputypes.BlendOperationAdd,
	}
	pipeline, err := d.dev.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Label:  "stilus-spans",
		Layout: d.playout,
		Vertex: wgpu.VertexState{Module: shader, EntryPoint: "vs_main"},
		Primitive: gputypes.PrimitiveState{
			Topology: gputypes.PrimitiveTopologyTriangleList,
		},
		Multisample: gputypes.MultisampleState{Count: 1, Mask: 0xFFFFFFFF},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fs_main",
			Targets: []gputypes.ColorTargetState{{
				Format:    Format,
				Blend:     &gputypes.BlendState{Color: over, Alpha: over},
				WriteMask: gputypes.ColorWriteMaskAll,
			}},
		},
	})
	if err != nil {
		shader.Release()
		return nil, nil, fmt.Errorf("gpu: pipeline: %w", err)
	}
	return shader, pipeline, nil
}

// WGPU returns the device the pipeline lives on.
func (d *Device) WGPU() *wgpu.Device { return d.dev }

// Close releases the pipelines, and the device, adapter and instance if
// Open made them. Targets and presenters must be closed
// first.
func (d *Device) Close() {
	d.present.release()
	if d.pipeline != nil {
		d.pipeline.Release()
		d.pipeline = nil
	}
	if d.playout != nil {
		d.playout.Release()
		d.playout = nil
	}
	if d.layout != nil {
		d.layout.Release()
		d.layout = nil
	}
	if d.shader != nil {
		d.shader.Release()
		d.shader = nil
	}
	if d.instance != nil {
		d.dev.Release()
		d.adapter.Release()
		d.instance.Release()
		d.instance, d.adapter = nil, nil
	}
	d.dev = nil
}
