package stilus_test

import (
	"fmt"
	"image"
	"image/color"

	"github.com/timzifer/stilus"
)

func Example() {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	c := stilus.NewCanvas(img)

	// Page space in points, drawn at 144 dpi.
	m := stilus.Scale(2, 2)
	c.ClipRect(stilus.Rect{X0: 5, Y0: 5, X1: 95, Y1: 45}, m)

	var p stilus.Path
	p.MoveTo(10, 40)
	p.CubicTo(30, 0, 70, 80, 90, 10)
	c.Stroke(&p, m, &stilus.StrokeStyle{Width: 1.5, Cap: stilus.RoundCap}, &stilus.Paint{Color: color.RGBA{0, 0, 0, 255}})

	var r stilus.Path
	r.Ellipse(50, 25, 20, 12)
	c.Fill(&r, m, stilus.NonZero, &stilus.Paint{Color: color.RGBA{0, 0, 128, 128}}) // premultiplied

	c.PopClip()
	fmt.Println(c.Err())
	// Output: <nil>
}

// areaBlitter shows the span interface used to plug the rasterizer into
// another compositor: it just integrates the coverage.
type areaBlitter struct{ area int }

func (b *areaBlitter) BlitRun(y, x0, x1 int, alpha uint8) { b.area += (x1 - x0) * int(alpha) }
func (b *areaBlitter) BlitCoverage(y, x int, cov []uint8) {
	for _, a := range cov {
		b.area += int(a)
	}
}

func ExampleRasterizer() {
	r := stilus.NewRasterizer(image.Rect(0, 0, 100, 100))
	var p stilus.Path
	p.Rect(10.5, 10.5, 50, 30)
	p.Rect(20, 60, 400, 10) // wide: interior rows arrive as runs
	var b areaBlitter
	r.Fill(&p, stilus.Identity, stilus.NonZero, &b)
	fmt.Printf("%.0f px²\n", float64(b.area)/255)
	// Output: 2300 px²
}
