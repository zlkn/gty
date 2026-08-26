package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/oliverbestmann/webgpu/wgpu"

	"gty/internal/font"
)

const (
	testWidth  = 576 // 576*4 = 2304 bytes/row, a multiple of the required 256
	testHeight = 680
	// An sRGB format, so the offscreen path exercises the same transfer function the
	// window does — with RGBA8Unorm here the double encode that washed out every colour
	// in the window could not be reproduced offscreen at all. RGBA rather than BGRA
	// because the read-back is an *image.RGBA: a BGRA target would quietly swap R and B
	// under every .R in this file.
	testFormat = wgpu.TextureFormatRGBA8UnormSrgb
)

// newTestGPU skips the test on a machine with no adapter.
func newTestGPU(t testing.TB) (*wgpu.Device, *wgpu.Queue) {
	t.Helper()

	instance := wgpu.CreateInstance(nil)
	t.Cleanup(instance.Release)

	adapter, err := instance.RequestAdapter(nil)
	if err != nil {
		t.Skipf("no adapter: %v", err)
	}
	t.Cleanup(adapter.Release)

	device, err := adapter.RequestDevice(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(device.Release)

	queue := device.GetQueue()
	t.Cleanup(queue.Release)
	return device, queue
}

func newTestText(t testing.TB, device *wgpu.Device, queue *wgpu.Queue) *text {
	t.Helper()
	txt, err := newText(device, queue, testFormat, fontSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(txt.release)
	return txt
}

// splitLayout is the tree the app has after one Ctrl+Shift+D. The layout pass comes
// first: a pane has no screen to write into until it has been given a grid.
func splitLayout(t *testing.T, txt *text, w, h int) ([]*pane, []image.Rectangle) {
	t.Helper()
	first, second := newPane(1), newPane(2)

	root := &node{pane: first}
	if !root.split(first, vertical, second) {
		t.Fatal("split did not find the root pane")
	}
	cellW, cellH := txt.CellSize()
	panes, dividers := layoutTree(root, image.Rect(0, 0, w, h), cellW, cellH)
	for _, p := range panes {
		fillDemo(p)
	}
	return panes, dividers
}

// renderOffscreen draws into a fresh texture and reads it back, so a frame can be
// looked at without a window.
func renderOffscreen(t *testing.T, device *wgpu.Device, queue *wgpu.Queue, draw func(pass *wgpu.RenderPassEncoder)) *image.RGBA {
	t.Helper()

	extent := wgpu.Extent3D{Width: testWidth, Height: testHeight, DepthOrArrayLayers: 1}
	target, err := device.TryCreateTexture(&wgpu.TextureDescriptor{
		Size:          extent,
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     wgpu.TextureDimension2D,
		Format:        testFormat,
		Usage:         wgpu.TextureUsageRenderAttachment | wgpu.TextureUsageCopySrc,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Release()
	view, err := target.TryCreateView(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Release()

	const bytesPerRow = testWidth * 4
	out, err := device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Size:  bytesPerRow * testHeight,
		Usage: wgpu.BufferUsageMapRead | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Release()

	encoder, err := device.TryCreateCommandEncoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Release()

	pass := encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View:       view,
			LoadOp:     wgpu.LoadOpClear,
			StoreOp:    wgpu.StoreOpStore,
			ClearValue: clearValue(backgroundRGBA, isSrgbFormat(testFormat)),
		}},
	})
	draw(pass)
	pass.End()
	pass.Release()

	encoder.TryCopyTextureToBuffer(
		target.AsImageCopy(),
		&wgpu.TexelCopyBufferInfo{
			Buffer: out,
			Layout: wgpu.TexelCopyBufferLayout{BytesPerRow: bytesPerRow, RowsPerImage: wgpu.CopyStrideUndefined},
		},
		&extent,
	)

	cmd, err := encoder.TryFinish(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cmd.Release()
	queue.Submit(cmd)

	out.TryMapAsync(wgpu.MapModeRead, 0, bytesPerRow*testHeight, func(status wgpu.MapAsyncStatus) {
		if status != wgpu.MapAsyncStatusSuccess {
			t.Errorf("map failed: %v", status)
		}
	})
	device.Poll(true, nil)

	// Copy out before unmapping: the mapped range does not outlive the buffer.
	pix := append([]byte(nil), out.GetMappedRange(0, bytesPerRow*testHeight)...)
	out.TryUnmap()
	return &image.RGBA{Pix: pix, Stride: bytesPerRow, Rect: image.Rect(0, 0, testWidth, testHeight)}
}

// Pixels are four bytes rather than a color.RGBA: this package has a colour type of its
// own, so image/color cannot be imported here without an alias, and the byte quadruple
// reads the same way the renderer's own [4]float32 does.

func pixelAt(img *image.RGBA, x, y int) [4]uint8 {
	c := img.RGBAAt(x, y)
	return [4]uint8{c.R, c.G, c.B, c.A}
}

// pixelOf is a renderer colour as it lands in memory. On an sRGB target a pixel of full
// coverage and opaque alpha comes back as the byte that was configured — the GPU's encode
// and the shader's decode cancel — which is what makes exact assertions possible at all.
func pixelOf(c [4]float32) [4]uint8 {
	q := func(v float32) uint8 { return uint8(min(max(v, 0), 1)*255 + 0.5) }
	return [4]uint8{q(c[0]), q(c[1]), q(c[2]), q(c[3])}
}

// near compares two pixels channel by channel, allowing tol counts of slack: two
// implementations of the sRGB curve (float64 in Go, f32 in WGSL) and the GPU's own encode
// do not have to agree to the last bit.
func near(got, want [4]uint8, tol int) bool {
	for i := range got {
		if int(got[i])-int(want[i]) > tol || int(want[i])-int(got[i]) > tol {
			return false
		}
	}
	return true
}

// Ink is measured as distance from the background, not as brightness. Brightness only
// stands in for "something was drawn" on a dark theme; on a light one the brightest thing
// on screen is the paper, and every one of these metrics read backwards.

// inkTol is how far a pixel may sit from the background and still count as untouched: the
// transfer function's own rounding, and nothing more. Deliberately small — an antialiased
// edge is meant to count as ink, and so is a colour some shader forgot to decode.
const inkTol = 6

// bgPixel is the untouched background, read from the top-left corner: padding means no
// pane draws there. Sampled rather than computed, so the ink metrics do not depend on the
// transfer function being right — that is TestSrgbTargetKeepsTheConfiguredColour's job.
func bgPixel(img *image.RGBA) [4]uint8 { return pixelAt(img, 0, 0) }

// inkDist is how far a pixel sits from the background, summed over the three channels.
func inkDist(got, bg [4]uint8) int {
	d := 0
	for i := range 3 {
		d += max(int(got[i])-int(bg[i]), int(bg[i])-int(got[i]))
	}
	return d
}

// inkPixels counts the pixels in r that differ from the background.
func inkPixels(img *image.RGBA, r image.Rectangle) int {
	bg, n := bgPixel(img), 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if inkDist(pixelAt(img, x, y), bg) > inkTol {
				n++
			}
		}
	}
	return n
}

