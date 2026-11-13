package gpu

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/timzifer/stilus"
)

// Image records (spans.wgsl, imageColor), in words:
//
//	0       imageKind | imgHas | imgMask
//	1       Color, premultiplied RGBA8, R in the low byte
//	2       Alpha
//	3…14    the image's sampler, 15…26 the mask's:
//	        plane offset (words, in the store), W, H, flags (smpBilinear, smpWrap),
//	        float32 Inv[0…3], int32 origin x, y, float32 u, v at the
//	        origin's centre (reduced modulo W, H when repeating)
//
// A sampler computes (u, v) at a pixel from the origin's, plus Inv's
// linear part times the pixel's integer distance from the origin, so that
// float32 holds small numbers.
//
// Planes are kept in the store, a buffer of the target that outlives
// flushes, as RGBA words, row-major without padding, keyed by the plane:
// a texture's planes do not change once made (stilus.Texture), so a page
// drawn again finds them there. When the store is full it is flushed and
// emptied; DropTextures empties it at once.
const (
	imageKind = 1 << 31
	imgHas    = 1 << 0
	imgMask   = 1 << 1

	smpBilinear = 1 << 0
	smpWrap     = 1 << 1

	samplerWords = 12
	imageWords   = 3 + 2*samplerWords
)

// imageBlitter returns the blitter of an image shader the GPU samples, or
// nil when its planes are too large for the store, which shades it on the
// CPU.
func (t *Target) imageBlitter(s *stilus.ImageShader) stilus.Blitter {
	f := s.Form()
	if !f.HasImage && f.HasMask && f.Color == 0 || f.HasImage && f.Alpha == 0 {
		return nothing{}
	}
	if planeBytes(&f) > storeMax(t) {
		return nil
	}
	t.image = f
	b := &t.grad
	b.recOK, b.img = false, &t.image
	return b
}

// planeBytes returns the bytes the planes of f take in the store.
func planeBytes(f *stilus.ImageForm) int {
	n := 0
	if f.HasImage {
		n += f.Image.Plane.W*f.Image.Plane.H*4 + 3
	}
	if f.HasMask {
		n += f.Mask.Plane.W*f.Mask.Plane.H*4 + 3
	}
	return n
}

// storeLimit bounds the store of a target (tests lower it).
var storeLimit = 256 << 20

// storeMax is the largest store a target keeps.
func storeMax(t *Target) int { return int(min(t.d.maxBind, uint64(storeLimit))) }

// storeSize is what a plane takes in the store: rows of RGBA words,
// aligned to 256 bytes.
func storeSize(p *stilus.Plane) int { return (p.W*p.H*4 + 255) &^ 255 }

// storeRoom makes room in the store for the planes of f not in it yet,
// flushing and emptying it if they do not fit, growing it if all of them
// do not fit an empty one, and reports whether they now fit.
func (t *Target) storeRoom(f *stilus.ImageForm) bool {
	need := func(all bool) int {
		n := 0
		for _, s := range []struct {
			p  *stilus.Plane
			ok bool
		}{{f.Image.Plane, f.HasImage}, {f.Mask.Plane, f.HasMask && f.Mask.Plane != f.Image.Plane}} {
			if !s.ok {
				continue
			}
			if _, in := t.planes[s.p]; all || !in {
				n += storeSize(s.p)
			}
		}
		return n
	}
	if n := need(false); t.store != nil && t.storeUsed+n <= int(t.store.Size()) {
		return true
	}
	n := need(true)
	if n > storeMax(t) {
		return false
	}
	if t.storeUsed > 0 {
		// The spans recorded read the planes where they are now.
		if err := t.Flush(); err != nil {
			t.setErr(err)
			return false
		}
		t.DropTextures()
	}
	if t.store == nil || n > int(t.store.Size()) {
		t.storeNeed = max(t.storeNeed*2, n, 1<<20)
		t.storeNeed = min(t.storeNeed, max(storeMax(t), n))
		if err := t.grow(len(t.spans), len(t.data)); err != nil {
			t.setErr(err)
			return false
		}
	}
	return true
}

