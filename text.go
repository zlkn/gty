package main

import (
	_ "embed"
	"fmt"
	"image"
	"os"
	"strings"
	"unsafe"

	"github.com/oliverbestmann/webgpu/wgpu"

	"gty/internal/font"
)

//go:embed text.wgsl
var textShader string

var (
	//go:embed assets/JetBrainsMonoNerdFontMono-Regular.ttf
	regularTTF []byte
	//go:embed assets/JetBrainsMonoNerdFontMono-SemiBold.ttf
	boldTTF []byte
	//go:embed assets/JetBrainsMonoNerdFontMono-Italic.ttf
	italicTTF []byte
	//go:embed assets/JetBrainsMonoNerdFontMono-SemiBoldItalic.ttf
	boldItalicTTF []byte
)

const embeddedFamily = "JetBrains Mono"

// initialInstances is the glyph buffer's starting capacity; Layout grows it.
const initialInstances = 1 << 14

// instance is one glyph quad. Layout must match the @location bindings in text.wgsl.
type instance struct {
	rect  [4]float32 // x, y, w, h in px
	uv    [4]float32 // u0, v0, u1, v1
	color [4]float32
	// darken is 1 for a glyph that wants the theme's coverage gamma and 0 for one drawn as
	// the rasteriser made it. Per instance rather than per frame because icons and text
	// share the pass; see font.FontManager.IconFace.
	darken float32
}

// chrome is a run of text drawn outside any pane, scissored to rect so that a tab bar
// label wider than its tab is clipped rather than spilling into the next one.
type chrome struct {
	rect  image.Rectangle
	cells []cell
	x, y  float32    // the top-left of the first cell, in framebuffer px
	fg    [4]float32 // one colour for the run
}

// chromeGroup is a laid-out chrome run and its slice of the shared instance buffer.
type chromeGroup struct {
	rect         image.Rectangle
	first, count uint32
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

	instances  []instance
	bufCap     int  // vertexBuf capacity, in instances
	texRows    int  // the sheet height the texture was created at
	maxTexture int  // the device's ceiling, kept for the rebake in SetScale
	srgb       bool // whether the target encodes what the shader writes; see srgb.go

	// The chrome runs this pass laid out, drawn ahead of the panes.
	chrome []chromeGroup

	// Scratch reused across frames: runes for the clipped row, gids for the cursor
	// row, which is shaped outside the scrollback's cache, and gids for a chrome run,
	// which has no row to cache into at all.
	runes      []rune
	scratch    []font.GID
	chromeGids []font.GID
}

