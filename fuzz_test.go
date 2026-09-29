package stilus

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"testing"
)

// pathFromBytes decodes a path and matrix from fuzz input.
func pathFromBytes(data []byte) (*Path, Matrix) {
	var p Path
	f := func() float32 {
		if len(data) < 4 {
			return 0
		}
		v := math.Float32frombits(binary.LittleEndian.Uint32(data))
		data = data[4:]
		return v
	}
	var m Matrix
	for i := range m {
		m[i] = float64(f())
	}
	if m == (Matrix{}) {
		m = Identity
	}
	for len(data) > 0 {
		v := Verb(data[0] % 5)
		data = data[1:]
		switch v {
		case MoveTo:
			p.MoveTo(f(), f())
		case LineTo:
			p.LineTo(f(), f())
		case QuadTo:
			p.QuadTo(f(), f(), f(), f())
		case CubicTo:
			p.CubicTo(f(), f(), f(), f(), f(), f())
		case Close:
			p.Close()
		}
	}
	return &p, m
}

func fuzzSeeds(f *testing.F) {
	le := func(vs ...float32) []byte {
		var b []byte
		for _, v := range vs {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
		}
		return b
	}
	f.Add(append(le(1, 0, 0, 1, 0, 0), append([]byte{0}, le(3, 3)...)...))
	seed := le(2, 0.3, -0.2, 1.5, 4, -3)
	seed = append(seed, 0)
	seed = append(seed, le(1, 2)...)
	seed = append(seed, 3)
	seed = append(seed, le(40, -10, -5, 50, 30, 30)...)
	seed = append(seed, 1)
	seed = append(seed, le(float32(math.Inf(1)), 5)...)
	seed = append(seed, 4)
	f.Add(seed)
}

func FuzzFill(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		p, m := pathFromBytes(data)
		clip := image.Rect(-3, 2, 37, 29)
		img := image.NewAlpha(clip) // MaskBlitter panics on spans outside
		r := NewRasterizer(clip)
		r.MaxEdges = 1 << 16
		r.Fill(p, m, NonZero, &MaskBlitter{img})
		r.Fill(p, m, EvenOdd, &MaskBlitter{img})
	})
}

func FuzzStroke(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		p, m := pathFromBytes(data)
		img := image.NewRGBA(image.Rect(-3, 2, 37, 29))
		c := NewCanvas(img)
		paint := &Paint{Color: color.RGBA{10, 20, 30, 200}}
		w := 0.0
		if len(data) > 0 {
			w = float64(data[len(data)-1]) / 16
		}
		for _, st := range []*StrokeStyle{
			{Width: w, Join: RoundJoin, Cap: RoundCap},
			{Width: w, Join: MiterJoin, MiterLimit: 4, Cap: SquareCap, Dash: []float64{1, 0.5}},
		} {
			c.Stroke(p, m, st, paint)
		}
		c.ClipPath(p, m, NonZero)
		c.Fill(p, m, EvenOdd, paint)
		if err := c.Err(); err != nil && err != ErrEdgeBudget {
			t.Fatal(err)
		}
	})
}
