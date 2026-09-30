package stilus

import (
	"fmt"
	"testing"
)

var benchmarkBounds Rect

func BenchmarkPathBounds(b *testing.B) {
	for _, n := range []int{2, 4, 1024} {
		p := Path{Points: make([]Point, n)}
		for i := range p.Points {
			p.Points[i] = Point{float32((i * 17) % 991), float32((i * 31) % 983)}
		}
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchmarkBounds = p.Bounds()
			}
		})
	}
}

func BenchmarkFill32(b *testing.B) {
	for _, n := range []int{16, 64, 1024, 8192} {
		d := make([]uint32, n)
		for _, tc := range []struct {
			name string
			fill func([]uint32, uint32)
		}{
			{"doubling", fill32},
			{"loop", func(d []uint32, v uint32) {
				for i := range d {
					d[i] = v
				}
			}},
		} {
			b.Run(fmt.Sprintf("%s/%d", tc.name, n), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(n * 4))
				for i := 0; i < b.N; i++ {
					tc.fill(d, 0xff123456)
				}
			})
		}
	}
}