// inkMass is the total distance from the background over r — how much ink the glyphs put
// on the paper, which is what bending the coverage curve changes.
func inkMass(img *image.RGBA, r image.Rectangle) int {
	bg, sum := bgPixel(img), 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sum += inkDist(pixelAt(img, x, y), bg)
		}
	}
	return sum
}

// maxInk is the largest distance from the background inside r — how visible whatever was
// drawn there is, which is what dimming and a hollow cursor change.
func maxInk(img *image.RGBA, r image.Rectangle) int {
	bg, m := bgPixel(img), 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			m = max(m, inkDist(pixelAt(img, x, y), bg))
		}
	}
	return m
}

// TestCursorSplitsLigature: a block covers the cell it sits on, so that cell has to
// come out of its ligature or the arrow next door paints straight over the cursor.
func TestCursorSplitsLigature(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	row := cellsOf("a=>b")
	ligated := slices.Clone(txt.shapeRow(row, 4, nil))
	onArrow := slices.Clone(txt.shapeRowSplit(row, 4, 1, nil))
	away := slices.Clone(txt.shapeRowSplit(row, 4, 3, nil))

	if len(ligated) != 4 || len(onArrow) != 4 {
		t.Fatalf("shaped %v and %v, want four glyphs each — one per cell", ligated, onArrow)
	}

	wantEq, _ := txt.fm.GlyphIndex(font.Regular, '=')
	wantGt, _ := txt.fm.GlyphIndex(font.Regular, '>')
	if onArrow[1] != wantEq || onArrow[2] != wantGt {
		t.Errorf("with the cursor on the '=' the row shaped %v, want the plain %d %d in the middle",
			onArrow, wantEq, wantGt)
	}
	if slices.Equal(ligated, onArrow) {
		t.Errorf("the split shaped the same glyphs %v as the ligated row: calt was not broken", ligated)
	}
	if !slices.Equal(ligated, away) {
		t.Errorf("a cursor two cells away changed the row to %v, want the ligated %v", away, ligated)
	}
}

