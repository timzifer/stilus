//go:build goexperiment.simd && amd64

package stilus

import (
	"os"
	"simd/archsimd"
	"strconv"
	"unsafe"
)

// Every kernel ends with ClearAVXUpperBits (VZEROUPPER): Go 1.26 and 1.27
// do not emit it, and the rasterizer's scalar float code is legacy SSE,
// which runs 3–5× slower while the upper halves of the YMM registers are
// dirty.

// minSIMDSpan is the span length below which the scalar kernels win: a
// single vector block plus VZEROUPPER costs more than a few scalar pixels.
var minSIMDSpan = 16

// useSIMD selects the AVX2 span kernels. STILUS_SIMD=0 disables them (for
// comparisons).
var useSIMD = archsimd.X86.AVX2() && os.Getenv("STILUS_SIMD") != "0"

// use512 selects the AVX-512 kernels (8 pixels per iteration, VPMOVWB pack,
// VPERMB coverage broadcast). STILUS_SIMD=256 restricts to AVX2.
var use512 = useSIMD && archsimd.X86.AVX512() && archsimd.X86.AVX512VBMI() && os.Getenv("STILUS_SIMD") != "256"

// repIdx8 replicates coverage byte k into the channel bytes of pixel k.
var repIdx8 = [32]uint8{0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 6, 6, 6, 6, 7, 7, 7, 7}

func init() {
	if v, err := strconv.Atoi(os.Getenv("STILUS_SIMD_MIN")); err == nil && v >= 4 {
		minSIMDSpan = v
	}
}

// SIMD reports whether vectorized span kernels are in use. They require
// GOEXPERIMENT=simd (Go 1.26+) on amd64 with AVX2.
func SIMD() bool { return useSIMD }

var (
	// repIdx replicates coverage byte k into the four channel bytes of pixel k.
	repIdx = [16]int8{0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3}
	// packIdx gathers the low bytes of the 16-bit lanes of each 128-bit half.
	packIdx = [32]int8{
		0, 2, 4, 6, 8, 10, 12, 14, -1, -1, -1, -1, -1, -1, -1, -1,
		0, 2, 4, 6, 8, 10, 12, 14, -1, -1, -1, -1, -1, -1, -1, -1,
	}
)

func bytesOf(d []uint32) []uint8 {
	if len(d) == 0 {
		return nil
	}
	return unsafe.Slice((*uint8)(unsafe.Pointer(unsafe.SliceData(d))), len(d)*4)
}

// div255 divides 16-bit lanes (≤ 255·255) by 255 with rounding:
// ((x+128)·257) >> 16, a single VPMULHUW. (A variable shift would compile to
// a legacy-SSE MOVQ inside the AVX loop.) Vector constants are passed as
// plain values: vectors inside structs are copied with legacy-SSE MOVUPS.
func div255v(x, k128, k257 archsimd.Uint16x16) archsimd.Uint16x16 {
	return x.Add(k128).MulHigh(k257)
}

// pack narrows 16 lanes holding bytes back to 16 bytes (four pixels).
func pack(v archsimd.Uint16x16, pk archsimd.Int8x32) archsimd.Uint8x16 {
	p := v.AsUint8x32().PermuteOrZeroGrouped(pk)
	return p.GetLo().AsUint64x2().InterleaveLo(p.GetHi().AsUint64x2()).AsUint8x16()
}

// coverage4 expands four coverage bytes (packed in w) to per-channel lanes.
func coverage4(w uint32, rep archsimd.Int8x16) archsimd.Uint16x16 {
	return archsimd.BroadcastUint32x4(w).AsUint8x16().PermuteOrZero(rep).ExtendToUint16()
}

func covOpaque(d []uint32, cov []uint8, c uint32, cx uint64) {
	if !useSIMD || len(cov) < minSIMDSpan {
		covOpaqueScalar(d, cov, c, cx)
		return
	}
	if use512 {
		covOpaque512(d, cov, c, cx)
		return
	}
	d = d[:len(cov)]
	db := bytesOf(d)
	k128 := archsimd.BroadcastUint16x16(128)
	k255 := archsimd.BroadcastUint16x16(255)
	k257 := archsimd.BroadcastUint16x16(257)
	rep := loadInt8x16(&repIdx)
	pk := loadInt8x32(&packIdx)
	c16 := archsimd.BroadcastUint32x4(c).AsUint8x16().ExtendToUint16()
	i := 0
	for ; i+4 <= len(cov); i += 4 {
		w := *(*uint32)(unsafe.Pointer(&cov[i]))
		if w == 0 {
			continue
		}
		if w == 0xffffffff {
			d[i], d[i+1], d[i+2], d[i+3] = c, c, c, c
			continue
		}
		a := coverage4(w, rep)
		px := db[i*4 : i*4+16]
		d16 := loadUint8x16(px).ExtendToUint16()
		r := div255v(d16.Mul(k255.Sub(a)).Add(c16.Mul(a)), k128, k257)
		storeUint8x16(pack(r, pk), px)
	}
	archsimd.ClearAVXUpperBits()
	covOpaqueScalar(d[i:], cov[i:], c, cx)
}

