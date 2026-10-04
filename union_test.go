package stilus

import (
	"bytes"
	"image"
	"math"
	"math/rand"
	"sync"
	"testing"
)

// segSink records the lines a Stroker emits.
type segSink struct{ s [][4]float64 }

func (k *segSink) AddLine(x0, y0, x1, y1 float64) {
	k.s = append(k.s, [4]float64{x0, y0, x1, y1})
}

// windAt returns the winding number of the lines segs around (px, py),
// counted as refRenderMode counts it.
func windAt(segs [][4]float64, px, py float64) int {
	w := 0
	for _, e := range segs {
		ax, ay, bx, by := e[0], e[1], e[2], e[3]
		if ay <= py && by > py {
			if (bx-ax)*(py-ay)-(px-ax)*(by-ay) > 0 {
				w++
			}
		} else if ay > py && by <= py {
			if (bx-ax)*(py-ay)-(px-ax)*(by-ay) < 0 {
				w--
			}
		}
	}
	return w
}

// polySegs returns the closed polygons' edges, reversed if rev is set.
func polySegs(polys [][][2]float64, rev bool) [][4]float64 {
	var s [][4]float64
	for _, p := range polys {
		for i := range p {
			a, b := p[i], p[(i+1)%len(p)]
			if rev {
				a, b = b, a
			}
			s = append(s, [4]float64{a[0], a[1], b[0], b[1]})
		}
	}
	return s
}

// strokeSegs returns the device-space outline of a stroke, reversed as
// FillUnion reverses it.
func strokeSegs(p *Path, m Matrix, st *StrokeStyle) [][4]float64 {
	var k segSink
	var s Stroker
	s.Stroke(&k, p, m, st)
	if strokeReversed(m, st, prepStroke(m, st)) {
		for i, e := range k.s {
			k.s[i] = [4]float64{e[2], e[3], e[0], e[1]}
		}
	}
	return k.s
}

func randStrokePath(rng *rand.Rand, w, h float64) *Path {
	var p Path
	pt := func() (float32, float32) { return float32(rng.Float64() * w), float32(rng.Float64() * h) }
	for s := 0; s < 1+rng.Intn(2); s++ {
		p.MoveTo(pt())
		for k := 0; k < 1+rng.Intn(5); k++ {
			switch rng.Intn(4) {
			case 0:
				x1, y1 := pt()
				x2, y2 := pt()
				x3, y3 := pt()
				p.CubicTo(x1, y1, x2, y2, x3, y3)
			case 1:
				x1, y1 := pt()
				x2, y2 := pt()
				p.QuadTo(x1, y1, x2, y2)
			default:
				p.LineTo(pt())
			}
		}
		if rng.Intn(3) == 0 {
			p.Close()
		}
	}
	return &p
}

func randUnionMatrix(rng *rand.Rand, w, h float64) Matrix {
	switch rng.Intn(4) {
	case 0:
		return Identity
	case 1:
		return Scale(1, -1).Mul(Translate(0, h)) // PDF's y flip
	case 2:
		return Translate(-w/2, -h/2).Mul(Rotate(rng.Float64() * 6)).Mul(Translate(w/2, h/2))
	default:
		return Translate(-w/2, -h/2).Mul(Scale(-1, 0.4+rng.Float64())).Mul(Rotate(rng.Float64())).Mul(Translate(w/2, h/2))
	}
}

// TestStrokeOutlineWinding checks what Union relies on: a stroke's outline
// winds the same way everywhere it covers, inner corners, round joins,
// caps and dashes included, and after FillUnion's reversal every outline
// winds the same way as every other.
func TestStrokeOutlineWinding(t *testing.T) {
	rng := rand.New(rand.NewSource(28))
	const w, h = 40.0, 40.0
	n := 600
	if testing.Short() {
		n = 100
	}
	for i := 0; i < n; i++ {
		p := randStrokePath(rng, w, h)
		m := randUnionMatrix(rng, w, h)
		st := StrokeStyle{
			Width: []float64{0, 0.6, 2, 6, 15}[rng.Intn(5)], Cap: Cap(rng.Intn(3)),
			Join: Join(rng.Intn(3)), MiterLimit: 1 + rng.Float64()*10,
		}
		if rng.Intn(4) == 0 {
			st.Dash, st.DashPhase = []float64{3 + rng.Float64()*5, 2 + rng.Float64()*5}, rng.Float64()*5
		}
		segs := strokeSegs(p, m, &st)
		for y := -15.0; y < h+15; y += 0.43 {
			for x := -15.0; x < w+15; x += 0.43 {
				if wn := windAt(segs, x, y); wn > 0 {
					t.Fatalf("case %d (%+v, m %v): winding %d at %.2f,%.2f", i, st, m, wn, x, y)
				}
			}
		}
	}
}

