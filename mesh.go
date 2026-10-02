package stilus

import (
	"image"
	"math"
)

// MeshVertex is a vertex of a Gouraud-shaded triangle: a point and either
// a premultiplied colour or a parameter into a Ramp.
type MeshVertex struct {
	X, Y float32
	C    uint32  // premultiplied, PackRGBA layout
	T    float32 // parameter into the ramp, 0 to 1
}

// MeshTriangle is a Gouraud-shaded triangle.
type MeshTriangle [3]MeshVertex

// FillMesh draws tris, transformed by m into device space, into dst
// within region, interpolating the colours of their vertices linearly (or,
// with a non-empty ramp, their parameters, looked up in the ramp). Pixels
// are replaced, not composited: the mesh is meant to be drawn into a
// transparent layer that is then composited through a Canvas, for example
// with a LayerShader, so that the mesh is clipped and blended like any
// fill. A triangle covers the pixels whose centres lie inside it, by the
// top-left rule, without antialiasing, so that triangles sharing an edge
// leave no seam and draw no pixel twice; later triangles overwrite
// earlier ones where they overlap. MeshShader paints the same pixels
// without a layer.
func FillMesh(dst *image.RGBA, region image.Rectangle, tris []MeshTriangle, m Matrix, ramp Ramp) {
	region = region.Intersect(dst.Rect)
	if region.Empty() || !m.finite() {
		return
	}
	var t target
	t.set(dst)
	var mt meshTri
	for i := range tris {
		if !mt.set(&tris[i], m, ramp) {
			continue
		}
		y0, y1 := max(mt.r0, region.Min.Y), min(mt.r1, region.Max.Y)
		if y0 >= y1 || mt.c1 <= region.Min.X || mt.c0 >= region.Max.X {
			continue
		}
		for y := y0; y < y1; y++ {
			i0, i1 := mt.span(y)
			lo, hi := max(i0, region.Min.X), min(i1, region.Max.X)
			if lo < hi {
				mt.paint(y, lo, t.row(y, lo, hi), ramp)
			}
		}
	}
}

// MeshShader paints triangles, transformed into device space,
// interpolating their vertex colours (or ramp parameters) barycentrically,
// without a layer. Pixels covered by no triangle are transparent. Fill the
// mesh's outline, the caller's path or the clip with it through a Canvas,
// so that the outer edge is antialiased and clipped like any fill. Inside
// the mesh a pixel gets exactly the colour FillMesh gives it.
//
// Set transforms and bins the triangles once; ShadeSpan then costs the
// triangles that cross the span's row, so one shader serves every band
// of a page. After Set, ShadeSpan may be called by several workers at
// once.
type MeshShader struct {
	// Alpha multiplies the mesh's colours; 255 is opaque, 0 paints
	// nothing.
	Alpha uint8

	tris []meshTri
	box  []meshBox // the rows and columns of tris, to skip them quickly
	ramp Ramp
	// A uniform grid of buckets of 2^shift device pixels a side from
	// (gx0, gy0), gw × gh of them, lists the triangles whose bounds touch
	// each bucket in stream order, for narrow spans; bands of 2^bandShift
	// rows list them for wide spans, so that a triangle is painted once
	// per span and not once per bucket.
	gx0, gy0  int
	gw, gh    int
	shift     uint
	bandShift uint
	r1, c1    int // the end of the rows and columns of all triangles
	grid      meshBins
	band      meshBins
	ok        bool
}

// meshBox is a triangle's rows [r0, r1) and columns [c0, c1).
type meshBox struct{ r0, r1, c0, c1 int32 }

// meshBins lists triangle ids per bin: bin b holds ids[start[b]:start[b+1]].
type meshBins struct {
	start, ids []int32
}

// meshBuckets bounds the number of buckets a MeshShader bins into;
// meshBands bounds the number of bands, and meshBandIDs (or 8 per
// triangle, if more) the entries of all bands together.
const (
	meshBuckets = 4096
	meshBands   = 1 << 16
	meshBandIDs = 1 << 20
)