func newText(device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat, sizePt, scale float64) (t *text, err error) {
	defer func() {
		if err != nil {
			t.release()
			t = nil
		}
	}()

	// The atlas is laid out to the device's ceiling and grows within it, so it has to
	// be told what that ceiling is.
	maxTexture := int(device.GetLimits().MaxTextureDimension2D)
	if maxTexture <= 0 || maxTexture > 32768 {
		maxTexture = 8192 // undefined or absurd; the WebGPU baseline
	}

	fm, err := newFontManager(sizePt, scale, maxTexture)
	if err != nil {
		return nil, err
	}
	t = &text{device: device, queue: queue, fm: fm, maxTexture: maxTexture, srgb: isSrgbFormat(format)}
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
					{Format: wgpu.VertexFormatFloat32, Offset: 48, ShaderLocation: 3},
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

// newFontManager loads the faces the grid is drawn with: the font the config named, then
// the embedded family, then whatever the machine has. The middle link is what makes the
// config safe — a plain font from it still gets its icons from a face designed for a
// terminal.
//
// A config font that will not load is a warning, not a failure: that covers a typo as well
// as a family whose four styles disagree about the cell, which is fatal to a grid.
func newFontManager(sizePt, scale float64, maxTexture int) (*font.FontManager, error) {
	warn := func(s string) { fmt.Fprintln(os.Stderr, "gty: font:", s) }
	lib := &font.Library{Warn: warn}
	embedded := [font.NumStyles]font.Source{
		font.Regular:    {Name: embeddedFamily, TTF: regularTTF},
		font.Bold:       {Name: embeddedFamily, TTF: boldTTF},
		font.Italic:     {Name: embeddedFamily, TTF: italicTTF},
		font.BoldItalic: {Name: embeddedFamily, TTF: boldItalicTTF},
	}
	opts := font.Options{
		Styles: embedded, Family: embeddedFamily,
		Finder: lib,
		// The display's scale rides in on the DPI: ppem is Size*DPI/72, so the same
		// point size rasterises at twice the pixels on a 2x panel.
		Size: sizePt, DPI: 72 * scale, MaxTexture: maxTexture,
		IconFill:   fontIconScale,
		BoxDrawing: fontBoxDrawing,
		Warn:       warn,
	}

	if fontFamily != "" && !strings.EqualFold(fontFamily, embeddedFamily) {
		styles, missing, err := lib.Family(fontFamily)
		switch {
		case err != nil:
			warn(fmt.Sprintf("%v; using %s", err, embeddedFamily))
		default:
			for _, style := range missing {
				warn(fmt.Sprintf("%s has no %s face; using its regular", fontFamily, style))
			}
			custom := opts
			custom.Styles, custom.Family = styles, fontFamily
			// One face: a fallback is styleless, so bold and italic would only cost slots.
			custom.Fallback = []font.Source{{Name: embeddedFamily, TTF: regularTTF}}
			if fm, err := font.NewManager(custom); err == nil {
				return fm, nil
			} else {
				warn(fmt.Sprintf("%s: %v; using %s", fontFamily, err, embeddedFamily))
			}
		}
	}
	return font.NewManager(opts)
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

// SetScale rebakes every face at a new display scale and hands the fresh sheet to the
// GPU. The cell size comes out of it, so the caller only has to relayout.
//
// The atlas is replaced whole rather than regrown: every glyph on it was rasterised at
// the old pixel size, and none of them is reusable at the new one. Cached glyph IDs
// elsewhere survive — the faces are the same files, and an ID does not depend on ppem.
func (t *text) SetScale(sizePt, scale float64) error {
	fm, err := newFontManager(sizePt, scale, t.maxTexture)
	if err != nil {
		return err
	}
	old := t.fm
	t.fm = fm
	if err := t.makeAtlasTexture(); err != nil {
		// Nothing was replaced yet, so putting the old manager back leaves the
		// renderer whole and drawing at the scale it already had.
		t.fm = old
		fm.Close()
		return err
	}
	old.Close()
	return t.makeBindGroup()
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
func (t *text) Layout(panes []*pane, focused *pane, runs []chrome) {
	t.instances = t.instances[:0]
	a := t.fm.Atlas
	slotW, slotH := float32(a.SlotW), float32(a.SlotH)
	atlasW, atlasH := float32(a.Img.Rect.Dx()), float32(a.Img.Rect.Dy())
	cellW, cellH := float32(t.fm.CellWidth), float32(t.fm.CellHeight)
	pad := float32(px(padding))

	// The pane loop below without a history to window, a cursor to prise out of a
	// ligature or a focus to dim by.
	t.chrome = t.chrome[:0]
	for _, c := range runs {
		first := uint32(len(t.instances))
		t.chromeGids = t.shapeRow(c.cells, len(c.cells), t.chromeGids[:0])
		for col, gid := range t.chromeGids {
			cl := cellAt(c.cells, col)
			k := t.fm.Resolve(cl.Style, gid, cl.Rune)
			u, v := a.Ensure(k)
			t.instances = append(t.instances, instance{
				rect:   [4]float32{c.x + float32(col)*cellW - float32(a.PadLeft), c.y - float32(a.PadTop), slotW, slotH},
				uv:     [4]float32{u, v, u + slotW/atlasW, v + slotH/atlasH},
				color:  c.fg,
				darken: glyphDarken(t.fm, k),
			})
		}
		t.chrome = append(t.chrome, chromeGroup{
			rect: c.rect, first: first, count: uint32(len(t.instances)) - first,
		})
	}

	for _, p := range panes {
		p.first, p.count = uint32(len(t.instances)), 0
		originX := float32(p.rect.Min.X) + pad
		originY := float32(p.rect.Min.Y) + pad

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
				cl := cellAt(row.cells, col)
				// Resolve, not the gid alone: an uncovered rune shapes to 0, and the
				// chain is where it is drawn from.
				k := t.fm.Resolve(cl.Style, gid, cl.Rune)
				u, v := a.Ensure(k)
				c, _ := cl.colors()
				if unfocused {
					// Dim the ink and leave the paint: brightness is the focus cue, and
					// darkening a background only makes it look like a hole.
					c = dim(c)
				}
				if invert && i == curLine && col == curCol {
					c = backgroundRGBA
				}
				t.instances = append(t.instances, instance{
					rect:   [4]float32{originX + float32(col)*cellW - float32(a.PadLeft), y, slotW, slotH},
					uv:     [4]float32{u, v, u + slotW/atlasW, v + slotH/atlasH},
					color:  c,
					darken: glyphDarken(t.fm, k),
				})
			}
		}
		p.count = uint32(len(t.instances)) - p.first
	}
	t.uploadGlyphs()
	t.upload()
}

// glyphDarken is whether the glyph wants the theme's coverage gamma. A frame and an
// icon do not: the bend would only fatten antialiasing that was asked for.
func glyphDarken(fm *font.FontManager, k font.Key) float32 {
	if k.Style == font.SynthBox || fm.IconFace(k.Style) {
		return 0
	}
	return 1
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
		float32(viewportW), float32(viewportH), srgbFlag(t.srgb), coverageExp,
	}))
	pass.SetPipeline(t.pipeline)
	pass.SetBindGroup(0, t.bindGroup, nil)
	pass.SetVertexBuffer(0, t.vertexBuf, 0, wgpu.WholeSize)

	surface := image.Rect(0, 0, int(viewportW), int(viewportH))
	// Intersected because a scissor past the attachment is a validation error, and a
	// rect can outlive the frame it was laid out for.
	group := func(rect image.Rectangle, first, count uint32) {
		r := rect.Intersect(surface)
		if count == 0 || r.Empty() {
			return
		}
		pass.SetScissorRect(uint32(r.Min.X), uint32(r.Min.Y), uint32(r.Dx()), uint32(r.Dy()))
		pass.Draw(4, count, 0, first)
	}
	for _, c := range t.chrome {
		group(c.rect, c.first, c.count)
	}
	for _, p := range panes {
		group(p.rect, p.first, p.count)
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
