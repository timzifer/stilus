package stilus

import (
	"image"
	"math"
	"math/rand"
	"testing"
)

// gridMesh returns a mesh of nx × ny jittered quads, two triangles each,
// over (x, y, w, h), with random vertex colours and parameters.
func gridMesh(rng *rand.Rand, nx, ny int, x, y, w, h float64) []MeshTriangle {
	vs := make([]MeshVertex, (nx+1)*(ny+1))
	for j := range ny + 1 {
		for i := range nx + 1 {
			px, py := x+w*float64(i)/float64(nx), y+h*float64(j)/float64(ny)
			if i > 0 && i < nx && j > 0 && j < ny {
				px += (rng.Float64() - 0.5) * w / float64(nx) * 0.6
				py += (rng.Float64() - 0.5) * h / float64(ny) * 0.6
			}
			a := uint8(128 + rng.Intn(128))
			vs[j*(nx+1)+i] = MeshVertex{
				X: float32(px), Y: float32(py),
				C: pack(uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), uint8(rng.Intn(int(a)+1)), a),
				T: float32(rng.Float64()*1.4 - 0.2),
			}
		}
	}
	var tris []MeshTriangle
	for j := range ny {
		for i := range nx {
			a, b := vs[j*(nx+1)+i], vs[j*(nx+1)+i+1]
			c, d := vs[(j+1)*(nx+1)+i], vs[(j+1)*(nx+1)+i+1]
			tris = append(tris, MeshTriangle{a, b, d}, MeshTriangle{a, d, c})
		}
	}
	return tris
}

// shadeImage paints s into an image of r, in spans of random lengths.
func shadeImage(s Shader, r image.Rectangle, rng *rand.Rand) *image.RGBA {
	img := image.NewRGBA(r)
	var t target
	t.set(img)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; {
			n := min(1+rng.Intn(90), r.Max.X-x)
			s.ShadeSpan(y, x, t.row(y, x, x+n))
			x += n
		}
	}
	return img
}

func sameImages(t *testing.T, name string, got, want *image.RGBA) {
	t.Helper()
	for y := want.Rect.Min.Y; y < want.Rect.Max.Y; y++ {
		for x := want.Rect.Min.X; x < want.Rect.Max.X; x++ {
			if g, w := got.RGBAAt(x, y), want.RGBAAt(x, y); g != w {
				t.Fatalf("%s: pixel (%d, %d) is %v, want %v", name, x, y, g, w)
			}
		}
	}
}

// MeshShader paints every pixel as FillMesh does, in spans of any length:
// meshes with shared edges, overlapping triangles (the last wins), ramps,
// transforms, and extents that make buckets larger than 32 pixels.
func TestMeshShaderMatchesFillMesh(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var tris []MeshTriangle
	tris = append(tris, gridMesh(rng, 9, 7, 10.3, 5.7, 230, 170)...)
	// Overlapping triangles across the grid.
	for range 30 {
		var tri MeshTriangle
		for k := range tri {
			a := uint8(rng.Intn(256))
			tri[k] = MeshVertex{X: float32(rng.Float64()*300 - 20), Y: float32(rng.Float64()*220 - 10),
				C: pack(a/2, a, a/3, a), T: float32(rng.Float64())}
		}
		tris = append(tris, tri)
	}
	// A flat triangle, and one with parameters far outside the ramp.
	tris = append(tris,
		MeshTriangle{{X: 100, Y: 100, C: pack(9, 8, 7, 200), T: 0.5}, {X: 140, Y: 100, C: pack(9, 8, 7, 200), T: 0.5}, {X: 120, Y: 150, C: pack(9, 8, 7, 200), T: 0.5}},
		MeshTriangle{{X: 0, Y: 0, T: -3e9}, {X: 60, Y: 0, T: 4e9}, {X: 0, Y: 60, T: 1}},
	)
	r := image.Rect(-7, -3, 290, 230)
	for _, tc := range []struct {
		name string
		m    Matrix
		ramp Ramp
	}{
		{"identity", Identity, nil},
		{"ramp", Identity, grayRamp(97)},
		{"rotated", Rotate(0.4).Mul(Translate(60, -40)), nil},
		{"scaled ramp", Scale(0.9, 1.1).Mul(Translate(0.25, 0.75)), grayRamp(3)},
		{"tiny", Scale(0.05, 0.05), nil},
	} {
		want := image.NewRGBA(r)
		FillMesh(want, r, tris, tc.m, tc.ramp)
		var s MeshShader
		s.Alpha = 255
		if !s.Set(tris, tc.m, tc.ramp) {
			t.Fatalf("%s: set", tc.name)
		}
		sameImages(t, tc.name, shadeImage(&s, r, rng), want)
	}

	// A mesh spread over a large extent, so that buckets grow.
	big := gridMesh(rng, 40, 3, -3000, 0, 9000, 600)
	r = image.Rect(-3100, -5, 6100, 610)
	want := image.NewRGBA(r)
	FillMesh(want, r, big, Identity, nil)
	var s MeshShader
	s.Alpha = 255
	s.Set(big, Identity, nil)
	if s.shift <= 5 {
		t.Errorf("buckets of %d pixels", 1<<s.shift)
	}
	if s.gw*s.gh > meshBuckets {
		t.Errorf("%d × %d buckets", s.gw, s.gh)
	}
	sameImages(t, "large", shadeImage(&s, r, rng), want)
}