// Set prepares the shader for tris, which m maps to device space; with a
// non-empty ramp the vertices' T is looked up in it. It reports false for
// singular or non-finite transforms, and keeps its buffers. The shader
// keeps ramp, not tris.
func (s *MeshShader) Set(tris []MeshTriangle, m Matrix, ramp Ramp) bool {
	s.ok = false
	s.tris, s.box = s.tris[:0], s.box[:0]
	if _, ok := m.Invert(); !ok || !m.finite() {
		return false
	}
	s.ramp = ramp
	c0, r0 := math.MaxInt, math.MaxInt
	c1, r1 := math.MinInt, math.MinInt
	var mt meshTri
	for i := range tris {
		if !mt.set(&tris[i], m, ramp) {
			continue
		}
		s.tris = append(s.tris, mt)
		s.box = append(s.box, meshBox{int32(mt.r0), int32(mt.r1), int32(mt.c0), int32(mt.c1)})
		c0, c1 = min(c0, mt.c0), max(c1, mt.c1)
		r0, r1 = min(r0, mt.r0), max(r1, mt.r1)
	}
	s.ok = true
	if len(s.tris) == 0 {
		return true
	}
	// Buckets of 32 pixels, larger while there would be too many.
	w, h := int64(c1)-int64(c0), int64(r1)-int64(r0)
	shift := uint(5)
	for ((w-1)>>shift+1)*((h-1)>>shift+1) > meshBuckets {
		shift++
	}
	// Bands of 4 rows, so that few triangles are visited in a row they do
	// not reach; larger while their entries would take too much memory.
	bandShift := uint(2)
	for (h-1)>>bandShift+1 > meshBands || s.bandEntries(r0, bandShift) > max(meshBandIDs, 8*len(s.tris)) {
		bandShift++
	}
	s.gx0, s.gy0, s.c1, s.r1, s.shift, s.bandShift = c0, r0, c1, r1, shift, bandShift
	s.gw, s.gh = int((w-1)>>shift+1), int((h-1)>>shift+1)
	s.bin(&s.grid, s.gw*s.gh, false)
	s.bin(&s.band, int((h-1)>>bandShift+1), true)
	return true
}

// bin lists the triangles in n bins: grid buckets, or bands.
func (s *MeshShader) bin(b *meshBins, n int, bands bool) {
	if cap(b.start) < n+2 {
		b.start = make([]int32, n+2)
	}
	b.start = b.start[:n+2]
	clear(b.start)
	w := s.gw
	if bands {
		w = 1
	}
	// Count into start[k+2], sum, then place each id at start[k+1], which
	// leaves start[k+1] at the end of bin k.
	total := 0
	for i := range s.tris {
		bx0, bx1, by0, by1 := s.bucketsOf(&s.tris[i], bands)
		for by := by0; by <= by1; by++ {
			for bx := bx0; bx <= bx1; bx++ {
				b.start[by*w+bx+2]++
			}
		}
		total += (bx1 - bx0 + 1) * (by1 - by0 + 1)
	}
	for j := 2; j < len(b.start); j++ {
		b.start[j] += b.start[j-1]
	}
	if cap(b.ids) < total {
		b.ids = make([]int32, total)
	}
	b.ids = b.ids[:total]
	for i := range s.tris {
		bx0, bx1, by0, by1 := s.bucketsOf(&s.tris[i], bands)
		for by := by0; by <= by1; by++ {
			for bx := bx0; bx <= bx1; bx++ {
				k := by*w + bx + 1
				b.ids[b.start[k]] = int32(i)
				b.start[k]++
			}
		}
	}
}

// bandEntries returns how many entries bands of 2^shift rows from row r0
// would hold.
func (s *MeshShader) bandEntries(r0 int, shift uint) int {
	n := 0
	for i := range s.box {
		c := &s.box[i]
		n += int((int64(c.r1)-1-int64(r0))>>shift-(int64(c.r0)-int64(r0))>>shift) + 1
	}
	return n
}

// bucketsOf returns the bucket columns and rows, inclusive, that t's
// bounds touch; for bands, the rows and column 0.
func (s *MeshShader) bucketsOf(t *meshTri, bands bool) (bx0, bx1, by0, by1 int) {
	gx, gy := int64(s.gx0), int64(s.gy0)
	if bands {
		return 0, 0, int((int64(t.r0) - gy) >> s.bandShift), int((int64(t.r1) - 1 - gy) >> s.bandShift)
	}
	return int((int64(t.c0) - gx) >> s.shift), int((int64(t.c1) - 1 - gx) >> s.shift),
		int((int64(t.r0) - gy) >> s.shift), int((int64(t.r1) - 1 - gy) >> s.shift)
}

