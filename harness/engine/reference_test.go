package engine

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// TestReferenceArea checks the reference against exact areas: a rectangle
// with edges between pixels, a rotated square, a circle of glyph size and
// a hairline, which the reference draws one pixel wide.
func TestReferenceArea(t *testing.T) {
	black := &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}}
	poly := func(pts ...float64) *stilus.Path {
		var p stilus.Path
		p.MoveTo(float32(pts[0]), float32(pts[1]))
		for i := 2; i < len(pts); i += 2 {
			p.LineTo(float32(pts[i]), float32(pts[i+1]))
		}
		if len(pts) > 4 {
			p.Close()
		}
		return &p
	}
	c, s := 50*math.Cos(math.Pi/6), 50*math.Sin(math.Pi/6)
	// Circle of radius 3.07 as eight quadratics tangent at their ends.
	var circle stilus.Path
	r, rc := 3.07, 3.07/math.Cos(math.Pi/8)
	circle.MoveTo(float32(600+r), 300)
	for i := 0; i < 8; i++ {
		am, a1 := (float64(i)+0.5)*math.Pi/4, float64(i+1)*math.Pi/4
		circle.QuadTo(float32(600+rc*math.Cos(am)), float32(300+rc*math.Sin(am)), float32(600+r*math.Cos(a1)), float32(300+r*math.Sin(a1)))
	}
	circle.Close()
	chord, sag := 2*r*math.Sin(math.Pi/8), (rc-r*math.Cos(math.Pi/8))/2
	circleArea := 8*0.5*r*r*math.Sin(math.Pi/4) + 8*(2.0/3)*chord*sag

	cases := []struct {
		name  string
		op    scenes.Op
		exact float64 // in pixels at 72 dpi
	}{
		{"rectangle", scenes.Op{Path: poly(100.3, 100.2, 140.7, 100.2, 140.7, 130.8, 100.3, 130.8), Paint: black}, 40.4 * 30.6},
		{"rotated square", scenes.Op{Path: poly(300-c+s, 300-s-c, 300+c+s, 300+s-c, 300+c-s, 300+s+c, 300-c-s, 300-s+c), Paint: black}, 10000},
		{"circle r=3.07", scenes.Op{Path: &circle, Paint: black}, circleArea},
		{"hairline", scenes.Op{Stroke: true, Path: poly(100.2, 500.1, 100.2+400/math.Sqrt2, 500.1+400/math.Sqrt2), Style: &stilus.StrokeStyle{}, Paint: black}, 400},
	}
	for _, k := range cases {
		d := &refDoc{s: &scenes.Scene{Name: k.name, Clip: stilus.Rect{X1: scenes.PageW, Y1: scenes.PageH}, Ops: []scenes.Op{k.op}}}
		img, err := d.Render(0, 72)
		if err != nil {
			t.Fatal(err)
		}
		got := ink(img.(*image.RGBA))
		if e := got/k.exact - 1; math.Abs(e) > 0.002 {
			t.Errorf("%s: ink %.2f, exact %.2f (%+.2f %%)", k.name, got, k.exact, 100*e)
		}
	}
}

func ink(img *image.RGBA) float64 {
	var s float64
	for i := 0; i < len(img.Pix); i += 4 {
		s += 1 - float64(img.Pix[i])/255
	}
	return s
}
