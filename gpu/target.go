package gpu

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"time"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu"
	"github.com/timzifer/stilus"
)

// MaxSize is the largest width and height of a target: span origins and
// sizes are packed into 16 bits.
const MaxSize = 1<<16 - 1

const (
	spanBytes = 16
	modeRun   = 0 << 30
	modeCov   = 1 << 30
	modeCols  = 2 << 30
	modeStrip = 3 << 30
	// stripWords is the size of a strip's parameters (spans.wgsl, Strip).
	stripWords = 13
	noMask     = 1<<32 - 1
	dataMask   = 1<<29 - 1
	// gradFlag marks a span whose colour is a gradient's: its colour word
	// is the word offset of the gradient's record (spans.wgsl, gradColor).
	gradFlag = 1 << 29
	// lookBack is how many spans of the current operation a run looks
	// back at for one on the row above that it can extend.
	lookBack = 8
)

// Target is a texture that a stilus.Canvas draws onto through
// ResetCompositor. It records spans until Flush. A target is not safe for
// concurrent use.
type Target struct {
	d    *Device
	w, h int
	tex  *wgpu.Texture
	view *wgpu.TextureView

	uniform *wgpu.Buffer
	spanBuf *wgpu.Buffer
	dataBuf *wgpu.Buffer
	// layerBuf holds the copies a layer operation reads (layer.go), at
	// least layerNeed bytes.
	layerBuf  *wgpu.Buffer
	layerNeed int
	// store keeps texture planes across flushes (image.go): storeUsed
	// bytes of it hold the planes of planes, storeNeed is its size.
	store     *wgpu.Buffer
	storeUsed int
	storeNeed int
	bind      *wgpu.BindGroup
	read      *wgpu.Buffer    // readback, made on first ReadRGBA
	pbind     *wgpu.BindGroup // texture group of presenters, made on first Draw

	spans   []byte // recorded spans, spanBytes each
	data    []byte // coverage bytes and colours
	opStart int    // first span of the current operation
	limit   int    // flush when spans or data reach it

	clear    bool
	clearCol gputypes.Color
	att      [1]wgpu.RenderPassColorAttachment
	passDesc wgpu.RenderPassDescriptor

	solid  solidBlitter
	grad   solidBlitter // a gradient computed on the GPU
	shaded shadedBlitter
	// flushes counts Flush and Clear, which drop the data a gradient's
	// record is in.
	flushes uint64

	// masks maps the generation of each clip mask uploaded since the last
	// Flush to the byte offset of its copy in data.
	masks map[uint64]int
	// planes maps the texture planes in the store to the word offset of
	// their RGBA copy; it keeps them alive until the store is reset.
	planes  map[*stilus.Plane]int
	image   stilus.ImageForm    // the current operation's, for grad.img
	gform   stilus.GradientForm // the current operation's, for grad.gform
	words   []uint32            // a plane's row, converted
	maskRow []uint8

	meshStart, meshIDs, meshFill []int32    // a mesh's grid, while recorded
	meshes                       *meshCache // shared with the recorders

	// The current operation's caller shader (custom.go).
	custom       Shader
	customType   *ShaderType
	customSmp    []stilus.SamplerForm
	customPlanes []*stilus.Plane

	// parent is the target of a recorder, nil for a target; recorders are
	// the recorders of a target, drawn after it.
	parent    *Target
	recorders []*Target
	// pending are a recorder's writes into the store, made by its
	// target's Flush.
	pending []storeWrite
	// layerOps are a recorder's layer operations since the last Flush.
	layerOps []layerOp

	layerTmp []byte // an image's copy for the layer buffer
	planeTmp []byte // a plane's copy for the store
	err      error  // the first layer operation that failed (Err)
}