// TestUnionFillOrientation checks that fills of either orientation, under
// mirroring transforms too, wind like stroke outlines after reversal.
func TestUnionFillOrientation(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	const w, h = 40.0, 40.0
	for i := 0; i < 200; i++ {
		var p Path
		switch i % 3 {
		case 0:
			p.Rect(5, 7, 20, 13)
		case 1:
			p.Ellipse(20, 20, 12, 7)
		default:
			// A ring: outer and inner contours of opposite orientation.
			p.Ellipse(20, 20, 15, 12)
			p.MoveTo(20, 14)
			p.CubicTo(16, 14, 14, 17, 14, 20)
			p.CubicTo(14, 23, 16, 26, 20, 26)
			p.CubicTo(24, 26, 26, 23, 26, 20)
			p.CubicTo(26, 17, 24, 14, 20, 14)
			p.Close()
		}
		if rng.Intn(2) == 0 {
			reversePath(&p)
		}
		m := randUnionMatrix(rng, w, h)
		segs := polySegs(refPolys(&p, m), fillReversed(&p, m))
		inside := 0
		for y := -10.0; y < h+10; y += 0.7 {
			for x := -10.0; x < w+10; x += 0.7 {
				switch wn := windAt(segs, x, y); {
				case wn > 0:
					t.Fatalf("case %d: winding %d at %.2f,%.2f", i, wn, x, y)
				case wn < 0:
					inside++
				}
			}
		}
		if inside == 0 {
			t.Fatalf("case %d: nothing inside", i)
		}
	}
}

// reversePath reverses p, which must consist of closed subpaths of lines
// and cubics.
func reversePath(p *Path) {
	var q Path
	pi := 0
	type seg struct {
		v   Verb
		pts []Point
	}
	var segs []seg
	var start Point
	flush := func() {
		if len(segs) == 0 {
			return
		}
		cur := segs[len(segs)-1].pts[len(segs[len(segs)-1].pts)-1]
		q.MoveTo(cur.X, cur.Y)
		for i := len(segs) - 1; i >= 0; i-- {
			s := segs[i]
			prev := start
			if i > 0 {
				prev = segs[i-1].pts[len(segs[i-1].pts)-1]
			}
			switch s.v {
			case LineTo:
				q.LineTo(prev.X, prev.Y)
			case CubicTo:
				q.CubicTo(s.pts[1].X, s.pts[1].Y, s.pts[0].X, s.pts[0].Y, prev.X, prev.Y)
			}
		}
		q.Close()
		segs = nil
	}
	for _, v := range p.Verbs {
		switch v {
		case MoveTo:
			flush()
			start = p.Points[pi]
		case LineTo, CubicTo:
			segs = append(segs, seg{v, p.Points[pi : pi+numPoints[v]]})
		}
		pi += numPoints[v]
	}
	flush()
	*p = q
}

func TestSignedArea(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 100; i++ {
		p := randStrokePath(rng, 100, 100)
		want := 0.0
		for _, poly := range refPolys(p, Identity) {
			for k := range poly {
				a, b := poly[k], poly[(k+1)%len(poly)]
				want += a[0]*b[1] - a[1]*b[0]
			}
		}
		got := signedArea(p)
		if math.Abs(got-want) > 5e-3*math.Abs(want)+0.5 {
			t.Fatalf("path %d: signed area %g, want %g", i, got, want)
		}
		// Far from the origin.
		for k := range p.Points {
			p.Points[k].X += 1e5
		}
		if far := signedArea(p); math.Abs(far-got) > 1e-4*math.Abs(got)+1 {
			t.Fatalf("path %d: signed area %g far from the origin, %g near it", i, far, got)
		}
	}
}

