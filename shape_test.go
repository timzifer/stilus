package stilus

import (
	"bytes"
	"image"
	"math"
	"math/rand"
	"sync"
	"testing"
)

// shapeCase is one random operation for the FillShape equality tests.
type shapeCase struct {
	p      Path
	m      Matrix
	stroke bool
	rule   FillRule
	st     StrokeStyle
	paint  Paint
}

func randShapeCase(rng *rand.Rand, w, h float64, g *LinearGradient) *shapeCase {
	c := &shapeCase{}
	pt := func() (float32, float32) {
		return float32(rng.Float64()*w*1.4 - w*0.2), float32(rng.Float64()*h*1.4 - h*0.2)
	}
	for sp := 0; sp < 1+rng.Intn(3); sp++ {
		c.p.MoveTo(pt())
		for k := 0; k < 1+rng.Intn(6); k++ {
			switch rng.Intn(5) {
			case 0:
				x1, y1 := pt()
				x2, y2 := pt()
				c.p.QuadTo(x1, y1, x2, y2)
			case 1:
				x1, y1 := pt()
				x2, y2 := pt()
				x3, y3 := pt()
				c.p.CubicTo(x1, y1, x2, y2, x3, y3)
			default:
				c.p.LineTo(pt())
			}
		}
		if rng.Intn(3) == 0 {
			c.p.Close()
		}
	}
	if rng.Intn(4) == 0 {
		c.p.Reset()
		x, y := pt()
		x1, y1 := pt()
		c.p.MoveTo(x, y)
		c.p.LineTo(x1, y1)
	}
	switch rng.Intn(4) {
	case 0:
		c.m = Identity
	case 1:
		c.m = Scale(-1, 1).Mul(Translate(w, 0)) // mirrored
	case 2:
		c.m = Translate(-w/2, -h/2).Mul(Rotate(rng.Float64())).Mul(Translate(w/2, h/2))
	default:
		c.m = Translate(-w/2, -h/2).Mul(Scale(1, 0.4+rng.Float64())).Mul(Rotate(rng.Float64())).Mul(Translate(w/2, h/2))
	}
	c.stroke = rng.Intn(3) != 0
	c.rule = FillRule(rng.Intn(2))
	widths := []float64{0, 0.5, 1, 2.5, 7, 16}
	c.st = StrokeStyle{
		Width: widths[rng.Intn(len(widths))], Cap: Cap(rng.Intn(3)), Join: Join(rng.Intn(3)),
		MiterLimit: []float64{1, 4, 10}[rng.Intn(3)],
	}
	switch rng.Intn(5) {
	case 0:
		c.st.Dash, c.st.DashPhase = []float64{9, 5}, rng.Float64()*10
	case 1:
		c.st.Dash = []float64{0.05, 0.07} // dense
	}
	switch rng.Intn(4) {
	case 0:
		c.paint.Color = rgba(0, 0, 128, 128)
	case 1:
		c.paint.Shader = g
	default:
		c.paint.Color = rgba(uint8(rng.Intn(256)), 0, 0, 255)
	}
	return c
}

func (c *shapeCase) draw(cv *Canvas) {
	if c.stroke {
		cv.Stroke(&c.p, c.m, &c.st, &c.paint)
	} else {
		cv.Fill(&c.p, c.m, c.rule, &c.paint)
	}
}

func (c *shapeCase) set(s *Shape) bool {
	if c.stroke {
		return s.SetStroke(&c.p, c.m, &c.st)
	}
	return s.SetFill(&c.p, c.m, c.rule)
}

