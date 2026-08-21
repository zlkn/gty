package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/oliverbestmann/webgpu/wgpu"

	"gty/internal/font"
)

const (
	testWidth  = 576 // 576*4 = 2304 bytes/row, a multiple of the required 256
	testHeight = 680
	testFormat = wgpu.TextureFormatRGBA8Unorm
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

// splitLayout is the tree the app has after one Ctrl+Shift+D.
func splitLayout(t *testing.T, txt *text, w, h int) ([]*pane, []image.Rectangle) {
	t.Helper()
	first := newPane(1)
	fillDemo(first)
	second := newPane(2)
	fillDemo(second)

	root := &node{pane: first}
	if !root.split(first, vertical, second) {
		t.Fatal("split did not find the root pane")
	}
	cellW, cellH := txt.CellSize()
	return layoutTree(root, image.Rect(0, 0, w, h), cellW, cellH)
}

// gridPane skips the layout pass: a pane with the grid and history the test wants.
func gridPane(id int, rect image.Rectangle, cols, rows int, lines ...line) *pane {
	p := newPane(id)
	for _, l := range lines {
		p.Write(l)
	}
	p.rect = rect
	p.setGrid(cols, rows)
	return p
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
			ClearValue: background,
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

// litPixels counts pixels brighter than the background.
func litPixels(img *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y).R > 100 {
				n++
			}
		}
	}
	return n
}

// maxRed stands in for how bright the text is, which is what dimming changes.
func maxRed(img *image.RGBA, r image.Rectangle) uint8 {
	var m uint8
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if c := img.RGBAAt(x, y).R; c > m {
				m = c
			}
		}
	}
	return m
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
	rct.Set(dividers, divider)

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

	lit := litPixels(img, img.Rect)
	t.Logf("pixels brighter than the background: %d", lit)
	if lit == 0 {
		t.Error("nothing was drawn")
	}

	div := dividers[0]
	if c := img.RGBAAt(div.Min.X, testHeight/2).R; c < 40 {
		t.Errorf("divider column %d has red %d, want the divider colour", div.Min.X, c)
	}

	// Both panes hold the same text, so this only passes if Layout dimmed the second.
	focused := maxRed(img, panes[0].rect)
	unfocused := maxRed(img, panes[1].rect)
	t.Logf("brightest text: focused %d, unfocused %d", focused, unfocused)
	if focused <= unfocused {
		t.Errorf("focused pane peaks at %d, unfocused at %d; want the focused one brighter", focused, unfocused)
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

	row := line{Text: "one", Style: font.Regular, Color: foreground}
	long := make([]line, 12)
	for i := range long {
		long[i] = row
	}
	left := gridPane(1, image.Rect(0, 0, 300, 400), 40, 20, row)
	right := gridPane(2, image.Rect(300, 0, 600, 400), 40, 20, long...)

	txt.Layout([]*pane{left, right}, left)

	if want := uint32(len(row.Text)); left.count != want {
		t.Errorf("left pane laid out %d quads, want %d", left.count, want)
	}
	if want := uint32(len(long) * len(row.Text)); right.count != want {
		t.Errorf("right pane laid out %d quads, want %d", right.count, want)
	}
}

// TestGridClipsToPaneColumns checks that off-pane cells are dropped by the grid,
// before the scissor ever sees them.
func TestGridClipsToPaneColumns(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 200, 100), 10, 1,
		line{Text: "scrolled off the top", Style: font.Regular, Color: foreground},
		line{Text: strings.Repeat("=", 30), Style: font.Regular, Color: foreground},
	)
	txt.Layout([]*pane{p}, p)

	if p.count != 10 {
		t.Errorf("pane of 10x1 cells laid out %d quads, want 10", p.count)
	}
}

// TestScrollRendersDifferentLines: the same pane at two scroll positions is two
// different frames, and a full history still costs only a screenful of quads.
func TestScrollRendersDifferentLines(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := newPane(1)
	fillDemo(p)
	cellW, cellH := txt.CellSize()
	p.rect = image.Rect(0, 0, testWidth, testHeight)
	p.setGrid((testWidth-2*padding)/cellW, (testHeight-2*padding)/cellH)

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
	p := newPane(1)
	fillDemo(p)
	cellW, cellH := txt.CellSize()
	p.rect = image.Rect(0, 0, testWidth, testHeight)
	p.setGrid((testWidth-2*padding)/cellW, (testHeight-2*padding)/cellH)
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
	lines := make([]line, rows)
	for i := range lines {
		lines[i] = line{Text: strings.Repeat("x", cols), Style: font.Regular, Color: foreground}
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
	if litPixels(img, img.Rect) == 0 {
		t.Error("nothing was drawn from the grown buffer")
	}
}
