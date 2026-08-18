struct Viewport {
    size: vec2<f32>,
    _pad: vec2<f32>,
};

@group(0) @binding(0) var<uniform> viewport: Viewport;

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
    out.color = inst.color;
    return out;
}

@fragment
fn fs_main(in: VertexOut) -> @location(0) vec4<f32> {
    return in.color;
}