// TestCursorSplitLeavesTheCacheLigated: the split runs outside the scrollback's cache
// on purpose, so the row is whole again the moment the cursor moves off it.
func TestCursorSplitLeavesTheCacheLigated(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 200, 100), 4, 1, "a=>b")
	shape := func(cells []cell, dst []font.GID) []font.GID { return txt.shapeRow(cells, p.cols, dst) }
	row, gen := p.rowAt(0)
	want := slices.Clone(row.shaped(gen, shape))

	p.cursor.shown = true
	p.scr.curCol = 1
	txt.Layout([]*pane{p}, p)

	row, gen = p.rowAt(0)
	if got := row.shaped(gen, shape); !slices.Equal(got, want) {
		t.Errorf("a frame with the cursor on the ligature left %v in the cache, want the ligated %v", got, want)
	}
}

// TestCursorInvertsTheCellGlyph checks the colour Layout gives the covered glyph,
// which is the half of the inversion the rect renderer cannot do.
func TestCursorInvertsTheCellGlyph(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 400, 100), 10, 1, "abcdefghij")
	p.cursor.shown = true
	p.scr.curRow, p.scr.curCol = 0, 2

	txt.Layout([]*pane{p}, p)
	if p.count != 10 {
		t.Fatalf("laid out %d quads for ten cells; the indices below would not line up", p.count)
	}
	if got := txt.instances[2].color; got != backgroundRGBA {
		t.Errorf("the covered glyph is %v, want the background %v", got, backgroundRGBA)
	}
	if got := txt.instances[1].color; got != foreground {
		t.Errorf("the cell beside the cursor is %v, want the plain foreground %v", got, foreground)
	}

	// An unfocused pane draws a rim instead of a fill, so nothing is covered and
	// nothing may be inverted.
	other := gridPane(2, image.Rect(400, 0, 800, 100), 10, 1, "abcdefghij")
	txt.Layout([]*pane{p, other}, other)
	if got := txt.instances[2].color; got != dim(foreground) {
		t.Errorf("an unfocused pane inverted its cursor cell to %v, want the dimmed %v", got, dim(foreground))
	}
}