// unionElem is one element of a reference union: its device-space edges,
// wound as FillUnion winds them.
type unionElem [][4]float64

// refUnion renders the union of the elements by 16×16 supersampling. With
// exact set, a point is covered when it is inside any of them; else, as
// exact-area accumulation computes it (refRenderMode's accum), the
// elements' winding numbers are integrated over the pixel and the NonZero
// rule applied to the integral, which differs from the exact union only
// where edges cross inside a pixel.
func refUnion(elems []unionElem, clip image.Rectangle, exact bool) *image.Alpha {
	const ss = 16
	img := image.NewAlpha(clip)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		for x := clip.Min.X; x < clip.Max.X; x++ {
			n, wsum := 0, 0
			for sy := 0; sy < ss; sy++ {
				py := float64(y) + (float64(sy)+0.5)/ss
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					in := false
					for _, e := range elems {
						w := windAt(e, px, py)
						wsum += w
						in = in || w != 0
					}
					if in {
						n++
					}
				}
			}
			f := float64(n) / (ss * ss)
			if !exact {
				f = math.Min(math.Abs(float64(wsum))/(ss*ss), 1)
			}
			img.SetAlpha(x, y, alphaOf(f))
		}
	}
	return img
}

// coverageOf draws with an opaque white paint onto a transparent image and
// returns the alpha channel: the coverage composited.
func coverageOf(clip image.Rectangle, draw func(c *Canvas, p *Paint)) *image.Alpha {
	img := image.NewRGBA(clip)
	c := NewCanvas(img)
	draw(c, &Paint{Color: rgba(255, 255, 255, 255)})
	if err := c.Err(); err != nil {
		panic(err)
	}
	a := image.NewAlpha(clip)
	for i := range a.Pix {
		a.Pix[i] = img.Pix[4*i+3]
	}
	return a
}

type unionCase struct {
	name   string
	fills  []*Path
	m      Matrix
	stroke *StrokeStyle
	seams  bool // drawn one by one, the elements leave seams
}

func unionCases() []unionCase {
	rect := func(x, y, w, h float32, rev bool) *Path {
		var p Path
		p.Rect(x, y, w, h)
		if rev {
			reversePath(&p)
		}
		return &p
	}
	line := func(x0, y0, x1, y1 float32) *Path {
		var p Path
		p.MoveTo(x0, y0)
		p.LineTo(x1, y1)
		return &p
	}
	// A dense diagonal hatch as in timzifer/stilus#28: 2.07 px strokes,
	// 1.75 px apart, covering the clip completely.
	var hatch []*Path
	for o := float32(-60); o < 60; o += 1.75 * math.Sqrt2 {
		hatch = append(hatch, line(o-5, -5, o+45, 45))
	}
	var hair []*Path
	for o := float32(-60); o < 60; o += 0.5 * math.Sqrt2 {
		hair = append(hair, line(o-5, -5, o+45, 45))
	}
	var poly []*Path
	for i := float32(0); i < 8; i++ {
		var p Path
		p.MoveTo(2+4.3*i, 3)
		p.LineTo(5+4.3*i, 20)
		p.LineTo(1+4.3*i, 37)
		poly = append(poly, &p)
	}
	return []unionCase{
		{name: "overlapping rects", fills: []*Path{rect(3.3, 4.6, 20.4, 13.2, false), rect(14.7, 9.1, 18.9, 22.8, true)}, m: Identity},
		{seams: true, name: "abutting rects", fills: []*Path{rect(3.3, 4.6, 12.45, 25.2, false), rect(15.75, 4.6, 14.1, 25.2, true), rect(3.3, 29.8, 26.55, 6.35, false)}, m: Identity},
		{seams: true, name: "abutting rects, rotated", fills: []*Path{rect(3.3, 4.6, 12.45, 25.2, true), rect(15.75, 4.6, 14.1, 25.2, false)},
			m: Translate(-20, -20).Mul(Rotate(0.37)).Mul(Translate(20, 20))},
		{seams: true, name: "abutting rects, mirrored", fills: []*Path{rect(3.3, 4.6, 12.45, 25.2, false), rect(15.75, 4.6, 14.1, 25.2, true)},
			m: Scale(1, -1).Mul(Translate(0, 40))},
		{seams: true, name: "hatch", fills: hatch, m: Identity, stroke: &StrokeStyle{Width: 2.07}},
		{seams: true, name: "hatch, mirrored", fills: hatch, m: Scale(-1, 1).Mul(Translate(40, 0)), stroke: &StrokeStyle{Width: 2.07}},
		{name: "hatch, hairlines mirrored", fills: hair, m: Scale(1, -1).Mul(Translate(0, 40)), stroke: &StrokeStyle{Width: 0.5}},
		{name: "polylines, round", fills: poly, m: Identity, stroke: &StrokeStyle{Width: 3.1, Join: RoundJoin, Cap: RoundCap}},
		{name: "polylines, miter, dashed", fills: poly, m: Scale(1, -1).Mul(Translate(0, 40)),
			stroke: &StrokeStyle{Width: 3.6, Join: MiterJoin, MiterLimit: 10, Cap: SquareCap, Dash: []float64{6, 1.5}}},
	}
}

