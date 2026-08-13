package main

import (
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"unsafe"

	"github.com/oliverbestmann/webgpu/wgpu"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed text.wgsl
var textShader string

//go:embed assets/JetBrainsMono.ttf
var fontTTF []byte

const (
	firstRune    = ' '
	lastRune     = '~'
	atlasCols    = 16
	slotPad      = 2
	maxInstances = 1 << 14
)

// instance is one glyph quad. Layout must match the @location bindings in text.wgsl.
type instance struct {
	rect  [4]float32 // x, y, w, h in px
	uv    [4]float32 // u0, v0, u1, v1
	color [4]float32
}

type glyph struct {
	u0, v0, u1, v1 float32
	w, h           float32
	offX, offY     float32 // ink origin relative to the pen, on the baseline
}

type atlas struct {
	img           *image.Alpha
	glyphs        []glyph
	cellW, cellH  float32
	ascent        float32
}

type text struct {
	queue *wgpu.Queue

	pipeline  *wgpu.RenderPipeline
	bindGroup *wgpu.BindGroup
	uniform   *wgpu.Buffer
	vertexBuf *wgpu.Buffer
	texture   *wgpu.Texture
	view      *wgpu.TextureView
	sampler   *wgpu.Sampler

	atlas     *atlas
	instances []instance
}

// bake rasterises the printable ASCII range into a single alpha texture.
//
// This is the throwaway half: it addresses glyphs by rune, so it cannot reach
// ligatures or anything GSUB substitutes. Swapping it for a GID-keyed baker on
// go-text/typesetting changes nothing above this function.
func bake(ttf []byte, sizePx float64) (*atlas, error) {
	sf, err := sfnt.Parse(ttf)
	if err != nil {
		return nil, fmt.Errorf("parse font: %w", err)
	}
	face, err := opentype.NewFace(sf, &opentype.FaceOptions{
		Size:    sizePx,
		DPI:     72, // size in points == size in pixels
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("new face: %w", err)
	}
	defer face.Close()

	metrics := face.Metrics()
	ascent, descent := metrics.Ascent.Ceil(), metrics.Descent.Ceil()
	advance, ok := face.GlyphAdvance('M')
	if !ok {
		return nil, fmt.Errorf("font has no 'M' to measure the cell with")
	}
	cellW, cellH := advance.Ceil(), ascent+descent

	count := int(lastRune-firstRune) + 1
	rows := (count + atlasCols - 1) / atlasCols
	slotW, slotH := cellW+2*slotPad, cellH+2*slotPad

	// R8 data is uploaded at bytesPerRow == width, which wgpu wants 256-aligned;
	// the spare columns on the right stay empty.
	width := (atlasCols*slotW + 255) &^ 255
	img := image.NewAlpha(image.Rect(0, 0, width, rows*slotH))

	glyphs := make([]glyph, count)
	fw, fh := float32(img.Rect.Dx()), float32(img.Rect.Dy())
	for i := range glyphs {
		penX := (i%atlasCols)*slotW + slotPad
		penY := (i/atlasCols)*slotH + slotPad + ascent
		dr, mask, maskp, _, ok := face.Glyph(fixed.P(penX, penY), rune(firstRune+i))
		if !ok || dr.Empty() {
			continue // space, and anything the font lacks
		}
		draw.DrawMask(img, dr, image.Opaque, image.Point{}, mask, maskp, draw.Src)
		glyphs[i] = glyph{
			u0: float32(dr.Min.X) / fw, v0: float32(dr.Min.Y) / fh,
			u1: float32(dr.Max.X) / fw, v1: float32(dr.Max.Y) / fh,
			w:    float32(dr.Dx()),
			h:    float32(dr.Dy()),
			offX: float32(dr.Min.X - penX),
			offY: float32(dr.Min.Y - penY),
		}
	}

	return &atlas{
		img:    img,
		glyphs: glyphs,
		cellW:  float32(cellW),
		cellH:  float32(cellH),
		ascent: float32(ascent),
	}, nil
}

func newText(device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat, sizePx float64) (t *text, err error) {
	defer func() {
		if err != nil {
			t.release()
			t = nil
		}
	}()

	a, err := bake(fontTTF, sizePx)
	if err != nil {
		return nil, err
	}
	t = &text{queue: queue, atlas: a}

	extent := wgpu.Extent3D{
		Width:              uint32(a.img.Rect.Dx()),
		Height:             uint32(a.img.Rect.Dy()),
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
		a.img.Pix,
		&wgpu.TexelCopyBufferLayout{BytesPerRow: uint32(a.img.Stride), RowsPerImage: wgpu.CopyStrideUndefined},
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
		Label:        "Glyph Sampler",
		AddressModeU: wgpu.AddressModeClampToEdge,
		AddressModeV: wgpu.AddressModeClampToEdge,
		AddressModeW: wgpu.AddressModeClampToEdge,
		MagFilter:    wgpu.FilterModeNearest,
		MinFilter:    wgpu.FilterModeNearest,
		MipmapFilter: wgpu.MipmapFilterModeNearest,
		LodMaxClamp:  1,
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
func (t *text) Set(lines []string, padPx float32, colour [4]float32) {
	t.instances = t.instances[:0]
	for row, line := range lines {
		baseline := padPx + float32(row)*t.atlas.cellH + t.atlas.ascent
		col := 0
		for _, r := range line {
			// Advance per rune, not per byte: range over a string yields byte
			// offsets, which stop matching cells as soon as text is non-ASCII.
			penX := padPx + float32(col)*t.atlas.cellW
			col++
			if r < firstRune || r > lastRune {
				continue
			}
			g := t.atlas.glyphs[r-firstRune]
			if g.w == 0 {
				continue
			}
			t.instances = append(t.instances, instance{
				rect:  [4]float32{penX + g.offX, baseline + g.offY, g.w, g.h},
				uv:    [4]float32{g.u0, g.v0, g.u1, g.v1},
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
}