// TestFillShapeEqualsDirect draws random fills and strokes under random
// clips band by band, directly and from a shape, and requires the same
// bytes. Stroke's culling against the clip is off: it replaces curves
// outside the clip by their chords, which a shape prepared for every band
// cannot do.
func TestFillShapeEqualsDirect(t *testing.T) {
	const w, h = 160, 150
	rng := rand.New(rand.NewSource(5))
	var g LinearGradient
	g.Ramp, g.Alpha = grayRamp(64), 255
	g.Set(0, 0, w, h, Identity)
	var mask Path
	mask.Ellipse(w/2, h/2, w*0.45, h*0.4)
	var s Shape
	for i := 0; i < 600; i++ {
		c := randShapeCase(rng, w, h, &g)
		if !c.set(&s) {
			t.Fatalf("case %d: Set reported false", i)
		}
		clip := rng.Intn(3)
		cr := Rect{rng.Float64() * 20, rng.Float64() * 20, w - rng.Float64()*20, h - rng.Float64()*20}
		bandH := []int{h, 1, 7, 16, 32, 50}[rng.Intn(6)]
		want := image.NewRGBA(image.Rect(0, 0, w, h))
		got := image.NewRGBA(image.Rect(0, 0, w, h))
		dc, sc := NewCanvas(want), NewCanvas(got)
		dc.s.noCull = true
		for y := 0; y < h; y += bandH {
			band := image.Rect(0, y, w, min(y+bandH, h))
			for _, cv := range []*Canvas{dc, sc} {
				cv.Reset(cv.dst, band)
				switch clip {
				case 1:
					cv.ClipRect(cr, Identity)
				case 2:
					cv.ClipPath(&mask, Identity, NonZero)
				}
			}
			c.draw(dc)
			sc.FillShape(&s, &c.paint)
			if dc.Err() != sc.Err() {
				t.Fatalf("case %d band %v: err %v, want %v", i, band, sc.Err(), dc.Err())
			}
		}
		if !bytes.Equal(want.Pix, got.Pix) {
			t.Fatalf("case %d (stroke %v, style %+v, clip %d, bands of %d): FillShape differs from direct drawing in %d bytes",
				i, c.stroke, c.st, clip, bandH, diffBytes(want.Pix, got.Pix))
		}
		// Nothing outside Bounds is touched.
		if b := s.Bounds(); true {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					if !image.Pt(x, y).In(b) && got.RGBAAt(x, y).A != 0 {
						t.Fatalf("case %d: pixel (%d, %d) painted outside Bounds %v", i, x, y, b)
					}
				}
			}
		}
	}
}