// ShadeSpan implements Shader.
func (s *MeshShader) ShadeSpan(y, x int, dst []uint32) {
	clear(dst)
	if !s.ok || len(s.tris) == 0 || s.Alpha == 0 || y < s.gy0 || y >= s.r1 {
		return
	}
	x0, x1 := max(x, s.gx0), min(x+len(dst), s.c1)
	if x0 >= x1 {
		return
	}
	gx, gy := int64(s.gx0), int64(s.gy0)
	by := int((int64(y) - gy) >> s.shift)
	bx0, bx1 := int((int64(x0)-gx)>>s.shift), int((int64(x1)-1-gx)>>s.shift)
	// Triangles are painted in stream order, so the last one that covers
	// a pixel wins, as in FillMesh.
	if bx1-bx0 >= 2 {
		s.paintBin(&s.band, int((int64(y)-gy)>>s.bandShift), y, x, x0, x1, dst)
	} else {
		for bx := bx0; bx <= bx1; bx++ {
			bl := int(max(int64(x0), gx+int64(bx)<<s.shift))
			bh := int(min(int64(x1), gx+int64(bx+1)<<s.shift))
			s.paintBin(&s.grid, by*s.gw+bx, y, x, bl, bh, dst)
		}
	}
	if s.Alpha != 255 {
		a := uint32(s.Alpha)
		for i, c := range dst[x0-x : x1-x] {
			dst[x0-x+i] = mul255(c, a)
		}
	}
}

// paintBin paints the pixels [bl, bh) of row y, a part of the span dst
// from x, with the triangles of bin k.
func (s *MeshShader) paintBin(b *meshBins, k, y, x, bl, bh int, dst []uint32) {
	y32, bl32, bh32 := int32(y), int32(bl), int32(bh)
	for _, id := range b.ids[b.start[k]:b.start[k+1]] {
		if c := s.box[id]; y32 < c.r0 || y32 >= c.r1 || c.c1 <= bl32 || c.c0 >= bh32 {
			continue
		}
		t := &s.tris[id]
		i0, i1 := t.span(y)
		if lo, hi := max(i0, bl), min(i1, bh); lo < hi {
			t.paint(y, lo, dst[lo-x:hi-x], s.ramp)
		}
	}
}

// Mesh values are stepped in 40.24 fixed point; colour bytes fit 32 bits.
const (
	meshFrac = 24
	meshOne  = 1 << meshFrac
)

// meshEdge is a non-horizontal edge of a triangle in device space. It
// crosses the row at centre yc at x + (yc − y)·slope, from its upper end,
// so that two triangles sharing the edge compute the same crossing to the
// bit. An edge at x = ±Inf with slope 0 bounds nothing.
type meshEdge struct {
	x, y, slope float64
}

func (e *meshEdge) at(yc float64) float64 { return e.x + float64((yc-e.y)*e.slope) }

// meshTri is a triangle in device space, prepared for painting row by row.
type meshTri struct {
	// A row's pixels lie between the crossings of the left edges
	// (inclusive) and the right edges (exclusive). Horizontal edges bound
	// nothing more than the rows: the top one lies at or above the first
	// row's centre, which it includes, the bottom one below the last.
	l, r   [2]meshEdge
	r0, r1 int // rows whose centres it may contain
	c0, c1 int // columns whose centres it may contain
	// In fixed point, the values at the centre of pixel (c0, r0) and
	// their steps per column and row: colour bytes or a ramp index.
	base, sx, sy [4]int64
	// The same plane in floating point, for values the fixed point cannot
	// hold: v(x, y) = val + gx·(x − x0) + gy·(y − y0).
	x0, y0      float64
	val, gx, gy [4]float64
	nv          int
	fixed       bool   // the plane fits the fixed point
	flat        bool   // one colour
	color       uint32 // the colour of a flat triangle
}