// NewTarget makes a w × h target, cleared to transparent.
func (d *Device) NewTarget(w, h int) (*Target, error) {
	if w <= 0 || h <= 0 || w > MaxSize || h > MaxSize {
		return nil, fmt.Errorf("gpu: target size %d×%d", w, h)
	}
	t := &Target{d: d, w: w, h: h, clear: true, masks: map[uint64]int{}, planes: map[*stilus.Plane]int{}}
	t.meshes = &meshCache{recs: map[uint64][]byte{}}
	t.limit = int(min(d.maxBind, 64<<20))
	t.solid.t, t.grad.t, t.shaded.t = t, t, t
	t.grad.flag = gradFlag
	var err error
	t.tex, err = d.dev.CreateTexture(&wgpu.TextureDescriptor{
		Label:         "stilus-target",
		Size:          wgpu.Extent3D{Width: uint32(w), Height: uint32(h), DepthOrArrayLayers: 1},
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     gputypes.TextureDimension2D,
		Format:        Format,
		Usage: gputypes.TextureUsageRenderAttachment | gputypes.TextureUsageCopySrc |
			gputypes.TextureUsageCopyDst | gputypes.TextureUsageTextureBinding,
	})
	if err != nil {
		return nil, fmt.Errorf("gpu: texture: %w", err)
	}
	if t.view, err = d.dev.CreateTextureView(t.tex, nil); err != nil {
		t.Close()
		return nil, fmt.Errorf("gpu: texture view: %w", err)
	}
	if err := t.initBuffers(); err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

// initBuffers makes the target's buffers and bind group.
func (t *Target) initBuffers() error {
	var err error
	if t.uniform, err = t.d.dev.CreateBuffer(&wgpu.BufferDescriptor{
		Label: "stilus-globals", Size: 16,
		Usage: wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	}); err != nil {
		return fmt.Errorf("gpu: buffer: %w", err)
	}
	var g [16]byte
	binary.LittleEndian.PutUint32(g[0:], math.Float32bits(float32(t.w)))
	binary.LittleEndian.PutUint32(g[4:], math.Float32bits(float32(t.h)))
	if err := t.d.queue.WriteBuffer(t.uniform, 0, g[:]); err != nil {
		return fmt.Errorf("gpu: write globals: %w", err)
	}
	return t.grow(1<<12, 1<<12)
}

// Bounds is the target's rectangle in device pixels, the region to pass to
// ResetCompositor.
func (t *Target) Bounds() image.Rectangle { return image.Rect(0, 0, t.w, t.h) }

// Texture returns the texture, current after Flush.
func (t *Target) Texture() *wgpu.Texture { return t.tex }

// View returns a view of the texture, for sampling it.
func (t *Target) View() *wgpu.TextureView { return t.view }

// Clear discards what was recorded and fills the target with c
// (premultiplied) at the next Flush.
func (t *Target) Clear(c color.RGBA) {
	t.spans, t.data, t.opStart = t.spans[:0], t.data[:0], 0
	clear(t.masks)
	t.flushes++
	t.clear = true
	t.clearCol = gputypes.Color{R: float64(c.R) / 255, G: float64(c.G) / 255, B: float64(c.B) / 255, A: float64(c.A) / 255}
}

// Blitter implements stilus.Compositor.
func (t *Target) Blitter(p *stilus.Paint) stilus.Blitter {
	t.closeLayer()
	t.opStart = len(t.spans) / spanBytes
	if p.Shader != nil {
		if b := t.gradient(p.Shader); b != nil {
			return b
		}
		t.shaded.s = p.Shader
		return &t.shaded
	}
	c := p.Color
	t.solid.col = uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16 | uint32(c.A)<<24
	return &t.solid
}

// grow makes the GPU buffers hold at least spans and data bytes.
func (t *Target) grow(spans, data int) error {
	if t.spanBuf != nil && int(t.spanBuf.Size()) >= spans && int(t.dataBuf.Size()) >= data &&
		int(t.layerBuf.Size()) >= t.layerNeed && int(t.store.Size()) >= t.storeNeed {
		return nil
	}
	newBuf := func(old *wgpu.Buffer, n int, label string) (*wgpu.Buffer, error) {
		if old != nil && int(old.Size()) >= n {
			return old, nil
		}
		size := uint64(1 << 12)
		if old != nil {
			size = old.Size()
		}
		for size < uint64(n) {
			size *= 2
		}
		b, err := t.d.dev.CreateBuffer(&wgpu.BufferDescriptor{
			Label: label, Size: size,
			Usage: wgpu.BufferUsageStorage | wgpu.BufferUsageCopyDst,
		})
		if err != nil {
			return nil, fmt.Errorf("gpu: buffer: %w", err)
		}
		if old != nil {
			old.Release()
		}
		return b, nil
	}
	var err error
	if t.spanBuf, err = newBuf(t.spanBuf, spans, "stilus-spans"); err != nil {
		return err
	}
	if t.dataBuf, err = newBuf(t.dataBuf, data, "stilus-data"); err != nil {
		return err
	}
	if t.layerBuf, err = newBuf(t.layerBuf, t.layerNeed, "stilus-layer"); err != nil {
		return err
	}
	if t.store, err = newBuf(t.store, t.storeNeed, "stilus-store"); err != nil {
		return err
	}
	if t.bind != nil {
		t.bind.Release()
	}
	t.bind, err = t.d.dev.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "stilus-spans",
		Layout: t.d.layout,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, Buffer: t.uniform},
			{Binding: 1, Buffer: t.spanBuf},
			{Binding: 2, Buffer: t.dataBuf},
			{Binding: 3, Buffer: t.layerBuf},
			{Binding: 4, Buffer: t.store},
		},
	})
	if err != nil {
		return fmt.Errorf("gpu: bind group: %w", err)
	}
	return nil
}

