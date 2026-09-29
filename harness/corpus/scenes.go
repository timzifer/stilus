package corpus

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// SceneMarker starts a comment line naming the scene a synthetic PDF was
// written from, so the stilus engine can draw the same geometry directly.
const SceneMarker = "%stilus-scene "

// WriteScenes writes each benchmark scene as a one-page A3 PDF into dir
// (synthetic/<name>.pdf): technical-drawing content with a known structure
// that every engine, including stilus, can render.
func WriteScenes(dir string) ([]string, error) {
	var names []string
	for _, s := range scenes.All() {
		path := filepath.Join(dir, "synthetic", s.Name+".pdf")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		if err := ScenePDF(f, s); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		names = append(names, path)
	}
	return names, nil
}

// ScenePDF writes s as a single-page PDF. Scene coordinates have their
// origin at the top left, so the content is flipped into PDF space.
func ScenePDF(w io.Writer, s *scenes.Scene) error {
	var c bytes.Buffer
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 32) }
	pt := func(p stilus.Point) string { return num(float64(p.X)) + " " + num(float64(p.Y)) }
	path := func(p *stilus.Path) {
		i := 0
		for _, v := range p.Verbs {
			switch v {
			case stilus.MoveTo:
				fmt.Fprintf(&c, "%s m\n", pt(p.Points[i]))
				i++
			case stilus.LineTo:
				fmt.Fprintf(&c, "%s l\n", pt(p.Points[i]))
				i++
			case stilus.QuadTo: // as a cubic
				p0 := p.Points[i-1]
				q, e := p.Points[i], p.Points[i+1]
				c1 := stilus.Point{X: p0.X + 2*(q.X-p0.X)/3, Y: p0.Y + 2*(q.Y-p0.Y)/3}
				c2 := stilus.Point{X: e.X + 2*(q.X-e.X)/3, Y: e.Y + 2*(q.Y-e.Y)/3}
				fmt.Fprintf(&c, "%s %s %s c\n", pt(c1), pt(c2), pt(e))
				i += 2
			case stilus.CubicTo:
				fmt.Fprintf(&c, "%s %s %s c\n", pt(p.Points[i]), pt(p.Points[i+1]), pt(p.Points[i+2]))
				i += 3
			case stilus.Close:
				c.WriteString("h\n")
			}
		}
	}
	fmt.Fprintf(&c, "1 0 0 -1 0 %s cm\n", num(scenes.PageH))
	r := s.Clip
	fmt.Fprintf(&c, "%s %s %s %s re W n\n", num(r.X0), num(r.Y0), num(r.X1-r.X0), num(r.Y1-r.Y0))
	if s.Mask != nil {
		path(s.Mask)
		c.WriteString("W n\n")
	}
	alphas := map[uint8]bool{}
	for _, op := range s.Ops {
		col := op.Paint.Color // premultiplied
		a := col.A
		un := func(v uint8) string {
			if a == 0 {
				return "0"
			}
			return num(float64(v) / float64(a))
		}
		fill := "rg"
		if op.Stroke {
			fill = "RG"
		}
		fmt.Fprintf(&c, "q %s %s %s %s\n", un(col.R), un(col.G), un(col.B), fill)
		if a != 255 {
			alphas[a] = true
			fmt.Fprintf(&c, "/A%d gs\n", a)
		}
		if op.Stroke {
			st := op.Style
			fmt.Fprintf(&c, "%s w %d J %d j %s M\n", num(st.Width), st.Cap, st.Join, num(max(st.MiterLimit, 1)))
			if len(st.Dash) > 0 {
				c.WriteString("[")
				for i, d := range st.Dash {
					if i > 0 {
						c.WriteString(" ")
					}
					c.WriteString(num(d))
				}
				fmt.Fprintf(&c, "] %s d\n", num(st.DashPhase))
			}
		}
		path(op.Path)
		switch {
		case op.Stroke:
			c.WriteString("S Q\n")
		case op.Rule == stilus.EvenOdd:
			c.WriteString("f* Q\n")
		default:
			c.WriteString("f Q\n")
		}
	}

	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(c.Bytes())
	zw.Close()

	var gs bytes.Buffer
	for _, a := range slices.Sorted(maps.Keys(alphas)) {
		fmt.Fprintf(&gs, "/A%d << /CA %s /ca %s >> ", a, num(float64(a)/255), num(float64(a)/255))
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Contents 4 0 R /Resources << /ExtGState << %s>> >> >>",
			num(scenes.PageW), num(scenes.PageH), gs.String()),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n" + SceneMarker + s.Name + "\n")
	offs := make([]int, 0, 4)
	for i, o := range objs {
		offs = append(offs, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	offs = append(offs, out.Len())
	fmt.Fprintf(&out, "4 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", z.Len())
	out.Write(z.Bytes())
	out.WriteString("\nendstream\nendobj\n")
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	_, err := w.Write(out.Bytes())
	return err
}
