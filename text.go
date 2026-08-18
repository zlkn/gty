package main

import (
	_ "embed"
	"fmt"
	"image"
	"os"
	"unsafe"

	"github.com/oliverbestmann/webgpu/wgpu"

	"gty/internal/font"
)

//go:embed text.wgsl
var textShader string

// Only the four faces a terminal needs are embedded; assets/ holds the whole
// family, and the NL ("no ligatures") variants must not be used here.
var (
	//go:embed assets/JetBrainsMono-Regular.ttf
	regularTTF []byte
	//go:embed assets/JetBrainsMono-Bold.ttf
	boldTTF []byte
	//go:embed assets/JetBrainsMono-Italic.ttf
	italicTTF []byte
	//go:embed assets/JetBrainsMono-BoldItalic.ttf
	boldItalicTTF []byte
)

// initialInstances is the glyph buffer's starting capacity; Layout grows it.
const initialInstances = 1 << 14

// instance is one glyph quad. Layout must match the @location bindings in text.wgsl.
type instance struct {
	rect  [4]float32 // x, y, w, h in px
	uv    [4]float32 // u0, v0, u1, v1
	color [4]float32
}

type text struct {
	device *wgpu.Device
	queue  *wgpu.Queue
	fm     *font.FontManager

	pipeline  *wgpu.RenderPipeline
	bindGroup *wgpu.BindGroup
	uniform   *wgpu.Buffer
	vertexBuf *wgpu.Buffer
	texture   *wgpu.Texture
	view      *wgpu.TextureView
	sampler   *wgpu.Sampler

	instances []instance
	bufCap    int        // vertexBuf capacity, in instances
	gids      []font.GID // scratch, reused across rows
}

func newText(device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat, sizePt float64) (t *text, err error) {
	defer func() {
		if err != nil {
			t.release()
			t = nil
		}
	}()

	faces := [font.NumStyles][]byte{
		font.Regular:    regularTTF,
		font.Bold:       boldTTF,
		font.Italic:     italicTTF,
		font.BoldItalic: boldItalicTTF,
	}
	fm, err := font.NewManager(faces, "JetBrains Mono", sizePt, 72)
	if err != nil {
		return nil, err
	}
	t = &text{device: device, queue: queue, fm: fm}
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
		Size:  uint64(initialInstances * unsafe.Sizeof(instance{})),
		Usage: wgpu.BufferUsageVertex | wgpu.BufferUsageCopyDst,
	}); err != nil {
		return t, err
	}
	t.bufCap = initialInstances

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

// line is one row of the grid, drawn in a single style and colour.
type line struct {
	Text  string
	Style font.Style
	Color [4]float32
}

func (t *text) CellSize() (w, h int) { return t.fm.CellWidth, t.fm.CellHeight }

// Layout lays every pane's lines onto that pane's own grid and records the pane's
// slice of the shared instance buffer. Newlines inside a line's text are not
// handled.
//
// The grid is the clip: cells past a pane's cols and rows are dropped here, so
// only what shows gets shaped.
//
// Every cell gets a slot-sized quad offset back by the atlas padding, so a
// ligature glyph that reaches over its neighbours lands where the font drew it.
// Cell origins still step by CellWidth — calt in this font is monospace
// preserving, one glyph per cell.
func (t *text) Layout(panes []*pane, focused *pane) {
	t.instances = t.instances[:0]
	a := t.fm.Atlas
	slotW, slotH := float32(a.SlotW), float32(a.SlotH)
	atlasW, atlasH := float32(a.Img.Rect.Dx()), float32(a.Img.Rect.Dy())
	cellW, cellH := float32(t.fm.CellWidth), float32(t.fm.CellHeight)

	for _, p := range panes {
		p.first, p.count = uint32(len(t.instances)), 0
		originX := float32(p.rect.Min.X) + padding
		originY := float32(p.rect.Min.Y) + padding

		for row, ln := range p.lines {
			if row >= p.rows {
				break
			}
			runes := []rune(ln.Text)
			if len(runes) > p.cols {
				runes = runes[:p.cols]
			}
			gids, ok := t.fm.Shaper(ln.Style).ShapeRow(t.gids[:0], runes, true)
			if !ok {
				// The font broke one-glyph-per-cell for this row; a per-cell renderer
				// cannot draw that, so fall back to plain cmap and lose the ligatures.
				gids = gids[:0]
				for _, r := range runes {
					gid, _ := t.fm.GlyphIndex(ln.Style, r)
					gids = append(gids, gid)
				}
			}
			t.gids = gids

			color := ln.Color
			if p != focused {
				color = dim(color)
			}
			y := originY + float32(row)*cellH - float32(a.PadTop)
			for col, gid := range gids {
				u, v, ok := a.GlyphUV(font.Key{Style: ln.Style, GID: gid})
				if !ok {
					continue
				}
				t.instances = append(t.instances, instance{
					rect:  [4]float32{originX + float32(col)*cellW - float32(a.PadLeft), y, slotW, slotH},
					uv:    [4]float32{u, v, u + slotW/atlasW, v + slotH/atlasH},
					color: color,
				})
			}
		}
		p.count = uint32(len(t.instances)) - p.first
	}
	t.upload()
}

