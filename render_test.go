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
	"gty/internal/vte"
)

const (
	testWidth  = 576 // 576*4 = 2304 bytes/row, a multiple of the required 256
	testHeight = 680
	// An sRGB format, so the offscreen path exercises the transfer function the window does.
	// RGBA rather than BGRA: the read-back is an *image.RGBA, and BGRA would swap R and B
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
	return newTestTextIn(t, device, queue, testFormat)
}

// newTestTextIn also points the coverage exponent at the format's blend space. The app
// does this in newApp once the surface is configured; without it a test renders to one
// space with the curve derived for the other.
func newTestTextIn(t testing.TB, device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat) *text {
	t.Helper()
	was := blendUsed
	blendUsed = spaceOf(format)
	refreshTheme()
	t.Cleanup(func() { blendUsed = was; refreshTheme() })

	txt, err := newText(device, queue, format, fontSize, 1)
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
	return renderOffscreenTo(t, device, queue, testFormat, draw)
}

// renderOffscreenTo is renderOffscreen against a stated format, for the tests that
// compare the two blend spaces. Both are RGBA8, so the read-back layout is the same.
func renderOffscreenTo(t *testing.T, device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat, draw func(pass *wgpu.RenderPassEncoder)) *image.RGBA {
	t.Helper()

	extent := wgpu.Extent3D{Width: testWidth, Height: testHeight, DepthOrArrayLayers: 1}
	target, err := device.TryCreateTexture(&wgpu.TextureDescriptor{
		Size:          extent,
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     wgpu.TextureDimension2D,
		Format:        format,
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
			ClearValue: clearValue(backgroundRGBA, isSrgbFormat(format)),
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

// Pixels are four bytes, not a color.RGBA: this package has a colour type of its own, so
// image/color cannot be imported without an alias.

func pixelAt(img *image.RGBA, x, y int) [4]uint8 {
	c := img.RGBAAt(x, y)
	return [4]uint8{c.R, c.G, c.B, c.A}
}

// pixelOf is a renderer colour as it lands in memory: at full coverage the GPU's encode and
// the shader's decode cancel, so it comes back as the byte that was configured.
func pixelOf(c [4]float32) [4]uint8 {
	q := func(v float32) uint8 { return uint8(min(max(v, 0), 1)*255 + 0.5) }
	return [4]uint8{q(c[0]), q(c[1]), q(c[2]), q(c[3])}
}

// near allows tol counts of slack: two implementations of the curve (float64 in Go, f32 in
// WGSL) and the GPU's encode need not agree to the last bit.
func near(got, want [4]uint8, tol int) bool {
	for i := range got {
		if int(got[i])-int(want[i]) > tol || int(want[i])-int(got[i]) > tol {
			return false
		}
	}
	return true
}

// Ink is distance from the background, not brightness: brightness only stands in for
// "something was drawn" on a dark theme, and on a light one the brightest thing is the paper.

// inkTol is the transfer function's rounding and nothing more — an antialiased edge is meant
// to count as ink, and so is a colour some shader forgot to decode.
const inkTol = 6

// bgPixel is the untouched background from the top-left corner, where padding means no pane
// draws. Sampled, not computed, so these metrics do not depend on the transfer function.
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

// inkMass is how much ink the glyphs put on the paper, which is what the coverage curve
// changes.
func inkMass(img *image.RGBA, r image.Rectangle) int {
	bg, sum := bgPixel(img), 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sum += inkDist(pixelAt(img, x, y), bg)
		}
	}
	return sum
}

// maxInk is how visible whatever was drawn is, which is what dimming and a hollow cursor
// change.
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

// TestCursorSplitLeavesTheCacheLigated: the split runs outside the row cache on purpose, so
// the row is whole again the moment the cursor moves off it.
func TestCursorSplitLeavesTheCacheLigated(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 200, 100), 4, 1, "a=>b")
	shape := func(cells []vte.Cell, dst []font.GID) []font.GID { return txt.shapeRow(cells, p.cols, dst) }
	want := slices.Clone(p.cache.shaped(&p.frame.Lines[0], shape))

	placeCursor(p, 0, 1)
	layout(txt, []*pane{p}, p, nil)

	if got := p.cache.shaped(&p.frame.Lines[0], shape); !slices.Equal(got, want) {
		t.Errorf("a frame with the cursor on the ligature left %v in the cache, want the ligated %v", got, want)
	}
}

// The bend a light theme applies to text has nothing to fix on a frame's straight runs
// and would only fatten the antialiasing on its arcs.
func TestBoxDrawingSkipsTheCoverageBend(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 400, 100), 6, 1, "╭──╮ab")
	layout(txt, []*pane{p}, p, nil)
	if p.count != 6 {
		t.Fatalf("laid out %d quads for six cells; the indices below would not line up", p.count)
	}
	for i := range 4 {
		if got := txt.instances[i].darken; got != 0 {
			t.Errorf("cell %d of the frame asks for the bend (darken=%v), want 0", i, got)
		}
	}
	if got := txt.instances[4].darken; got != 1 {
		t.Errorf("the text beside the frame is darken=%v, want the theme's bend", got)
	}
}