// set prepares tri, transformed by m, and reports whether it can cover a
// pixel.
func (t *meshTri) set(tri *MeshTriangle, m Matrix, ramp Ramp) bool {
	var px, py [3]float64
	for k := range 3 {
		px[k], py[k] = m.Apply(float64(tri[k].X), float64(tri[k].Y))
		if !(math.Abs(px[k]) < 1<<30 && math.Abs(py[k]) < 1<<30) {
			return false
		}
	}
	area := (px[1]-px[0])*(py[2]-py[0]) - (px[2]-px[0])*(py[1]-py[0])
	if area == 0 || math.IsNaN(area) {
		return false
	}
	// Rows and columns whose centres the triangle can contain: row
	// centres lie in [ylo, yhi).
	ylo, yhi := min(py[0], py[1], py[2]), max(py[0], py[1], py[2])
	xlo, xhi := min(px[0], px[1], px[2]), max(px[0], px[1], px[2])
	t.r0, t.r1 = int(math.Ceil(ylo-0.5)), int(math.Ceil(yhi-0.5))
	t.c0, t.c1 = int(math.Ceil(xlo-0.5)), int(math.Ceil(xhi-0.5))
	if t.r0 >= t.r1 || t.c0 >= t.c1 {
		return false
	}
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	// Edge k runs from vertex k to k+1; inside is to its left for a
	// positive area, so an edge running up bounds from the left.
	t.l = [2]meshEdge{{x: math.Inf(-1)}, {x: math.Inf(-1)}}
	t.r = [2]meshEdge{{x: math.Inf(1)}, {x: math.Inf(1)}}
	nl, nr := 0, 0
	for k := range 3 {
		ax, ay := px[k], py[k]
		bx, by := px[(k+1)%3], py[(k+1)%3]
		if ay == by {
			continue
		}
		left := sign*(by-ay) < 0
		if ay > by {
			ax, ay, bx, by = bx, by, ax, ay
		}
		e := meshEdge{ax, ay, (bx - ax) / (by - ay)}
		if left {
			t.l[nl] = e
			nl++
		} else {
			t.r[nr] = e
			nr++
		}
	}

	// Values: four colour bytes, or the ramp parameter scaled to an index.
	t.nv = 4
	var v [3][4]float64
	for k := range 3 {
		if len(ramp) > 0 {
			v[k] = [4]float64{float64(tri[k].T) * float64(len(ramp)-1)}
		} else {
			r, g, b, a := unpack(tri[k].C)
			v[k] = [4]float64{float64(r), float64(g), float64(b), float64(a)}
		}
	}
	if len(ramp) > 0 {
		t.nv = 1
	}
	ex1, ey1 := px[1]-px[0], py[1]-py[0]
	ex2, ey2 := px[2]-px[0], py[2]-py[0]
	t.x0, t.y0 = px[0], py[0]
	t.fixed, t.flat = true, true
	// Bounds that keep every partial sum of the fixed point below 2^53.
	const lim = 1 << 27
	w, h := float64(t.c1-t.c0+1), float64(t.r1-t.r0+1)
	dx, dy := float64(t.c0)+0.5-t.x0, float64(t.r0)+0.5-t.y0
	inv := 1 / area
	for c := range t.nv {
		d1, d2 := v[1][c]-v[0][c], v[2][c]-v[0][c]
		t.val[c] = v[0][c]
		t.gx[c] = (d1*ey2 - d2*ey1) * inv
		t.gy[c] = (d2*ex1 - d1*ex2) * inv
		t.flat = t.flat && d1 == 0 && d2 == 0
		t.fixed = t.fixed && math.Abs(v[0][c]) < lim && math.Abs(v[1][c]) < lim &&
			math.Abs(v[2][c]) < lim && math.Abs(t.gx[c])*w < lim && math.Abs(t.gy[c])*h < lim
		t.base[c] = int64((t.val[c] + float64(t.gx[c]*dx) + float64(t.gy[c]*dy)) * meshOne)
		t.sx[c] = int64(t.gx[c] * meshOne)
		t.sy[c] = int64(t.gy[c] * meshOne)
	}
	if t.flat && t.fixed {
		t.color = t.at(t.base[0], t.base[1], t.base[2], t.base[3], ramp)
	}
	return true
}

// span returns the pixels [i0, i1) of row y, one of the triangle's rows,
// whose centres lie inside the triangle; it is empty if none do.
func (t *meshTri) span(y int) (i0, i1 int) {
	yc := float64(y) + 0.5
	lo := max(float64(t.c0), t.l[0].at(yc), t.l[1].at(yc))
	hi := min(float64(t.c1), t.r[0].at(yc), t.r[1].at(yc))
	return int(math.Ceil(lo - 0.5)), int(math.Ceil(hi - 0.5))
}