// Flush draws what was recorded since the last Flush onto the texture:
// the target's own spans first, then those of its recorders. Layer
// operations split the spans into segments: the spans before a layer are
// drawn, then the layer's copies are made and its spans drawn, strip by
// strip if its copies do not fit at once (ADR 0014, 0017). A target's
// segments are drawn in order; its recorders' in rounds, every
// recorder's segment k in one render pass.
func (t *Target) Flush() error {
	if t.parent != nil {
		return errors.New("gpu: a recorder is flushed by its target")
	}
	n := len(t.spans) / spanBytes
	draw := n > 0 || t.clear
	for _, r := range t.recorders {
		draw = draw || len(r.spans) > 0
	}
	if !draw {
		return nil
	}
	if err := t.prepareLayers(); err != nil {
		return err
	}
	rounds := 1
	for _, r := range t.recorders {
		if err := r.prepareLayers(); err != nil {
			return err
		}
		rounds = max(rounds, len(r.layerOps)+1)
	}
	if err := t.upload(); err != nil {
		return err
	}
	for _, r := range t.recorders {
		if err := r.upload(); err != nil {
			return err
		}
	}
	// The target's own segments.
	for k := 0; k <= len(t.layerOps); k++ {
		if k > 0 {
			if err := t.drawLayerOp(&t.layerOps[k-1]); err != nil {
				return err
			}
		}
		from, to := t.segment(k)
		if err := t.drawPass(nil, func(pass *wgpu.RenderPassEncoder) { t.drawSpans(pass, from, to) }); err != nil {
			return err
		}
	}
	// The recorders' segments, round by round.
	for k := range rounds {
		if k > 0 {
			for _, r := range t.recorders {
				if k-1 < len(r.layerOps) {
					if err := r.drawLayerOp(&r.layerOps[k-1]); err != nil {
						return err
					}
				}
			}
		}
		if len(t.recorders) == 0 {
			break
		}
		if err := t.drawPass(nil, func(pass *wgpu.RenderPassEncoder) {
			for _, r := range t.recorders {
				if k <= len(r.layerOps) {
					from, to := r.segment(k)
					r.drawSpans(pass, from, to)
				}
			}
		}); err != nil {
			return err
		}
	}
	t.reset()
	for _, r := range t.recorders {
		r.reset()
	}
	t.clear = false
	return nil
}

