package stilus

import (
	"image"
	"math"
)

// A GlyphCache keeps the coverage mask of every glyph it has drawn, per
// size and subpixel position, so a glyph is rasterized once and then
// composited: the text of a page repeats a few dozen glyphs thousands of
// times. Masks are keyed by font, glyph, the linear part of the glyph's
// device matrix (to 1/64 pixel per em) and the position of its origin to a
// quarter pixel in x and y. Under a rectangle clip a mask is composited
// straight onto the destination where it keeps clear of the border pixels
// a fractional clip covers partially; under a mask clip, or touching such
// a border, it goes through the canvas as a pixel-aligned rectangle with a
// MaskShader, so it honours the clip stack like every other fill. Both
// give the same bytes.
//
// A cache is not safe for concurrent use; give each worker its own: no
// locks while drawing, and a mask costs about as much to make as one
// direct fill of the glyph, so a worker that meets a glyph twice has
// already gained. Keys hold a font id, not a font, so a cache keeps no
// font alive.

const (
	// MaxCachedEm is the em size in device pixels above which glyphs are
	// filled as paths: large glyphs are few, and their masks big.
	MaxCachedEm = 160
	// DefaultGlyphCacheBytes bounds the masks of a cache whose MaxBytes
	// is zero; a full cache evicts masks to make room.
	DefaultGlyphCacheBytes = 4 << 20

	// maxMaskArea bounds one mask (glyphs can reach far out of their em).
	maxMaskArea = 400 * 400
	subpixel    = 4
	// maxFreeMasks bounds the evicted masks kept for reuse.
	maxFreeMasks = 8
)

type glyphKey struct {
	font       uint64
	gid        int32
	a, b, c, d int32
	fx, fy     uint8
}

// GlyphCache is a cache of glyph coverage masks. The zero value is ready
// to use.
type GlyphCache struct {
	// MaxBytes bounds the masks kept, counted by their allocations plus
	// 64 bytes an entry; 0 means DefaultGlyphCacheBytes. A mask larger
	// than the budget is drawn but not kept. Evicted masks kept for reuse
	// take at most an eighth more; the rasterizer's scratch, sized by the
	// largest mask made, and the map are not counted.
	MaxBytes int

	masks map[glyphKey]int32 // index into ents
	ents  []glyphEnt
	bytes int
	rng   uint64 // eviction choices; 0 until seeded

	// free holds evicted masks, whose memory the next masks reuse: a
	// cache that churns allocates nothing once it has evicted a few.
	// Their pixels, freeBytes, stay within an eighth of the budget.
	free      []*image.Alpha
	freeBytes int
	// misses counts the masks made, evictions those dropped.
	misses, evictions int

	r       *Rasterizer
	mb      MaskBlitter
	tmp     image.Alpha // rasterized, before trim
	scratch []uint8
	shader  MaskShader
	rect    Path
	paint   Paint
}

