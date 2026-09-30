//go:build goexperiment.simd && amd64 && go1.27

package stilus

import "simd/archsimd"

// simd/archsimd API as of Go 1.27: loads and stores take slices. The
// kernels in simd_amd64.go go through these wrappers so they build with
// Go 1.26 as well (simd_api_go126.go); all of them inline.

func loadInt8x16(a *[16]int8) archsimd.Int8x16     { return archsimd.LoadInt8x16(a[:]) }
func loadInt8x32(a *[32]int8) archsimd.Int8x32     { return archsimd.LoadInt8x32(a[:]) }
func loadUint8x32a(a *[32]uint8) archsimd.Uint8x32 { return archsimd.LoadUint8x32(a[:]) }

func loadUint8x16(s []uint8) archsimd.Uint8x16     { return archsimd.LoadUint8x16(s) }
func storeUint8x16(v archsimd.Uint8x16, s []uint8) { v.Store(s) }
func loadUint8x32(s []uint8) archsimd.Uint8x32     { return archsimd.LoadUint8x32(s) }
func storeUint8x32(v archsimd.Uint8x32, s []uint8) { v.Store(s) }

func truncToUint8(v archsimd.Uint16x32) archsimd.Uint8x32 { return v.TruncToUint8() }