// drawPass draws in a render pass over the target's texture, clipped to
// scissor if not nil; the first pass of a Flush after Clear clears.
func (t *Target) drawPass(scissor *image.Rectangle, draw func(pass *wgpu.RenderPassEncoder)) error {
	enc, err := t.d.dev.CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{Label: "stilus-flush"})
	if err != nil {
		return fmt.Errorf("gpu: encoder: %w", err)
	}
	// The descriptor is kept on the target: built here, it would be
	// allocated for every flush.
	t.att[0] = wgpu.RenderPassColorAttachment{View: t.view, LoadOp: gputypes.LoadOpLoad, StoreOp: gputypes.StoreOpStore}
	if t.clear {
		t.att[0].LoadOp, t.att[0].ClearValue = gputypes.LoadOpClear, t.clearCol
		t.clear = false
	}
	t.passDesc = wgpu.RenderPassDescriptor{Label: "stilus-spans", ColorAttachments: t.att[:]}
	pass, err := enc.BeginRenderPass(&t.passDesc)
	if err != nil {
		enc.DiscardEncoding()
		return fmt.Errorf("gpu: render pass: %w", err)
	}
	pass.SetPipeline(t.d.pipeline)
	if scissor != nil {
		sc := scissor.Intersect(t.Bounds())
		pass.SetScissorRect(gputypes.ScissorRect{X: uint32(sc.Min.X), Y: uint32(sc.Min.Y), Width: uint32(sc.Dx()), Height: uint32(sc.Dy())})
	}
	draw(pass)
	if err := pass.End(); err != nil {
		enc.DiscardEncoding()
		return fmt.Errorf("gpu: end pass: %w", err)
	}
	cmd, err := enc.Finish()
	if err != nil {
		return fmt.Errorf("gpu: finish: %w", err)
	}
	if _, err := t.d.queue.Submit(cmd); err != nil {
		return fmt.Errorf("gpu: submit: %w", err)
	}
	return nil
}

// upload makes the buffers hold what was recorded and writes it.
func (t *Target) upload() error {
	store := t.store
	if err := t.grow(len(t.spans), len(t.data)); err != nil {
		return err
	}
	if err := t.uploadStore(t.store != store); err != nil {
		return err
	}
	q := t.d.queue
	if len(t.spans) > 0 {
		if err := q.WriteBuffer(t.spanBuf, 0, t.spans); err != nil {
			return fmt.Errorf("gpu: write spans: %w", err)
		}
		if len(t.data) > 0 {
			if err := q.WriteBuffer(t.dataBuf, 0, pad4(t.data)); err != nil {
				return fmt.Errorf("gpu: write data: %w", err)
			}
		}
	}
	return nil
}

// drawSpans draws the recorded spans [from, to) into pass.
func (t *Target) drawSpans(pass *wgpu.RenderPassEncoder, from, to int) {
	if to > from {
		pass.SetBindGroup(0, t.bind, nil)
		pass.Draw(gputypes.DrawArgs{VertexCount: 6, InstanceCount: uint32(to - from), FirstInstance: uint32(from)})
	}
}

// gap returns the spans between layer operations: before the first for
// k = 0, else from the end of operation k−1 to the start of operation k
// (or the end).
func (t *Target) gap(k int) (from, to int) {
	if k > 0 {
		from = t.layerOps[k-1].end
	}
	to = len(t.spans) / spanBytes
	if k < len(t.layerOps) {
		to = t.layerOps[k].start
	}
	return from, to
}

// reset drops what was recorded, once drawn.
func (t *Target) reset() {
	t.layerOps = t.layerOps[:0]
	t.spans, t.data, t.opStart = t.spans[:0], t.data[:0], 0
	clear(t.masks)
	t.flushes++
}

// pad4 extends b to a multiple of four bytes, as buffer writes need.
func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// ReadRGBA flushes and copies the target into dst, whose rectangle must
// lie within the target's bounds.
func (t *Target) ReadRGBA(dst *image.RGBA) error {
	r := dst.Rect
	if !r.In(t.Bounds()) {
		return errors.New("gpu: ReadRGBA outside the target")
	}
	if r.Empty() {
		return nil
	}
	if err := t.Flush(); err != nil {
		return err
	}
	bpr := (r.Dx()*4 + 255) &^ 255
	size := uint64(bpr * r.Dy())
	if t.read == nil || t.read.Size() < size {
		if t.read != nil {
			t.read.Release()
		}
		var err error
		t.read, err = t.d.dev.CreateBuffer(&wgpu.BufferDescriptor{
			Label: "stilus-readback", Size: size,
			Usage: wgpu.BufferUsageCopyDst | wgpu.BufferUsageMapRead,
		})
		if err != nil {
			t.read = nil
			return fmt.Errorf("gpu: readback buffer: %w", err)
		}
	}
	enc, err := t.d.dev.CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{Label: "stilus-readback"})
	if err != nil {
		return fmt.Errorf("gpu: encoder: %w", err)
	}
	enc.CopyTextureToBuffer(t.tex, t.read, []wgpu.BufferTextureCopy{{
		BufferLayout: wgpu.ImageDataLayout{BytesPerRow: uint32(bpr), RowsPerImage: uint32(r.Dy())},
		TextureBase: wgpu.ImageCopyTexture{
			Texture: t.tex,
			Origin:  wgpu.Origin3D{X: uint32(r.Min.X), Y: uint32(r.Min.Y)},
		},
		Size: wgpu.Extent3D{Width: uint32(r.Dx()), Height: uint32(r.Dy()), DepthOrArrayLayers: 1},
	}})
	cmd, err := enc.Finish()
	if err != nil {
		return fmt.Errorf("gpu: finish: %w", err)
	}
	if _, err := t.d.queue.Submit(cmd); err != nil {
		return fmt.Errorf("gpu: submit: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := t.read.Map(ctx, wgpu.MapModeRead, 0, size); err != nil {
		return fmt.Errorf("gpu: map: %w", err)
	}
	rng, err := t.read.MappedRange(0, size)
	if err != nil {
		_ = t.read.Unmap()
		return fmt.Errorf("gpu: mapped range: %w", err)
	}
	src := rng.Bytes()
	for y := 0; y < r.Dy(); y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+r.Dx()*4], src[y*bpr:])
	}
	return t.read.Unmap()
}