// FillGlyph fills outline (nonzero), transformed by m into device space,
// with paint through c. The outline is identified by font and glyph: the
// cache assumes that equal ids have equal outlines. Small glyphs painted
// with a solid colour are drawn from the cache; large ones, and any glyph
// painted with a shader, are filled as paths.
//
// FillGlyph makes the glyph's key and looks it up before it learns that
// the glyph lies outside the clip, and a per-glyph test would need the
// outline's bounds. When a page is drawn in bands, each playing every
// glyph, the caller should cull whole text runs against the band
// (Canvas.Clip) by their box, known from the font's bounding box and the
// advances, before calling FillGlyph for their glyphs.
func (gc *GlyphCache) FillGlyph(c *Canvas, font uint64, glyph int32, outline *Path, m Matrix, paint *Paint) {
	clip := c.Clip()
	if clip.Empty() {
		return
	}
	ox, oy := math.Floor(m[4]), math.Floor(m[5])
	if paint.Shader != nil || !(sigmaMax(m) <= MaxCachedEm) ||
		!(math.Abs(ox) < 1<<24 && math.Abs(oy) < 1<<24) {
		c.Fill(outline, m, NonZero, paint)
		return
	}
	fx := int(math.Round((m[4] - ox) * subpixel))
	fy := int(math.Round((m[5] - oy) * subpixel))
	if fx == subpixel {
		ox, fx = ox+1, 0
	}
	if fy == subpixel {
		oy, fy = oy+1, 0
	}
	origin := image.Pt(int(ox), int(oy))
	key := glyphKey{
		font: font, gid: glyph,
		a: q64(m[0]), b: q64(m[1]), c: q64(m[2]), d: q64(m[3]),
		fx: uint8(fx), fy: uint8(fy),
	}
	var mask *image.Alpha
	i, ok := gc.masks[key]
	if ok {
		e := &gc.ents[i]
		e.used = true
		mask = e.mask
	} else {
		// The matrix the mask is made with: the quantized linear part and
		// the subpixel phase of the origin.
		mm := Matrix{
			float64(key.a) / 64, float64(key.b) / 64, float64(key.c) / 64, float64(key.d) / 64,
			float64(fx) / subpixel, float64(fy) / subpixel,
		}
		bb, ok := pixelBox(outline, mm)
		if !ok {
			// Too far out for a mask's integer bounds: the path may still
			// cover the clip.
			c.Fill(outline, m, NonZero, paint)
			return
		}
		if !bb.Add(origin).Overlaps(clip) {
			return // not cached: this worker may never need it
		}
		// Divided, not multiplied: the area overflows a 32-bit int.
		if bb.Dx() > maxMaskArea/bb.Dy() {
			c.Fill(outline, m, NonZero, paint)
			return
		}
		mask = gc.rasterize(outline, mm, bb)
		gc.misses++
		if !gc.store(key, mask) {
			// Larger than the whole budget: drawn, not kept.
			defer gc.recycle(mask)
		}
	}
	if mask == nil {
		return
	}
	r := mask.Rect.Add(origin)
	if !r.Overlaps(clip) {
		return
	}
	// The direct blit gives the bytes of the rectangle fill below where
	// that takes fillRect's path: an unmasked clip whose fractional
	// borders the glyph does not reach (elsewhere the frac blitter passes
	// runs on unchanged), an edge budget of at least two, and corners
	// exact in float32.
	const lim = 1 << 23
	if st := c.top(); st.mask == nil && st.clearOfFrac(r.Intersect(clip)) && (c.r.MaxEdges == 0 || c.r.MaxEdges >= 2) &&
		r.Min.X >= -lim && r.Min.Y >= -lim && r.Max.X <= lim && r.Max.Y <= lim {
		c.blitMask(mask, origin, PackRGBA(paint.Color), r.Intersect(clip))
		return
	}
	gc.shader = MaskShader{Mask: mask, X: origin.X, Y: origin.Y, Color: PackRGBA(paint.Color)}
	gc.paint.Shader = &gc.shader
	gc.rect.Reset()
	gc.rect.Rect(float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(r.Dy()))
	c.Fill(&gc.rect, Identity, NonZero, &gc.paint)
	gc.shader.Mask = nil
}

// blitMask composites the premultiplied colour col through mask, moved by
// origin, over the pixels of r, which must lie within the clip bounds and
// the moved mask. It rounds as the MaskShader path does (the colour scaled
// by coverage, then composited over), so an opaque colour does not go
// through covOpaque, whose single blend can round differently: only full
// coverage, where both agree, stores the colour.
func (c *Canvas) blitMask(mask *image.Alpha, origin image.Point, col uint32, r image.Rectangle) {
	defer c.guard()
	if col == 0 {
		return
	}
	cx := expand(col)
	opaque := col>>alphaShift&0xff == 255
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := mask.PixOffset(r.Min.X-origin.X, y-origin.Y)
		cov := mask.Pix[o : o+r.Dx()]
		d := c.solid.t.row(y, r.Min.X, r.Max.X)
		if !opaque {
			covOver(d, cov, cx)
			continue
		}
		for i, a := range cov {
			switch a {
			case 0:
			case 255:
				d[i] = col
			default:
				d[i] = overx(cx, d[i], uint32(a))
			}
		}
	}
}

