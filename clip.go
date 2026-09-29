package stilus

import (
	"image"
	"math"
)

// div255 returns round(v/255) for v <= 255*255.
func div255(v uint32) uint32 {
	v += 128
	return (v + v>>8) >> 8
}

// clipMask is an 8-bit coverage mask over bounds. Rows are written left to
// right by the rasterizer; only [lo, hi) of each row is valid and everything
// outside it is zero, so the mask is never cleared as a whole. [olo, ohi) is
// a fully opaque interval per row: runs inside it stay runs.
type clipMask struct {
	bounds   image.Rectangle
	stride   int
	pix      []uint8
	lo, hi   []int32
	olo, ohi []int32
}

func (m *clipMask) reset(b image.Rectangle) {
	m.bounds = b
	m.stride = b.Dx()
	if n := b.Dx() * b.Dy(); cap(m.pix) < n {
		m.pix = make([]uint8, n)
	} else {
		m.pix = m.pix[:n]
	}
	h := b.Dy()
	if cap(m.lo) < h {
		m.lo = make([]int32, h)
		m.hi = make([]int32, h)
		m.olo = make([]int32, h)
		m.ohi = make([]int32, h)
	}
	m.lo, m.hi, m.olo, m.ohi = m.lo[:h], m.hi[:h], m.olo[:h], m.ohi[:h]
	clear(m.lo)
	clear(m.hi)
	clear(m.olo)
	clear(m.ohi)
}

// maskWriter records coverage into a clipMask.
type maskWriter struct{ m *clipMask }

func (w *maskWriter) prepare(r int, x0, x1 int) []uint8 {
	m := w.m
	lo, hi := int(m.lo[r]), int(m.hi[r])
	ox := m.bounds.Min.X
	row := m.pix[r*m.stride : (r+1)*m.stride]
	if lo == hi {
		m.lo[r] = int32(x0)
	} else if x0 > hi {
		clear(row[hi-ox : x0-ox])
	}
	if x1 > hi {
		m.hi[r] = int32(x1)
	}
	return row[x0-ox : x1-ox]
}

func (w *maskWriter) BlitRun(y, x0, x1 int, alpha uint8) {
	r := y - w.m.bounds.Min.Y
	d := w.prepare(r, x0, x1)
	for i := range d {
		d[i] = alpha
	}
	if alpha == 255 && int32(x1-x0) > w.m.ohi[r]-w.m.olo[r] {
		w.m.olo[r], w.m.ohi[r] = int32(x0), int32(x1)
	}
}

func (w *maskWriter) BlitCoverage(y, x int, cov []uint8) {
	r := y - w.m.bounds.Min.Y
	copy(w.prepare(r, x, x+len(cov)), cov)
}

// maskBlitter multiplies coverage by a clip mask before passing it on.
type maskBlitter struct {
	m       *clipMask
	next    Blitter
	scratch []uint8
}

func (b *maskBlitter) buf(n int) []uint8 {
	if cap(b.scratch) < n {
		b.scratch = make([]uint8, n+n/2+64)
	}
	return b.scratch[:n]
}

func (b *maskBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	m := b.m
	r := y - m.bounds.Min.Y
	if r < 0 || r >= len(m.lo) {
		return
	}
	lo, hi := int(m.lo[r]), int(m.hi[r])
	if x0 < lo {
		x0 = lo
	}
	if x1 > hi {
		x1 = hi
	}
	if x0 >= x1 {
		return
	}
	olo, ohi := int(m.olo[r]), int(m.ohi[r])
	if olo <= x0 && x1 <= ohi {
		b.next.BlitRun(y, x0, x1, alpha)
		return
	}
	s0, s1 := max(olo, x0), min(ohi, x1)
	if s0 >= s1 {
		b.masked(r, y, x0, x1, alpha)
		return
	}
	if x0 < s0 {
		b.masked(r, y, x0, s0, alpha)
	}
	b.next.BlitRun(y, s0, s1, alpha)
	if s1 < x1 {
		b.masked(r, y, s1, x1, alpha)
	}
}

func (b *maskBlitter) masked(r, y, x0, x1 int, alpha uint8) {
	m := b.m
	row := m.pix[r*m.stride+x0-m.bounds.Min.X:]
	a := uint32(alpha)
	c := b.buf(x1 - x0)
	for i := range c {
		c[i] = uint8(div255(a * uint32(row[i])))
	}
	emitCoverage(b.next, y, x0, c)
}

func (b *maskBlitter) BlitCoverage(y, x int, cov []uint8) {
	m := b.m
	r := y - m.bounds.Min.Y
	if r < 0 || r >= len(m.lo) {
		return
	}
	lo, hi := int(m.lo[r]), int(m.hi[r])
	x0, x1 := x, x+len(cov)
	if x0 < lo {
		x0 = lo
	}
	if x1 > hi {
		x1 = hi
	}
	if x0 >= x1 {
		return
	}
	row := m.pix[r*m.stride+x0-m.bounds.Min.X:]
	cv := cov[x0-x:]
	c := b.buf(x1 - x0)
	for i := range c {
		c[i] = uint8(div255(uint32(cv[i]) * uint32(row[i])))
	}
	emitCoverage(b.next, y, x0, c)
}