// Close releases the target's GPU resources.
func (t *Target) Close() {
	for _, b := range []**wgpu.Buffer{&t.uniform, &t.spanBuf, &t.dataBuf, &t.layerBuf, &t.store, &t.read} {
		if *b != nil {
			(*b).Release()
			*b = nil
		}
	}
	if t.bind != nil {
		t.bind.Release()
		t.bind = nil
	}
	if t.pbind != nil {
		t.pbind.Release()
		t.pbind = nil
	}
	for len(t.recorders) > 0 {
		(&Recorder{t.recorders[0]}).Close()
	}
	if t.parent != nil {
		// The texture is the target's.
		t.view, t.tex = nil, nil
	}
	if t.view != nil {
		t.view.Release()
		t.view = nil
	}
	if t.tex != nil {
		t.tex.Release()
		t.tex = nil
	}
}

// reserve flushes when spans more spans and data more bytes would pass
// the limit, and reports whether recording may go on.
func (t *Target) reserve(spans, data int) bool {
	if len(t.spans)+spans*spanBytes <= t.limit && len(t.data)+data+3 <= t.limit {
		return true
	}
	if open := len(t.layerOps) > 0 && t.layerOps[len(t.layerOps)-1].end < 0; open && t.parent == nil &&
		len(t.spans)+spans*spanBytes <= int(t.d.maxBind) && len(t.data)+data+3 <= int(t.d.maxBind) {
		// A layer operation being recorded is not cut by a flush: the
		// buffers grow.
		return true
	}
	if spans*spanBytes > t.limit || data+3 > t.limit || t.parent != nil {
		// A single span larger than a binding, or a recorder full: dropped.
		if t.parent != nil {
			t.setErr(ErrRecorderFull)
		}
		return false
	}
	if err := t.Flush(); err != nil {
		return false
	}
	return true
}

// clipRow limits [x0, x1) on row y to the target and returns the
// offset of the new x0 from the old.
func (t *Target) clipRow(y, x0, x1 int) (int, int, int, bool) {
	if y < 0 || y >= t.h {
		return 0, 0, 0, false
	}
	o := 0
	if x0 < 0 {
		o, x0 = -x0, 0
	}
	x1 = min(x1, t.w)
	return x0, x1, o, x0 < x1
}

func (t *Target) span(x, y, w, h int, col, data uint32) {
	t.spans = binary.LittleEndian.AppendUint32(t.spans, uint32(x)|uint32(y)<<16)
	t.spans = binary.LittleEndian.AppendUint32(t.spans, uint32(w)|uint32(h)<<16)
	t.spans = binary.LittleEndian.AppendUint32(t.spans, col)
	t.spans = binary.LittleEndian.AppendUint32(t.spans, data)
}

