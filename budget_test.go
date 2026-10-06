package stilus

import (
	"errors"
	"image"
	"image/color"
	"sync"
	"testing"
)

// mipLevelBytes returns the bytes the levels of t below its base take
// once made, each level and each distinct wrap level once.
func mipLevelBytes(t *Texture) int {
	n := 0
	for k := 1; k <= t.Levels(); k++ {
		p := t.Level(k)
		n += p.Bytes()
		if w := t.wrapLevel(k); w != p {
			n += w.Bytes()
		}
	}
	return n
}

// MipBytes counts the levels as made: rounded up, a one-pixel axis kept,
// and the wrap levels that differ from them.
func TestMipBytesCoversLevels(t *testing.T) {
	var colours Palette
	for i := range colours {
		colours[i] = PackRGBA(color.RGBA{uint8(i), 0, uint8(255 - i), 255})
	}
	planes := map[string]func(w, h int) Plane{
		"rgba": func(w, h int) Plane {
			return Plane{Kind: PlaneRGBA, W: w, H: h, Stride: w, Pix32: make([]uint32, w*h)}
		},
		"gray": func(w, h int) Plane {
			return Plane{Kind: PlaneIndex, W: w, H: h, Stride: w, Pix8: make([]uint8, w*h), Pal: GrayPalette}
		},
		"alpha": func(w, h int) Plane {
			return Plane{Kind: PlaneIndex, W: w, H: h, Stride: w, Pix8: make([]uint8, w*h), Pal: AlphaPalette}
		},
		"indexed": func(w, h int) Plane {
			return Plane{Kind: PlaneIndex, W: w, H: h, Stride: w, Pix8: make([]uint8, w*h), Pal: &colours}
		},
	}
	sizes := [][2]int{{4096, 1}, {1, 4096}, {3, 3}, {256, 256}, {5, 7}, {100, 3}, {1, 1}, {2, 1}, {1000, 600}, {17, 4096}}
	for name, mk := range planes {
		for _, sz := range sizes {
			tex := NewTexture(mk(sz[0], sz[1]))
			est := tex.MipBytes()
			if got := mipLevelBytes(tex); est != got {
				t.Errorf("%s %dx%d: MipBytes %d, levels take %d", name, sz[0], sz[1], est, got)
			}
		}
	}
	// The cases of the report: the levels of a thin texture take about W·H,
	// and its wrap levels as much again.
	for _, c := range []struct{ w, h, want int }{
		{4096, 1, 2 * 16376}, {1, 4096, 2 * 16376}, {256, 256, 87380},
	} {
		tex := NewTexture(planes["rgba"](c.w, c.h))
		if got := tex.MipBytes(); got != c.want {
			t.Errorf("%dx%d: MipBytes %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

// A glyph whose mask alone exceeds MaxBytes is drawn but not kept.
func TestGlyphCacheOversizedEntry(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	c := NewCanvas(img)
	var outline Path
	outline.Rect(0, 0, 1, 1)
	gc := GlyphCache{MaxBytes: 100}
	paint := &Paint{Color: color.RGBA{A: 255}}
	gc.FillGlyph(c, 1, 1, &outline, Scale(20, 20), paint)
	if gc.bytes > gc.MaxBytes || len(gc.ents) != 0 {
		t.Fatalf("%d bytes in %d entries cached, budget %d", gc.bytes, len(gc.ents), gc.MaxBytes)
	}
	if img.RGBAAt(10, 10).A != 255 || img.RGBAAt(25, 25).A != 0 {
		t.Fatalf("glyph not drawn: alpha %d, %d", img.RGBAAt(10, 10).A, img.RGBAAt(25, 25).A)
	}
	// Drawn again, the same bytes, and still nothing kept.
	want := append([]uint8(nil), img.Pix...)
	gc.FillGlyph(c, 1, 1, &outline, Scale(20, 20), paint)
	if gc.bytes > gc.MaxBytes || string(img.Pix) != string(want) {
		t.Fatalf("second draw: %d bytes cached, pixels changed %v", gc.bytes, string(img.Pix) != string(want))
	}
}

// A reused evicted mask counts its whole allocation.
func TestGlyphCacheReusedMaskAccounting(t *testing.T) {
	gc := GlyphCache{MaxBytes: 1 << 20}
	big := &image.Alpha{Pix: make([]uint8, 512), Stride: 16, Rect: image.Rect(0, 0, 16, 32)}
	gc.recycle(big)
	if gc.freeBytes != 512 {
		t.Fatalf("free %d bytes", gc.freeBytes)
	}
	m := gc.newMask(image.Rect(0, 0, 2, 2))
	if len(m.Pix) != 4 || cap(m.Pix) != 512 || gc.freeBytes != 0 {
		t.Fatalf("mask len %d cap %d, free %d", len(m.Pix), cap(m.Pix), gc.freeBytes)
	}
	if !gc.store(glyphKey{gid: 1}, m) {
		t.Fatal("not stored")
	}
	if gc.bytes != 64+512 {
		t.Fatalf("entry counted %d bytes, want %d", gc.bytes, 64+512)
	}
	gc.evict()
	if gc.bytes != 0 || gc.freeBytes != 512 {
		t.Fatalf("after eviction: %d bytes cached, %d free", gc.bytes, gc.freeBytes)
	}
}

// Lowering MaxBytes brings the cache within it at the next store.
func TestGlyphCacheBudgetReduction(t *testing.T) {
	var g Path
	g.MoveTo(0.1, 0)
	g.LineTo(0.6, 0)
	g.CubicTo(0.9, 0.3, 0.7, 0.8, 0.3, 0.7)
	g.Close()
	c := NewCanvas(image.NewRGBA(image.Rect(0, 0, 400, 400)))
	paint := &Paint{Color: rgba(20, 20, 20, 255)}
	gc := GlyphCache{MaxBytes: 1 << 20}
	draw := func(id int32) {
		gc.FillGlyph(c, 1, id, &g, Matrix{16, 0, 0, -16, float64(id%20) * 18, float64(id/20)*18 + 16}, paint)
	}
	for id := range int32(200) {
		draw(id)
	}
	full := gc.bytes
	gc.MaxBytes = full / 4
	draw(210)
	if gc.bytes > gc.MaxBytes || gc.freeBytes > gc.MaxBytes/8 {
		t.Fatalf("%d bytes cached (was %d), %d free, budget %d", gc.bytes, full, gc.freeBytes, gc.MaxBytes)
	}
	n := 0
	for i := range gc.ents {
		n += gc.ents[i].size()
	}
	if n != gc.bytes {
		t.Fatalf("entries take %d bytes, counted %d", n, gc.bytes)
	}
}

// zigzag returns a closed path of n non-horizontal edges.
func zigzag(n int) *Path {
	var p Path
	p.MoveTo(0, 0)
	for i := 1; i < n; i++ {
		p.LineTo(float32(i)*0.1, float32(i%2)*50)
	}
	p.Close()
	return &p
}

// A shape stops recording at its budget, before the canvas edge budget
// is consulted, and every draw reports the truncation.
func TestShapeRecordBudget(t *testing.T) {
	p := zigzag(1000)
	var s Shape
	s.MaxBytes = 100 * shapeEdgeCost
	s.SetFill(p, Identity, NonZero)

	c := NewCanvas(image.NewRGBA(image.Rect(0, 0, 128, 64)))
	c.FillShape(&s, white)
	if n := len(s.forms[0].edges); n != 100 {
		t.Fatalf("%d edges recorded, budget 100", n)
	}
	if !errors.Is(c.Err(), ErrEdgeBudget) {
		t.Fatalf("FillShape: err %v", c.Err())
	}
	var u Union
	u.Shape(&s)
	c2 := NewCanvas(image.NewRGBA(image.Rect(0, 0, 128, 64)))
	c2.FillUnion(&u, white)
	if !errors.Is(c2.Err(), ErrEdgeBudget) {
		t.Fatalf("FillUnion: err %v", c2.Err())
	}

	// Within its budget a shape draws as before.
	s.MaxBytes = 0
	s.SetFill(p, Identity, NonZero)
	c3 := NewCanvas(image.NewRGBA(image.Rect(0, 0, 128, 64)))
	c3.FillShape(&s, white)
	if c3.Err() != nil || len(s.forms[0].edges) != 1000 {
		t.Fatalf("err %v, %d edges", c3.Err(), len(s.forms[0].edges))
	}
}

// A stroke's strips and edges count against the budget of each form, and
// recording stops early.
func TestShapeStrokeBudget(t *testing.T) {
	p := zigzag(2000)
	st := &StrokeStyle{Width: 2, Join: MiterJoin}
	var s Shape
	s.MaxBytes = 50 * shapeStripCost
	s.SetStroke(p, Identity, st)
	for i, paint := range []*Paint{white, {Color: rgba(0, 0, 0, 128)}} {
		c := NewCanvas(image.NewRGBA(image.Rect(0, 0, 256, 64)))
		c.FillShape(&s, paint)
		if !errors.Is(c.Err(), ErrEdgeBudget) {
			t.Fatalf("draw %d: err %v", i, c.Err())
		}
	}
	for i := range s.forms {
		f := &s.forms[i]
		if !f.made.Load() {
			continue
		}
		used := len(f.edges)*shapeEdgeCost + len(f.strips)*shapeStripCost
		if used > s.MaxBytes || !f.overflow {
			t.Fatalf("form %d: %d bytes recorded, overflow %v, budget %d", i, used, f.overflow, s.MaxBytes)
		}
	}
}

// Canvases with different clips and edge budgets making a shared shape
// at once get the same form.
func TestShapeBudgetConcurrent(t *testing.T) {
	p := zigzag(5000)
	for range 20 {
		var s Shape
		s.MaxBytes = 300 * shapeEdgeCost
		s.SetFill(p, Identity, NonZero)
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for w := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := NewCanvas(image.NewRGBA(image.Rect(0, 0, 512, 64)))
				c.r.MaxEdges = 4 + w*1000
				c.ClipRect(Rect{X0: float64(w * 60), Y0: 0, X1: float64(w*60 + 8), Y1: 64}, Identity)
				c.FillShape(&s, white)
				errs[w] = c.Err()
			}()
		}
		wg.Wait()
		if n := len(s.forms[0].edges); n != 300 {
			t.Fatalf("%d edges recorded, budget 300", n)
		}
		for w, err := range errs {
			if !errors.Is(err, ErrEdgeBudget) {
				t.Fatalf("canvas %d: err %v", w, err)
			}
		}
	}
}