// TestCursorRendersFilledAndHollow is the pixel half: the focused pane's block is
// solid with the glyph punched out of it, the unfocused pane's is a rim around an
// untouched cell.
func TestCursorRendersFilledAndHollow(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)
	rct, err := newRects(device, queue, testFormat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rct.release)

	cellW, cellH := txt.CellSize()
	// The unfocused cursor sits on the space, so its cell holds no ink of its own and
	// the rim is the only thing in it.
	const row = "MM MMMMM"
	focused := gridPane(1, image.Rect(0, 0, testWidth/2, 200), 8, 1, row)
	unfocused := gridPane(2, image.Rect(testWidth/2, 0, testWidth, 200), 8, 1, row)
	focused.cursor.shown, unfocused.cursor.shown = true, true
	focused.scr.curRow, focused.scr.curCol = 0, 1     // on an 'M'
	unfocused.scr.curRow, unfocused.scr.curCol = 0, 2 // on the space
	panes := []*pane{focused, unfocused}

	txt.Layout(panes, focused)
	fills, rims := cursorRects(panes, focused, cellW, cellH)
	rct.Reset()
	rct.Add(fills, cursorColor)
	rct.Add(rims, dim(cursorColor))
	rct.Upload()

	img := renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
		rct.Draw(pass, testWidth, testHeight)
		txt.Draw(pass, panes, testWidth, testHeight)
	})

	cell, ok := focused.cursorCell(cellW, cellH)
	if !ok {
		t.Fatal("the focused pane reports no cursor cell")
	}
	area, ink := cellW*cellH, inkPixels(img, cell)
	if ink*2 <= area {
		t.Errorf("the focused cursor cell has %d of %d pixels drawn on; the block did not draw", ink, area)
	}
	// The 'M' punches about 36 of the 220 pixels back to the background colour. A block
	// drawn without the inversion would leave the cell solid.
	//
	// Which also cross-checks the two routes a colour takes to the target: the glyph's
	// interior is backgroundRGBA through text.wgsl, the clear is the same colour through
	// clearValue. If only one of them decoded, those pixels would sit 7 counts off the
	// clear and be counted as ink instead.
	if blank := area - ink; blank < 10 {
		t.Errorf("only %d of %d pixels in the focused cursor cell are the background; the glyph was not inverted into the block", blank, area)
	}

	cell, ok = unfocused.cursorCell(cellW, cellH)
	if !ok {
		t.Fatal("the unfocused pane reports no cursor cell")
	}
	rim := image.Rect(cell.Min.X, cell.Min.Y, cell.Max.X, cell.Min.Y+cursorOutlineWidth)
	inside, edge := maxInk(img, cell.Inset(cursorOutlineWidth)), maxInk(img, rim)
	if edge <= inside {
		t.Errorf("the unfocused cursor peaks at %d on its rim and %d inside; want a hollow box", edge, inside)
	}

	f, _ := os.Create("/tmp/gty-cursor.png")
	defer f.Close()
	png.Encode(f, img)
}

// TestRenderToPNG draws the frame main() draws — two panes and the divider between
// them — and reads it back so the result can be looked at.
func TestRenderToPNG(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)
	rct, err := newRects(device, queue, testFormat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rct.release)

	panes, dividers := splitLayout(t, txt, testWidth, testHeight)
	txt.Layout(panes, panes[0])
	rct.Set(dividers, selectionColor)

	a := txt.fm.Atlas
	t.Logf("instances: %d | panes %d | dividers %d | atlas %dx%d = %d KiB | glyphs %d | slot %dx%d",
		len(txt.instances), len(panes), len(dividers),
		a.Img.Rect.Dx(), a.Img.Rect.Dy(), len(a.Img.Pix)/1024,
		len(a.Glyphs()), a.SlotW, a.SlotH)

	// Contiguous, disjoint ranges covering the buffer are what makes one Draw per
	// pane with firstInstance correct.
	var next uint32
	for _, p := range panes {
		if p.count == 0 {
			t.Errorf("pane %d (%v, %dx%d cells) drew nothing", p.id, p.rect, p.cols, p.rows)
		}
		if p.first != next {
			t.Errorf("pane %d starts at %d, want %d", p.id, p.first, next)
		}
		next = p.first + p.count
	}
	if int(next) != len(txt.instances) {
		t.Errorf("panes cover %d instances of %d", next, len(txt.instances))
	}

	img := renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
		rct.Draw(pass, testWidth, testHeight)
		txt.Draw(pass, panes, testWidth, testHeight)
	})

	ink := inkPixels(img, img.Rect)
	t.Logf("pixels differing from the background: %d", ink)
	if ink == 0 {
		t.Error("nothing was drawn")
	}

	// A divider is an opaque quad of full coverage, so its pixel is exactly the colour
	// it was given — which makes this a second, free guard against a missing decode.
	div := dividers[0]
	if got, want := pixelAt(img, div.Min.X, testHeight/2), pixelOf(selectionColor); !near(got, want, 2) {
		t.Errorf("divider column %d is %v, want the divider colour %v", div.Min.X, got, want)
	}

	// Both panes hold the same text, so they can only differ if Layout dimmed the second.
	// Only that they differ: dim() multiplies towards black, so on a light theme the
	// dimmed pane sits further from the background rather than nearer, and asserting a
	// direction here would be asserting the theme.
	focused, unfocused := maxInk(img, panes[0].rect), maxInk(img, panes[1].rect)
	t.Logf("ink furthest from the background: focused %d, unfocused %d", focused, unfocused)
	if focused == unfocused {
		t.Errorf("both panes peak at %d; the unfocused one was not dimmed", focused)
	}

	// The exact colours, which pixels cannot give: a glyph edge is a blend, and the
	// extreme pixel of a light stem need not reach full coverage. Same reason
	// TestCursorInvertsTheCellGlyph works on instances.
	if got := txt.instances[panes[0].first].color; got != foreground {
		t.Errorf("the focused pane draws in %v, want the foreground %v", got, foreground)
	}
	if got, want := txt.instances[panes[1].first].color, dim(foreground); got != want {
		t.Errorf("the unfocused pane draws in %v, want it dimmed to %v", got, want)
	}

	f, _ := os.Create("/tmp/gty-frame.png")
	defer f.Close()
	png.Encode(f, img)
}