// run records a run, extending a run of the current operation on the row
// above with the same columns and alpha instead where there is one.
func (t *Target) run(y, x0, x1 int, alpha uint8, col, flag uint32) {
	x0, x1, _, ok := t.clipRow(y, x0, x1)
	if !ok || alpha == 0 {
		return
	}
	pos := uint32(x0)
	size := uint32(x1 - x0)
	data := modeRun | flag | uint32(alpha)
	n := len(t.spans) / spanBytes
	for i := n - 1; i >= max(t.opStart, n-lookBack); i-- {
		s := t.spans[i*spanBytes:]
		sp := binary.LittleEndian.Uint32(s)
		ss := binary.LittleEndian.Uint32(s[4:])
		if sp&0xffff != pos || ss&0xffff != size || binary.LittleEndian.Uint32(s[12:]) != data {
			continue
		}
		if int(sp>>16)+int(ss>>16) == y {
			binary.LittleEndian.PutUint32(s[4:], ss+1<<16)
			return
		}
	}
	if !t.reserve(1, 0) {
		return
	}
	t.span(x0, y, x1-x0, 1, col, data)
}

// solidBlitter records spans of one colour, or of a gradient whose record
// it uploads.
type solidBlitter struct {
	t    *Target
	col  uint32 // the colour, or the word offset of the gradient's record
	flag uint32 // gradFlag for a gradient
	cov  []uint8

	rec      []byte               // the paint's record, uploaded when first needed
	img      *stilus.ImageForm    // an image's form, its record made at upload
	gform    *stilus.GradientForm // a gradient's, its origin written at upload
	custom   bool                 // a caller's shader's, its record made at upload
	recFlush uint64               // t.flushes when it was uploaded, with recOK
	recOK    bool
}

// ready reserves room for spans more spans and data more bytes, and
// uploads the paint's record where the data no longer holds it: a
// gradient's, or an image's with its planes, whose origin is (x, y), the
// first pixel drawn after the upload.
func (b *solidBlitter) ready(x, y, spans, data int) bool {
	t := b.t
	if b.flag == 0 {
		return t.reserve(spans, data)
	}
	need := data + len(b.rec) + 3
	if b.img != nil {
		need = data + imageWords*4 + 3
	}
	if b.custom {
		// The caller's record, of a size known when it is made; a
		// recorder grows, a target makes room for a generous one.
		need = data + 1<<16
	}
	if !t.reserve(spans, need) {
		return false
	}
	if !b.recOK || b.recFlush != t.flushes {
		if b.img != nil {
			b.rec = t.imageRecord(b.rec[:0], b.img, x, y)
		}
		if b.gform != nil {
			gradOrigin(b.rec, b.gform, x, y)
		}
		if b.custom {
			b.rec = t.customRecord(b.rec[:0], x, y)
		}
		t.data = pad4(t.data)
		off := len(t.data) / 4
		if off > dataMask {
			return false
		}
		t.data = append(t.data, b.rec...)
		b.col, b.recFlush, b.recOK = uint32(off), t.flushes, true
	}
	return true
}

func (b *solidBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	if b.flag != 0 && !b.ready(x0, y, 1, 0) {
		return
	}
	b.t.run(y, x0, x1, alpha, b.col, b.flag)
}

func (b *solidBlitter) BlitCoverage(y, x int, cov []uint8) {
	t := b.t
	x0, x1, o, ok := t.clipRow(y, x, x+len(cov))
	if !ok {
		return
	}
	cov = cov[o : o+x1-x0]
	if !b.ready(x0, y, 1, len(cov)) {
		return
	}
	off := len(t.data)
	if off > dataMask {
		return
	}
	t.data = append(t.data, cov...)
	t.span(x0, y, x1-x0, 1, b.col, modeCov|b.flag|uint32(off))
}

