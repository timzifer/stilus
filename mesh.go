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
// earlier ones where they overlap.
func FillMesh(dst *image.RGBA, region image.Rectangle, tris []MeshTriangle, m Matrix, ramp Ramp) {
	region = region.Intersect(dst.Rect)
	if region.Empty() || !m.finite() {
		return
	}
	var t target
	t.set(dst)
	for i := range tris {
		fillTriangle(&t, region, &tris[i], m, ramp)
	}
}

// meshValues returns the four bytes of the colour of v, or its ramp
// parameter, as floats.
func meshValues(v *MeshVertex, ramp Ramp) [4]float64 {
	if len(ramp) > 0 {
		return [4]float64{float64(v.T)}
	}
	r, g, b, a := unpack(v.C)
	return [4]float64{float64(r), float64(g), float64(b), float64(a)}
}

func fillTriangle(t *target, region image.Rectangle, tri *MeshTriangle, m Matrix, ramp Ramp) {
	var px, py [3]float64
	for k := range 3 {
		px[k], py[k] = m.Apply(float64(tri[k].X), float64(tri[k].Y))
		if !(math.Abs(px[k]) < 1<<30 && math.Abs(py[k]) < 1<<30) {
			return
		}
	}
	area := (px[1]-px[0])*(py[2]-py[0]) - (px[2]-px[0])*(py[1]-py[0])
	if area == 0 || math.IsNaN(area) {
		return
	}
	// Rows whose centres the triangle can contain.
	ylo, yhi := min(py[0], py[1], py[2]), max(py[0], py[1], py[2])
	y0 := max(int(math.Ceil(ylo-0.5)), region.Min.Y)
	y1 := min(int(math.Ceil(yhi-0.5)), region.Max.Y)
	xlo, xhi := min(px[0], px[1], px[2]), max(px[0], px[1], px[2])
	if y0 >= y1 || xhi < float64(region.Min.X) || xlo > float64(region.Max.X) {
		return
	}
	// The plane of each value: v(x, y) = v0 + gx·(x−x0) + gy·(y−y0).
	var val, gx, gy [4]float64
	v0, v1, v2 := meshValues(&tri[0], ramp), meshValues(&tri[1], ramp), meshValues(&tri[2], ramp)
	ex1, ey1 := px[1]-px[0], py[1]-py[0]
	ex2, ey2 := px[2]-px[0], py[2]-py[0]
	nv := 4
	if len(ramp) > 0 {
		nv = 1
	}
	for c := range nv {
		d1, d2 := v1[c]-v0[c], v2[c]-v0[c]
		gx[c] = (d1*ey2 - d2*ey1) / area
		gy[c] = (d2*ex1 - d1*ex2) / area
		val[c] = v0[c]
	}
	// Edge k runs from vertex k to k+1; inside is to its left for a
	// positive area. Its x crossing at a row bounds the span from the left
	// or from the right.
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	for y := y0; y < y1; y++ {
		yc := float64(y) + 0.5
		lo, hi := float64(region.Min.X), float64(region.Max.X)
		ok := true
		for k := range 3 {
			ax, ay := px[k], py[k]
			bx, by := px[(k+1)%3], py[(k+1)%3]
			// Inside: sign·((b−a) × (p−a)) ≥ 0, linear in x:
			// A·x + B ≥ 0 with A = −sign·(by−ay).
			A := -sign * (by - ay)
			B := sign * ((bx-ax)*(yc-ay) + (by-ay)*ax)
			switch {
			case A > 0:
				lo = max(lo, -B/A) // left edge: inclusive
			case A < 0:
				hi = min(hi, -B/A) // right edge: exclusive
			default:
				// A horizontal edge: the row is inside or not; the top
				// edge (inside below it) is inclusive.
				if B < 0 || (B == 0 && sign*(bx-ax) < 0) {
					ok = false
				}
			}
		}
		if !ok {
			continue
		}
		i0 := max(int(math.Ceil(lo-0.5)), region.Min.X)
		i1 := min(int(math.Ceil(hi-0.5)), region.Max.X)
		if i0 >= i1 {
			continue
		}
		row := t.row(y, i0, i1)
		dy := yc - py[0]
		dx := float64(i0) + 0.5 - px[0]
		if len(ramp) > 0 {
			v := val[0] + gx[0]*dx + gy[0]*dy
			for i := range row {
				row[i] = ramp.At(v + gx[0]*float64(i))
			}
			continue
		}
		var s [4]float64
		for c := range 4 {
			s[c] = val[c] + gx[c]*dx + gy[c]*dy
		}
		for i := range row {
			fi := float64(i)
			a := clampByte(s[3] + gx[3]*fi)
			row[i] = pack(
				min(clampByte(s[0]+gx[0]*fi), a),
				min(clampByte(s[1]+gx[1]*fi), a),
				min(clampByte(s[2]+gx[2]*fi), a),
				a)
		}
	}
}

func clampByte(v float64) uint8 {
	if !(v > 0) {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