func covOver(d []uint32, cov []uint8, cx uint64) {
	if !useSIMD || len(cov) < minSIMDSpan {
		covOverScalar(d, cov, cx)
		return
	}
	if use512 {
		covOver512(d, cov, cx)
		return
	}
	d = d[:len(cov)]
	db := bytesOf(d)
	k128 := archsimd.BroadcastUint16x16(128)
	k255 := archsimd.BroadcastUint16x16(255)
	k257 := archsimd.BroadcastUint16x16(257)
	rep := loadInt8x16(&repIdx)
	pk := loadInt8x32(&packIdx)
	c16 := archsimd.BroadcastUint32x4(compact(cx)).AsUint8x16().ExtendToUint16()
	i := 0
	for ; i+4 <= len(cov); i += 4 {
		w := *(*uint32)(unsafe.Pointer(&cov[i]))
		if w == 0 {
			continue
		}
		s := div255v(c16.Mul(coverage4(w, rep)), k128, k257)
		sa := s.PermuteScalarsLoGrouped(3, 3, 3, 3).PermuteScalarsHiGrouped(3, 3, 3, 3)
		px := db[i*4 : i*4+16]
		d16 := loadUint8x16(px).ExtendToUint16()
		r := div255v(d16.Mul(k255.Sub(sa)), k128, k257).Add(s)
		storeUint8x16(pack(r, pk), px)
	}
	archsimd.ClearAVXUpperBits()
	covOverScalar(d[i:], cov[i:], cx)
}

func runOver(d []uint32, s uint32) {
	if !useSIMD || len(d) < 4 {
		runOverScalar(d, s)
		return
	}
	if use512 && len(d) >= 8 {
		runOver512(d, s)
		return
	}
	db := bytesOf(d)
	k128 := archsimd.BroadcastUint16x16(128)
	k257 := archsimd.BroadcastUint16x16(257)
	pk := loadInt8x32(&packIdx)
	s16 := archsimd.BroadcastUint32x4(s).AsUint8x16().ExtendToUint16()
	inv := archsimd.BroadcastUint16x16(uint16(255 - (s>>alphaShift)&0xff))
	i := 0
	for ; i+4 <= len(d); i += 4 {
		px := db[i*4 : i*4+16]
		d16 := loadUint8x16(px).ExtendToUint16()
		storeUint8x16(pack(div255v(d16.Mul(inv), k128, k257).Add(s16), pk), px)
	}
	archsimd.ClearAVXUpperBits()
	runOverScalar(d[i:], s)
}

// AVX-512 kernels: eight pixels in 16-bit lanes of one ZMM register.

func div255w(x, k128, k257 archsimd.Uint16x32) archsimd.Uint16x32 {
	return x.Add(k128).MulHigh(k257)
}

func coverage8(w uint64, rep archsimd.Uint8x32) archsimd.Uint16x32 {
	return archsimd.BroadcastUint64x4(w).AsUint8x32().Permute(rep).ExtendToUint16()
}

func covOpaque512(d []uint32, cov []uint8, c uint32, cx uint64) {
	d = d[:len(cov)]
	db := bytesOf(d)
	k128 := archsimd.BroadcastUint16x32(128)
	k255 := archsimd.BroadcastUint16x32(255)
	k257 := archsimd.BroadcastUint16x32(257)
	rep := loadUint8x32a(&repIdx8)
	c16 := archsimd.BroadcastUint32x8(c).AsUint8x32().ExtendToUint16()
	i := 0
	for ; i+8 <= len(cov); i += 8 {
		w := *(*uint64)(unsafe.Pointer(&cov[i]))
		if w == 0 {
			continue
		}
		px := db[i*4 : i*4+32]
		if w == ^uint64(0) {
			fill32(d[i:i+8], c)
			continue
		}
		a := coverage8(w, rep)
		d16 := loadUint8x32(px).ExtendToUint16()
		storeUint8x32(truncToUint8(div255w(d16.Mul(k255.Sub(a)).Add(c16.Mul(a)), k128, k257)), px)
	}
	archsimd.ClearAVXUpperBits()
	covOpaqueScalar(d[i:], cov[i:], c, cx)
}

func covOver512(d []uint32, cov []uint8, cx uint64) {
	d = d[:len(cov)]
	db := bytesOf(d)
	k128 := archsimd.BroadcastUint16x32(128)
	k255 := archsimd.BroadcastUint16x32(255)
	k257 := archsimd.BroadcastUint16x32(257)
	rep := loadUint8x32a(&repIdx8)
	c16 := archsimd.BroadcastUint32x8(compact(cx)).AsUint8x32().ExtendToUint16()
	i := 0
	for ; i+8 <= len(cov); i += 8 {
		w := *(*uint64)(unsafe.Pointer(&cov[i]))
		if w == 0 {
			continue
		}
		s := div255w(c16.Mul(coverage8(w, rep)), k128, k257)
		sa := s.PermuteScalarsLoGrouped(3, 3, 3, 3).PermuteScalarsHiGrouped(3, 3, 3, 3)
		px := db[i*4 : i*4+32]
		d16 := loadUint8x32(px).ExtendToUint16()
		storeUint8x32(truncToUint8(div255w(d16.Mul(k255.Sub(sa)), k128, k257).Add(s)), px)
	}
	archsimd.ClearAVXUpperBits()
	covOverScalar(d[i:], cov[i:], cx)
}

func runOver512(d []uint32, s uint32) {
	db := bytesOf(d)
	k128 := archsimd.BroadcastUint16x32(128)
	k257 := archsimd.BroadcastUint16x32(257)
	s16 := archsimd.BroadcastUint32x8(s).AsUint8x32().ExtendToUint16()
	inv := archsimd.BroadcastUint16x32(uint16(255 - (s>>alphaShift)&0xff))
	i := 0
	for ; i+8 <= len(d); i += 8 {
		px := db[i*4 : i*4+32]
		d16 := loadUint8x32(px).ExtendToUint16()
		storeUint8x32(truncToUint8(div255w(d16.Mul(inv), k128, k257).Add(s16)), px)
	}
	archsimd.ClearAVXUpperBits()
	runOverScalar(d[i:], s)
}