// upload grows the instance buffer if the frame needs more room, then writes it.
func (t *text) upload() {
	if need := len(t.instances); need > t.bufCap {
		buf, n, err := growBuffer(t.device, "Glyph Instances", t.vertexBuf,
			wgpu.BufferUsageVertex|wgpu.BufferUsageCopyDst, t.bufCap, need, unsafe.Sizeof(instance{}))
		if err != nil {
			fmt.Fprintf(os.Stderr, "gty: grow glyph buffer to %d: %v; text clipped\n", need, err)
			t.instances = t.instances[:t.bufCap]
		} else {
			t.vertexBuf, t.bufCap = buf, n
		}
	}
	if len(t.instances) == 0 {
		return
	}
	t.queue.WriteBuffer(t.vertexBuf, 0, wgpu.ToBytes(t.instances))
}

// Draw paints each pane's slice of the buffer, clipped to that pane.
//
// The scissor is load-bearing, not cosmetic: a quad is slot-sized and PadLeft is
// about three cells wide on this face, so a ligature in a pane's first column
// would otherwise paint over the pane to its left.
func (t *text) Draw(pass *wgpu.RenderPassEncoder, panes []*pane, viewportW, viewportH uint32) {
	if len(t.instances) == 0 {
		return
	}
	t.queue.WriteBuffer(t.uniform, 0, wgpu.ToBytes([]float32{
		float32(viewportW), float32(viewportH), 0, 0,
	}))
	pass.SetPipeline(t.pipeline)
	pass.SetBindGroup(0, t.bindGroup, nil)
	pass.SetVertexBuffer(0, t.vertexBuf, 0, wgpu.WholeSize)

	surface := image.Rect(0, 0, int(viewportW), int(viewportH))
	for _, p := range panes {
		// A scissor past the attachment is a validation error, and a pane rect can
		// outlive the frame it was laid out for.
		r := p.rect.Intersect(surface)
		if p.count == 0 || r.Empty() {
			continue
		}
		pass.SetScissorRect(uint32(r.Min.X), uint32(r.Min.Y), uint32(r.Dx()), uint32(r.Dy()))
		pass.Draw(4, p.count, 0, p.first)
	}
}

// release is idempotent: newText releases a half-built renderer on its error
// paths, and the window releases it again on teardown.
func (t *text) release() {
	if t == nil {
		return
	}
	if t.bindGroup != nil {
		t.bindGroup.Release()
		t.bindGroup = nil
	}
	if t.pipeline != nil {
		t.pipeline.Release()
		t.pipeline = nil
	}
	if t.vertexBuf != nil {
		t.vertexBuf.Release()
		t.vertexBuf, t.bufCap = nil, 0
	}
	if t.uniform != nil {
		t.uniform.Release()
		t.uniform = nil
	}
	if t.sampler != nil {
		t.sampler.Release()
		t.sampler = nil
	}
	if t.view != nil {
		t.view.Release()
		t.view = nil
	}
	if t.texture != nil {
		t.texture.Release()
		t.texture = nil
	}
	if t.fm != nil {
		t.fm.Close()
		t.fm = nil
	}
}
