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
	//go:embed assets/JetBrainsMonoNerdFontMono-Light.ttf
	regularTTF []byte
	//go:embed assets/JetBrainsMonoNerdFontMono-Regular.ttf
	boldTTF []byte
	//go:embed assets/JetBrainsMonoNerdFontMono-LightItalic.ttf
	italicTTF []byte
	//go:embed assets/JetBrainsMonoNerdFontMono-Italic.ttf
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
	bufCap    int // vertexBuf capacity, in instances
	texRows   int // the sheet height the texture was created at

	// Scratch reused across frames: runes for the clipped row, gids for the cursor
	// row, which is shaped outside the scrollback's cache.
	runes   []rune
	scratch []font.GID
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
	// The atlas is laid out to the device's ceiling and grows within it, so it has to
	// be told what that ceiling is.
	maxTexture := int(device.GetLimits().MaxTextureDimension2D)
	if maxTexture <= 0 || maxTexture > 32768 {
		maxTexture = 8192 // undefined or absurd; the WebGPU baseline
	}

	fm, err := font.NewManager(faces, "JetBrains Mono", sizePt, 72, maxTexture)
	if err != nil {
		return nil, err
	}
	t = &text{device: device, queue: queue, fm: fm}
	if err = t.makeAtlasTexture(); err != nil {
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

	if err = t.makeBindGroup(); err != nil {
		return t, err
	}
	return t, nil
}

// makeAtlasTexture creates the glyph texture at the sheet's current size and uploads
// all of it. Called once at startup, and again each time the sheet grows rows.
func (t *text) makeAtlasTexture() error {
	img := t.fm.Atlas.Img
	extent := wgpu.Extent3D{
		Width:              uint32(img.Rect.Dx()),
		Height:             uint32(img.Rect.Dy()),
		DepthOrArrayLayers: 1,
	}
	texture, err := t.device.TryCreateTexture(&wgpu.TextureDescriptor{
		Label:         "Glyph Atlas",
		Size:          extent,
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     wgpu.TextureDimension2D,
		Format:        wgpu.TextureFormatR8Unorm,
		Usage:         wgpu.TextureUsageTextureBinding | wgpu.TextureUsageCopyDst,
	})
	if err != nil {
		// Almost always the device's maximum dimension. Say the size, or the error is
		// a riddle.
		return fmt.Errorf("glyph atlas %dx%d: %w", extent.Width, extent.Height, err)
	}
	if err = t.queue.TryWriteTexture(
		texture.AsImageCopy(),
		img.Pix,
		&wgpu.TexelCopyBufferLayout{BytesPerRow: uint32(img.Stride), RowsPerImage: wgpu.CopyStrideUndefined},
		&extent,
	); err != nil {
		texture.Release()
		return err
	}
	view, err := texture.TryCreateView(nil)
	if err != nil {
		texture.Release()
		return err
	}

	// Only now that the replacement is whole: a submitted frame still holding the old
	// one keeps its own reference, so releasing here is safe.
	if t.view != nil {
		t.view.Release()
	}
	if t.texture != nil {
		t.texture.Release()
	}
	t.texture, t.view, t.texRows = texture, view, img.Rect.Dy()
	t.fm.Atlas.TakeDirty() // the whole sheet just went up; nothing is owed
	return nil
}

// makeBindGroup rebinds the pipeline's resources. The view changes whenever the atlas
// texture is replaced, and the bind group holds it.
func (t *text) makeBindGroup() error {
	layout := t.pipeline.GetBindGroupLayout(0)
	defer layout.Release()

	group, err := t.device.TryCreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "Text Bind Group",
		Layout: layout,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, Buffer: t.uniform, Size: wgpu.WholeSize},
			{Binding: 1, TextureView: t.view, Size: wgpu.WholeSize},
			{Binding: 2, Sampler: t.sampler, Size: wgpu.WholeSize},
		},
	})
	if err != nil {
		return err
	}
	if t.bindGroup != nil {
		t.bindGroup.Release()
	}
	t.bindGroup = group
	return nil
}