func TestMeshShaderNoSeams(t *testing.T) {
	// The fan of eight around a centre: inside, every pixel is opaque.
	var tris []MeshTriangle
	cx, cy := float32(31.3), float32(30.7)
	c := pack(10, 20, 30, 255)
	for k := range 8 {
		a0, a1 := float64(k)*math.Pi/4, float64(k+1)*math.Pi/4
		tris = append(tris, MeshTriangle{
			{X: cx, Y: cy, C: c},
			{X: cx + float32(25*math.Cos(a0)), Y: cy + float32(25*math.Sin(a0)), C: c},
			{X: cx + float32(25*math.Cos(a1)), Y: cy + float32(25*math.Sin(a1)), C: c},
		})
	}
	var s MeshShader
	s.Alpha = 255
	s.Set(tris, Identity, nil)
	img := shadeImage(&s, image.Rect(0, 0, 64, 64), rand.New(rand.NewSource(1)))
	for y := range 64 {
		for x := range 64 {
			d := math.Hypot(float64(x)+0.5-float64(cx), float64(y)+0.5-float64(cy))
			if p := img.RGBAAt(x, y); d < 22 && p != rgba(10, 20, 30, 255) {
				t.Fatalf("pixel (%d, %d) is %v", x, y, p)
			}
		}
	}
}

func TestMeshShaderAlphaAndEdges(t *testing.T) {
	tris := []MeshTriangle{
		{{X: 0, Y: 0, C: pack(200, 100, 0, 255)}, {X: 100, Y: 0, C: pack(200, 100, 0, 255)}, {X: 0, Y: 100, C: pack(200, 100, 0, 255)}},
	}
	var s MeshShader
	if !s.Set(tris, Identity, nil) {
		t.Fatal("set")
	}
	if c := shade(&s, 10, 10, 1)[0]; c.A != 0 {
		t.Errorf("alpha 0 paints %v", c)
	}
	s.Alpha = 128
	if c := shade(&s, 10, 10, 1)[0]; c != rgba(100, 50, 0, 128) {
		t.Errorf("alpha 128: %v", c)
	}
	// Outside the triangle and the grid: transparent, also over garbage.
	dst := []uint32{1, 2, 3, 4, 5, 6}
	s.ShadeSpan(-5, 0, dst[:2])
	s.ShadeSpan(99, 98, dst[2:])
	s.ShadeSpan(10, 1000, dst[4:])
	for i, c := range dst {
		if c != 0 {
			t.Errorf("pixel %d: %v", i, UnpackRGBA(c))
		}
	}
	if s.Set(tris, Scale(0, 1), nil) {
		t.Error("singular transform accepted")
	}
	if c := shade(&s, 10, 10, 1)[0]; c.A != 0 {
		t.Error("unset shader paints")
	}
	if !s.Set(nil, Identity, nil) {
		t.Error("an empty mesh is drawable")
	}
	if c := shade(&s, 10, 10, 1)[0]; c.A != 0 {
		t.Error("empty mesh paints")
	}
}

// Drawn through a Canvas over the mesh's outline, the shader gives what a
// layer from FillMesh gives through a LayerShader.
func TestMeshShaderCanvas(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	tris := gridMesh(rng, 6, 5, 20.5, 10.25, 200, 150)
	m := Rotate(0.1).Mul(Translate(15, 5))
	var outline Path
	outline.Rect(20.5, 10.25, 200, 150)
	r := image.Rect(0, 0, 260, 220)

	want := image.NewRGBA(r)
	layer := image.NewRGBA(r)
	FillMesh(layer, r, tris, m, nil)
	NewCanvas(want).Fill(&outline, m, NonZero, &Paint{Shader: &LayerShader{Src: layer, Alpha: 255}})

	got := image.NewRGBA(r)
	var s MeshShader
	s.Alpha = 255
	s.Set(tris, m, nil)
	NewCanvas(got).Fill(&outline, m, NonZero, &Paint{Shader: &s})
	sameImages(t, "canvas", got, want)
}