// TestPaneContentIsIndependent gives two panes the same geometry and different
// content.
func TestPaneContentIsIndependent(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	const row = "one"
	long := make([]string, 12)
	for i := range long {
		long[i] = row
	}
	left := gridPane(1, image.Rect(0, 0, 300, 400), 40, 20, row)
	right := gridPane(2, image.Rect(300, 0, 600, 400), 40, 20, long...)

	txt.Layout([]*pane{left, right}, left)

	if want := uint32(len(row)); left.count != want {
		t.Errorf("left pane laid out %d quads, want %d", left.count, want)
	}
	if want := uint32(len(long) * len(row)); right.count != want {
		t.Errorf("right pane laid out %d quads, want %d", right.count, want)
	}
}

// TestScreenWrapsRatherThanClips: writing past the right edge is a wrap now, not a
// truncation. A 25-character line into a ten-column pane is three screen rows, and the
// first two have been pushed into the history.
func TestScreenWrapsRatherThanClips(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 200, 300), 10, 5, strings.Repeat("=", 25))
	txt.Layout([]*pane{p}, p)

	if got, want := viewText(p)[:3], []string{"==========", "==========", "====="}; !slices.Equal(got, want) {
		t.Errorf("the wrapped line reads %q, want %q", got, want)
	}
	if p.count != 25 {
		t.Errorf("a 25-character line laid out %d quads, want 25", p.count)
	}
}

// TestHistoryClipsToPaneColumns: a pane that has narrowed keeps history rows wider
// than it is — there is no reflow — so the renderer has to cut them at the grid.
func TestHistoryClipsToPaneColumns(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 400, 100), 30, 1, strings.Repeat("=", 30), "next")
	if p.buf.Len() == 0 {
		t.Fatal("nothing reached the history; the fixture proves nothing")
	}
	if got := len(p.buf.Row(0).cells); got != 30 {
		t.Fatalf("the history row holds %d cells, want the 30 it was written at", got)
	}

	p.setGrid(10, 1)
	p.scrollBy(1) // back onto the wide history row
	txt.Layout([]*pane{p}, p)

	if p.count != 10 {
		t.Errorf("a 30-cell history row in a 10-column pane laid out %d quads, want 10", p.count)
	}
}

// TestScrollRendersDifferentLines: the same pane at two scroll positions is two
// different frames, and a full history still costs only a screenful of quads.
func TestScrollRendersDifferentLines(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p, _ := scrollPane(txt)
	draw := func(pass *wgpu.RenderPassEncoder) { txt.Draw(pass, []*pane{p}, testWidth, testHeight) }

	txt.Layout([]*pane{p}, p)
	if int(p.count) > p.cols*p.rows {
		t.Errorf("a %dx%d pane over %d lines laid out %d quads", p.cols, p.rows, p.buf.Len(), p.count)
	}
	tail := renderOffscreen(t, device, queue, draw)

	if !p.scrollBy(20) {
		t.Fatal("a full history did not scroll")
	}
	txt.Layout([]*pane{p}, p)
	back := renderOffscreen(t, device, queue, draw)

	if bytes.Equal(tail.Pix, back.Pix) {
		t.Error("scrolling back 20 lines rendered the same frame")
	}
}

// scrollPane is a full history over a screen-sized grid: the shape a wheel notch
// has to keep up with.
func scrollPane(txt *text) (*pane, []*pane) {
	cellW, cellH := txt.CellSize()
	p := gridPane(1, image.Rect(0, 0, testWidth, testHeight),
		(testWidth-2*padding)/cellW, (testHeight-2*padding)/cellH)
	fillDemo(p)
	return p, []*pane{p}
}

