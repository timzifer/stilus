// Spans recorded from stilus blitters, composited source-over onto a
// premultiplied RGBA8 target (ADR 0007, 0008).
//
// A span is a rectangle of pixels with a mode:
//   0 run:      colour × alpha (data)
//   1 coverage: colour × coverage byte, data is the byte offset of x0's
//               coverage in words
//   2 colours:  one premultiplied colour per pixel, data is the word offset
//               of x0's colour in words (spans of this mode are one row)
//   3 strip:    colour × the coverage of a stilus.Strip, data is the word
//               offset of its parameters (stripParams); the rectangle is
//               the strip's rows and the clip's columns, and the quad
//               drawn is the parallelogram around the strip's rows
//
// With bit 29 of data set, the colour of modes 0, 1 and 3 is a paint's,
// computed per pixel from the record at the word offset color: a
// gradient's (gradient.go) or, with bit 31 of its first word, an image's
// (image.go).

struct Span {
    pos: u32,   // x0 | y0 << 16
    size: u32,  // w | h << 16
    color: u32, // premultiplied RGBA8, R in the low byte
    data: u32,  // mode << 30 | gradient << 29 | datum
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

// A strip's parameters, relative to its origin (ox, y0): row r = y − y0
// covers at most the local columns [floor(xl + r·sl), floor(xr + r·sl)],
// and a pixel's coverage is its area between k2 <= nx·x + ny·y <= k1.
struct Strip {
    nx: f32,
    ny: f32,
    k1: f32,
    k2: f32,
    xl: f32,
    xr: f32,
    sl: f32,
    ox: i32,
    // The clip's mask, if moff is not 0xffffffff: bytes from moff in
    // words, mw × mh from (mx, my), row-major.
    moff: u32,
    mx: i32,
    my: i32,
    mw: u32,
    mh: u32,
}

fn strip(d: u32) -> Strip {
    var s: Strip;
    s.nx = bitcast<f32>(words[d]);
    s.ny = bitcast<f32>(words[d + 1u]);
    s.k1 = bitcast<f32>(words[d + 2u]);
    s.k2 = bitcast<f32>(words[d + 3u]);
    s.xl = bitcast<f32>(words[d + 4u]);
    s.xr = bitcast<f32>(words[d + 5u]);
    s.sl = bitcast<f32>(words[d + 6u]);
    s.ox = bitcast<i32>(words[d + 7u]);
    s.moff = words[d + 8u];
    s.mx = bitcast<i32>(words[d + 9u]);
    s.my = bitcast<i32>(words[d + 10u]);
    s.mw = words[d + 11u];
    s.mh = words[d + 12u];
    return s;
}

@vertex
fn vs_main(@builtin(vertex_index) vi: u32, @builtin(instance_index) ii: u32) -> VOut {
    let s = spans[ii];
    var cx = array<f32, 6>(0.0, 1.0, 0.0, 0.0, 1.0, 1.0);
    var cy = array<f32, 6>(0.0, 0.0, 1.0, 1.0, 0.0, 1.0);
    let x0 = f32(s.pos & 0xffffu);
    let y0 = f32(s.pos >> 16u);
    let h = f32(s.size >> 16u);
    var x = x0 + f32(s.size & 0xffffu) * cx[vi];
    let y = y0 + h * cy[vi];
    if ((s.data >> 30u) == 3u) {
        // The edges of the parallelogram lie two pixels outside the
        // strip's columns at each row's centre, so that every pixel of a
        // row's columns has its centre inside; the fragment tests them.
        let st = strip(s.data & 0x1fffffffu);
        let r = h * cy[vi] - 0.5;
        let left = f32(st.ox) + st.xl + r * st.sl - 2.0;
        let right = f32(st.ox) + st.xr + r * st.sl + 2.0;
        x = mix(left, right, cx[vi]);
    }
    var out: VOut;
    out.pos = vec4<f32>(x / g.size.x * 2.0 - 1.0, 1.0 - y / g.size.y * 2.0, 0.0, 1.0);
    out.id = ii;
    return out;
}

// area is the area of the unit pixel on the side n·(p − c) <= u of a line
// with the normal n: stilus's trapezoid.
fn area(u: f32, h: f32, mt: f32, inv2ab: f32, inva: f32) -> f32 {
    if (u <= -h) {
        return 0.0;
    }
    if (u >= h) {
        return 1.0;
    }
    if (u < -mt) {
        let v = u + h;
        return v * v * inv2ab;
    }
    if (u > mt) {
        let v = h - u;
        return 1.0 - v * v * inv2ab;
    }
    return 0.5 + u * inva;
}

fn stripCoverage(s: Span, px: vec2<f32>) -> f32 {
    let x0 = i32(s.pos & 0xffffu);
    let y0 = i32(s.pos >> 16u);
    let x = i32(px.x);
    let y = i32(px.y);
    if (x < x0 || x >= x0 + i32(s.size & 0xffffu) || y < y0 || y >= y0 + i32(s.size >> 16u)) {
        return 0.0;
    }
    let st = strip(s.data & 0x1fffffffu);
    let r = f32(y - y0);
    let lx = x - st.ox;
    // A column outside a row's span has no area between the lines; one
    // more on each side keeps float32 rounding of the span from dropping
    // a pixel that has.
    if (lx < i32(floor(st.xl + r * st.sl)) - 1 || lx > i32(floor(st.xr + r * st.sl)) + 1) {
        return 0.0;
    }
    var a = abs(st.nx);
    var b = abs(st.ny);
    if (a < b) {
        let t = a;
        a = b;
        b = t;
    }
    let h = (a + b) * 0.5;
    let mt = (a - b) * 0.5;
    var inv2ab = 0.0;
    if (b > 1e-9) {
        inv2ab = 1.0 / (2.0 * a * b);
    }
    let inva = 1.0 / a;
    let d = st.nx * (f32(lx) + 0.5) + st.ny * (r + 0.5);
    let c = area(st.k1 - d, h, mt, inv2ab, inva) - area(st.k2 - d, h, mt, inv2ab, inva);
    let cov = u32(clamp(floor(c * 256.0), 0.0, 255.0));
    if (st.moff == 0xffffffffu) {
        return f32(cov);
    }
    let mx = x - st.mx;
    let my = y - st.my;
    if (mx < 0 || my < 0 || u32(mx) >= st.mw || u32(my) >= st.mh) {
        return 0.0;
    }
    let b = st.moff + u32(my) * st.mw + u32(mx);
    let m = (words[b >> 2u] >> ((b & 3u) * 8u)) & 255u;
    // round(cov·m/255) as stilus's div255
    let v = cov * m + 128u;
    return f32((v + (v >> 8u)) >> 8u);
}

fn wf(i: u32) -> f32 {
    return bitcast<f32>(words[i]);
}

fn div255(v: u32) -> u32 {
    let x = v + 128u;
    return (x + (x >> 8u)) >> 8u;
}

// gradT returns a gradient's parameter at the pixel centre p, and false
// where no circle of a radial gradient passes through it.
fn gradT(r: u32, p: vec2<f32>) -> vec2<f32> {
    let flags = words[r];
    let q = r + 6u;
    if ((flags & 1u) == 0u) {
        return vec2<f32>(wf(q) * p.x + wf(q + 1u) * p.y + wf(q + 2u), 1.0);
    }
    let u = wf(q) * p.x + (wf(q + 2u) * p.y + wf(q + 4u));
    let v = wf(q + 1u) * p.x + (wf(q + 3u) * p.y + wf(q + 5u));
    let r0 = wf(q + 6u);
    let dr = wf(q + 9u);
    let qa = wf(q + 10u);
    let b = u * wf(q + 7u) + v * wf(q + 8u) + r0 * dr;
    let c = u * u + v * v - r0 * r0;
    var t1: f32;
    var t2: f32;
    if ((flags & 16u) != 0u) {
        if (b == 0.0) {
            return vec2<f32>(0.0, 0.0);
        }
        t1 = c / (2.0 * b);
        t2 = t1;
    } else {
        let d = b * b - qa * c;
        if (d < 0.0) {
            return vec2<f32>(0.0, 0.0);
        }
        let sq = sqrt(d);
        t1 = (b + sq) / qa;
        t2 = (b - sq) / qa;
        if (t1 < t2) {
            let x = t1;
            t1 = t2;
            t2 = x;
        }
    }
    let e0 = (flags & 2u) != 0u;
    let e1 = (flags & 4u) != 0u;
    if (!(r0 + t1 * dr < 0.0) && !(t1 < 0.0 && !e0) && !(t1 > 1.0 && !e1)) {
        return vec2<f32>(t1, 1.0);
    }
    if (!(r0 + t2 * dr < 0.0) && !(t2 < 0.0 && !e0) && !(t2 > 1.0 && !e1)) {
        return vec2<f32>(t2, 1.0);
    }
    return vec2<f32>(0.0, 0.0);
}

// gradColor returns a gradient's premultiplied colour at the pixel centre
// p, as stilus's Gradient computes it from t: Outside where it does not
// extend, the nearest ramp entry or the blend between knots, times Alpha.
fn gradColor(r: u32, p: vec2<f32>) -> vec4<f32> {
    let flags = words[r];
    let n = words[r + 1u];
    let ramp = r + words[r + 2u];
    let knots = r + words[r + 3u];
    let tv = gradT(r, p);
    var t = tv.x;
    var c = words[r + 4u];
    var inside = tv.y > 0.0;
    if (inside && t < 0.0) {
        inside = (flags & 2u) != 0u;
        t = 0.0;
    }
    if (inside && t > 1.0) {
        inside = (flags & 4u) != 0u;
        t = 1.0;
    }
    if (inside) {
        if ((flags & 8u) == 0u) {
            c = words[ramp + u32(t * f32(n - 1u) + 0.5)];
        } else {
            var i = 0u;
            loop {
                if (i + 1u >= n || wf(knots + i + 1u) > t) {
                    break;
                }
                i = i + 1u;
            }
            let k0 = wf(knots + i);
            if (t <= k0 || i + 1u == n) {
                c = words[ramp + i];
            } else {
                let w = u32((t - k0) * (1.0 / (wf(knots + i + 1u) - k0)) * 256.0 + 0.5);
                let a = words[ramp + i];
                let b = words[ramp + i + 1u];
                c = 0u;
                for (var s = 0u; s < 32u; s = s + 8u) {
                    let ca = (a >> s) & 255u;
                    let cb = (b >> s) & 255u;
                    c = c | (((ca * (256u - w) + cb * w + 128u) >> 8u) << s);
                }
            }
        }
    }
    let alpha = words[r + 5u];
    if (alpha != 255u) {
        var m = 0u;
        for (var s = 0u; s < 32u; s = s + 8u) {
            m = m | (div255(((c >> s) & 255u) * alpha) << s);
        }
        c = m;
    }
    return unpack4x8unorm(c);
}

// mul8 multiplies each channel of c by a/255, rounded: stilus's mul255.
fn mul8(c: u32, a: u32) -> u32 {
    var m = 0u;
    for (var s = 0u; s < 32u; s = s + 8u) {
        m = m | (div255(((c >> s) & 255u) * a) << s);
    }
    return m;
}

// lerp8 mixes two colours channel-wise, a·(256 − t) + b·t over 256,
// truncated: stilus's lerp.
fn lerp8(a: u32, b: u32, t: u32) -> u32 {
    if (a == b) {
        return a;
    }
    var m = 0u;
    for (var s = 0u; s < 32u; s = s + 8u) {
        m = m | ((((a >> s) & 255u) * (256u - t) + ((b >> s) & 255u) * t) >> 8u << s);
    }
    return m;
}

// texIndex is the pixel of [0, n) that u falls in: clamped to the edges,
// or modulo n when the plane repeats.
fn texIndex(u: f32, n: u32, wrap: bool) -> u32 {
    if (wrap) {
        let r = u - floor(u / f32(n)) * f32(n);
        return min(u32(max(r, 0.0)), n - 1u);
    }
    if (!(u > 0.0)) {
        return 0u;
    }
    if (u >= f32(n)) {
        return n - 1u;
    }
    return u32(u);
}

// texSplit returns the pixels on either side of u and the weight of the
// second in 1/256: stilus's split, or its periodic counterpart.
fn texSplit(u: f32, n: u32, wrap: bool) -> vec3<u32> {
    if (wrap) {
        let r = max(u - floor(u / f32(n)) * f32(n), 0.0);
        let i = min(u32(r), n - 1u);
        return vec3<u32>(i, (i + 1u) % n, u32((r - f32(i)) * 256.0));
    }
    if (!(u > 0.0)) {
        return vec3<u32>(0u, 0u, 0u);
    }
    if (u >= f32(n - 1u)) {
        return vec3<u32>(n - 1u, n - 1u, 0u);
    }
    let i = u32(u);
    return vec3<u32>(i, i + 1u, u32((u - f32(i)) * 256.0));
}

// sampleAt reads the sampler at q of an image record at pixel centre p.
fn sampleAt(q: u32, p: vec2<f32>) -> u32 {
    let off = words[q];
    let w = words[q + 1u];
    let h = words[q + 2u];
    let fl = words[q + 3u];
    let dx = f32(i32(floor(p.x)) - bitcast<i32>(words[q + 8u]));
    let dy = f32(i32(floor(p.y)) - bitcast<i32>(words[q + 9u]));
    var u = wf(q + 10u) + wf(q + 4u) * dx + wf(q + 6u) * dy;
    var v = wf(q + 11u) + wf(q + 5u) * dx + wf(q + 7u) * dy;
    let wrap = (fl & 2u) != 0u;
    if ((fl & 1u) == 0u) {
        return words[off + texIndex(v, h, wrap) * w + texIndex(u, w, wrap)];
    }
    let sx = texSplit(u - 0.5, w, wrap);
    let sy = texSplit(v - 0.5, h, wrap);
    let r0 = off + sy.x * w;
    let r1 = off + sy.y * w;
    let a = lerp8(words[r0 + sx.x], words[r0 + sx.y], sx.z);
    let b = lerp8(words[r1 + sx.x], words[r1 + sx.y], sx.z);
    return lerp8(a, b, sy.z);
}

// imageColor is an ImageShader's colour at pixel centre p: the image
// times Alpha, or Color, times the mask's level.
fn imageColor(r: u32, p: vec2<f32>) -> vec4<f32> {
    let flags = words[r];
    var c = words[r + 1u];
    if ((flags & 1u) != 0u) {
        c = sampleAt(r + 3u, p);
        let alpha = words[r + 2u];
        if (alpha != 255u) {
            c = mul8(c, alpha);
        }
    }
    if ((flags & 2u) != 0u) {
        let a = sampleAt(r + 15u, p) & 255u;
        if (a == 0u) {
            c = 0u;
        } else if (a != 255u) {
            c = mul8(c, a);
        }
    }
    return unpack4x8unorm(c);
}

fn base(s: Span, p: vec2<f32>) -> vec4<f32> {
    if ((s.data & 0x20000000u) != 0u) {
        if ((words[s.color] & 0x80000000u) != 0u) {
            return imageColor(s.color, p);
        }
        return gradColor(s.color, p);
    }
    return unpack4x8unorm(s.color);
}

@fragment
fn fs_main(in: VOut) -> @location(0) vec4<f32> {
    let s = spans[in.id];
    let mode = s.data >> 30u;
    let d = s.data & 0x1fffffffu;
    if (mode == 3u) {
        let cov = stripCoverage(s, in.pos.xy);
        if (cov == 0.0) {
            return vec4<f32>(0.0);
        }
        return base(s, in.pos.xy) * (cov / 255.0);
    }
    let i = u32(in.pos.x) - (s.pos & 0xffffu);
    if (mode == 2u) {
        return unpack4x8unorm(words[d + i]);
    }
    var a = f32(d);
    if (mode == 1u) {
        let b = d + i;
        a = f32((words[b >> 2u] >> ((b & 3u) * 8u)) & 255u);
    }
    return base(s, in.pos.xy) * (a / 255.0);
}