// The floating point path, for planes the fixed point cannot hold, gives
// the colours of the fixed point path within rounding.
func TestMeshFloatPath(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for _, ramp := range []Ramp{nil, grayRamp(200)} {
		for _, tri := range gridMesh(rng, 4, 4, 3.3, 1.9, 90, 70) {
			var mt meshTri
			if !mt.set(&tri, Identity, ramp) || !mt.fixed {
				continue
			}
			for y := mt.r0; y < mt.r1; y++ {
				i0, i1 := mt.span(y)
				if i0 >= i1 {
					continue
				}
				fixed := make([]uint32, i1-i0)
				mt.paint(y, i0, fixed, ramp)
				mt.fixed = false
				float := make([]uint32, i1-i0)
				mt.paint(y, i0, float, ramp)
				mt.fixed = true
				for i := range fixed {
					if !nearColor(fixed[i], float[i], 1) {
						t.Fatalf("pixel (%d, %d): %v fixed, %v float", i0+i, y, UnpackRGBA(fixed[i]), UnpackRGBA(float[i]))
					}
				}
			}
		}
	}
}

func TestMeshShaderNoAllocs(t *testing.T) {
	tris := gridMesh(rand.New(rand.NewSource(5)), 12, 12, 0, 0, 400, 300)
	var s MeshShader
	s.Alpha = 200
	s.Set(tris, Identity, grayRamp(64))
	dst := make([]uint32, 400)
	allocs := testing.AllocsPerRun(10, func() {
		s.Set(tris, Identity, grayRamp(64)[:64:64])
		s.Set(tris, Identity, nil)
		for y := range 300 {
			s.ShadeSpan(y, 0, dst)
		}
	})
	if allocs > 1 { // grayRamp
		t.Errorf("%v allocations", allocs)
	}
}

// A full-page mesh, drawn in bands of 64 rows as a renderer with workers
// does: through a band-sized layer (FillMesh, then LayerShader) and with
// one MeshShader for all bands. Large translucent triangles, and small
// opaque ones as a tessellated patch mesh gives.
func BenchmarkMesh(b *testing.B) {
	const band = 64
	page := image.Rect(0, 0, 1000, 1000)
	var outline Path
	outline.Rect(0, 0, 1000, 1000)
	dst := image.NewRGBA(page)
	c := NewCanvas(dst)
	opaque := gridMesh(rand.New(rand.NewSource(9)), 128, 128, 0, 0, 1000, 1000)
	for i := range opaque {
		for k := range opaque[i] {
			cr, cg, cb, _ := unpack(opaque[i][k].C)
			opaque[i][k].C = pack(cr, cg, cb, 255)
		}
	}
	for _, mesh := range []struct {
		name string
		tris []MeshTriangle
		ramp Ramp
	}{
		{"32x32-translucent", gridMesh(rand.New(rand.NewSource(9)), 32, 32, 0, 0, 1000, 1000), nil},
		{"128x128-opaque", opaque, nil},
		{"128x128-ramp", opaque, grayRamp(256)},
	} {
		b.Run(mesh.name+"/layer", func(b *testing.B) {
			layer := image.NewRGBA(image.Rect(0, 0, 1000, band))
			ls := &LayerShader{Alpha: 255}
			paint := &Paint{Shader: ls}
			for b.Loop() {
				for y := 0; y < 1000; y += band {
					r := image.Rect(0, y, 1000, y+band)
					layer.Rect = r
					clear(layer.Pix)
					FillMesh(layer, r, mesh.tris, Identity, mesh.ramp)
					ls.Src = layer
					c.Reset(dst, r)
					c.Fill(&outline, Identity, NonZero, paint)
				}
			}
		})
		b.Run(mesh.name+"/shader", func(b *testing.B) {
			var s MeshShader
			s.Alpha = 255
			paint := &Paint{Shader: &s}
			for b.Loop() {
				s.Set(mesh.tris, Identity, mesh.ramp)
				for y := 0; y < 1000; y += band {
					c.Reset(dst, image.Rect(0, y, 1000, y+band))
					c.Fill(&outline, Identity, NonZero, paint)
				}
			}
		})
	}
}