func (t *text) CellSize() (w, h int) { return t.fm.CellWidth, t.fm.CellHeight }

// clip is the row's runes, cut to cols cells and stopped at the last cell carrying
// anything, in the renderer's scratch buffer. The buffer is reused because a resize
// reshapes every visible row at once.
//
// An unwritten cell becomes a space rather than being dropped, so the cells after it
// still land in their own columns.
func (t *text) clip(cells []cell, cols int) []rune {
	if len(cells) > cols {
		cells = cells[:cols]
	}
	cells = trimBlanks(cells)

	t.runes = t.runes[:0]
	for _, c := range cells {
		r := c.Rune
		if r == 0 {
			r = ' '
		}
		t.runes = append(t.runes, r)
	}
	return t.runes
}

// plainRow maps each cell straight through cmap. Used when the font broke
// one-glyph-per-cell somewhere in the row: a per-cell renderer cannot draw that, so the
// row loses its ligatures rather than rendering garbage.
func (t *text) plainRow(cells []cell, runes []rune, dst []font.GID) []font.GID {
	for i, r := range runes {
		gid, _ := t.fm.GlyphIndex(styleAt(cells, i), r)
		dst = append(dst, gid)
	}
	return dst
}

// styleAt is the face cell i is drawn in, defaulting past the end of the row.
func styleAt(cells []cell, i int) font.Style {
	if i < len(cells) {
		return cells[i].Style
	}
	return font.Regular
}

// shapeRow shapes the row clipped to cols cells, appending to dst.
func (t *text) shapeRow(cells []cell, cols int, dst []font.GID) []font.GID {
	return t.shapeRuns(cells, cols, -1, dst)
}

// shapeRowSplit shapes l with the cell at col isolated: the runs either side keep
// their ligatures, the cursor's own cell is shaped without calt. Splitting is what
// breaks the join — substitution is contextual, so a run on its own cannot ligate
// into its neighbours.
//
// This is needed because a ligature is drawn in its last cell and reaches back over
// the earlier ones: a block on the first cell of "=>" would be painted over by the
// arrow next door, in the same colour, leaving a hole instead of a cursor.
//
// The result must not reach the scrollback's cache. The row is only shaped this way
// while the cursor is on it, and the cached ligated run stays correct for the moment
// the cursor leaves.
func (t *text) shapeRowSplit(cells []cell, cols, col int, dst []font.GID) []font.GID {
	return t.shapeRuns(cells, cols, col, dst)
}

// shapeRuns shapes the row one face at a time, isolating the cell at bare — or none,
// when bare is negative.
//
// Runs of a single face have to be shaped apart anyway: the shaper is per face, and a
// ligature spanning a bold boundary could not be drawn from one atlas slot. The cursor
// is the same mechanism with one more boundary in it.
func (t *text) shapeRuns(cells []cell, cols, bare int, dst []font.GID) []font.GID {
	runes := t.clip(cells, cols)
	if len(runes) == 0 {
		return dst
	}
	base := len(dst)

	for start := 0; start < len(runes); {
		end := start + 1
		if start != bare {
			for end < len(runes) && end != bare && styleAt(cells, end) == styleAt(cells, start) {
				end++
			}
		}
		var ok bool
		dst, ok = t.fm.Shaper(styleAt(cells, start)).ShapeRow(dst, runes[start:end], start != bare)
		if !ok {
			return t.plainRow(cells, runes, dst[:base])
		}
		start = end
	}
	return dst
}