func diffBytes(a, b []byte) int {
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// TestFillShapeBands checks that a shape drawn in bands is the shape drawn
// whole, up to the rounding where an edge is cut at a band border (3 levels
// over 5000 random cases).
// (Stroke is not: per band it flattens the curves outside the band to
// their chords, which moves its analytic rows.)
func TestFillShapeBands(t *testing.T) {
	const w, h = 160, 150
	rng := rand.New(rand.NewSource(9))
	var s Shape
	const limit = 3
	for i := 0; i < 400; i++ {
		c := randShapeCase(rng, w, h, nil)
		c.paint = Paint{Color: rgba(0, 0, 0, 255)}
		c.set(&s)
		whole := image.NewRGBA(image.Rect(0, 0, w, h))
		banded := image.NewRGBA(image.Rect(0, 0, w, h))
		NewCanvas(whole).FillShape(&s, &c.paint)
		bc := NewCanvas(banded)
		bandH := 1 + rng.Intn(40)
		for y := 0; y < h; y += bandH {
			bc.Reset(banded, image.Rect(0, y, w, min(y+bandH, h)))
			bc.FillShape(&s, &c.paint)
		}
		for k := range whole.Pix {
			if d := int(whole.Pix[k]) - int(banded.Pix[k]); d < -limit || d > limit {
				t.Fatalf("case %d, bands of %d: byte %d is %d, %d unbanded", i, bandH, k, banded.Pix[k], whole.Pix[k])
			}
		}
	}
}

func TestShapeEmpty(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	c := NewCanvas(img)
	var s Shape
	c.FillShape(&s, &Paint{Color: rgba(0, 0, 0, 255)}) // zero value
	var p Path
	p.Rect(0, 0, 8, 8)
	if s.SetFill(&p, Matrix{math.NaN(), 0, 0, 1, 0, 0}, NonZero) {
		t.Error("SetFill accepted a non-finite transform")
	}
	if s.SetStroke(&p, Matrix{1, 0, 0, math.Inf(1), 0, 0}, &StrokeStyle{Width: 1}) {
		t.Error("SetStroke accepted a non-finite transform")
	}
	c.FillShape(&s, &Paint{Color: rgba(0, 0, 0, 255)})
	var empty Path
	s.SetStroke(&empty, Identity, &StrokeStyle{Width: 1})
	c.FillShape(&s, &Paint{Color: rgba(0, 0, 0, 255)})
	for _, v := range img.Pix {
		if v != 0 {
			t.Fatal("an empty shape painted")
		}
	}
	if !s.Bounds().Empty() {
		t.Errorf("empty shape has bounds %v", s.Bounds())
	}
	s.SetFill(&p, Identity, NonZero)
	if got := s.Bounds(); got != image.Rect(0, 0, 8, 8) {
		t.Errorf("Bounds = %v, want (0,0)-(8,8)", got)
	}
}

// TestShapeConcurrent draws one set of shapes from several canvases at
// once, band by band, under the race detector, and compares the page with
// one drawn directly.
func TestShapeConcurrent(t *testing.T) {
	const w, h, bands = 200, 192, 12
	rng := rand.New(rand.NewSource(77))
	cases := make([]*shapeCase, 120)
	shapes := make([]Shape, len(cases))
	for i := range cases {
		cases[i] = randShapeCase(rng, w, h, nil)
		cases[i].paint = Paint{Color: rgba(uint8(i), 40, 90, 255)}
		cases[i].set(&shapes[i])
	}
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(image.Rect(0, 0, w, h))
	var wg sync.WaitGroup
	for b := 0; b < bands; b++ {
		band := image.Rect(0, h*b/bands, w, h*(b+1)/bands)
		dc := NewCanvas(want)
		dc.Reset(want, band)
		dc.s.noCull = true
		for _, c := range cases {
			c.draw(dc)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewCanvas(got)
			c.Reset(got, band)
			for i := range shapes {
				c.FillShape(&shapes[i], &cases[i].paint)
			}
		}()
	}
	wg.Wait()
	if !bytes.Equal(want.Pix, got.Pix) {
		t.Fatalf("concurrent shapes differ in %d bytes", diffBytes(want.Pix, got.Pix))
	}
}

// TestShapeBins checks the binned record lists against a linear filter.
func TestShapeBins(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for n := 0; n < 300; n += 7 {
		rows := make([][2]int32, n)
		for i := range rows {
			r0 := int32(rng.Intn(4000) - 1000)
			l := int32(1 + rng.Intn(1+rng.Intn(3000)))
			rows[i] = [2]int32{r0, r0 + l}
		}
		var b rowBins
		flat := make([]int32, 0, 2*n)
		for _, r := range rows {
			flat = append(flat, r[0], r[1])
		}
		b.build(flat)
		if len(b.start) == 0 {
			continue
		}
		if len(b.ids) > 4*n || len(b.start)-1 > n {
			t.Errorf("n %d: %d bins, %d entries", n, len(b.start)-1, len(b.ids))
		}
		for q := 0; q < 50; q++ {
			y0 := rng.Intn(6000) - 1500
			y1 := y0 + 1 + rng.Intn(300)
			seen := map[int32]int{}
			b0, b1 := b.span(y0, y1)
			for bi := b0; bi <= b1; bi++ {
				for _, id := range b.ids[b.start[bi]:b.start[bi+1]] {
					if bi == b0 || b.bin(int64(rows[id][0])) == bi {
						seen[id]++
					}
				}
			}
			for i, r := range rows {
				hit := r[1] > int32(y0) && r[0] < int32(y1)
				if hit && seen[int32(i)] != 1 {
					t.Fatalf("n %d rows [%d,%d): record %d %v seen %d times", n, y0, y1, i, r, seen[int32(i)])
				}
			}
			for id, k := range seen {
				if k != 1 {
					t.Fatalf("record %d seen %d times", id, k)
				}
			}
		}
	}
}

// TestShapeNoAllocs: rebuilding and drawing a shape allocates nothing
// once warm.
func TestShapeNoAllocs(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	c := NewCanvas(img)
	var p Path
	for i := 0; i < 40; i++ {
		p.MoveTo(float32(i*7), 0)
		p.LineTo(float32(300-i*7), 300)
		p.CubicTo(10, 20, 200, 30, float32(i), 290)
	}
	st := &StrokeStyle{Width: 2, Join: RoundJoin, Dash: []float64{20, 4}}
	paint := &Paint{Color: rgba(0, 0, 0, 255)}
	var s Shape
	run := func() {
		s.SetStroke(&p, Identity, st)
		for y := 0; y < 300; y += 25 {
			c.Reset(img, image.Rect(0, y, 300, y+25))
			c.FillShape(&s, paint)
		}
		s.SetFill(&p, Identity, EvenOdd)
		for y := 0; y < 300; y += 25 {
			c.Reset(img, image.Rect(0, y, 300, y+25))
			c.FillShape(&s, paint)
		}
	}
	run()
	if a := testing.AllocsPerRun(5, run); a != 0 {
		t.Errorf("%.1f allocs per run", a)
	}
}