// TestCursorInvertsTheCellGlyph checks the colour Layout gives the covered glyph,
// which is the half of the inversion the rect renderer cannot do.
func TestCursorInvertsTheCellGlyph(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 400, 100), 10, 1, "abcdefghij")
	placeCursor(p, 0, 2)

	layout(txt, []*pane{p}, p, nil)
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
	layout(txt, []*pane{p, other}, other, nil)
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
	placeCursor(focused, 0, 1)   // on an 'M'
	placeCursor(unfocused, 0, 2) // on the space
	panes := []*pane{focused, unfocused}

	layout(txt, panes, focused, nil)
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
	// The 'M' punches about 36 of the 220 pixels back to the background. Also cross-checks the two routes a colour takes to the target: the glyph's
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
	layout(txt, panes, panes[0], nil)
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

	// An opaque quad of full coverage, so its pixel is exactly the colour it was given — a
	// free second guard against a missing decode.
	div := dividers[0]
	if got, want := pixelAt(img, div.Min.X, testHeight/2), pixelOf(selectionColor); !near(got, want, 2) {
		t.Errorf("divider column %d is %v, want the divider colour %v", div.Min.X, got, want)
	}

	// Both panes hold the same text, so they can only differ if Layout dimmed the second.
	// Only that they differ: dim() multiplies towards black, so on a light theme the dimmed
	// pane sits further from the background, and a direction here would assert the theme.
	focused, unfocused := maxInk(img, panes[0].rect), maxInk(img, panes[1].rect)
	t.Logf("ink furthest from the background: focused %d, unfocused %d", focused, unfocused)
	if focused == unfocused {
		t.Errorf("both panes peak at %d; the unfocused one was not dimmed", focused)
	}

	// The exact colours, which pixels cannot give: a glyph edge is a blend and a light stem
	// need not reach full coverage.
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

	layout(txt, []*pane{left, right}, left, nil)

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
	layout(txt, []*pane{p}, p, nil)

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
	if p.frame.HistLen == 0 {
		t.Fatal("nothing reached the history; the fixture proves nothing")
	}
	if got := len(p.term.GetViewport(0, 1)[0].Cells); got != 30 {
		t.Fatalf("the history row holds %d cells, want the 30 it was written at", got)
	}

	p.setGrid(10, 1)
	p.scrollBy(1) // back onto the wide history row
	layout(txt, []*pane{p}, p, nil)

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

	layout(txt, []*pane{p}, p, nil)
	if int(p.count) > p.cols*p.rows {
		t.Errorf("a %dx%d pane over %d lines laid out %d quads", p.cols, p.rows, p.frame.HistLen, p.count)
	}
	tail := renderOffscreen(t, device, queue, draw)

	if !p.scrollBy(20) {
		t.Fatal("a full history did not scroll")
	}
	layout(txt, []*pane{p}, p, nil)
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
	layout(txt, panes, p, nil)

	for b.Loop() {
		if !p.scrollBy(wheelLines) {
			p.scroll = 0
		}
		layout(txt, panes, p, nil)
	}
}

