package main

import (
	_ "embed"
	"unsafe"

	"github.com/oliverbestmann/webgpu/wgpu"

	"gty/internal/font"
)

//go:embed text.wgsl
var textShader string

//go:embed assets/JetBrainsMono-Regular.ttf
var fontTTF []byte

const maxInstances = 1 << 14

// instance is one glyph quad. Layout must match the @location bindings in text.wgsl.
type instance struct {
	rect  [4]float32 // x, y, w, h in px
	uv    [4]float32 // u0, v0, u1, v1
	color [4]float32
}

type text struct {
	queue *wgpu.Queue
	fm    *font.FontManager

	pipeline  *wgpu.RenderPipeline
	bindGroup *wgpu.BindGroup
	uniform   *wgpu.Buffer
	vertexBuf *wgpu.Buffer
	texture   *wgpu.Texture
	view      *wgpu.TextureView
	sampler   *wgpu.Sampler

	instances []instance
	gids      []font.GID // scratch, reused across rows
}

func newText(device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat, sizePt float64) (t *text, err error) {
	defer func() {
		if err != nil {
			t.release()
			t = nil
		}
	}()

	fm, err := font.NewManager(fontTTF, "JetBrains Mono", sizePt, 72)
	if err != nil {
		return nil, err
	}
	t = &text{queue: queue, fm: fm}
	img := fm.Atlas.Img

	extent := wgpu.Extent3D{
		Width:              uint32(img.Rect.Dx()),
		Height:             uint32(img.Rect.Dy()),
		DepthOrArrayLayers: 1,
	}
	t.texture, err = device.TryCreateTexture(&wgpu.TextureDescriptor{
		Label:         "Glyph Atlas",
		Size:          extent,
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     wgpu.TextureDimension2D,
		Format:        wgpu.TextureFormatR8Unorm,
		Usage:         wgpu.TextureUsageTextureBinding | wgpu.TextureUsageCopyDst,
	})
	if err != nil {
		return t, err
	}
	if err = queue.TryWriteTexture(
		t.texture.AsImageCopy(),
		img.Pix,
		&wgpu.TexelCopyBufferLayout{BytesPerRow: uint32(img.Stride), RowsPerImage: wgpu.CopyStrideUndefined},
		&extent,
	); err != nil {
		return t, err
	}
	if t.view, err = t.texture.TryCreateView(nil); err != nil {
		return t, err
	}

	// Nearest, not linear: the atlas is rasterised at the exact display size, so
	// filtering would only smear it.
	if t.sampler, err = device.TryCreateSampler(&wgpu.SamplerDescriptor{
		Label:         "Glyph Sampler",
		AddressModeU:  wgpu.AddressModeClampToEdge,
		AddressModeV:  wgpu.AddressModeClampToEdge,
		AddressModeW:  wgpu.AddressModeClampToEdge,
		MagFilter:     wgpu.FilterModeNearest,
		MinFilter:     wgpu.FilterModeNearest,
		MipmapFilter:  wgpu.MipmapFilterModeNearest,
		LodMaxClamp:   1,
		MaxAnisotropy: 1,
	}); err != nil {
		return t, err
	}

	if t.uniform, err = device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Label: "Viewport Uniform",
		Size:  4 * 4,
		Usage: wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	}); err != nil {
		return t, err
	}
	if t.vertexBuf, err = device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Label: "Glyph Instances",
		Size:  uint64(maxInstances * unsafe.Sizeof(instance{})),
		Usage: wgpu.BufferUsageVertex | wgpu.BufferUsageCopyDst,
	}); err != nil {
		return t, err
	}

	shader, err := device.TryCreateShaderModule(&wgpu.ShaderModuleDescriptor{
		Label:      "text.wgsl",
		WGSLSource: &wgpu.ShaderSourceWGSL{Code: textShader},
	})
	if err != nil {
		return t, err
	}
	defer shader.Release()

	t.pipeline, err = device.TryCreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Label: "Text Pipeline",
		Vertex: wgpu.VertexState{
			Module:     shader,
			EntryPoint: "vs_main",
			Buffers: []wgpu.VertexBufferLayout{{
				ArrayStride: uint64(unsafe.Sizeof(instance{})),
				StepMode:    wgpu.VertexStepModeInstance,
				Attributes: []wgpu.VertexAttribute{
					{Format: wgpu.VertexFormatFloat32x4, Offset: 0, ShaderLocation: 0},
					{Format: wgpu.VertexFormatFloat32x4, Offset: 16, ShaderLocation: 1},
					{Format: wgpu.VertexFormatFloat32x4, Offset: 32, ShaderLocation: 2},
				},
			}},
		},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fs_main",
			Targets: []wgpu.ColorTargetState{{
				Format: format,
				Blend: &wgpu.BlendState{
					Color: wgpu.BlendComponent{
						SrcFactor: wgpu.BlendFactorSrcAlpha,
						DstFactor: wgpu.BlendFactorOneMinusSrcAlpha,
						Operation: wgpu.BlendOperationAdd,
					},
					Alpha: wgpu.BlendComponent{
						SrcFactor: wgpu.BlendFactorOne,
						DstFactor: wgpu.BlendFactorOneMinusSrcAlpha,
						Operation: wgpu.BlendOperationAdd,
					},
				},
				WriteMask: wgpu.ColorWriteMaskAll,
			}},
		},
		Primitive: wgpu.PrimitiveState{
			Topology: wgpu.PrimitiveTopologyTriangleStrip,
			CullMode: wgpu.CullModeNone,
		},
		Multisample: wgpu.MultisampleState{Count: 1, Mask: 0xFFFFFFFF},
	})
	if err != nil {
		return t, err
	}

	layout := t.pipeline.GetBindGroupLayout(0)
	defer layout.Release()
	t.bindGroup, err = device.TryCreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "Text Bind Group",
		Layout: layout,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, Buffer: t.uniform, Size: wgpu.WholeSize},
			{Binding: 1, TextureView: t.view, Size: wgpu.WholeSize},
			{Binding: 2, Sampler: t.sampler, Size: wgpu.WholeSize},
		},
	})
	if err != nil {
		return t, err
	}
	return t, nil
}

