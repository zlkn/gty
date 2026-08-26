struct Viewport {
    size: vec2<f32>,
    // srgb is 1.0 when the target applies the sRGB encode itself, so the colours have to
    // be decoded on the way in. Written from the pipeline's own target format, so the
    // shader and the attachment cannot disagree about it.
    srgb: f32,
    _pad: f32,
};

@group(0) @binding(0) var<uniform> viewport: Viewport;

// srgbToLinear is the inverse sRGB transfer function. Instance colours arrive the way
// the config file writes them, sRGB-encoded; a target in an *UnormSrgb format encodes
// again on write, so handing one over untouched washes the whole theme out.
//
// Kept in step with srgbToLinear in srgb.go, which does this to the clear value — the one
// colour no shader sees — and with the copy in text.wgsl.
fn srgbToLinear(c: vec3<f32>) -> vec3<f32> {
    let lo = c / 12.92;
    let hi = pow((c + vec3<f32>(0.055)) / 1.055, vec3<f32>(2.4));
    return select(hi, lo, c <= vec3<f32>(0.04045));
}

struct Instance {
    @location(0) rect: vec4<f32>,
    @location(1) color: vec4<f32>,
};

struct VertexOut {
    @builtin(position) pos: vec4<f32>,
    @location(0) color: vec4<f32>,
};

@vertex
fn vs_main(inst: Instance, @builtin(vertex_index) index: u32) -> VertexOut {
    // Same index-derived quad and px-to-NDC mapping as text.wgsl, minus the atlas.
    let corner = vec2<f32>(f32(index & 1u), f32((index >> 1u) & 1u));
    let px = inst.rect.xy + corner * inst.rect.zw;

    var out: VertexOut;
    out.pos = vec4<f32>(
        px.x / viewport.size.x * 2.0 - 1.0,
        1.0 - px.y / viewport.size.y * 2.0,
        0.0,
        1.0,
    );
    // Decoded here rather than in fs_main: the colour is constant over the quad, so four
    // vertices pay for it instead of every covered pixel, and interpolating four equal
    // values is exact. Alpha is left alone — it is a fraction of coverage, not a colour.
    let rgb = select(inst.color.rgb, srgbToLinear(inst.color.rgb), viewport.srgb > 0.5);
    out.color = vec4<f32>(rgb, inst.color.a);
    return out;
}

@fragment
fn fs_main(in: VertexOut) -> @location(0) vec4<f32> {
    return in.color;
}
