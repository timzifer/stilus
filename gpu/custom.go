package gpu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/timzifer/stilus"
)

// ShaderType is a caller's shading function in WGSL, registered on a
// device (ADR 0018). Spans painted with a Shader of this type are coloured
// on the GPU by it.
type ShaderType struct {
	d    *Device
	id   uint32
	name string
	src  string
}

// Name returns the WGSL function's name.
func (s *ShaderType) Name() string { return s.name }

// Shader is a caller's shader that a target colours on the GPU: on a
// stilus canvas drawing onto an image it is a stilus.Shader like any, and
// its ShadeSpan must paint what its WGSL computes.
type Shader interface {
	stilus.Shader
	// Type returns the shader's type on d, nil when it has none there;
	// the target then shades it on the CPU.
	Type(d *Device) *ShaderType
	// Samplers returns the textures the shader samples, nil for none.
	// Their planes are made room for in the target's store before
	// AppendRecord is called.
	Samplers() []stilus.SamplerForm
	// AppendRecord appends the shader's record for one operation, what
	// its WGSL reads from rec on: with r.AppendSampler, the blocks its
	// WGSL passes to sampleAt.
	AppendRecord(dst []byte, r *Record) []byte
}

// Record is what a shader's AppendRecord is given: the operation's origin,
// the first pixel it draws, and its samplers.
type Record struct {
	// X, Y is the origin, the first pixel the operation draws.
	X, Y int
	t    *Target
	smp  []stilus.SamplerForm
}

// SamplerWords is the size, in words, of a sampler block.
const SamplerWords = samplerWords

// AppendSampler appends the block of the shader's sampler i, for the
// WGSL's sampleAt(q, p) with q the block's word index (rec + its offset
// in words): its plane in the store, its transform from the origin.
func (r *Record) AppendSampler(dst []byte, i int) []byte {
	s := &r.smp[i]
	off := r.t.plane(s.Plane)
	return appendSampler(dst, s, off, r.X, r.Y)
}

// The record of a span coloured by a caller's shader: word 0 is
// customKind | the type's id, the caller's record follows at word 1.
const customKind = 1 << 26

// wgslName matches a WGSL identifier.
var wgslName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewShaderType registers wgsl on d, the source of the function
//
//	fn name(p: vec2<f32>, rec: u32) -> vec4<f32>
//
// and of what it calls, which returns the premultiplied colour at the
// pixel centre p (device pixels) from its record, starting at word rec
// of the data. It may use the helpers of the span shader: rword(i) and
// rfloat(i) read word i, sampleAt(q, p) samples the sampler block at
// word q (a packed RGBA8 colour, R in the low byte), ch(c, i), pack4,
// mul8 and lerp8 take colours apart and mix them, unpack4x8unorm makes a
// colour of a word. Its names must not clash with the span shader's.
//
// The span shader is compiled again with every type: register types
// before drawing, not while targets record.
func (d *Device) NewShaderType(name, wgsl string) (*ShaderType, error) {
	if !wgslName.MatchString(name) {
		return nil, fmt.Errorf("gpu: shader type name %q", name)
	}
	for _, t := range d.types {
		if t.name == name {
			return nil, fmt.Errorf("gpu: shader type %q exists", name)
		}
	}
	if len(d.types) >= 1<<16 {
		return nil, errors.New("gpu: too many shader types")
	}
	st := &ShaderType{d: d, id: uint32(len(d.types)), name: name, src: wgsl}
	d.types = append(d.types, st)
	shader, pipeline, err := d.buildPipeline(d.spansSource())
	if err != nil {
		d.types = d.types[:len(d.types)-1]
		return nil, fmt.Errorf("gpu: shader type %q: %w", name, err)
	}
	old, oldPipe := d.shader, d.pipeline
	d.retired = append(d.retired, func() { oldPipe.Release(); old.Release() })
	d.shader, d.pipeline = shader, pipeline
	return st, nil
}

// spansSource is the span shader with the callers' types and their
// dispatch.
func (d *Device) spansSource() string {
	var b strings.Builder
	b.WriteString(spansWGSL)
	for _, t := range d.types {
		fmt.Fprintf(&b, "\n// shader type %d: %s\n%s\n", t.id, t.name, t.src)
	}
	b.WriteString("\nfn customColor(id: u32, rec: u32, p: vec2<f32>) -> vec4<f32> {\n    switch (id) {\n")
	for _, t := range d.types {
		fmt.Fprintf(&b, "        case %du: { return %s(p, rec); }\n", t.id, t.name)
	}
	b.WriteString("        default: { return vec4<f32>(0.0); }\n    }\n}\n")
	return b.String()
}

// customBlitter returns the blitter of a caller's shader the GPU colours,
// or nil for one without a type on this device or whose textures do not
// fit, which is shaded on the CPU.
func (t *Target) customBlitter(s Shader) stilus.Blitter {
	st := s.Type(t.d)
	if st == nil || st.d != t.d {
		return nil
	}
	smp := s.Samplers()
	n := 0
	for _, s := range smp {
		n += storeSize(s.Plane)
	}
	if n > storeMax(t) || t.parent != nil && t.storeUsed+n > storeMax(t) {
		return nil
	}
	b := &t.grad
	b.recOK, b.img, b.gform = false, nil, nil
	t.custom, t.customType = s, st
	t.customSmp = append(t.customSmp[:0], smp...)
	b.custom = true
	return b
}

// customRecord appends the record of the current operation's caller
// shader with its origin at (x, y), its planes made room for at once.
func (t *Target) customRecord(r []byte, x, y int) []byte {
	ps := t.customPlanes[:0]
	for _, s := range t.customSmp {
		ps = append(ps, s.Plane)
	}
	t.customPlanes = ps
	if !t.storeFit(ps) {
		t.setErr(fmt.Errorf("gpu: the textures of shader %q do not fit the store", t.customType.name))
	}
	r = binary.LittleEndian.AppendUint32(r, customKind|t.customType.id)
	return t.custom.AppendRecord(r, &Record{X: x, Y: y, t: t, smp: t.customSmp})
}