// TestFillUnionRef compares unions with the exact union of their elements:
// overlapping and abutting rectangles of either orientation, and a dense
// hatch, where elements drawn one by one leave seams.
func TestFillUnionRef(t *testing.T) {
	clip := image.Rect(0, 0, 40, 40)
	for _, tc := range unionCases() {
		var elems []unionElem
		var u Union
		for _, p := range tc.fills {
			if tc.stroke != nil {
				u.Stroke(p, tc.m, tc.stroke)
				elems = append(elems, strokeSegs(p, tc.m, tc.stroke))
			} else {
				u.Fill(p, tc.m, NonZero)
				elems = append(elems, polySegs(refPolys(p, tc.m), fillReversed(p, tc.m)))
			}
		}
		want := refUnion(elems, clip, false)
		got := coverageOf(clip, func(c *Canvas, paint *Paint) { c.FillUnion(&u, paint) })
		mean, max := diff(got, want)
		// Where edges of the elements do not cross inside pixels, that is
		// the exact union.
		if emean, _ := diff(got, refUnion(elems, clip, true)); tc.seams && emean > 0.5 {
			t.Errorf("%s: union differs from the exact union: mean %.3f/255", tc.name, emean)
		}
		// As TestFillRandom and checkAgainstRef: the supersampling grid
		// alone is off by a few levels at edges, and curves are flattened
		// at 0.1 px, up to ~26/255 for round joins.
		lim := 16
		if tc.stroke != nil && (tc.stroke.Join == RoundJoin || tc.stroke.Cap == RoundCap) {
			lim = 32
		}
		if mean > 0.5 || max > lim {
			t.Errorf("%s: union differs from the reference: mean %.3f/255, max %d/255", tc.name, mean, max)
		}
		sep := coverageOf(clip, func(c *Canvas, paint *Paint) {
			for _, p := range tc.fills {
				if tc.stroke != nil {
					c.Stroke(p, tc.m, tc.stroke, paint)
				} else {
					c.Fill(p, tc.m, NonZero, paint)
				}
			}
		})
		if _, smax := diff(sep, want); tc.seams && smax <= max+16 {
			t.Errorf("%s: separate fills show no seams (max %d/255, union %d/255)", tc.name, smax, max)
		}
	}
}