// BenchmarkScrollLayout is a wheel notch over history that has been shaped already,
// so it measures the quad emit alone.
func BenchmarkScrollLayout(b *testing.B) {
	device, queue := newTestGPU(b)
	txt := newTestText(b, device, queue)
	p, panes := scrollPane(txt)
	txt.Layout(panes, p)

	for b.Loop() {
		if !p.scrollBy(wheelLines) {
			p.scroll = 0
		}
		txt.Layout(panes, p)
	}
}

// BenchmarkScrollLayoutCold reshapes the whole screen every notch — what scrolling
// would cost without the cache in the scrollback.
func BenchmarkScrollLayoutCold(b *testing.B) {
	device, queue := newTestGPU(b)
	txt := newTestText(b, device, queue)
	p, panes := scrollPane(txt)

	for b.Loop() {
		// Two width changes leave cols where it was and every cached row stale.
		p.buf.setCols(p.cols + 1)
		p.buf.setCols(p.cols)
		txt.Layout(panes, p)
	}
}

// TestLayoutGrowsVertexBuffer covers the overflow path: a grid larger than the
// initial buffer grows it instead of losing its tail.
func TestLayoutGrowsVertexBuffer(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	const (
		rows = 210
		cols = 80
	)
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = strings.Repeat("x", cols)
	}
	p := gridPane(1, image.Rect(0, 0, testWidth, testHeight), cols, rows, lines...)

	txt.Layout([]*pane{p}, p)

	if want := uint32(rows * cols); p.count != want {
		t.Fatalf("laid out %d quads, want %d — none may be dropped", p.count, want)
	}
	if len(txt.instances) <= initialInstances {
		t.Fatalf("%d instances does not exceed the initial capacity %d; the test proves nothing",
			len(txt.instances), initialInstances)
	}
	if txt.bufCap < len(txt.instances) {
		t.Errorf("buffer holds %d instances, need %d", txt.bufCap, len(txt.instances))
	}

	// The grown buffer still has to work as a vertex source.
	img := renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
		txt.Draw(pass, []*pane{p}, testWidth, testHeight)
	})
	if inkPixels(img, img.Rect) == 0 {
		t.Error("nothing was drawn from the grown buffer")
	}
}

// TestNonASCIIRenders is the regression for a terminal that swallowed most of Unicode:
// the atlas used to hold printable ASCII and the GSUB outputs and nothing else, so
// Cyrillic, box drawing and the tick a shell prints were all present in the font,
// absent from the sheet, and drawn as nothing at all.
func TestNonASCIIRenders(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	const line = "привет ┌─┐ héllo"
	p := gridPane(1, image.Rect(0, 0, testWidth, 200), 40, 2, line)
	txt.Layout([]*pane{p}, p)

	if want := uint32(len([]rune(line))); p.count != want {
		t.Fatalf("laid out %d quads for %d runes", p.count, want)
	}

	// Every rune has to land on its own slot; the replacement box would mean the sheet
	// still cannot reach them.
	notdefU, notdefV := txt.fm.Atlas.Ensure(font.Key{Style: font.Regular, GID: 0})
	for i, inst := range txt.instances[:p.count] {
		r := []rune(line)[i]
		if r == ' ' {
			continue
		}
		if inst.uv[0] == notdefU && inst.uv[1] == notdefV {
			t.Errorf("rune %q drew the replacement box; the face has it and the sheet should too", r)
		}
	}

	img := renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
		txt.Draw(pass, []*pane{p}, testWidth, testHeight)
	})
	if ink := inkPixels(img, img.Rect); ink == 0 {
		t.Error("nothing was drawn")
	}
}

// TestMissingGlyphDrawsABox: a rune the face genuinely lacks — a Nerd Font icon — has
// to leave a mark. Drawing nothing is indistinguishable from a space, which is what
// made a prompt full of them look merely empty.
func TestMissingGlyphDrawsABox(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	if _, ok := txt.fm.GlyphIndex(font.Regular, '\U000f10fe'); ok {
		t.Skip("the embedded face has Nerd Font icons after all")
	}
	p := gridPane(1, image.Rect(0, 0, testWidth, 200), 40, 2, "a\U000f10feb")
	txt.Layout([]*pane{p}, p)

	if p.count != 3 {
		t.Fatalf("laid out %d quads for three cells", p.count)
	}
	u, v := txt.fm.Atlas.Ensure(font.Key{Style: font.Regular, GID: 0})
	if got := txt.instances[1].uv; got[0] != u || got[1] != v {
		t.Errorf("the missing rune drew from %v, want the replacement box at %v,%v", got[:2], u, v)
	}
}