// fracBlitter applies fractional coverage of a rectangle clip on its border
// rows and columns; the interior passes through untouched.
type fracBlitter struct {
	b       image.Rectangle
	f       [4]uint8 // left, top, right, bottom
	next    Blitter
	scratch []uint8
	one     [1]uint8
}

func (b *fracBlitter) rowFactor(y int) uint32 {
	f := uint32(255)
	if y == b.b.Min.Y {
		f = uint32(b.f[1])
	}
	if y == b.b.Max.Y-1 {
		f = div255(f * uint32(b.f[3]))
	}
	return f
}

func (b *fracBlitter) colFactor(x int) uint32 {
	f := uint32(255)
	if x == b.b.Min.X {
		f = uint32(b.f[0])
	}
	if x == b.b.Max.X-1 {
		f = div255(f * uint32(b.f[2]))
	}
	return f
}

func (b *fracBlitter) BlitRun(y, x0, x1 int, alpha uint8) {
	a := div255(uint32(alpha) * b.rowFactor(y))
	if a == 0 {
		return
	}
	if x0 == b.b.Min.X && b.f[0] != 255 {
		c := uint8(div255(a * b.colFactor(x0)))
		if c != 0 {
			b.one[0] = c
			b.next.BlitCoverage(y, x0, b.one[:])
		}
		x0++
	}
	last := x1 == b.b.Max.X && b.f[2] != 255 && x1 > x0
	if last {
		x1--
	}
	if x0 < x1 {
		b.next.BlitRun(y, x0, x1, uint8(a))
	}
	if last {
		if c := uint8(div255(a * b.colFactor(x1))); c != 0 {
			b.one[0] = c
			b.next.BlitCoverage(y, x1, b.one[:])
		}
	}
}

func (b *fracBlitter) BlitCoverage(y, x int, cov []uint8) {
	if cap(b.scratch) < len(cov) {
		b.scratch = make([]uint8, len(cov)+len(cov)/2+64)
	}
	c := b.scratch[:len(cov)]
	rf := b.rowFactor(y)
	for i, v := range cov {
		c[i] = uint8(div255(uint32(v) * rf))
	}
	if x == b.b.Min.X {
		c[0] = uint8(div255(uint32(c[0]) * uint32(b.f[0])))
	}
	if e := x + len(c) - 1; e == b.b.Max.X-1 {
		c[len(c)-1] = uint8(div255(uint32(c[len(c)-1]) * uint32(b.f[2])))
	}
	emitCoverage(b.next, y, x, c)
}

// clipState is one level of the clip stack.
type clipState struct {
	bounds image.Rectangle
	frac   [4]uint8 // coverage of the border column/row: left, top, right, bottom
	mask   *clipMask
}

var noFrac = [4]uint8{255, 255, 255, 255}

// snap rounds values within 1/512 px of an integer, so pixel-aligned clips
// produced by float arithmetic stay on the fast path.
func snap(v float64) float64 {
	if r := math.Round(v); math.Abs(v-r) < 1.0/512 {
		return r
	}
	return v
}

// rectSpan converts [a, b) to integer bounds plus coverage of the first and
// last cell.
func rectSpan(a, b float64) (i0, i1 int, f0, f1 uint8) {
	a, b = snap(a), snap(b)
	fa, cb := math.Floor(a), math.Ceil(b)
	i0, i1 = int(fa), int(cb)
	if i1-i0 == 1 {
		return i0, i1, toCov(b - a), 255
	}
	return i0, i1, toCov(fa + 1 - a), toCov(b - (cb - 1))
}

// intersectRect intersects a clip state with a device-space rectangle.
func (s clipState) intersectRect(r Rect) clipState {
	const lim = 1 << 30
	r.X0 = math.Max(math.Min(r.X0, lim), -lim)
	r.Y0 = math.Max(math.Min(r.Y0, lim), -lim)
	r.X1 = math.Max(math.Min(r.X1, lim), -lim)
	r.Y1 = math.Max(math.Min(r.Y1, lim), -lim)
	if r.Empty() {
		return clipState{}
	}
	x0, x1, fl, fr := rectSpan(r.X0, r.X1)
	y0, y1, ft, fb := rectSpan(r.Y0, r.Y1)
	n := s
	n.bounds = s.bounds.Intersect(image.Rect(x0, y0, x1, y1))
	if n.bounds.Empty() {
		return clipState{}
	}
	pick := func(nv, ov, rv int, of, rf uint8) uint8 {
		f := uint32(255)
		if nv == ov {
			f = uint32(of)
		}
		if nv == rv {
			f = div255(f * uint32(rf))
		}
		return uint8(f)
	}
	n.frac[0] = pick(n.bounds.Min.X, s.bounds.Min.X, x0, s.frac[0], fl)
	n.frac[1] = pick(n.bounds.Min.Y, s.bounds.Min.Y, y0, s.frac[1], ft)
	n.frac[2] = pick(n.bounds.Max.X, s.bounds.Max.X, x1, s.frac[2], fr)
	n.frac[3] = pick(n.bounds.Max.Y, s.bounds.Max.Y, y1, s.frac[3], fb)
	return n
}
