package stilus_test

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/timzifer/stilus"
)

// TestCurveArea checks that filled curves enclose their exact area. Plain
// chords lie inside convex arcs and lost up to 4.6 % of a circle of glyph
// size; the flattened polyline must neither lose nor gain area.
func TestCurveArea(t *testing.T) {
	// Area of the unit circle drawn as four cubics with k = 0.5522847498.
	const cubicUnit = 3.1424723293678163
	for _, r := range []float64{1.3, 2.07, 3.07, 5.5, 12.3, 50.13, 150.2} {
		cx, cy := 200.3, 200.4
		gotC := fillInk(cubicCircle(cx, cy, r))
		gotQ := fillInk(quadCircle(cx, cy, r))
		ec := gotC/(cubicUnit*r*r) - 1
		eq := gotQ/quadCircleArea(r) - 1
		if math.Abs(ec) > 0.004 || math.Abs(eq) > 0.004 {
			t.Errorf("r=%g: area off by %+.2f %% (cubics), %+.2f %% (quadratics); want within 0.4 %%", r, 100*ec, 100*eq)
		}
	}
}

// cubicCircle is a circle of radius r as four cubics.
func cubicCircle(cx, cy, r float64) *stilus.Path {
	const k = 0.5522847498
	var p stilus.Path
	p.MoveTo(float32(cx+r), float32(cy))
	q := [][6]float64{{r, k * r, k * r, r, 0, r}, {-k * r, r, -r, k * r, -r, 0}, {-r, -k * r, -k * r, -r, 0, -r}, {k * r, -r, r, -k * r, r, 0}}
	for _, a := range q {
		p.CubicTo(float32(cx+a[0]), float32(cy+a[1]), float32(cx+a[2]), float32(cy+a[3]), float32(cx+a[4]), float32(cy+a[5]))
	}
	p.Close()
	return &p
}

// quadCircle is a circle of radius r as eight quadratics, each tangent to
// the circle at its ends.
func quadCircle(cx, cy, r float64) *stilus.Path {
	rc := r / math.Cos(math.Pi/8)
	var p stilus.Path
	p.MoveTo(float32(cx+r), float32(cy))
	for i := 0; i < 8; i++ {
		am, a1 := (float64(i)+0.5)*math.Pi/4, float64(i+1)*math.Pi/4
		p.QuadTo(float32(cx+rc*math.Cos(am)), float32(cy+rc*math.Sin(am)), float32(cx+r*math.Cos(a1)), float32(cy+r*math.Sin(a1)))
	}
	p.Close()
	return &p
}

// quadCircleArea is the area of quadCircle: eight triangles to the chords
// plus eight parabolic segments of 2/3 · chord · sagitta, the sagitta being
// half the control point's distance from the chord.
func quadCircleArea(r float64) float64 {
	chord := 2 * r * math.Sin(math.Pi/8)
	sag := (r/math.Cos(math.Pi/8) - r*math.Cos(math.Pi/8)) / 2
	return 8*0.5*r*r*math.Sin(math.Pi/4) + 8*(2.0/3)*chord*sag
}

// fillInk fills p in black and returns its coverage summed over pixels.
func fillInk(p *stilus.Path) float64 {
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	c := stilus.NewCanvas(img)
	c.Fill(p, stilus.Identity, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}})
	var s float64
	for i := 3; i < len(img.Pix); i += 4 {
		s += float64(img.Pix[i]) / 255
	}
	return s
}