// TestFillUnionOne checks that a union of one element draws what the
// element draws on its own: the bytes of Fill for fills, and for strokes
// those of Stroke's outline path (translucent paint, more than one
// segment) within a level. Elements a union cannot sum (EvenOdd fills,
// dense dashes) are drawn on their own.
func TestFillUnionOne(t *testing.T) {
	const w, h = 120, 100
	rng := rand.New(rand.NewSource(11))
	var u Union
	for i := 0; i < 300; i++ {
		c := randShapeCase(rng, w, h, nil)
		if c.paint.Shader == nil {
			c.paint.Color = rgba(0, 0, 100, 160)
		}
		if c.stroke && singleSegment(&c.p, &c.st) {
			c.p.LineTo(3, 4)
		}
		want := image.NewRGBA(image.Rect(0, 0, w, h))
		got := image.NewRGBA(image.Rect(0, 0, w, h))
		clip := Rect{X0: rng.Float64() * 20, Y0: rng.Float64() * 20, X1: w - rng.Float64()*20, Y1: h - rng.Float64()*20}
		dc, uc := NewCanvas(want), NewCanvas(got)
		dc.ClipRect(clip, Identity)
		uc.ClipRect(clip, Identity)
		c.draw(dc)
		u.Reset()
		if c.stroke {
			u.Stroke(&c.p, c.m, &c.st)
		} else {
			u.Fill(&c.p, c.m, c.rule)
		}
		uc.FillUnion(&u, &c.paint)
		md := 0
		for k := range want.Pix {
			md = max(md, int(want.Pix[k])-int(got.Pix[k]), int(got.Pix[k])-int(want.Pix[k]))
		}
		// A union's strokes take the analytic path, which computes the
		// rows between their ends in closed form: within a level of the
		// outline's accumulated coverage.
		if lim := map[bool]int{false: 0, true: 1}[c.stroke]; md > lim {
			t.Fatalf("case %d (stroke %v, rule %d, style %+v): union of one differs in %d bytes, by up to %d",
				i, c.stroke, c.rule, c.st, diffBytes(want.Pix, got.Pix), md)
		}
	}
}

// TestFillUnionShapes checks that a union of shapes draws the bytes of the
// union of their paths, drawn whole and in bands, and that a union drawn
// in bands stays within 3/255 of the union drawn whole (as FillShape).
func TestFillUnionShapes(t *testing.T) {
	const w, h = 160, 150
	rng := rand.New(rand.NewSource(12))
	for i := 0; i < 60; i++ {
		var pu, su Union
		n := 1 + rng.Intn(12)
		shapes := make([]Shape, n)
		for k := 0; k < n; k++ {
			c := randShapeCase(rng, w, h, nil)
			c.set(&shapes[k])
			if c.stroke {
				pu.Stroke(&c.p, c.m, &c.st)
			} else {
				pu.Fill(&c.p, c.m, c.rule)
			}
			su.Shape(&shapes[k])
		}
		paint := &Paint{Color: rgba(0, 0, 0, 255)}
		bandH := 1 + rng.Intn(40)
		draw := func(u *Union, banded bool) *image.RGBA {
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			c := NewCanvas(img)
			c.s.noCull = true
			if !banded {
				c.FillUnion(u, paint)
				return img
			}
			for y := 0; y < h; y += bandH {
				c.Reset(img, image.Rect(0, y, w, min(y+bandH, h)))
				c.FillUnion(u, paint)
			}
			return img
		}
		whole := draw(&pu, false)
		for _, banded := range []bool{false, true} {
			pi, si := draw(&pu, banded), draw(&su, banded)
			if !bytes.Equal(pi.Pix, si.Pix) {
				t.Fatalf("case %d, banded %v: shapes differ from paths in %d bytes", i, banded, diffBytes(pi.Pix, si.Pix))
			}
			for k := range whole.Pix {
				if d := int(whole.Pix[k]) - int(pi.Pix[k]); d < -3 || d > 3 {
					t.Fatalf("case %d, bands of %d: byte %d is %d, %d unbanded", i, bandH, k, pi.Pix[k], whole.Pix[k])
				}
			}
		}
	}
}

