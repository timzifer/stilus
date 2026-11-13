package stilus_test

import (
	"image/color"
	"math"
	"math/rand"
	"testing"

	"github.com/timzifer/stilus"
)

func edgeAt(e stilus.MeshEdge, yc float64) float64 {
	if math.IsInf(e.X, 0) {
		return e.X
	}
	return e.X + (yc-e.Y)*e.Slope
}

// formMesh returns the colour of a MeshForm at device pixel (x, y) and
// whether a triangle contains it.
func formMesh(f stilus.MeshForm, x, y int) (uint32, bool) {
	for i := f.Len() - 1; i >= 0; i-- {
		t := f.Triangle(i)
		if y < t.R0 || y >= t.R1 {
			continue
		}
		yc := float64(y) + 0.5
		lo := max(float64(t.C0), edgeAt(t.L[0], yc), edgeAt(t.L[1], yc))
		hi := min(float64(t.C1), edgeAt(t.R[0], yc), edgeAt(t.R[1], yc))
		if x < int(math.Ceil(lo-0.5)) || x >= int(math.Ceil(hi-0.5)) {
			continue
		}
		var v [4]int
		for c := range t.N {
			w := t.V[c] + t.GX[c]*(float64(x)+0.5-t.X0) + t.GY[c]*(yc-t.Y0)
			v[c] = int(math.Floor(w + 0.5))
		}
		var col uint32
		if t.N == 1 {
			r := f.Ramp()
			col = r[min(max(v[0], 0), len(r)-1)]
		} else {
			a := min(max(v[3], 0), 255)
			ch := func(k int) uint8 { return uint8(min(max(v[k], 0), 255, a)) }
			col = stilus.PackRGBA(color.RGBA{ch(0), ch(1), ch(2), uint8(a)})
		}
		if f.Alpha() != 255 {
			col = mulc(col, f.Alpha())
		}
		return col, true
	}
	return 0, false
}

// TestMeshForm: the pixels a MeshForm's triangles contain are those the
// shader paints, and their colours within 1 (the shader's fixed point),
// for colour and ramp meshes with overlapping, flat and thin triangles.
func TestMeshForm(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	ramp := make(stilus.Ramp, 256)
	for i := range ramp {
		ramp[i] = stilus.PackRGBA(color.RGBA{uint8(i), uint8(255 - i), 40, 255})
	}
	dst := make([]uint32, 140)
	var pix, covered int
	for c := 0; c < 60; c++ {
		tris := make([]stilus.MeshTriangle, 0, 30)
		for k := 0; k < 30; k++ {
			var tri stilus.MeshTriangle
			cx, cy := rng.Float64()*120, rng.Float64()*90
			for v := range tri {
				a := uint8(rng.Intn(256))
				tri[v] = stilus.MeshVertex{
					X: float32(cx + (rng.Float64()-0.5)*50), Y: float32(cy + (rng.Float64()-0.5)*40),
					C: stilus.PackRGBA(color.RGBA{uint8(rng.Intn(int(a) + 1)), uint8(rng.Intn(int(a) + 1)), 0, a}),
					T: float32(rng.Float64()),
				}
			}
			if k%7 == 0 {
				tri[1].C, tri[2].C = tri[0].C, tri[0].C // flat
			}
			tris = append(tris, tri)
		}
		var rp stilus.Ramp
		if c%2 == 1 {
			rp = ramp
		}
		var sh stilus.MeshShader
		sh.Alpha = []uint8{255, 180}[c%2]
		m := stilus.Matrix{1 + rng.Float64(), rng.Float64() * 0.3, -rng.Float64() * 0.3, 1 + rng.Float64(), 3, 2}
		if !sh.Set(tris, m, rp) {
			t.Fatal("Set")
		}
		f, ok := sh.Form()
		if !ok {
			t.Fatal("no form")
		}
		for y := -2; y < 200; y += 3 {
			sh.ShadeSpan(y, -5, dst)
			for i, want := range dst {
				got, in := formMesh(f, i-5, y)
				pix++
				if !in {
					if want != 0 {
						t.Fatalf("case %d (%d,%d): no triangle, shader %08x", c, i-5, y, want)
					}
					continue
				}
				covered++
				lim := 1
				if rp != nil {
					lim = 2 // an index one off: one ramp step, times alpha
				}
				if d := chanDiff(got, want); d > lim {
					t.Fatalf("case %d (%d,%d): form %08x, shader %08x", c, i-5, y, got, want)
				}
			}
		}
	}
	t.Logf("%d pixels, %d in a triangle", pix, covered)
	var empty stilus.MeshShader
	if _, ok := empty.Form(); ok {
		t.Fatal("unset shader has a form")
	}
}
