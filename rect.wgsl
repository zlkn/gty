struct Viewport {
    size: vec2<f32>,
    // 1.0 when the target encodes itself, so colours are decoded on the way in. Written
    // from the pipeline's own target format, so the two cannot disagree.
    srgb: f32,
    _pad: f32,
};

@group(0) @binding(0) var<uniform> viewport: Viewport;

// srgbToLinear: instance colours arrive sRGB-encoded, and an *UnormSrgb target encodes
// again on write. Kept in step with srgbToLinear in srgb.go and the copy in text.wgsl.
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
    // Here rather than in fs_main: the colour is constant over the quad, so four vertices
    // pay instead of every pixel. Alpha is coverage, not colour.
    let rgb = select(inst.color.rgb, srgbToLinear(inst.color.rgb), viewport.srgb > 0.5);
    out.color = vec4<f32>(rgb, inst.color.a);
    return out;
}

@fragment
fn fs_main(in: VertexOut) -> @location(0) vec4<f32> {
    return in.color;
}