// Set lays text out on the cell grid, anchored at the top-left with padPx inset.
// One element of lines is one row; newlines inside an element are not handled.
//
// Every cell gets a slot-sized quad offset back by the atlas padding, so a
// ligature glyph that reaches over its neighbours lands where the font drew it.
// Cell origins still step by CellWidth — calt in this font is monospace
// preserving, one glyph per cell.
func (t *text) Set(lines []string, padPx float32, colour [4]float32) {
	t.instances = t.instances[:0]
	a := t.fm.Atlas
	slotW, slotH := float32(a.SlotW), float32(a.SlotH)
	atlasW, atlasH := float32(a.Img.Rect.Dx()), float32(a.Img.Rect.Dy())
	cellW, cellH := float32(t.fm.CellWidth), float32(t.fm.CellHeight)

	for row, line := range lines {
		runes := []rune(line)
		gids, ok := t.fm.Shaper.ShapeRow(t.gids[:0], runes, true)
		if !ok {
			// The font broke one-glyph-per-cell for this row; a per-cell renderer
			// cannot draw that, so fall back to plain cmap and lose the ligatures.
			gids = gids[:0]
			for _, r := range runes {
				gid, _ := t.fm.GlyphIndex(r)
				gids = append(gids, gid)
			}
		}
		t.gids = gids

		y := padPx + float32(row)*cellH - float32(a.PadTop)
		for col, gid := range gids {
			u, v, ok := a.GlyphUV(gid)
			if !ok {
				continue
			}
			t.instances = append(t.instances, instance{
				rect:  [4]float32{padPx + float32(col)*cellW - float32(a.PadLeft), y, slotW, slotH},
				uv:    [4]float32{u, v, u + slotW/atlasW, v + slotH/atlasH},
				color: colour,
			})
		}
	}
	if len(t.instances) > maxInstances {
		t.instances = t.instances[:maxInstances]
	}
	t.queue.WriteBuffer(t.vertexBuf, 0, wgpu.ToBytes(t.instances))
}

func (t *text) Draw(pass *wgpu.RenderPassEncoder, viewportW, viewportH uint32) {
	if len(t.instances) == 0 {
		return
	}
	t.queue.WriteBuffer(t.uniform, 0, wgpu.ToBytes([]float32{
		float32(viewportW), float32(viewportH), 0, 0,
	}))
	pass.SetPipeline(t.pipeline)
	pass.SetBindGroup(0, t.bindGroup, nil)
	pass.SetVertexBuffer(0, t.vertexBuf, 0, wgpu.WholeSize)
	pass.Draw(4, uint32(len(t.instances)), 0, 0)
}

func (t *text) release() {
	if t == nil {
		return
	}
	if t.bindGroup != nil {
		t.bindGroup.Release()
	}
	if t.pipeline != nil {
		t.pipeline.Release()
	}
	if t.vertexBuf != nil {
		t.vertexBuf.Release()
	}
	if t.uniform != nil {
		t.uniform.Release()
	}
	if t.sampler != nil {
		t.sampler.Release()
	}
	if t.view != nil {
		t.view.Release()
	}
	if t.texture != nil {
		t.texture.Release()
	}
	if t.fm != nil {
		t.fm.Close()
	}
}

