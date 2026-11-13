package gpu

import (
	"encoding/binary"

	"github.com/timzifer/stilus"
)

// Coverage-mask records (spans.wgsl, alphaMaskColor), in words:
//
//	0       alphaMaskKind
//	1       Color, premultiplied RGBA8, R in the low byte
//	2, 3    int32 device position of the mask's first pixel
//	4, 5    its width and height
//	6…      its bytes, row-major without padding, four to a word
//
// The bytes are copied into the record when the operation starts: a
// GlyphCache recycles its masks, so a mask is not the same from one
// operation to the next. Glyph masks are small; copying them costs less
// than shading their pixels on the CPU.
const (
	alphaMaskKind = 1 << 28
	alphaMaskHead = 6
)

// alphaMaskBlitter returns the blitter of a mask shader the GPU paints, or
// nil for a mask too large, which is shaded on the CPU.
func (t *Target) alphaMaskBlitter(s *stilus.MaskShader) stilus.Blitter {
	m := s.Mask
	if m == nil || s.Color == 0 {
		return nothing{}
	}
	w, h := m.Rect.Dx(), m.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nothing{}
	}
	if w*h > t.limit/4 {
		return nil
	}
	b := &t.grad
	b.recOK, b.img, b.gform = false, nil, nil
	r := b.rec[:0]
	put := func(v uint32) { r = binary.LittleEndian.AppendUint32(r, v) }
	put(alphaMaskKind)
	put(rgba(s.Color))
	put(uint32(int32(m.Rect.Min.X + s.X)))
	put(uint32(int32(m.Rect.Min.Y + s.Y)))
	put(uint32(w))
	put(uint32(h))
	for y := 0; y < h; y++ {
		o := y * m.Stride
		r = append(r, m.Pix[o:o+w]...)
	}
	for len(r)%4 != 0 {
		r = append(r, 0)
	}
	b.rec = r
	return b
}