// TestSrgbTargetKeepsTheConfiguredColour is the regression for the double encode. A target
// in an *UnormSrgb format applies the sRGB transfer function on write, so a colour handed
// over already encoded comes back gamma-brightened: #424242 as #8b8b8b, #f2f2f2 as
// #f9f9f9, the whole theme washed out. Both routes a colour can take to the target are
// covered — the shader's, and the clear value's, which no shader ever sees.
//
// Full coverage and opaque alpha on purpose: no blending and no antialiasing enter the
// expected values, so the only thing this can be measuring is the transfer function.
func TestSrgbTargetKeepsTheConfiguredColour(t *testing.T) {
	if !isSrgbFormat(testFormat) {
		t.Fatalf("testFormat %v does not encode; the test proves nothing", testFormat)
	}
	device, queue := newTestGPU(t)
	rct, err := newRects(device, queue, testFormat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rct.release)

	// Two quads: a colour straight from the theme, and one dim() computed from it. The
	// second is the one that proves the decode applies to whatever the renderer works
	// out at run time, not only to the literals in the theme.
	rct.Reset()
	rct.Add([]image.Rectangle{image.Rect(0, 0, testWidth, testHeight/4)}, foreground)
	rct.Add([]image.Rectangle{image.Rect(0, testHeight/4, testWidth, testHeight/2)}, dim(foreground))
	rct.Upload()
	img := renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
		rct.Draw(pass, testWidth, testHeight)
	})

	if got, want := pixelAt(img, testWidth/2, testHeight/8), pixelOf(foreground); !near(got, want, 2) {
		t.Errorf("a quad of %v came back %v, want %v; a red of 0x8b means the shader is not decoding",
			foreground, got, want)
	}
	if got, want := pixelAt(img, testWidth/2, testHeight*3/8), pixelOf(dim(foreground)); !near(got, want, 2) {
		t.Errorf("a dimmed quad came back %v, want %v; dim() runs in sRGB and has to survive the decode",
			got, want)
	}
	if got, want := pixelAt(img, testWidth/2, testHeight*3/4), pixelOf(backgroundRGBA); !near(got, want, 2) {
		t.Errorf("the clear came back %v, want the configured background %v; 0xf9 means clearValue is not decoding",
			got, want)
	}
}

// TestCoverageGammaThickensOnlyTheEdges is the point of bending the coverage curve: the
// partial pixels around a stem gain weight, and the fully covered core does not move. An
// exponent leaves 0 and 1 alone, so anything else here means the curve is being applied
// as a scale somewhere.
func TestCoverageGammaThickensOnlyTheEdges(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	// Letters with plenty of edge: round sides and diagonals, not just uprights.
	p := gridPane(1, image.Rect(0, 0, testWidth, 200), 8, 1, "eaoswzgm")
	txt.Layout([]*pane{p}, p)
	render := func(exp float32) *image.RGBA {
		was := coverageExp
		coverageExp = exp
		defer func() { coverageExp = was }()
		return renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
			txt.Draw(pass, []*pane{p}, testWidth, testHeight)
		})
	}

	plain, bent := render(1), render(coverageExponent(foreground, backgroundRGBA, 0))
	row := image.Rect(0, 0, testWidth, 200)

	if got, want := inkMass(bent, row), inkMass(plain, row); got <= want {
		t.Errorf("bent coverage put %d of ink on the paper against %d untouched; the edges did not thicken",
			got, want)
	}
	// The core is a fixed point: the darkest pixel in the row cannot move.
	if got, want := maxInk(bent, row), maxInk(plain, row); got != want {
		t.Errorf("the darkest pixel went from %d to %d; the curve moved the core, not the edges",
			want, got)
	}
	t.Logf("ink mass %d -> %d (%.0f%%), darkest %d either way",
		inkMass(plain, row), inkMass(bent, row),
		100*float64(inkMass(bent, row))/float64(inkMass(plain, row)), maxInk(plain, row))
}
