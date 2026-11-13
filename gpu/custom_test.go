package gpu_test

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/gpu"
)

// typeRuns counts the runs of TestShaderTypes.
var typeRuns int

// stripes paints diagonal stripes of two colours, period pixels wide.
type stripes struct {
	a, b   color.RGBA // premultiplied
	period int
	typ    *gpu.ShaderType
}

const stripesWGSL = `
fn stripes(p: vec2<f32>, rec: u32) -> vec4<f32> {
    let x = i32(floor(p.x));
    let y = i32(floor(p.y));
    let period = i32(rword(rec + 2u));
    var c = rword(rec);
    if (((x + y) / period) % 2 != 0) {
        c = rword(rec + 1u);
    }
    return unpack4x8unorm(c);
}
`

func (s *stripes) ShadeSpan(y, x int, dst []uint32) {
	for i := range dst {
		c := s.a
		if (x+i+y)/s.period%2 != 0 {
			c = s.b
		}
		dst[i] = stilus.PackRGBA(c)
	}
}

func (s *stripes) Type(d *gpu.Device) *gpu.ShaderType { return s.typ }
func (s *stripes) Samplers() []stilus.SamplerForm     { return nil }
func (s *stripes) AppendRecord(dst []byte, r *gpu.Record) []byte {
	word := func(c color.RGBA) uint32 {
		return uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16 | uint32(c.A)<<24
	}
	dst = binary.LittleEndian.AppendUint32(dst, word(s.a))
	dst = binary.LittleEndian.AppendUint32(dst, word(s.b))
	return binary.LittleEndian.AppendUint32(dst, uint32(s.period))
}

// tinted samples a texture and multiplies it by a tint, channel by
// channel, rounded as stilus multiplies.
type tinted struct {
	smp  stilus.Sampler
	tint color.RGBA
	typ  *gpu.ShaderType
}

const tintedWGSL = `
fn tinted(p: vec2<f32>, rec: u32) -> vec4<f32> {
    let c = sampleAt(rec + 1u, p);
    let k = rword(rec);
    var m = 0u;
    for (var s = 0u; s < 32u; s = s + 8u) {
        m = m | (div255(((c >> s) & 255u) * ((k >> s) & 255u)) << s);
    }
    return unpack4x8unorm(m);
}
`

func (s *tinted) ShadeSpan(y, x int, dst []uint32) {
	s.smp.Sample(y, x, dst)
	k := s.tint
	mul := func(v, a uint8) uint8 {
		t := uint32(v)*uint32(a) + 128
		return uint8((t + t>>8) >> 8)
	}
	for i, v := range dst {
		c := stilus.UnpackRGBA(v)
		dst[i] = stilus.PackRGBA(color.RGBA{mul(c.R, k.R), mul(c.G, k.G), mul(c.B, k.B), mul(c.A, k.A)})
	}
}

func (s *tinted) Type(d *gpu.Device) *gpu.ShaderType { return s.typ }
func (s *tinted) Samplers() []stilus.SamplerForm {
	f, _ := s.smp.Form()
	return []stilus.SamplerForm{f}
}
func (s *tinted) AppendRecord(dst []byte, r *gpu.Record) []byte {
	k := s.tint
	dst = binary.LittleEndian.AppendUint32(dst, uint32(k.R)|uint32(k.G)<<8|uint32(k.B)<<16|uint32(k.A)<<24)
	return r.AppendSampler(dst, 0)
}

// TestShaderTypes draws callers' shaders, a procedural one and one that
// samples a texture, as fills, strokes and through recorders, against
// their ShadeSpan on the CPU canvas; a shader without a type on the
// device is shaded on the CPU; bad types are refused.
func TestShaderTypes(t *testing.T) {
	d := device(t)
	// The test device outlives a run of the test (-count): its types get
	// names of their own each time.
	typeRuns++
	name := func(s string) string { return fmt.Sprintf("%s_%d", s, typeRuns) }
	src := func(wgsl, s string) string { return strings.Replace(wgsl, "fn "+s+"(", "fn "+name(s)+"(", 1) }
	st, err := d.NewShaderType(name("stripes"), src(stripesWGSL, "stripes"))
	if err != nil {
		t.Fatal(err)
	}
	tt, err := d.NewShaderType(name("tinted"), src(tintedWGSL, "tinted"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.NewShaderType(name("stripes"), src(stripesWGSL, "stripes")); err == nil {
		t.Error("a type registered twice")
	}
	if _, err := d.NewShaderType(name("broken"), "fn "+name("broken")+"(p: vec2<f32>, rec: u32) -> vec4<f32> { return nope; }"); err == nil {
		t.Error("a type that does not compile")
	} else if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error does not name the type: %v", err)
	}
	const n = 40
	pix := make([]uint32, n*n)
	for i := range pix {
		pix[i] = stilus.PackRGBA(color.RGBA{uint8(i * 7), uint8(i * 3), 200, 255})
	}
	tex := stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: pix})
	sh1 := &stripes{a: color.RGBA{200, 30, 0, 255}, b: color.RGBA{0, 0, 90, 160}, period: 7, typ: st}
	// A tint whose alpha is at least its channels keeps the colours
	// premultiplied.
	sh2 := &tinted{tint: color.RGBA{200, 160, 120, 230}, typ: tt}
	sh2.smp.Setup(tex, stilus.Scale(2.3, 2.3).Mul(stilus.Translate(90, 10)), true)
	cpuOnly := &stripes{a: color.RGBA{0, 120, 0, 255}, b: color.RGBA{0, 0, 0, 0}, period: 5}
	const w, h = 220, 140
	ops := func(c *stilus.Canvas) {
		var e stilus.Path
		e.Ellipse(50, 60, 45, 40)
		c.Fill(&e, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: sh1})
		var r stilus.Path
		r.Rect(92, 12, 88, 88)
		c.Fill(&r, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: sh2})
		var l stilus.Path
		l.MoveTo(10, 135)
		l.LineTo(200, 100)
		c.Stroke(&l, stilus.Identity, &stilus.StrokeStyle{Width: 4}, &stilus.Paint{Shader: sh2})
		var q stilus.Path
		q.Rect(185, 10, 30, 120)
		c.Fill(&q, stilus.Identity, stilus.NonZero, &stilus.Paint{Shader: cpuOnly})
	}
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(want, white)
	ops(stilus.NewCanvas(want))
	tg, err := d.NewTarget(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	check := func(name string, draw func()) {
		t.Helper()
		got := image.NewRGBA(want.Rect)
		tg.Clear(white)
		draw()
		if err := tg.ReadRGBA(got); err != nil {
			t.Fatal(err)
		}
		if tg.Err() != nil {
			t.Fatal(tg.Err())
		}
		mx, mean := deviation(want.Pix, got.Pix)
		t.Logf("%s: max %d/255, mean %.4f/255", name, mx, mean)
		if mx > 2 {
			t.Errorf("%s: max %d/255, mean %.4f/255", name, mx, mean)
		}
	}
	check("target", func() {
		var c stilus.Canvas
		c.ResetCompositor(tg, tg.Bounds())
		ops(&c)
	})
	var recs []*gpu.Recorder
	for range 3 {
		r, _ := tg.NewRecorder()
		recs = append(recs, r)
	}
	check("recorders", func() {
		for i, r := range recs {
			var c stilus.Canvas
			c.ResetCompositor(r, image.Rect(0, h*i/3, w, h*(i+1)/3))
			ops(&c)
		}
	})
}
