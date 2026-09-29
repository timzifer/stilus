package stilus

import "math"

// Verb is a path construction command.
type Verb uint8

const (
	MoveTo  Verb = iota // 1 point
	LineTo              // 1 point
	QuadTo              // 2 points
	CubicTo             // 3 points
	Close               // 0 points
)

// numPoints is the number of points consumed by each verb.
var numPoints = [...]int{MoveTo: 1, LineTo: 1, QuadTo: 2, CubicTo: 3, Close: 0}

// Path is a sequence of subpaths. The zero value is an empty path ready to
// use. Reset keeps the backing storage, so a Path can be rebuilt many times
// without allocating.
type Path struct {
	Verbs  []Verb
	Points []Point
}

// Reset empties the path, keeping its storage.
func (p *Path) Reset() {
	p.Verbs = p.Verbs[:0]
	p.Points = p.Points[:0]
}

// Empty reports whether the path has no drawing commands.
func (p *Path) Empty() bool { return len(p.Verbs) == 0 }

// MoveTo starts a new subpath at (x, y).
func (p *Path) MoveTo(x, y float32) {
	p.Verbs = append(p.Verbs, MoveTo)
	p.Points = append(p.Points, Point{x, y})
}

// LineTo adds a line segment to (x, y).
func (p *Path) LineTo(x, y float32) {
	p.Verbs = append(p.Verbs, LineTo)
	p.Points = append(p.Points, Point{x, y})
}

// QuadTo adds a quadratic Bézier segment.
func (p *Path) QuadTo(cx, cy, x, y float32) {
	p.Verbs = append(p.Verbs, QuadTo)
	p.Points = append(p.Points, Point{cx, cy}, Point{x, y})
}

// CubicTo adds a cubic Bézier segment.
func (p *Path) CubicTo(c1x, c1y, c2x, c2y, x, y float32) {
	p.Verbs = append(p.Verbs, CubicTo)
	p.Points = append(p.Points, Point{c1x, c1y}, Point{c2x, c2y}, Point{x, y})
}

// Close closes the current subpath.
func (p *Path) Close() { p.Verbs = append(p.Verbs, Close) }

// Rect appends a closed rectangle subpath (PDF "re").
func (p *Path) Rect(x, y, w, h float32) {
	p.MoveTo(x, y)
	p.LineTo(x+w, y)
	p.LineTo(x+w, y+h)
	p.LineTo(x, y+h)
	p.Close()
}

// kappa is the cubic control-point distance for a quarter circle.
const kappa = 0.5522847498307936

// Ellipse appends a closed ellipse centred at (cx, cy).
func (p *Path) Ellipse(cx, cy, rx, ry float32) {
	kx, ky := rx*kappa, ry*kappa
	p.MoveTo(cx+rx, cy)
	p.CubicTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubicTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubicTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubicTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
}

// Bounds returns the bounding box of all points (including control points).
func (p *Path) Bounds() Rect {
	if len(p.Points) == 0 {
		return Rect{}
	}
	r := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, q := range p.Points {
		x, y := float64(q.X), float64(q.Y)
		r.X0 = math.Min(r.X0, x)
		r.Y0 = math.Min(r.Y0, y)
		r.X1 = math.Max(r.X1, x)
		r.Y1 = math.Max(r.Y1, y)
	}
	return r
}

// asRect reports whether p is a single axis-aligned rectangle (PDF "re"
// followed by optional close), returning it in path coordinates.
func (p *Path) asRect() (Rect, bool) {
	v := p.Verbs
	if len(v) < 4 || len(v) > 6 || v[0] != MoveTo || v[1] != LineTo || v[2] != LineTo || v[3] != LineTo {
		return Rect{}, false
	}
	pts := p.Points
	n := 4
	switch {
	case len(v) == 4:
	case len(v) == 5 && v[4] == Close:
	case len(v) == 5 && v[4] == LineTo:
		n = 5
	case len(v) == 6 && v[4] == LineTo && v[5] == Close:
		n = 5
	default:
		return Rect{}, false
	}
	if len(pts) < n {
		return Rect{}, false
	}
	if n == 5 && pts[4] != pts[0] {
		return Rect{}, false
	}
	a, b, c, d := pts[0], pts[1], pts[2], pts[3]
	horizFirst := a.Y == b.Y && b.X == c.X && c.Y == d.Y && d.X == a.X
	vertFirst := a.X == b.X && b.Y == c.Y && c.X == d.X && d.Y == a.Y
	if !horizFirst && !vertFirst {
		return Rect{}, false
	}
	r := Rect{
		math.Min(float64(a.X), float64(c.X)), math.Min(float64(a.Y), float64(c.Y)),
		math.Max(float64(a.X), float64(c.X)), math.Max(float64(a.Y), float64(c.Y)),
	}
	return r, true
}