// Layout lays each pane's visible window of history onto that pane's own grid and
// records the pane's slice of the shared instance buffer. Newlines inside a line's
// text are not handled.
//
// The grid is the clip: rows past the window are never visited and cells past cols
// are dropped by shapeRow, so only what shows gets shaped.
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

		shape := func(cells []cell, dst []font.GID) []font.GID { return t.shapeRow(cells, p.cols, dst) }
		from, to := p.visible()

		// Only a focused pane's block actually covers its glyph, and that is the only
		// case that has to invert the cell and prise it out of its ligature. An
		// unfocused pane draws a rim, a bar and an underline sit clear of the ink —
		// so at most one cell per frame takes the split path.
		curLine, curCol, hasCursor := p.cursorAt()
		invert := hasCursor && p == focused && p.cursor.shape == cursorBlock

		unfocused := p != focused

		for i := from; i < to; i++ {
			row, gen := p.rowAt(i)
			var gids []font.GID
			if invert && i == curLine {
				t.scratch = t.shapeRowSplit(row.cells, p.cols, curCol, t.scratch[:0])
				gids = t.scratch
			} else {
				gids = row.shaped(gen, shape)
			}

			y := originY + float32(i-from)*cellH - float32(a.PadTop)
			for col, gid := range gids {
				u, v := a.Ensure(font.Key{Style: styleAt(row.cells, col), GID: gid})
				c, _ := cellAt(row.cells, col).colors()
				if unfocused {
					// Dim the ink and leave the paint: brightness is the focus cue, and
					// darkening a background only makes it look like a hole.
					c = dim(c)
				}
				if invert && i == curLine && col == curCol {
					c = backgroundRGBA
				}
				t.instances = append(t.instances, instance{
					rect:  [4]float32{originX + float32(col)*cellW - float32(a.PadLeft), y, slotW, slotH},
					uv:    [4]float32{u, v, u + slotW/atlasW, v + slotH/atlasH},
					color: c,
				})
			}
		}
		p.count = uint32(len(t.instances)) - p.first
	}
	t.uploadGlyphs()
	t.upload()
}

// uploadGlyphs copies whatever this pass had to rasterise into the atlas texture.
//
// Only the slots that changed. The sheet is megabytes and almost all of it is already
// on the GPU; slots are handed out in order, so a frame's new glyphs are consecutive
// and coalesce into one copy per row of the grid.
func (t *text) uploadGlyphs() {
	a := t.fm.Atlas
	if a.Img.Rect.Dy() != t.texRows {
		// The sheet grew rows. Nothing already on it moved, but the texture is the
		// wrong size now, so it and the bind group holding its view are replaced.
		if err := t.makeAtlasTexture(); err != nil {
			fmt.Fprintln(os.Stderr, "gty: regrow glyph atlas:", err)
			return
		}
		if err := t.makeBindGroup(); err != nil {
			fmt.Fprintln(os.Stderr, "gty: rebind glyph atlas:", err)
		}
		return
	}

	dirty := a.TakeDirty()
	for i := 0; i < len(dirty); {
		j := i + 1
		for j < len(dirty) && dirty[j] == dirty[j-1]+1 && dirty[j]%a.Cols != 0 {
			j++
		}
		t.writeSlots(a.SlotRect(dirty[i]).Union(a.SlotRect(dirty[j-1])))
		i = j
	}
}

func (t *text) writeSlots(r image.Rectangle) {
	img := t.fm.Atlas.Img
	from := r.Min.Y*img.Stride + r.Min.X
	to := from + (r.Dy()-1)*img.Stride + r.Dx()

	if err := t.queue.TryWriteTexture(
		&wgpu.TexelCopyTextureInfo{
			Texture: t.texture,
			Origin:  wgpu.Origin3D{X: uint32(r.Min.X), Y: uint32(r.Min.Y)},
		},
		img.Pix[from:to],
		&wgpu.TexelCopyBufferLayout{BytesPerRow: uint32(img.Stride), RowsPerImage: wgpu.CopyStrideUndefined},
		&wgpu.Extent3D{Width: uint32(r.Dx()), Height: uint32(r.Dy()), DepthOrArrayLayers: 1},
	); err != nil {
		fmt.Fprintf(os.Stderr, "gty: upload glyphs %v: %v\n", r, err)
	}
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