// DropTextures empties the store of texture planes; planes drawn later
// are uploaded again. Spans recorded since the last Flush must not read
// the store any more, so call it after Flush.
func (t *Target) DropTextures() {
	clear(t.planes)
	t.storeUsed = 0
}

// plane uploads p into the store unless it is there, and returns its word
// offset. storeRoom has made room.
func (t *Target) plane(p *stilus.Plane) int {
	if off, ok := t.planes[p]; ok {
		return off
	}
	off := t.storeUsed
	n := storeSize(p)
	if cap(t.planeTmp) < n {
		t.planeTmp = make([]byte, n)
	}
	buf := t.planeTmp[:0]
	if cap(t.words) < p.W {
		t.words = make([]uint32, p.W)
	}
	row := t.words[:p.W]
	for y := 0; y < p.H; y++ {
		switch p.Kind {
		case stilus.PlaneRGBA:
			copy(row, p.Pix32[y*p.Stride:][:p.W])
		case stilus.PlaneIndex:
			for x, i := range p.Pix8[y*p.Stride:][:p.W] {
				row[x] = p.Pal[i]
			}
		default:
			for x := range row {
				row[x] = p.At(x, y)
			}
		}
		for _, c := range row {
			buf = binary.LittleEndian.AppendUint32(buf, rgba(c))
		}
	}
	buf = buf[:n]
	if err := t.d.queue.WriteBuffer(t.store, uint64(off), buf); err != nil {
		t.setErr(fmt.Errorf("gpu: write texture: %w", err))
	}
	t.storeUsed += n
	t.planes[p] = off / 4
	return off / 4
}

// imageRecord appends the record of f with its origin at pixel (x, y),
// uploading its planes.
func (t *Target) imageRecord(r []byte, f *stilus.ImageForm, x, y int) []byte {
	var io, mo int
	if !t.storeRoom(f) {
		t.setErr(fmt.Errorf("gpu: texture planes larger than the store"))
	}
	if f.HasImage {
		io = t.plane(f.Image.Plane)
	}
	if f.HasMask {
		mo = t.plane(f.Mask.Plane)
	}
	flags := uint32(imageKind)
	if f.HasImage {
		flags |= imgHas
	}
	if f.HasMask {
		flags |= imgMask
	}
	put := func(v uint32) { r = binary.LittleEndian.AppendUint32(r, v) }
	put(flags)
	put(rgba(f.Color))
	put(uint32(f.Alpha))
	sampler := func(s *stilus.SamplerForm, off int, ok bool) {
		if !ok {
			for range samplerWords {
				put(0)
			}
			return
		}
		p, m := s.Plane, s.Inv
		fl := uint32(0)
		if s.Bilinear {
			fl |= smpBilinear
		}
		if s.Wrap {
			fl |= smpWrap
		}
		fx, fy := float64(x)+0.5, float64(y)+0.5
		u, v := m[0]*fx+m[2]*fy+m[4], m[1]*fx+m[3]*fy+m[5]
		if s.Wrap {
			u -= math.Floor(u/float64(p.W)) * float64(p.W)
			v -= math.Floor(v/float64(p.H)) * float64(p.H)
		}
		put(uint32(off))
		put(uint32(p.W))
		put(uint32(p.H))
		put(fl)
		for _, k := range [4]float64{m[0], m[1], m[2], m[3]} {
			put(math.Float32bits(float32(k)))
		}
		put(uint32(int32(x)))
		put(uint32(int32(y)))
		put(math.Float32bits(float32(u)))
		put(math.Float32bits(float32(v)))
	}
	sampler(&f.Image, io, f.HasImage)
	sampler(&f.Mask, mo, f.HasMask)
	return r
}