// BenchmarkScrollLayoutCold reshapes the whole screen every frame — what a frame would cost
// without the row cache.
func BenchmarkScrollLayoutCold(b *testing.B) {
	device, queue := newTestGPU(b)
	txt := newTestText(b, device, queue)
	p, panes := scrollPane(txt)

	for b.Loop() {
		p.cache.reset() // every cached row stale, as a width change would leave them
		layout(txt, panes, p, nil)
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

	layout(txt, []*pane{p}, p, nil)

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
	layout(txt, []*pane{p}, p, nil)

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
	layout(txt, []*pane{p}, p, nil)

	if p.count != 3 {
		t.Fatalf("laid out %d quads for three cells", p.count)
	}
	u, v := txt.fm.Atlas.Ensure(font.Key{Style: font.Regular, GID: 0})
	if got := txt.instances[1].uv; got[0] != u || got[1] != v {
		t.Errorf("the missing rune drew from %v, want the replacement box at %v,%v", got[:2], u, v)
	}
}

// TestSrgbTargetKeepsTheConfiguredColour is the regression for the double encode: a colour
// handed to an *UnormSrgb target already encoded comes back gamma-brightened, #424242 as
// #8b8b8b. Both routes are covered — the shader's, and the clear value's.
//
// Full coverage and opaque alpha, so no blending or antialiasing enters the expected values.
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

	// Two quads: a theme colour, and one dim() computed from it — the second proves the
	// decode applies to what the renderer works out at run time, not just to literals.
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

// TestCoverageGammaLeavesIconsAlone: the curve is for text stems. An icon is line art at
// twice that size, where the same bend fills the counters in.
func TestCoverageGammaLeavesIconsAlone(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)
	cellW, cellH := txt.CellSize()

	// Two cells of room after the icon: it is scaled to the line's height and overhangs.
	p := gridPane(1, image.Rect(0, 0, testWidth, 200), 12, 1, "\U0000F015   eaosw")
	layout(txt, []*pane{p}, p, nil)
	render := func(exp float32) *image.RGBA {
		was := coverageExp
		coverageExp = exp
		defer func() { coverageExp = was }()
		return renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
			txt.Draw(pass, []*pane{p}, testWidth, testHeight)
		})
	}

	plain, bent := render(1), render(coverageExponent(foreground, backgroundRGBA, 0, blendLinear))
	// From zero, not from the cell: the overhang reaches back into the padding.
	icon := image.Rect(0, padding, padding+3*cellW, padding+cellH)
	letters := image.Rect(padding+4*cellW, padding, padding+12*cellW, padding+cellH)

	if inkMass(plain, icon) == 0 {
		t.Fatal("no icon was drawn; the rest of this test would pass on an empty cell")
	}
	if got, want := inkMass(bent, icon), inkMass(plain, icon); got != want {
		t.Errorf("the icon carries %d of ink against %d untouched; the curve reached it", got, want)
	}
	if got, want := inkMass(bent, letters), inkMass(plain, letters); got <= want {
		t.Errorf("the letters carry %d of ink against %d untouched; the curve stopped reaching text too", got, want)
	}
}

// TestCoverageGammaThickensOnlyTheEdges: the partial pixels around a stem gain weight and
// the core does not move. An exponent fixes 0 and 1, so anything else means it is being
// applied as a scale.
func TestCoverageGammaThickensOnlyTheEdges(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	// Letters with plenty of edge: round sides and diagonals, not just uprights.
	p := gridPane(1, image.Rect(0, 0, testWidth, 200), 8, 1, "eaoswzgm")
	layout(txt, []*pane{p}, p, nil)
	render := func(exp float32) *image.RGBA {
		was := coverageExp
		coverageExp = exp
		defer func() { coverageExp = was }()
		return renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
			txt.Draw(pass, []*pane{p}, testWidth, testHeight)
		})
	}

	plain, bent := render(1), render(coverageExponent(foreground, backgroundRGBA, 0, blendLinear))
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

