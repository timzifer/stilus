package gpu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"

	"github.com/gogpu/wgpu"
	"github.com/timzifer/stilus"
)

// Layer composites the target Src, its pixel (0, 0) at (X, Y), onto the
// target it is drawn onto, as stilus.LayerShader composites an image: the
// layer's pixels times Alpha and Mask's alpha, blended with the backdrop
// by Blend, the backdrop of a non-isolated group removed first with
// Initial and Alone, on ink amounts with Subtractive and Black (ADR 0014).
// Mask, Initial and Alone lie at (X, Y) too; outside Mask it is 0.
//
// Fill the layer's rectangle with it through a canvas drawing onto a
// target. Drawn onto anything else, it paints nothing.
type Layer struct {
	Src            *Target
	X, Y           int
	Mask           *Target
	Alpha          uint8
	Blend          stilus.BlendMode
	Initial, Alone *Target
	Subtractive    bool
	Black          bool
}

// ShadeSpan implements stilus.Shader: a Layer is composited by a target
// and paints nothing elsewhere.
func (l *Layer) ShadeSpan(y, x int, dst []uint32) { clear(dst) }

// Layer records (spans.wgsl, layerColor), in words:
//
//	0       layerKind | mode (bits 0–7) | layerMask | layerRemove |
//	        layerSubtractive | layerBackdrop
//	1       Alpha
//	2…5     the region R: int32 x, y, its width and height
//	6       the stride of every copy, in words
//	7…11    word offsets in the layer buffer of the copies of the layer,
//	        the backdrop, the mask, the initial backdrop, the group alone
//
// Copies are R's pixels as RGBA words, rows of stride words; a mask's
// alpha is in the word's top byte.
const (
	layerKind        = 1 << 27
	layerMask        = 1 << 8
	layerRemove      = 1 << 9
	layerSubtractive = 1 << 10
	layerBackdrop    = 1 << 11

	// layerWords is the size of a layer record.
	layerWords = 12

	// blendBackdrop is stilus's B(cb, cs) = cb, the mode of a CMYK pair's
	// black image in Hue, Saturation and Color.
	blendBackdrop = 255
)

// ErrLayer is reported by Err for a layer that could not be drawn.
var ErrLayer = errors.New("gpu: layer not drawn")

// Err returns the first error a layer operation met since the target was
// made, nil for none.
func (t *Target) Err() error { return t.err }

func (t *Target) setErr(err error) {
	if t.err == nil {
		t.err = err
	}
}

// layerSrc is one image a layer reads: an image in memory or a target,
// at an offset.
type layerSrc struct {
	rgba  *image.RGBA
	alpha *image.Alpha
	tg    *Target
	x, y  int // where tg's pixel (0, 0) lies
	mask  bool
}

// layerMode is LayerShader's mode: the black image of a CMYK pair takes K
// from the backdrop in Hue, Saturation and Color, from the source in
// Luminosity.
func layerMode(bm stilus.BlendMode, black bool) uint32 {
	if black && bm >= stilus.BlendHue {
		if bm == stilus.BlendLuminosity {
			return uint32(stilus.BlendNormal)
		}
		return blendBackdrop
	}
	return uint32(bm)
}