// BlitStrip records a strip whole: the GPU computes its coverage
// (spans.wgsl, stripCoverage) in float32, relative to an origin at the
// strip's first row, so that its magnitudes stay small.
func (b *solidBlitter) BlitStrip(s *stilus.Strip) {
	t := b.t
	x0, x1 := max(s.X0, 0), min(s.X1, t.w)
	y0, y1 := max(s.Y0, 0), min(s.Y1, t.h)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	fy := float64(y0)
	ox := math.Floor(s.XL + fy*s.SL)
	if !(math.Abs(ox) < 1<<30) {
		return
	}
	var mb image.Rectangle
	need := 3 + stripWords*4
	if s.Mask != nil {
		mb = s.Mask.Bounds()
		if _, ok := t.masks[s.Mask.Gen()]; !ok {
			need += mb.Dx()*mb.Dy() + 3
		}
	}
	if !b.ready(x0, y0, 1, need) {
		// Larger than a binding (a page-sized mask on a small device):
		// the rows instead.
		b.stripRows(s)
		return
	}
	moff := uint32(noMask)
	if s.Mask != nil {
		off, ok := t.masks[s.Mask.Gen()]
		if !ok {
			off = len(t.data)
			for y := mb.Min.Y; y < mb.Max.Y; y++ {
				t.maskRow = s.Mask.Row(y, t.maskRow)
				t.data = append(t.data, t.maskRow...)
			}
			t.masks[s.Mask.Gen()] = off
		}
		moff = uint32(off)
	}
	t.data = pad4(t.data)
	off := len(t.data) / 4
	if off > dataMask || moff != noMask && moff > dataMask {
		return
	}
	k := s.NX*ox + s.NY*fy
	for _, v := range [7]float64{s.NX, s.NY, s.K1 - k, s.K2 - k, s.XL + fy*s.SL - ox, s.XR + fy*s.SL - ox, s.SL} {
		t.data = binary.LittleEndian.AppendUint32(t.data, math.Float32bits(float32(v)))
	}
	for _, v := range [6]uint32{uint32(int32(ox)), moff, uint32(int32(mb.Min.X)), uint32(int32(mb.Min.Y)), uint32(mb.Dx()), uint32(mb.Dy())} {
		t.data = binary.LittleEndian.AppendUint32(t.data, v)
	}
	t.span(x0, y0, x1-x0, y1-y0, b.col, modeStrip|b.flag|uint32(off))
}

// stripRows blits a strip's rows as the canvas would have.
func (b *solidBlitter) stripRows(s *stilus.Strip) {
	for j := s.Y0; j < s.Y1; j++ {
		var x int
		x, b.cov = s.Row(j, b.cov[:0])
		if len(b.cov) > 0 {
			b.BlitCoverage(j, x, b.cov)
		}
	}
}

// shadedBlitter evaluates a shader on the CPU and records its colours,
// multiplied by the coverage.
type shadedBlitter struct {
	t       *Target
	s       stilus.Shader
	scratch []uint32
}

// shade returns the shader's premultiplied colours over [x0, x1) of row y
// as bytes in memory order R, G, B, A, appended to the target's data, and
// their word offset.
func (b *shadedBlitter) shade(y, x0, x1 int) ([]byte, int, bool) {
	t := b.t
	n := x1 - x0
	if !t.reserve(1, n*4) {
		return nil, 0, false
	}
	t.data = pad4(t.data)
	off := len(t.data)
	if off/4 > dataMask {
		return nil, 0, false
	}
	if cap(b.scratch) < n {
		b.scratch = make([]uint32, n)
	}
	sc := b.scratch[:n]
	b.s.ShadeSpan(y, x0, sc)
	for _, v := range sc {
		c := stilus.UnpackRGBA(v)
		t.data = append(t.data, c.R, c.G, c.B, c.A)
	}
	return t.data[off:], off / 4, true
}

func (b *shadedBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	x0, x1, _, ok := b.t.clipRow(y, x0, x1)
	if !ok || alpha == 0 {
		return
	}
	px, off, ok := b.shade(y, x0, x1)
	if !ok {
		return
	}
	if alpha != 255 {
		for i := range px {
			px[i] = mul255(px[i], alpha)
		}
	}
	b.t.span(x0, y, x1-x0, 1, 0, modeCols|uint32(off))
}

func (b *shadedBlitter) BlitCoverage(y, x int, cov []uint8) {
	x0, x1, o, ok := b.t.clipRow(y, x, x+len(cov))
	if !ok {
		return
	}
	cov = cov[o : o+x1-x0]
	px, off, ok := b.shade(y, x0, x1)
	if !ok {
		return
	}
	for i := range px {
		if a := cov[i/4]; a != 255 {
			px[i] = mul255(px[i], a)
		}
	}
	b.t.span(x0, y, x1-x0, 1, 0, modeCols|uint32(off))
}

// mul255 is v·a/255, rounded.
func mul255(v, a uint8) uint8 {
	x := uint32(v)*uint32(a) + 128
	return uint8((x + x>>8) >> 8)
}