// pixelBox returns the device pixels p can touch under m, grown by one
// pixel. It reports false if the bounds are non-finite or too large for
// integer pixel coordinates.
func pixelBox(p *Path, m Matrix) (image.Rectangle, bool) {
	if len(p.Points) == 0 {
		return image.Rectangle{}, true
	}
	b := m.transformRect(p.Bounds())
	const lim = 1 << 30
	if !(b.X0 >= -lim && b.Y0 >= -lim && b.X1 <= lim && b.Y1 <= lim) {
		return image.Rectangle{}, false
	}
	return image.Rect(int(math.Floor(b.X0-1)), int(math.Floor(b.Y0-1)), int(math.Ceil(b.X1+1)), int(math.Ceil(b.Y1+1))), true
}

// q64 quantizes a matrix entry to 1/64.
func q64(v float64) int32 {
	return int32(math.Round(max(min(v, 1<<24), -1<<24) * 64))
}

// rasterize returns the coverage of outline under m over bb, or nil if it
// covers nothing.
func (gc *GlyphCache) rasterize(outline *Path, m Matrix, bb image.Rectangle) *image.Alpha {
	if bb.Empty() {
		return nil
	}
	if gc.r == nil {
		gc.r = NewRasterizer(bb)
	}
	n := bb.Dx() * bb.Dy()
	if cap(gc.scratch) < n {
		gc.scratch = make([]uint8, n)
	}
	gc.scratch = gc.scratch[:n]
	clear(gc.scratch)
	mask := &gc.tmp
	*mask = image.Alpha{Pix: gc.scratch, Stride: bb.Dx(), Rect: bb}
	gc.r.SetClip(bb)
	gc.mb.Mask = mask
	gc.r.Fill(outline, m, NonZero, &gc.mb)
	gc.mb.Mask = nil
	out := gc.trim(mask)
	mask.Pix = nil
	return out
}

// trim returns a copy of the part of mask that has coverage (every pixel of
// a cached mask is composited), or nil if none has.
func (gc *GlyphCache) trim(mask *image.Alpha) *image.Alpha {
	r := mask.Rect
	x0, y0, x1, y1 := r.Max.X, r.Max.Y, r.Min.X, r.Min.Y
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := mask.Pix[(y-r.Min.Y)*mask.Stride:][:r.Dx()]
		for x, a := range row {
			if a != 0 {
				x0, x1 = min(x0, r.Min.X+x), max(x1, r.Min.X+x+1)
				y0, y1 = min(y0, y), max(y1, y+1)
			}
		}
	}
	// A literal, not image.Rect, which would order the bounds left
	// inverted when no pixel has coverage.
	t := image.Rectangle{Min: image.Pt(x0, y0), Max: image.Pt(x1, y1)}
	if t.Empty() {
		return nil
	}
	out := gc.newMask(t)
	for y := t.Min.Y; y < t.Max.Y; y++ {
		copy(out.Pix[(y-t.Min.Y)*out.Stride:][:t.Dx()], mask.Pix[mask.PixOffset(t.Min.X, y):])
	}
	return out
}

// recycle keeps the evicted mask m for reuse if there is room, else in
// place of the smallest kept one if m is larger: larger masks fit more.
func (gc *GlyphCache) recycle(m *image.Alpha) {
	if m == nil {
		return
	}
	n, room := cap(m.Pix), gc.budget()/8-gc.freeBytes
	if len(gc.free) < maxFreeMasks {
		if n <= room {
			if gc.free == nil {
				gc.free = make([]*image.Alpha, 0, maxFreeMasks)
			}
			gc.free = append(gc.free, m)
			gc.freeBytes += n
		}
		return
	}
	k := 0
	for i, f := range gc.free {
		if cap(f.Pix) < cap(gc.free[k].Pix) {
			k = i
		}
	}
	if d := n - cap(gc.free[k].Pix); d > 0 && d <= room {
		gc.free[k] = m
		gc.freeBytes += d
	}
}

// budget returns the bytes the cache's masks may take.
func (gc *GlyphCache) budget() int {
	if gc.MaxBytes <= 0 {
		return DefaultGlyphCacheBytes
	}
	return gc.MaxBytes
}

