package stilus

import (
	"math/bits"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Textures hold pictures the way their samples come: a picture of one
// component of at most eight bits (a scan, a grey or indexed image) keeps
// one byte a pixel and a palette, a one-bit picture (a fax, a stencil
// mask) one bit a pixel and two palette entries, and only colour pictures
// take four bytes a pixel. A 600 dpi bilevel A4 scan is 4 MB this way
// instead of 140.
//
// Drawing a texture much smaller than its samples reads a mip level: the
// picture averaged over blocks of 2^k × 2^k samples, made once when first
// needed and kept with the texture. Sampling a level that is at most twice
// as fine as the device, bilinearly, gives a downscaled picture without
// aliasing at a cost per device pixel that does not grow with the picture.
// Levels of a grey or alpha picture stay one byte a pixel.

// PlaneKind is how a Plane stores its samples.
type PlaneKind uint8

// Plane kinds.
const (
	PlaneRGBA  PlaneKind = iota // Pix32, premultiplied, in PackRGBA layout
	PlaneIndex                  // Pix8, indexes into Pal
	PlaneBits                   // Pix8, one bit a pixel (MSB first) indexing Pal[0] or Pal[1]
)

// Palette maps samples to premultiplied colours in PackRGBA layout.
type Palette [256]uint32

// Plane is a picture of W × H pixels.
type Plane struct {
	Kind   PlaneKind
	W, H   int
	Stride int // pixels per row for PlaneRGBA and PlaneIndex, bytes for PlaneBits
	Pix8   []uint8
	Pix32  []uint32
	Pal    *Palette
}

// At returns the premultiplied colour of pixel (x, y), which must be
// inside the plane.
func (p *Plane) At(x, y int) uint32 {
	switch p.Kind {
	case PlaneRGBA:
		return p.Pix32[y*p.Stride+x]
	case PlaneIndex:
		return p.Pal[p.Pix8[y*p.Stride+x]]
	}
	return p.Pal[p.Pix8[y*p.Stride+x>>3]>>(7-uint(x)&7)&1]
}

// Bytes returns the memory the samples of p take.
func (p *Plane) Bytes() int { return len(p.Pix8) + 4*len(p.Pix32) }

// pack returns r, g, b, a (premultiplied) in PackRGBA layout.
func pack(r, g, b, a uint8) uint32 {
	v := [4]byte{r, g, b, a}
	return *(*uint32)(unsafe.Pointer(&v))
}

// unpack is the inverse of pack.
func unpack(c uint32) (r, g, b, a uint8) {
	v := *(*[4]byte)(unsafe.Pointer(&c))
	return v[0], v[1], v[2], v[3]
}

// Shared palettes: opaque greys and levels of alpha (white premultiplied,
// so all four channels are the level). They must not be modified.
var GrayPalette, AlphaPalette = func() (*Palette, *Palette) {
	var g, a Palette
	for i := range 256 {
		v := uint8(i)
		g[i] = pack(v, v, v, 255)
		a[i] = pack(v, v, v, v)
	}
	return &g, &a
}()

// maxMip bounds the mip levels (a reduction by 2048): block sums of four
// 8-bit channels must fit 32 bits.
const maxMip = 11

// Texture is a Plane and its mip levels, made on demand. It is safe for
// concurrent use: levels may be requested by several workers at once. The
// base plane must not change once the texture is made.
type Texture struct {
	base Plane
	// gray and alpha say that every colour of base is an opaque grey or
	// a level of alpha, so that its levels can be one byte a pixel.
	gray, alpha bool

	mu   sync.Mutex
	mips [maxMip + 1]atomic.Pointer[Plane]
}

// NewTexture returns a texture of p.
func NewTexture(p Plane) *Texture {
	t := &Texture{base: p}
	if p.Kind != PlaneRGBA {
		n := 256
		if p.Kind == PlaneBits {
			n = 2
		}
		t.gray, t.alpha = true, true
		for _, c := range p.Pal[:n] {
			r, g, b, a := unpack(c)
			t.gray = t.gray && r == g && g == b && a == 255
			t.alpha = t.alpha && r == g && g == b && b == a
		}
	}
	return t
}

// Base returns the plane of t at full resolution.
func (t *Texture) Base() *Plane { return &t.base }

// Levels returns the number of levels below the base: halvings until the
// plane is one pixel, at most 11.
func (t *Texture) Levels() int {
	n := max(t.base.W, t.base.H) - 1
	return min(bits.Len(uint(n)), maxMip)
}

// MipBytes estimates the memory all levels of t can take.
func (t *Texture) MipBytes() int {
	px := t.base.W * t.base.H / 3
	if t.gray || t.alpha {
		return px
	}
	return 4 * px
}

// Level returns mip level k (0 is the base), making it if needed.
func (t *Texture) Level(k int) *Plane {
	k = min(k, t.Levels())
	if k <= 0 {
		return &t.base
	}
	if p := t.mips[k].Load(); p != nil {
		return p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.mips[k].Load(); p != nil {
		return p
	}
	p := t.downsample(k)
	t.mips[k].Store(p)
	return p
}

// downsample averages base over blocks of 2^k × 2^k pixels (smaller at
// the right and bottom edges). Levels are made from base directly, so a
// deep level costs no intermediate ones.
func (t *Texture) downsample(k int) *Plane {
	src := &t.base
	f := 1 << k
	w, h := (src.W+f-1)>>k, (src.H+f-1)>>k
	out := &Plane{W: w, H: h, Stride: w}
	single := t.gray || t.alpha
	if single {
		out.Kind, out.Pix8, out.Pal = PlaneIndex, make([]uint8, w*h), GrayPalette
		if !t.gray {
			out.Pal = AlphaPalette
		}
	} else {
		out.Kind, out.Pix32 = PlaneRGBA, make([]uint32, w*h)
	}
	sums := make([]uint32, 4*w)
	var ones []uint32
	if src.Kind == PlaneBits {
		ones = make([]uint32, w)
	}
	for dy := range h {
		y0, y1 := dy<<k, min((dy+1)<<k, src.H)
		if ones != nil {
			t.countBits(ones, k, y0, y1)
		} else {
			clear(sums)
			for sy := y0; sy < y1; sy++ {
				for sx := range src.W {
					r, g, b, a := unpack(src.At(sx, sy))
					s := sums[4*(sx>>k):][:4]
					s[0] += uint32(r)
					s[1] += uint32(g)
					s[2] += uint32(b)
					s[3] += uint32(a)
				}
			}
		}
		rows := uint32(y1 - y0)
		for dx := range w {
			n := rows * uint32(min(f, src.W-dx<<k))
			var c uint32
			if ones != nil {
				// Blend the two palette entries by the share of ones.
				c = mixCount(src.Pal[0], src.Pal[1], ones[dx], n)
			} else {
				s := sums[4*dx:][:4]
				c = pack(uint8((s[0]+n/2)/n), uint8((s[1]+n/2)/n), uint8((s[2]+n/2)/n), uint8((s[3]+n/2)/n))
			}
			if single {
				r, _, _, a := unpack(c)
				if t.gray {
					out.Pix8[dy*w+dx] = r
				} else {
					out.Pix8[dy*w+dx] = a
				}
			} else {
				out.Pix32[dy*w+dx] = c
			}
		}
	}
	return out
}

// countBits sets ones[dx] to the number of set bits of the bit plane base
// in rows [y0, y1) and columns [dx<<k, (dx+1)<<k).
func (t *Texture) countBits(ones []uint32, k, y0, y1 int) {
	src := &t.base
	clear(ones)
	for sy := y0; sy < y1; sy++ {
		row := src.Pix8[sy*src.Stride:][:(src.W+7)/8]
		if k >= 3 {
			// Blocks are whole bytes; the last byte may hold padding.
			for i, b := range row {
				if rest := src.W - 8*i; rest < 8 {
					b &= 0xff << (8 - uint(rest))
				}
				ones[(8*i)>>k] += uint32(bits.OnesCount8(b))
			}
			continue
		}
		for sx := range src.W {
			ones[sx>>k] += uint32(row[sx>>3] >> (7 - uint(sx)&7) & 1)
		}
	}
}

// mixCount returns the average of n pixels of which ones are c1 and the
// others c0, per channel and rounded.
func mixCount(c0, c1, ones, n uint32) uint32 {
	zeros := n - ones
	var out uint32
	for s := uint(0); s < 32; s += 8 {
		v := (c0>>s&0xff)*zeros + (c1>>s&0xff)*ones
		out |= (v + n/2) / n << s
	}
	return out
}