// TestTabBarRenders draws a two-tab frame: the active tab is filled, its label is
// undimmed, and the panes sit clear of the bar.
func TestTabBarRenders(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)
	rct, err := newRects(device, queue, testFormat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rct.release)

	cellW, cellH := txt.CellSize()
	surface := image.Rect(0, 0, testWidth, testHeight)
	tabs := []*tab{tabOf1(1), tabOf1(2)}
	setTitle(tabs[0].focused, "fish")
	setTitle(tabs[1].focused, "htop")

	bar, content := splitBar(surface, len(tabs), cellH)
	if bar.Empty() {
		t.Fatalf("two tabs in a %v surface got no bar", surface)
	}
	for _, tb := range tabs {
		tb.panes, _ = layoutTree(tb.root, content, cellW, cellH)
		feedText(tb.panes[0], "hello")
	}

	const active = 1
	fills, labels := layoutBar(tabs, active, bar, cellW, cellH)
	panes := tabs[active].panes
	layout(txt, panes, panes[0], labels)
	rct.Reset()
	rct.AddQuads(fills)
	rct.Upload()

	// The chrome runs come first and the panes follow, contiguously: one Draw per group
	// with firstInstance is only correct if the ranges are disjoint and in order.
	var next uint32
	for i, c := range txt.chrome {
		if c.count == 0 {
			t.Errorf("label %d drew nothing", i)
		}
		if c.first != next {
			t.Errorf("label %d starts at %d, want %d", i, c.first, next)
		}
		next = c.first + c.count
	}
	for _, p := range panes {
		if p.first != next {
			t.Errorf("pane %d starts at %d, want %d", p.id, p.first, next)
		}
		next = p.first + p.count
	}
	if int(next) != len(txt.instances) {
		t.Errorf("the groups cover %d instances of %d", next, len(txt.instances))
	}

	img := renderOffscreen(t, device, queue, func(pass *wgpu.RenderPassEncoder) {
		rct.Draw(pass, testWidth, testHeight)
		txt.Draw(pass, panes, testWidth, testHeight)
	})

	// base is the divider's underside; the bar keeps dividerPad of air below it.
	slice, beyond := labels[active].rect, bar.Max.X-px(dividerInset)-1
	base := bar.Max.Y - px(dividerPad)
	if got, want := pixelAt(img, slice.Min.X+1, base-1), pixelOf(palette[tabAccent]); !near(got, want, 2) {
		t.Errorf("under the active tab is %v, want the underline %v", got, want)
	}
	if got, want := pixelAt(img, beyond, base-1), pixelOf(selectionColor); !near(got, want, 2) {
		t.Errorf("past the last tab the line is %v, want the divider %v", got, want)
	}
	if got, want := pixelAt(img, bar.Max.X-1, base-1), pixelOf(backgroundRGBA); !near(got, want, 2) {
		t.Errorf("the far edge is %v, want the divider inset off it", got)
	}
	// Lighter under the line than on it, and the terminal starts clear of both.
	if got, want := pixelAt(img, beyond, base), pixelOf(mix(selectionColor, backgroundRGBA, dividerFade)); !near(got, want, 2) {
		t.Errorf("under the divider is %v, want the lighter row %v", got, want)
	}
	if got, want := pixelAt(img, beyond, bar.Max.Y-1), pixelOf(backgroundRGBA); !near(got, want, 2) {
		t.Errorf("the gap before the terminal is %v, want the background %v", got, want)
	}
	// One pixel above the divider is the underline's alone; the divider is thinner.
	above := base - px(dividerWidth) - 1
	if got, want := pixelAt(img, slice.Min.X+1, above), pixelOf(palette[tabAccent]); !near(got, want, 2) {
		t.Errorf("the underline is %v at y %d, want it thicker than the divider", got, above)
	}
	if got, want := pixelAt(img, beyond, above), pixelOf(backgroundRGBA); !near(got, want, 2) {
		t.Errorf("the bar is %v above the divider at its far end, want the background %v", got, want)
	}

	for i, l := range labels {
		if ink := inkPixels(img, l.rect); ink == 0 {
			t.Errorf("tab %d drew no label", i)
		}
	}
	for i, c := range txt.chrome {
		if got := txt.instances[c.first].color; got != foreground {
			t.Errorf("tab %d's label is %v, want the plain foreground %v", i, got, foreground)
		}
	}

	// The bar is chrome: no pane may reach into it, and no glyph may be drawn over it.
	for _, p := range panes {
		if p.rect.Min.Y < bar.Max.Y {
			t.Errorf("pane %d starts at y %d, inside a bar ending at %d", p.id, p.rect.Min.Y, bar.Max.Y)
		}
	}

	f, _ := os.Create("/tmp/gty-tabs.png")
	defer f.Close()
	png.Encode(f, img)
}

// chroma is how far a pixel is from grey: the spread of its channels.
func chroma(p [4]uint8) int {
	lo, hi := int(p[0]), int(p[0])
	for _, v := range p[:3] {
		lo, hi = min(lo, int(v)), max(hi, int(v))
	}
	return hi - lo
}

// TestBlendSpaceKeepsColourSaturated is the regression guard for the whole point of
// blendGamma: linear light mixes a half-covered pixel towards grey, so a teal glyph
// antialiases to something nearly colourless. Same glyph, same colours, two formats,
// each with the coverage curve its own space wants.
func TestBlendSpaceKeepsColourSaturated(t *testing.T) {
	device, queue := newTestGPU(t)

	const ink = "#007474"
	render := func(format wgpu.TextureFormat) *image.RGBA {
		txt := newTestTextIn(t, device, queue, format)
		p := gridPane(1, image.Rect(0, 0, testWidth, testHeight), 40, 8,
			"\x1b[38;2;0;116;116mMMMM")
		layout(txt, []*pane{p}, p, nil)
		return renderOffscreenTo(t, device, queue, format, func(pass *wgpu.RenderPassEncoder) {
			txt.Draw(pass, []*pane{p}, testWidth, testHeight)
		})
	}
	lin, gam := render(wgpu.TextureFormatRGBA8UnormSrgb), render(wgpu.TextureFormatRGBA8Unorm)

	// The most colourful pixel each way. A glyph's core is the ink in both spaces; the
	// difference lives entirely in the partly covered pixels around it.
	best := func(img *image.RGBA) (int, [4]uint8) {
		top, at := 0, [4]uint8{}
		for y := range testHeight {
			for x := range testWidth {
				if p := pixelAt(img, x, y); chroma(p) > top {
					top, at = chroma(p), p
				}
			}
		}
		return top, at
	}
	linTop, linPx := best(lin)
	gamTop, gamPx := best(gam)
	t.Logf("%s antialiases to at best %v (chroma %d) linearly, %v (chroma %d) in gamma space",
		ink, linPx, linTop, gamPx, gamTop)

	if gamTop <= linTop {
		t.Errorf("gamma space peaks at chroma %d and linear at %d; gamma has to hold more colour",
			gamTop, linTop)
	}
}