// layerBlitter snapshots what a layer operation reads into the layer
// buffer and returns the blitter of its spans.
func (t *Target) layerBlitter(sh stilus.Shader) stilus.Blitter {
	var (
		srcs   [5]layerSrc // layer, backdrop (unused here), mask, initial, alone
		r      image.Rectangle
		alpha  uint8
		mode   uint32
		sub    bool
		remove bool
	)
	switch l := sh.(type) {
	case *stilus.LayerShader:
		if l.Src == nil {
			return nothing{}
		}
		r = l.Src.Rect
		srcs[0] = layerSrc{rgba: l.Src}
		if l.Mask != nil {
			srcs[2] = layerSrc{alpha: l.Mask, mask: true}
		}
		if l.Initial != nil && l.Alone != nil {
			srcs[3], srcs[4], remove = layerSrc{rgba: l.Initial}, layerSrc{rgba: l.Alone}, true
		}
		alpha, mode, sub = l.Alpha, layerMode(l.Blend, l.Black), l.Subtractive
	case *Layer:
		if l.Src == nil {
			return nothing{}
		}
		for _, s := range []*Target{l.Src, l.Mask, l.Initial, l.Alone} {
			if s == t {
				t.setErr(fmt.Errorf("%w: a layer drawn onto itself", ErrLayer))
				return nothing{}
			}
		}
		r = l.Src.Bounds().Add(image.Pt(l.X, l.Y))
		srcs[0] = layerSrc{tg: l.Src, x: l.X, y: l.Y}
		if l.Mask != nil {
			srcs[2] = layerSrc{tg: l.Mask, x: l.X, y: l.Y, mask: true}
		}
		if l.Initial != nil && l.Alone != nil {
			srcs[3], srcs[4], remove = layerSrc{tg: l.Initial, x: l.X, y: l.Y}, layerSrc{tg: l.Alone, x: l.X, y: l.Y}, true
		}
		alpha, mode, sub = l.Alpha, layerMode(l.Blend, l.Black), l.Subtractive
	default:
		return nil
	}
	r = r.Intersect(t.Bounds())
	if r.Empty() || alpha == 0 {
		return nothing{}
	}
	backdrop := mode != uint32(stilus.BlendNormal)
	flags := uint32(layerKind) | mode
	if srcs[2] != (layerSrc{}) {
		flags |= layerMask
	}
	if remove {
		flags |= layerRemove
	}
	if sub {
		flags |= layerSubtractive
	}
	if backdrop {
		flags |= layerBackdrop
	}
	if backdrop {
		srcs[1] = layerSrc{tg: t.root()}
	}
	return t.recordLayer(srcs, r, alpha, flags)
}

// root is the target a recorder draws onto, or t.
func (t *Target) root() *Target {
	if t.parent != nil {
		return t.parent
	}
	return t
}

// layerLimit bounds the layer buffer below the storage binding (tests
// lower it).
var layerLimit = 1 << 62

// layerCap is the most the layer buffer holds for one strip of a layer.
func (t *Target) layerCap() int { return int(min(t.d.maxBind, uint64(layerLimit))) }

