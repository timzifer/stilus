// Spans recorded from stilus blitters, composited source-over onto a
// premultiplied RGBA8 target (ADR 0007).
//
// A span is a rectangle of pixels with a mode:
//   0 run:      colour × alpha (data)
//   1 coverage: colour × coverage byte, data is the byte offset of x0's
//               coverage in words
//   2 colours:  one premultiplied colour per pixel, data is the word offset
//               of x0's colour in words; rows of the span repeat the first
//               (spans of this mode are one row tall)

struct Span {
    pos: u32,   // x0 | y0 << 16
    size: u32,  // w | h << 16
    color: u32, // premultiplied RGBA8, R in the low byte
    data: u32,  // mode << 30 | datum
}

struct Globals {
    size: vec2<f32>,
    pad: vec2<f32>,
}

@group(0) @binding(0) var<uniform> g: Globals;
@group(0) @binding(1) var<storage, read> spans: array<Span>;
@group(0) @binding(2) var<storage, read> words: array<u32>;

struct VOut {
    @builtin(position) pos: vec4<f32>,
    @location(0) @interpolate(flat) id: u32,
}

@vertex
fn vs_main(@builtin(vertex_index) vi: u32, @builtin(instance_index) ii: u32) -> VOut {
    let s = spans[ii];
    var cx = array<f32, 6>(0.0, 1.0, 0.0, 0.0, 1.0, 1.0);
    var cy = array<f32, 6>(0.0, 0.0, 1.0, 1.0, 0.0, 1.0);
    let x = f32(s.pos & 0xffffu) + f32(s.size & 0xffffu) * cx[vi];
    let y = f32(s.pos >> 16u) + f32(s.size >> 16u) * cy[vi];
    var out: VOut;
    out.pos = vec4<f32>(x / g.size.x * 2.0 - 1.0, 1.0 - y / g.size.y * 2.0, 0.0, 1.0);
    out.id = ii;
    return out;
}

@fragment
fn fs_main(in: VOut) -> @location(0) vec4<f32> {
    let s = spans[in.id];
    let i = u32(in.pos.x) - (s.pos & 0xffffu);
    let mode = s.data >> 30u;
    let d = s.data & 0x3fffffffu;
    if (mode == 2u) {
        return unpack4x8unorm(words[d + i]);
    }
    var a = f32(d);
    if (mode == 1u) {
        let b = d + i;
        a = f32((words[b >> 2u] >> ((b & 3u) * 8u)) & 255u);
    }
    return unpack4x8unorm(s.color) * (a / 255.0);
}
