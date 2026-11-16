package stilus

import (
	"image"
	"testing"
)

// TestThinHairlines checks the coverage of strokes thinner than a device
// pixel: drawn at their width unless StrokeStyle.FullHairlines, one pixel
// wide at full coverage with it and for width 0, through every way a
// stroke is drawn.
func TestThinHairlines(t *testing.T) {
	var p Path
	p.MoveTo(10, 20.5)
	p.LineTo(90, 20.5)
	for _, tc := range []struct {
		name string
		st   StrokeStyle
		m    Matrix
		want uint8 // alpha on the line
	}{
		{"width 0", StrokeStyle{}, Identity, 255},
		{"width 0, full", StrokeStyle{FullHairlines: true}, Identity, 255},
		{"quarter", StrokeStyle{Width: 0.25}, Identity, 64},
		{"quarter, full", StrokeStyle{Width: 0.25, FullHairlines: true}, Identity, 255},
		{"half, scaled down", StrokeStyle{Width: 1}, Scale(0.5, 0.5), 128},
		{"most of a pixel", StrokeStyle{Width: 0.9}, Identity, 230},
		{"one pixel", StrokeStyle{Width: 1}, Identity, 255},
		{"tiny", StrokeStyle{Width: 0.001}, Identity, 0},
		{"tiny, full", StrokeStyle{Width: 0.001, FullHairlines: true}, Identity, 255},
	} {
		q := p
		if tc.m != Identity {
			// Keep the line on the pixel row 20 of the device.
			q = Path{}
			q.MoveTo(20, 41)
			q.LineTo(180, 41)
		}
		check := func(how string, draw func(c *Canvas)) {
			t.Helper()
			img := image.NewRGBA(image.Rect(0, 0, 100, 40))
			c := NewCanvas(img)
			draw(c)
			if err := c.Err(); err != nil {
				t.Fatalf("%s, %s: %v", tc.name, how, err)
			}
			if a := img.RGBAAt(50, 20).A; int(a)-int(tc.want) > 1 || int(tc.want)-int(a) > 1 {
				t.Errorf("%s, %s: alpha %d, want %d", tc.name, how, a, tc.want)
			}
			if a := img.RGBAAt(50, 19).A; a != 0 {
				t.Errorf("%s, %s: alpha %d above the line, want 0", tc.name, how, a)
			}
		}
		check("Stroke", func(c *Canvas) { c.Stroke(&q, tc.m, &tc.st, white) })
		check("FillShape", func(c *Canvas) {
			var s Shape
			s.SetStroke(&q, tc.m, &tc.st)
			c.FillShape(&s, white)
		})
		check("FillUnion", func(c *Canvas) {
			var u Union
			u.Stroke(&q, tc.m, &tc.st)
			c.FillUnion(&u, white)
		})
		check("ClipStroke", func(c *Canvas) {
			c.ClipStroke(&q, tc.m, &tc.st)
			var r Path
			r.MoveTo(0, 0)
			r.LineTo(100, 0)
			r.LineTo(100, 40)
			r.LineTo(0, 40)
			r.Close()
			c.Fill(&r, Identity, NonZero, white)
			c.PopClip()
		})
		if got, want := IsHairline(tc.m, &tc.st), tc.st.Width == 0 || tc.st.FullHairlines && tc.st.Width*tc.m[0] < 1; got != want {
			t.Errorf("%s: IsHairline %v, want %v", tc.name, got, want)
		}
	}
}

// TestThinHairlinesMerge checks the blot thin hairlines close together
// make: dense lines, a third of a pixel apart and a sixth of a pixel wide,
// cover half of every pixel by default, as their ink does, instead of all
// of it.
func TestThinHairlinesMerge(t *testing.T) {
	var p Path
	for y := float32(2); y < 38; y += 1.0 / 3 {
		p.MoveTo(2, y)
		p.LineTo(38, y)
	}
	for _, full := range []bool{false, true} {
		img := image.NewRGBA(image.Rect(0, 0, 40, 40))
		c := NewCanvas(img)
		c.Stroke(&p, Identity, &StrokeStyle{Width: 1.0 / 6, FullHairlines: full}, white)
		a := img.RGBAAt(20, 20).A
		if full && a != 255 || !full && (a < 124 || a > 132) {
			t.Errorf("full %v: alpha %d in the hatch", full, a)
		}
	}
}