// copyLayer fills the layer buffer: region r of each source at its
// offset, rows of stride bytes; images are written, targets copied.
func (t *Target) copyLayer(srcs []layerSrc, offs []int, r image.Rectangle, stride int) error {
	w, h := r.Dx(), r.Dy()
	var enc *wgpu.CommandEncoder
	encoder := func() (*wgpu.CommandEncoder, error) {
		if enc != nil {
			return enc, nil
		}
		var err error
		enc, err = t.d.dev.CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{Label: "stilus-layer"})
		return enc, err
	}
	for i, s := range srcs {
		switch {
		case s.rgba != nil || s.alpha != nil:
			if cap(t.layerTmp) < stride*h {
				t.layerTmp = make([]byte, stride*h)
			}
			buf := t.layerTmp[:stride*h]
			clear(buf)
			for y := 0; y < h; y++ {
				row := buf[y*stride : y*stride+w*4]
				py := r.Min.Y + y
				if s.rgba != nil {
					if py >= s.rgba.Rect.Min.Y && py < s.rgba.Rect.Max.Y {
						x0, x1 := max(r.Min.X, s.rgba.Rect.Min.X), min(r.Max.X, s.rgba.Rect.Max.X)
						if x0 < x1 {
							o := s.rgba.PixOffset(x0, py)
							copy(row[(x0-r.Min.X)*4:], s.rgba.Pix[o:o+(x1-x0)*4])
						}
					}
					continue
				}
				m := s.alpha
				if py >= m.Rect.Min.Y && py < m.Rect.Max.Y {
					for x := max(r.Min.X, m.Rect.Min.X); x < min(r.Max.X, m.Rect.Max.X); x++ {
						row[(x-r.Min.X)*4+3] = m.Pix[m.PixOffset(x, py)]
					}
				}
			}
			if err := t.d.queue.WriteBuffer(t.layerBuf, uint64(offs[i]), buf); err != nil {
				return fmt.Errorf("gpu: write layer: %w", err)
			}
		case s.tg != nil:
			e, err := encoder()
			if err != nil {
				return fmt.Errorf("gpu: encoder: %w", err)
			}
			// The part of r the target covers; the rest stays zero.
			sb := s.tg.Bounds().Add(image.Pt(s.x, s.y)).Intersect(r)
			if sb != r {
				e.ClearBuffer(t.layerBuf, uint64(offs[i]), uint64(stride*h))
			}
			if sb.Empty() {
				continue
			}
			e.CopyTextureToBuffer(s.tg.tex, t.layerBuf, []wgpu.BufferTextureCopy{{
				BufferLayout: wgpu.ImageDataLayout{
					Offset:       uint64(offs[i] + (sb.Min.Y-r.Min.Y)*stride + (sb.Min.X-r.Min.X)*4),
					BytesPerRow:  uint32(stride),
					RowsPerImage: uint32(sb.Dy()),
				},
				TextureBase: wgpu.ImageCopyTexture{
					Texture: s.tg.tex,
					Origin:  wgpu.Origin3D{X: uint32(sb.Min.X - s.x), Y: uint32(sb.Min.Y - s.y)},
				},
				Size: wgpu.Extent3D{Width: uint32(sb.Dx()), Height: uint32(sb.Dy()), DepthOrArrayLayers: 1},
			}})
		}
	}
	if enc == nil {
		return nil
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

// layerLayout places the copies of the sources over r in the layer
// buffer: rows of stride bytes, one copy after the other.
func layerLayout(srcs *[5]layerSrc, r image.Rectangle) (stride int, offs [5]int, total int) {
	stride = (r.Dx()*4 + 255) &^ 255
	size := stride * r.Dy()
	for i := range srcs {
		if srcs[i] == (layerSrc{}) {
			continue
		}
		offs[i] = total
		total += size
	}
	return stride, offs, total
}

// putLayerRecord appends a layer record: flags, alpha, the region r, the
// stride and the offsets of the copies.
func putLayerRecord(rec []byte, flags uint32, alpha uint8, r image.Rectangle, stride int, offs [5]int) []byte {
	put := func(v uint32) { rec = binary.LittleEndian.AppendUint32(rec, v) }
	put(flags)
	put(uint32(alpha))
	put(uint32(int32(r.Min.X)))
	put(uint32(int32(r.Min.Y)))
	put(uint32(r.Dx()))
	put(uint32(r.Dy()))
	put(uint32(stride / 4))
	for _, o := range offs {
		put(uint32(o / 4))
	}
	return rec
}

// layerOp is a layer operation: a barrier in the spans of a target or a
// recorder. Its copies are made at the target's Flush, of the part of the
// layer its spans reach, after the spans before it are drawn, in strips
// of rows when they do not fit the layer buffer at once.
type layerOp struct {
	srcs       [5]layerSrc
	r          image.Rectangle // the layer within the target
	flags      uint32
	alpha      uint8
	start, end int // its spans; end -1 while recording it
	rec        int // the byte offset of its record in data
	region     image.Rectangle
	stride     int
	offs       [5]int
	rows       int // rows per strip whose copies fit the layer buffer
}

// recordLayer records a layer operation of a recorder: its record, whose
// region and copies Flush fills in, and the blitter of its spans.
func (t *Target) recordLayer(srcs [5]layerSrc, r image.Rectangle, alpha uint8, flags uint32) stilus.Blitter {
	if !t.reserve(1, 4*layerWords+3) {
		return nothing{}
	}
	b := &t.grad
	t.data = pad4(t.data)
	off := len(t.data)
	t.data = putLayerRecord(t.data, flags, alpha, image.Rectangle{}, 0, [5]int{})
	b.rec = append(b.rec[:0], t.data[off:]...)
	b.col, b.recFlush, b.recOK, b.img, b.gform = uint32(off/4), t.flushes, true, nil, nil
	t.layerOps = append(t.layerOps, layerOp{srcs: srcs, r: r, flags: flags, alpha: alpha, start: len(t.spans) / spanBytes, end: -1, rec: off})
	return b
}

// closeLayer ends the spans of the layer operation being recorded.
func (t *Target) closeLayer() {
	if n := len(t.layerOps); n > 0 && t.layerOps[n-1].end < 0 {
		t.layerOps[n-1].end = len(t.spans) / spanBytes
	}
}

// prepareLayers finds the region of each layer operation of a recorder,
// the bounds of its spans within the layer, lays its copies out, writes
// its record and flushes the targets it copies from.
func (t *Target) prepareLayers() error {
	t.closeLayer()
	for i := range t.layerOps {
		op := &t.layerOps[i]
		var bb image.Rectangle
		for k := op.start; k < op.end; k++ {
			s := t.spans[k*spanBytes:]
			pos, size := binary.LittleEndian.Uint32(s), binary.LittleEndian.Uint32(s[4:])
			x, y := int(pos&0xffff), int(pos>>16)
			bb = bb.Union(image.Rect(x, y, x+int(size&0xffff), y+int(size>>16)))
		}
		op.region = bb.Intersect(op.r)
		var total int
		op.stride, op.offs, total = layerLayout(&op.srcs, op.region)
		op.rows = op.region.Dy()
		if total > t.layerCap() {
			// Strips of rows whose copies fit.
			perRow := total / max(op.region.Dy(), 1)
			op.rows = t.layerCap() / perRow
			if op.rows == 0 {
				t.setErr(fmt.Errorf("%w: a row of %d pixels needs %d bytes, the layer buffer holds %d", ErrLayer, op.region.Dx(), perRow, t.layerCap()))
				op.region = image.Rectangle{}
				op.rows = 0
			}
			total = perRow * op.rows
		}
		t.layerNeed = max(t.layerNeed, total)
		copy(t.data[op.rec:], putLayerRecord(t.layerTmp[:0], op.flags, op.alpha, op.region, op.stride, op.offs))
		for _, s := range op.srcs {
			if s.tg != nil && s.tg != t.root() {
				if err := s.tg.Flush(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// stripped reports whether a layer operation is drawn in several strips.
func (op *layerOp) stripped() bool { return op.rows < op.region.Dy() }

// prepareStrip points the record of a layer operation at the copies of
// strip, its rows y0 on, and makes them.
func (t *Target) prepareStrip(op *layerOp, strip image.Rectangle) error {
	_, offs, _ := layerLayout(&op.srcs, strip)
	// The record indexes the copies from the region's first row:
	// (y − r.Min.Y)·stride + x. A strip's copy starts at its own first
	// row, so each offset is moved back by the rows before the strip, in
	// u32 arithmetic as the shader adds.
	shift := uint32((strip.Min.Y - op.region.Min.Y) * op.stride / 4)
	var w [5 * 4]byte
	for i, o := range offs {
		binary.LittleEndian.PutUint32(w[4*i:], uint32(o/4)-shift)
	}
	if err := t.d.queue.WriteBuffer(t.dataBuf, uint64(op.rec+7*4), w[:]); err != nil {
		return fmt.Errorf("gpu: write layer record: %w", err)
	}
	return t.copyLayer(op.srcs[:], offs[:], strip, op.stride)
}

// drawLayerOp draws a layer operation that does not fit the layer buffer
// at once, strip by strip, each clipped to its rows; for one that does,
// it only makes the copies, and its spans are drawn with the spans after
// it.
func (t *Target) drawLayerOp(op *layerOp) error {
	r := op.region
	if r.Empty() || op.rows <= 0 {
		return nil
	}
	if !op.stripped() {
		return t.prepareStrip(op, r)
	}
	for y0 := r.Min.Y; y0 < r.Max.Y; y0 += op.rows {
		strip := image.Rect(r.Min.X, y0, r.Max.X, min(y0+op.rows, r.Max.Y))
		if err := t.prepareStrip(op, strip); err != nil {
			return err
		}
		if err := t.root().drawPass(&strip, func(pass *wgpu.RenderPassEncoder) {
			t.drawSpans(pass, op.start, op.end)
		}); err != nil {
			return err
		}
	}
	return nil
}

// segment returns the spans drawn in one pass after layer operation k−1:
// its own spans when it was not drawn in strips, and those up to the next
// operation; for k = 0, those before the first.
func (t *Target) segment(k int) (from, to int) {
	from, to = t.gap(k)
	if k > 0 {
		if op := &t.layerOps[k-1]; !op.stripped() || op.region.Empty() {
			from = op.start
		}
	}
	return from, to
}
