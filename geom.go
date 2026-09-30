package stilus

import "math"

// Point is a 2D point. Path coordinates are stored as float32 to keep paths
// (and display lists built on top of them) compact.
type Point struct{ X, Y float32 }

// Rect is an axis-aligned rectangle [X0,X1)×[Y0,Y1).
type Rect struct{ X0, Y0, X1, Y1 float64 }

// Empty reports whether r has no area. NaN bounds count as empty.
func (r Rect) Empty() bool { return !(r.X0 < r.X1 && r.Y0 < r.Y1) }

func (r Rect) hasNaN() bool {
	return math.IsNaN(r.X0) || math.IsNaN(r.Y0) || math.IsNaN(r.X1) || math.IsNaN(r.Y1)
}

// Intersect returns the intersection of r and s.
func (r Rect) Intersect(s Rect) Rect {
	b := Rect{max(r.X0, s.X0), max(r.Y0, s.Y0), min(r.X1, s.X1), min(r.Y1, s.Y1)}
	if b.hasNaN() {
		// Preserve math.Min/Max's infinity precedence over NaN.
		return Rect{math.Max(r.X0, s.X0), math.Max(r.Y0, s.Y0), math.Min(r.X1, s.X1), math.Min(r.Y1, s.Y1)}
	}
	return b
}

// Matrix is an affine transform in PDF order [a b c d e f]:
//
//	x' = a·x + c·y + e
//	y' = b·x + d·y + f
type Matrix [6]float64

// Identity is the identity transform.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

// Scale returns a scaling transform.
func Scale(sx, sy float64) Matrix { return Matrix{sx, 0, 0, sy, 0, 0} }

// Translate returns a translation.
func Translate(tx, ty float64) Matrix { return Matrix{1, 0, 0, 1, tx, ty} }

// Rotate returns a rotation by rad radians.
func Rotate(rad float64) Matrix {
	s, c := math.Sincos(rad)
	return Matrix{c, s, -s, c, 0, 0}
}

// Mul returns the transform that applies m first and then n.
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// Apply transforms (x, y).
func (m Matrix) Apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// ApplyVec transforms the vector (x, y), ignoring translation.
func (m Matrix) ApplyVec(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y, m[1]*x + m[3]*y
}

// Det returns the determinant of the linear part.
func (m Matrix) Det() float64 { return m[0]*m[3] - m[1]*m[2] }

// Invert returns the inverse transform; ok is false if m is singular.
func (m Matrix) Invert() (inv Matrix, ok bool) {
	d := m.Det()
	if d == 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return Identity, false
	}
	id := 1 / d
	inv[0] = m[3] * id
	inv[1] = -m[1] * id
	inv[2] = -m[2] * id
	inv[3] = m[0] * id
	inv[4] = -(m[4]*inv[0] + m[5]*inv[2])
	inv[5] = -(m[4]*inv[1] + m[5]*inv[3])
	return inv, true
}

// finite reports whether all matrix entries are finite.
func (m Matrix) finite() bool {
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// axisAligned reports whether m maps axis-aligned rectangles to axis-aligned
// rectangles (no rotation or shear, or a 90° rotation).
func (m Matrix) axisAligned() bool {
	return (m[1] == 0 && m[2] == 0) || (m[0] == 0 && m[3] == 0)
}

// transformRect returns the device-space bounding box of r under m.
func (m Matrix) transformRect(r Rect) Rect {
	x0, y0 := m.Apply(r.X0, r.Y0)
	x1, y1 := m.Apply(r.X1, r.Y0)
	x2, y2 := m.Apply(r.X1, r.Y1)
	x3, y3 := m.Apply(r.X0, r.Y1)
	b := Rect{min(x0, x1, x2, x3), min(y0, y1, y2, y3), max(x0, x1, x2, x3), max(y0, y1, y2, y3)}
	if !b.hasNaN() {
		return b
	}
	// Even finite inputs can overflow into infinities and NaNs. Retain
	// math.Min/Max's original results for these exceptional corners.
	return Rect{
		math.Min(math.Min(x0, x1), math.Min(x2, x3)),
		math.Min(math.Min(y0, y1), math.Min(y2, y3)),
		math.Max(math.Max(x0, x1), math.Max(x2, x3)),
		math.Max(math.Max(y0, y1), math.Max(y2, y3)),
	}
}