// paint writes the colours of the pixels from lo on in row y into dst.
// A pixel's values are the plane's at its own position, exactly, so they
// do not depend on where a run starts: FillMesh and MeshShader agree, in
// any band or span.
func (t *meshTri) paint(y, lo int, dst []uint32, ramp Ramp) {
	if t.flat && t.fixed {
		fill32(dst, t.color)
		return
	}
	if !t.fixed {
		// Values too large for the fixed point: per pixel in floating
		// point, clamped as the fixed point would.
		dy := float64(y) + 0.5 - t.y0
		for i := range dst {
			dx := float64(lo+i) + 0.5 - t.x0
			var v [4]int64
			for c := range t.nv {
				v[c] = fixedOf(t.val[c] + float64(t.gx[c]*dx) + float64(t.gy[c]*dy))
			}
			dst[i] = t.at(v[0], v[1], v[2], v[3], ramp)
		}
		return
	}
	// Values are linear along the run: when both ends need no clamping
	// (and colours are premultiplied at both ends), no pixel between them
	// does. Values carry the rounding half.
	n, ny, m := int64(lo-t.c0), int64(y-t.r0), int64(len(dst)-1)
	if t.nv == 1 {
		st := t.sx[0]
		v := t.base[0] + t.sy[0]*ny + st*n + meshOne/2
		last := int64(len(ramp) - 1)
		if e := v + st*m; min(v, e) >= 0 && max(v, e)>>meshFrac <= last {
			for i := range dst {
				dst[i] = ramp[v>>meshFrac]
				v += st
			}
			return
		}
		for i := range dst {
			dst[i] = ramp[min(max(v>>meshFrac, 0), last)]
			v += st
		}
		return
	}
	sr, sg, sb, sa := t.sx[0], t.sx[1], t.sx[2], t.sx[3]
	r := t.base[0] + t.sy[0]*ny + sr*n + meshOne/2
	g := t.base[1] + t.sy[1]*ny + sg*n + meshOne/2
	b := t.base[2] + t.sy[2]*ny + sb*n + meshOne/2
	a := t.base[3] + t.sy[3]*ny + sa*n + meshOne/2
	re, ge, be, ae := r+sr*m, g+sg*m, b+sb*m, a+sa*m
	if min(r, g, b, re, ge, be) >= 0 && max(a, ae) < 256<<meshFrac &&
		r <= a && g <= a && b <= a && re <= ae && ge <= ae && be <= ae {
		// Every value fits 32 bits: two channels are stepped in one word,
		// exactly, as the low one never leaves [0, 2^32).
		rg, ba := uint64(g)<<32+uint64(r), uint64(a)<<32+uint64(b)
		srg, sba := uint64(sg)<<32+uint64(sr), uint64(sa)<<32+uint64(sb)
		for i := range dst {
			dst[i] = pack(uint8(rg>>meshFrac), uint8(rg>>(32+meshFrac)), uint8(ba>>meshFrac), uint8(ba>>(32+meshFrac)))
			rg += srg
			ba += sba
		}
		return
	}
	for i := range dst {
		ab := byteOf(a)
		dst[i] = pack(min(byteOf(r), ab), min(byteOf(g), ab), min(byteOf(b), ab), ab)
		r, g, b, a = r+sr, g+sg, b+sb, a+sa
	}
}

// fixedOf converts a value on the floating point path to fixed point,
// clamped to what it holds; NaN becomes the lowest value.
func fixedOf(v float64) int64 {
	const lim = 1 << 27
	if !(v > -lim) {
		v = -lim
	}
	return int64(min(v, lim) * meshOne)
}

// at returns the colour of fixed point values: colour bytes, or a ramp
// index in r.
func (t *meshTri) at(r, g, b, a int64, ramp Ramp) uint32 {
	r, g, b, a = r+meshOne/2, g+meshOne/2, b+meshOne/2, a+meshOne/2
	if t.nv == 1 {
		return ramp[min(max(r>>meshFrac, 0), int64(len(ramp)-1))]
	}
	ab := byteOf(a)
	return pack(min(byteOf(r), ab), min(byteOf(g), ab), min(byteOf(b), ab), ab)
}

// byteOf rounds a fixed point value that carries the rounding half to a
// byte, clamped.
func byteOf(v int64) uint8 {
	return uint8(min(max(v>>meshFrac, 0), 255))
}
