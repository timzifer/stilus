// Draws a part of a target's texture as a quad into a render pass
// (present.go). The texture is premultiplied; the pipeline blends
// source-over.

struct Slot {
    rect: vec4<f32>,   // x0, y0, x1, y1 in pixels of the viewport
    view: vec4<f32>,   // viewport width, height
    uv: vec4<f32>,     // the source rectangle in texture coordinates
    limit: vec4<f32>,  // the texel centres at its edges
    alpha: f32,
};

@group(0) @binding(0) var<uniform> u: Slot;
@group(1) @binding(0) var tex: texture_2d<f32>;
@group(1) @binding(1) var samp: sampler;

struct VOut {
    @builtin(position) pos: vec4<f32>,
    @location(0) uv: vec2<f32>,
};

@vertex
fn vs_main(@builtin(vertex_index) i: u32) -> VOut {
    let c = vec2<f32>(f32(i & 1u), f32((i >> 1u) & 1u));
    let p = mix(u.rect.xy, u.rect.zw, c);
    var o: VOut;
    o.pos = vec4<f32>(p.x / u.view.x * 2.0 - 1.0, 1.0 - p.y / u.view.y * 2.0, 0.0, 1.0);
    o.uv = mix(u.uv.xy, u.uv.zw, c);
    return o;
}

@fragment
fn fs_main(i: VOut) -> @location(0) vec4<f32> {
    let uv = clamp(i.uv, u.limit.xy, u.limit.zw);
    return textureSample(tex, samp, uv) * u.alpha;
}
