package gpu

import (
	"encoding/binary"
	"math"
	"sync"

	"github.com/timzifer/stilus"
)

// Mesh records (spans.wgsl, meshColor), in words:
//
//	0       meshKind | meshRamp (ramp indices rather than colours)
//	1       Alpha
//	2, 3    int32 origin: the mesh's bounds' minimum
//	4, 5, 6 the grid: cell size 2^shift, gw × gh cells
//	7…11    offsets relative to the record: cell starts (gw·gh + 1),
//	        triangle ids per cell, triangles, ramp; the ramp's length
//	12…     those arrays
//
// A triangle takes meshTriWords: int32 R0, R1, C0, C1 and float32 edges
// L[0], L[1], R[0], R[1] (X, Y, Slope), X0, Y0, V, GX, GY, all relative to
// the origin, so that float32 holds small numbers. A cell lists the
// triangles whose rows and columns reach it, in stream order.
const (
	meshKind = 1 << 30
	meshRamp = 1 << 0

	meshHead     = 12
	meshTriWords = 4 + 12 + 2 + 12
	meshCells    = 4096
)

// meshCacheMax bounds the bytes of the mesh records a target keeps.
const meshCacheMax = 32 << 20

// meshCacheOff makes every operation its mesh's record (benchmarks).
var meshCacheOff = false

// meshCache holds the mesh records of a target and its recorders, by the
// generation of the mesh's triangles: a mesh is prepared once for every
// band of a page that draws it, and again only after its shader's next
// Set.
type meshCache struct {
	mu     sync.Mutex
	recs   map[uint64][]byte
	bytes  int
	builds int // records made, for tests
}

// meshBlitter returns the blitter of a mesh shader the GPU shades.
func (t *Target) meshBlitter(s *stilus.MeshShader) stilus.Blitter {
	f, ok := s.Form()
	if !ok {
		return nothing{}
	}
	c := t.meshes
	c.mu.Lock()
	rec, ok := c.recs[f.Gen()]
	if meshCacheOff {
		rec, ok = t.meshRecord(nil, f), true
	}
	if !ok {
		rec = t.meshRecord(nil, f)
		if c.bytes+len(rec) > meshCacheMax {
			clear(c.recs)
			c.bytes = 0
		}
		c.recs[f.Gen()] = rec
		c.bytes += len(rec)
		c.builds++
	}
	c.mu.Unlock()
	if len(rec) > t.limit/4 {
		return nil
	}
	b := &t.grad
	b.recOK, b.img = false, nil
	b.rec = append(b.rec[:0], rec...)
	// Alpha is the shader's now, not its Gen's.
	binary.LittleEndian.PutUint32(b.rec[4:], uint32(f.Alpha()))
	return b
}

// meshRecord appends the record of f.
func (t *Target) meshRecord(r []byte, f stilus.MeshForm) []byte {
	bb := f.Bounds()
	w, h := bb.Dx(), bb.Dy()
	shift := uint(4)
	for ((w-1)>>shift+1)*((h-1)>>shift+1) > meshCells {
		shift++
	}
	gw, gh := (w-1)>>shift+1, (h-1)>>shift+1
	n := f.Len()
	// Count, then fill, the triangles of each cell.
	cells := gw * gh
	if cap(t.meshStart) < cells+1 {
		t.meshStart = make([]int32, cells+1)
	}
	start := t.meshStart[:cells+1]
	clear(start)
	cellsOf := func(tr *stilus.MeshFormTriangle) (int, int, int, int) {
		x0 := max(tr.C0-bb.Min.X, 0) >> shift
		x1 := min(tr.C1-bb.Min.X-1, w-1) >> shift
		y0 := max(tr.R0-bb.Min.Y, 0) >> shift
		y1 := min(tr.R1-bb.Min.Y-1, h-1) >> shift
		return x0, x1, y0, y1
	}
	for i := range n {
		tr := f.Triangle(i)
		x0, x1, y0, y1 := cellsOf(&tr)
		for cy := y0; cy <= y1; cy++ {
			for cx := x0; cx <= x1; cx++ {
				start[cy*gw+cx+1]++
			}
		}
	}
	for i := 1; i <= cells; i++ {
		start[i] += start[i-1]
	}
	total := int(start[cells])
	if cap(t.meshIDs) < total {
		t.meshIDs = make([]int32, total)
	}
	ids := t.meshIDs[:total]
	if cap(t.meshFill) < cells {
		t.meshFill = make([]int32, cells)
	}
	fill := t.meshFill[:cells]
	copy(fill, start[:cells])
	for i := range n {
		tr := f.Triangle(i)
		x0, x1, y0, y1 := cellsOf(&tr)
		for cy := y0; cy <= y1; cy++ {
			for cx := x0; cx <= x1; cx++ {
				k := cy*gw + cx
				ids[fill[k]] = int32(i)
				fill[k]++
			}
		}
	}
	ramp := f.Ramp()
	flags := uint32(meshKind)
	if ramp != nil {
		flags |= meshRamp
	}
	ox, oy := bb.Min.X, bb.Min.Y
	starts := meshHead
	idsOff := starts + cells + 1
	trisOff := idsOff + total
	rampOff := trisOff + n*meshTriWords
	put := func(v uint32) { r = binary.LittleEndian.AppendUint32(r, v) }
	puti := func(v int) { put(uint32(int32(v))) }
	putf := func(v float64) { put(math.Float32bits(float32(v))) }
	put(flags)
	put(uint32(f.Alpha()))
	puti(ox)
	puti(oy)
	put(uint32(shift))
	put(uint32(gw))
	put(uint32(gh))
	put(uint32(starts))
	put(uint32(idsOff))
	put(uint32(trisOff))
	put(uint32(rampOff))
	put(uint32(len(ramp)))
	for _, v := range start {
		put(uint32(v))
	}
	for _, v := range ids {
		put(uint32(v))
	}
	edge := func(e stilus.MeshEdge, left bool) {
		if math.IsInf(e.X, 0) {
			// Bounds nothing: far out on its side.
			x := 1e30
			if left {
				x = -1e30
			}
			putf(x)
			putf(0)
			putf(0)
			return
		}
		putf(e.X - float64(ox))
		putf(e.Y - float64(oy))
		putf(e.Slope)
	}
	for i := range n {
		tr := f.Triangle(i)
		puti(tr.R0 - oy)
		puti(tr.R1 - oy)
		puti(tr.C0 - ox)
		puti(tr.C1 - ox)
		edge(tr.L[0], true)
		edge(tr.L[1], true)
		edge(tr.R[0], false)
		edge(tr.R[1], false)
		putf(tr.X0 - float64(ox))
		putf(tr.Y0 - float64(oy))
		for _, a := range [3][4]float64{tr.V, tr.GX, tr.GY} {
			for _, v := range a {
				putf(v)
			}
		}
	}
	for _, c := range ramp {
		put(rgba(c))
	}
	return r
}