// TestFillUnionConcurrent draws one union of shapes from several canvases
// at once, band by band, under the race detector.
func TestFillUnionConcurrent(t *testing.T) {
	const w, h, bands = 200, 192, 12
	rng := rand.New(rand.NewSource(13))
	shapes := make([]Shape, 80)
	var u Union
	for i := range shapes {
		randShapeCase(rng, w, h, nil).set(&shapes[i])
		u.Shape(&shapes[i])
	}
	paint := &Paint{Color: rgba(20, 40, 90, 255)}
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	got := image.NewRGBA(image.Rect(0, 0, w, h))
	var wg sync.WaitGroup
	for b := 0; b < bands; b++ {
		band := image.Rect(0, h*b/bands, w, h*(b+1)/bands)
		dc := NewCanvas(want)
		dc.Reset(want, band)
		dc.FillUnion(&u, paint)
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewCanvas(got)
			c.Reset(got, band)
			c.FillUnion(&u, paint)
		}()
	}
	wg.Wait()
	if !bytes.Equal(want.Pix, got.Pix) {
		t.Fatalf("concurrent unions differ in %d bytes", diffBytes(want.Pix, got.Pix))
	}
}

func TestFillUnionEmpty(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	c := NewCanvas(img)
	paint := &Paint{Color: rgba(0, 0, 0, 255)}
	var u Union
	c.FillUnion(&u, paint)
	var p, empty Path
	p.Rect(0, 0, 8, 8)
	u.Fill(&p, Matrix{math.NaN(), 0, 0, 1, 0, 0}, NonZero)
	u.Stroke(&p, Matrix{1, 0, 0, math.Inf(1), 0, 0}, &StrokeStyle{Width: 1})
	u.Fill(&empty, Identity, NonZero)
	u.Stroke(&empty, Identity, &StrokeStyle{Width: 1})
	var s Shape
	u.Shape(&s)
	if u.Len() != 0 {
		t.Errorf("Len = %d after adding nothing drawable", u.Len())
	}
	c.FillUnion(&u, paint)
	u.Fill(&p, Identity, NonZero)
	c.FillUnion(&u, &Paint{})
	for _, v := range img.Pix {
		if v != 0 {
			t.Fatal("an empty union painted")
		}
	}
	u.Reset()
	if u.Len() != 0 {
		t.Errorf("Len = %d after Reset", u.Len())
	}
}

func TestFillUnionEdgeBudget(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	c := NewCanvas(img)
	c.r.MaxEdges = 5 // each rectangle has two edges that are not horizontal
	var u Union
	paths := make([]Path, 4)
	for i := range paths {
		paths[i].Rect(float32(i*10+3), 3, 5, 50.5)
		u.Fill(&paths[i], Identity, NonZero)
	}
	c.FillUnion(&u, &Paint{Color: rgba(0, 0, 0, 255)})
	if c.Err() != ErrEdgeBudget {
		t.Errorf("Err = %v, want ErrEdgeBudget", c.Err())
	}
}

func TestFillUnionNoAllocs(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	c := NewCanvas(img)
	paths := make([]Path, 60)
	for i := range paths {
		p := &paths[i]
		p.MoveTo(float32(i*5), 0)
		p.LineTo(float32(300-i*5), 300)
		if i%3 == 0 {
			p.CubicTo(10, 20, 200, 30, float32(i), 290)
		}
	}
	st := &StrokeStyle{Width: 2, Join: RoundJoin, Dash: []float64{20, 4}}
	dense := &StrokeStyle{Width: 2, Dash: []float64{0.05, 0.07}}
	paint := &Paint{Color: rgba(0, 0, 0, 255)}
	var shapes [2]Shape
	var u Union
	run := func() {
		u.Reset()
		for i := range paths {
			switch i % 4 {
			case 0:
				u.Fill(&paths[i], Identity, NonZero)
			case 1:
				u.Stroke(&paths[i], Identity, st)
			case 2:
				u.Stroke(&paths[i], Scale(1, -1).Mul(Translate(0, 300)), dense)
			default:
				u.Fill(&paths[i], Identity, EvenOdd)
			}
		}
		shapes[0].SetStroke(&paths[1], Identity, st)
		shapes[1].SetFill(&paths[3], Identity, NonZero)
		u.Shape(&shapes[0])
		u.Shape(&shapes[1])
		for y := 0; y < 300; y += 25 {
			c.Reset(img, image.Rect(0, y, 300, y+25))
			c.FillUnion(&u, paint)
		}
	}
	run()
	if a := testing.AllocsPerRun(5, run); a != 0 {
		t.Errorf("%.1f allocs per run", a)
	}
}