// newMask returns a mask of r, its pixels unset: the smallest evicted one
// large enough, sparing the larger ones for larger masks, else a new one.
func (gc *GlyphCache) newMask(r image.Rectangle) *image.Alpha {
	n := r.Dx() * r.Dy()
	k := -1
	for i, m := range gc.free {
		if c := cap(m.Pix); c >= n && (k < 0 || c < cap(gc.free[k].Pix)) {
			k = i
		}
	}
	if k < 0 {
		return &image.Alpha{Pix: make([]uint8, n), Stride: r.Dx(), Rect: r}
	}
	m, last := gc.free[k], len(gc.free)-1
	gc.free[k], gc.free[last] = gc.free[last], nil
	gc.free = gc.free[:last]
	gc.freeBytes -= cap(m.Pix)
	*m = image.Alpha{Pix: m.Pix[:n], Stride: r.Dx(), Rect: r}
	return m
}

// glyphEnt is a cached mask (nil: nothing to draw at this size).
type glyphEnt struct {
	key  glyphKey
	mask *image.Alpha
	used bool // drawn since the eviction scan last passed it
}

// size returns the bytes an entry counts against MaxBytes: its mask's
// whole allocation, which may be a larger evicted mask reused.
func (e *glyphEnt) size() int {
	n := 64 // key and map overhead
	if e.mask != nil {
		n += cap(e.mask.Pix)
	}
	return n
}

// store caches mask, evicting entries until it fits the budget, and
// reports whether it did: an entry larger than the whole budget is not
// kept, and evicts nothing.
//
// A full cache evicts entries chosen at random, sparing those drawn since
// they were last considered: frequent glyphs stay, and a set of glyphs
// drawn over and over that is a little larger than the budget keeps most
// of its masks, where emptying the cache, or evicting the least recently
// used mask, would have every glyph rasterized anew on each pass.
func (gc *GlyphCache) store(key glyphKey, mask *image.Alpha) bool {
	e := glyphEnt{key: key, mask: mask}
	n := e.size()
	limit := gc.budget()
	if n > limit {
		return false
	}
	if gc.masks == nil {
		gc.masks = make(map[glyphKey]int32, 256)
	}
	for gc.bytes+n > limit && len(gc.ents) > 0 {
		gc.evict()
	}
	gc.masks[key] = int32(len(gc.ents))
	gc.ents = append(gc.ents, e)
	gc.bytes += n
	return true
}

// evict removes one entry: the first not drawn since it was last
// considered among a few chosen at random, else the last of them.
func (gc *GlyphCache) evict() {
	if gc.rng == 0 {
		gc.rng = 0x9e3779b97f4a7c15
	}
	var j int
	for try := 0; try < 4; try++ {
		// xorshift64
		gc.rng ^= gc.rng << 13
		gc.rng ^= gc.rng >> 7
		gc.rng ^= gc.rng << 17
		j = int(gc.rng % uint64(len(gc.ents)))
		if !gc.ents[j].used {
			break
		}
		gc.ents[j].used = false
	}
	e := &gc.ents[j]
	gc.evictions++
	gc.bytes -= e.size()
	delete(gc.masks, e.key)
	gc.recycle(e.mask)
	last := len(gc.ents) - 1
	if j != last {
		*e = gc.ents[last]
		gc.masks[e.key] = int32(j)
	}
	gc.ents[last] = glyphEnt{}
	gc.ents = gc.ents[:last]
}

// MaskShader paints a solid premultiplied colour (PackRGBA layout) through
// a coverage mask whose origin is at (X, Y) in device space. Spans must lie
// within the mask's rectangle moved by (X, Y).
type MaskShader struct {
	Mask  *image.Alpha
	X, Y  int
	Color uint32
}

// ShadeSpan implements Shader.
func (s *MaskShader) ShadeSpan(y, x int, dst []uint32) {
	o := s.Mask.PixOffset(x-s.X, y-s.Y)
	row := s.Mask.Pix[o : o+len(dst)]
	c := s.Color
	for i, a := range row {
		switch a {
		case 0:
			dst[i] = 0
		case 255:
			dst[i] = c
		default:
			dst[i] = mul255(c, uint32(a))
		}
	}
}
