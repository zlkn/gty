// Command gty is a skeleton for a GPU terminal: a glfw window, a WebGPU surface
// and a tree of split panes, each drawing text from a shared glyph atlas. The loop
// is event-driven rather than a game loop so an idle terminal costs no CPU.
//
// Keys: Ctrl+Shift+D and Ctrl+Shift+E split the focused pane, Ctrl+Shift+W closes
// it, Ctrl+Tab cycles the focus, Escape or Ctrl+Q quits. The wheel scrolls the pane
// under the mouse; Shift+PageUp/PageDown and Ctrl+Shift+Up/Down scroll the focused
// one.
package main

import (
	"fmt"
	"image"
	"os"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/go-gl/glfw/v3.4/glfw"
	"github.com/oliverbestmann/webgpu/wgpu"
	"github.com/oliverbestmann/webgpu/wgpuglfw"

	"gty/internal/font"
)

const (
	initialWidth  = 900
	initialHeight = 600
	title         = "gty"
	fontSize      = 16
	padding       = 8
	dividerWidth  = 1
	wheelLines    = 3

	// dimFactor darkens unfocused panes. They have no border of their own, so
	// brightness is the whole focus cue.
	dimFactor = 0.45
)

var HELLO_WORLD = []string{
	`hello, world`,
	`!= == === -> <- => <=> |> ?? ::`,
	`func f[T any](x T) T { return x }`,
}

// fillDemo fills a pane's history to capacity until a PTY writes real output. Lines
// are numbered so the scroll position is readable, and cycle the styles so all four
// stay visible.
func fillDemo(p *pane) {
	styles := []font.Style{font.Regular, font.Bold, font.Italic, font.BoldItalic}
	for i := range maxScrollback {
		l := line{
			Text:  fmt.Sprintf("pane %d  %05d | %s", p.id, i, HELLO_WORLD[i%len(HELLO_WORLD)]),
			Style: styles[i/len(HELLO_WORLD)%len(styles)],
			Color: foreground,
		}
		if i%10 == 0 {
			l.Color = label
		}
		p.Write(l)
	}
}

var (
	background = wgpu.Color{R: 0.09, G: 0.10, B: 0.12, A: 1}
	foreground = [4]float32{0.85, 0.87, 0.91, 1}
	label      = [4]float32{0.45, 0.62, 0.81, 1}
	divider    = [4]float32{0.20, 0.22, 0.26, 1}
)

func dim(c [4]float32) [4]float32 {
	return [4]float32{c[0] * dimFactor, c[1] * dimFactor, c[2] * dimFactor, c[3]}
}

func init() {
	// glfw must talk to the window manager from the thread it was initialised on.
	runtime.LockOSThread()
}

type app struct {
	window   *glfw.Window
	instance *wgpu.Instance
	surface  *wgpu.Surface
	device   *wgpu.Device
	queue    *wgpu.Queue
	config   *wgpu.SurfaceConfiguration
	text     *text
	rects    *rects

	// panes and dividers are what the last relayout derived from root.
	root     *node
	panes    []*pane
	dividers []image.Rectangle
	focused  *pane
	nextID   int

	// Atomic because the PTY reader will eventually set it from its own
	// goroutine; see Damage.
	dirty atomic.Bool
}

// newPane is the one place content is bound to a pane, and where a PTY will be
// spawned.
func (a *app) newPane() *pane {
	a.nextID++
	p := newPane(a.nextID)
	fillDemo(p)
	return p
}

func main() {
	a, err := newApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gty:", err)
		os.Exit(1)
	}
	defer a.release()

	if err := a.run(); err != nil {
		fmt.Fprintln(os.Stderr, "gty:", err)
		os.Exit(1)
	}
}

func newApp() (*app, error) {
	if err := glfw.Init(); err != nil {
		return nil, fmt.Errorf("glfw init: %w", err)
	}

	glfw.WindowHint(glfw.ClientAPI, glfw.NoAPI)
	window, err := glfw.CreateWindow(initialWidth, initialHeight, title, nil, nil)
	if err != nil {
		glfw.Terminate()
		return nil, fmt.Errorf("create window: %w", err)
	}

	a := &app{window: window}
	a.instance = wgpu.CreateInstance(nil)
	a.surface = a.instance.CreateSurface(wgpuglfw.GetSurfaceDescriptor(window))

	adapter, err := a.instance.RequestAdapter(&wgpu.RequestAdapterOptions{
		CompatibleSurface: a.surface,
	})
	if err != nil {
		a.release()
		return nil, fmt.Errorf("request adapter: %w", err)
	}
	defer adapter.Release()

	a.device, err = adapter.RequestDevice(nil)
	if err != nil {
		a.release()
		return nil, fmt.Errorf("request device: %w", err)
	}
	a.queue = a.device.GetQueue()

	// Framebuffer pixels, not window units, so the surface is right on HiDPI
	// from the first frame.
	w, h := window.GetFramebufferSize()
	caps := a.surface.GetCapabilities(adapter)
	a.config = &wgpu.SurfaceConfiguration{
		Usage:       wgpu.TextureUsageRenderAttachment,
		Format:      caps.Formats[0],
		Width:       uint32(w),
		Height:      uint32(h),
		PresentMode: wgpu.PresentModeFifo, // vsync
		AlphaMode:   caps.AlphaModes[0],
	}
	a.surface.Configure(a.device, a.config)

	if a.text, err = newText(a.device, a.queue, a.config.Format, fontSize); err != nil {
		a.release()
		return nil, fmt.Errorf("text renderer: %w", err)
	}
	if a.rects, err = newRects(a.device, a.queue, a.config.Format); err != nil {
		a.release()
		return nil, fmt.Errorf("rect renderer: %w", err)
	}

	a.root = &node{pane: a.newPane()}
	a.focused = a.root.pane
	a.relayout()

	window.SetFramebufferSizeCallback(func(_ *glfw.Window, width, height int) {
		a.resize(width, height)
	})
	window.SetKeyCallback(func(w *glfw.Window, key glfw.Key, _ int, action glfw.Action, mods glfw.ModifierKey) {
		if action != glfw.Press && action != glfw.Repeat {
			return
		}
		a.onKey(w, key, mods)
	})
	window.SetScrollCallback(func(w *glfw.Window, _, yoff float64) {
		a.scrollAt(w, yoff)
	})

	a.dirty.Store(true)
	return a, nil
}

func (a *app) run() error {
	for !a.window.ShouldClose() {
		// Draw before blocking: the window starts dirty, and WaitEvents would
		// otherwise sit there until some unrelated event arrives, leaving the
		// surface with nothing presented.
		if a.dirty.Swap(false) && a.config.Width > 0 && a.config.Height > 0 {
			if err := a.render(); err != nil {
				return err
			}
		}
		glfw.WaitEvents()
	}
	return nil
}

// Damage marks the window as needing a repaint and wakes the event loop. Safe
// to call from any goroutine — this is how the PTY reader will drive redraws.
func (a *app) Damage() {
	a.dirty.Store(true)
	glfw.PostEmptyEvent()
}

// onKey claims the window-management keys; the rest will go to the focused pane's
// PTY.
func (a *app) onKey(w *glfw.Window, key glfw.Key, mods glfw.ModifierKey) {
	ctrl := mods&glfw.ModControl != 0
	shift := mods&glfw.ModShift != 0

	switch {
	case key == glfw.KeyEscape, key == glfw.KeyQ && ctrl:
		w.SetShouldClose(true)
	case key == glfw.KeyD && ctrl && shift:
		a.splitFocused(vertical)
	case key == glfw.KeyE && ctrl && shift:
		a.splitFocused(horizontal)
	case key == glfw.KeyW && ctrl && shift:
		a.closeFocused(w)
	case key == glfw.KeyTab && ctrl:
		a.focusNext()
	case key == glfw.KeyPageUp && shift:
		a.scrollFocused(a.focused.rows - 1)
	case key == glfw.KeyPageDown && shift:
		a.scrollFocused(-(a.focused.rows - 1))
	case key == glfw.KeyUp && ctrl && shift:
		a.scrollFocused(1)
	case key == glfw.KeyDown && ctrl && shift:
		a.scrollFocused(-1)
	}
}

func (a *app) scrollFocused(lines int) {
	if a.focused.scrollBy(lines) {
		a.relayout()
		a.Damage()
	}
}

// scrollAt scrolls the pane under the mouse, which a terminal does regardless of
// which pane has the focus.
func (a *app) scrollAt(w *glfw.Window, yoff float64) {
	// The cursor comes in window coordinates and pane rects are framebuffer pixels;
	// on HiDPI those are different numbers.
	x, y := w.GetCursorPos()
	sx, sy := framebufferScale(w)
	at := image.Pt(int(x*sx), int(y*sy))

	lines := int(yoff * wheelLines)
	if lines == 0 && yoff != 0 {
		lines = 1 // a touchpad's fractional notch still has to move something
		if yoff < 0 {
			lines = -1
		}
	}
	for _, p := range a.panes {
		if at.In(p.rect) {
			if p.scrollBy(lines) {
				a.relayout()
				a.Damage()
			}
			return
		}
	}
}

func framebufferScale(w *glfw.Window) (x, y float64) {
	winW, winH := w.GetSize()
	if winW == 0 || winH == 0 {
		return 1, 1
	}
	fbW, fbH := w.GetFramebufferSize()
	return float64(fbW) / float64(winW), float64(fbH) / float64(winH)
}

func (a *app) splitFocused(d dir) {
	nu := a.newPane()
	if !a.root.split(a.focused, d, nu) {
		return
	}
	a.focused = nu
	a.relayout()
	a.Damage()
}

// closeFocused drops the focused pane; the last pane takes the window with it.
func (a *app) closeFocused(w *glfw.Window) {
	next := a.root.close(a.focused)
	if next == nil {
		w.SetShouldClose(true)
		return
	}
	a.focused = next
	a.relayout()
	a.Damage()
}

func (a *app) focusNext() {
	next := nextPane(a.panes, a.focused)
	if next == a.focused {
		return
	}
	a.focused = next
	a.relayout() // the dimming is baked into the instance colours
	a.Damage()
}

// relayout recomputes pane rects from the surface size and refills both instance
// buffers.
func (a *app) relayout() {
	cellW, cellH := a.text.CellSize()
	surface := image.Rect(0, 0, int(a.config.Width), int(a.config.Height))
	a.panes, a.dividers = layoutTree(a.root, surface, cellW, cellH)
	a.text.Layout(a.panes, a.focused)
	a.rects.Set(a.dividers, divider)
}

func (a *app) resize(width, height int) {
	if width <= 0 || height <= 0 {
		return // iconified
	}
	a.config.Width, a.config.Height = uint32(width), uint32(height)
	a.surface.Configure(a.device, a.config)
	a.relayout()

	// Repaint synchronously: this runs inside WaitEvents, and deferring to the
	// next iteration shows a stale buffer while the window is dragged. Clear the
	// flag before the frame, not after, so Damage arriving mid-frame survives
	// into the next loop iteration.
	a.dirty.Store(false)
	if err := a.render(); err != nil {
		fmt.Fprintln(os.Stderr, "gty: resize render:", err)
	}
}

func (a *app) render() error {
	surfaceTexture, err := a.surface.TryGetCurrentTexture()
	if err != nil {
		return ignoreTransient(err)
	}
	texture, ok := surfaceTexture.Get()
	if !ok {
		return nil // occluded; nothing to present into
	}
	defer texture.Release()

	view, err := texture.TryCreateView(nil)
	if err != nil {
		return err
	}
	defer view.Release()

	encoder, err := a.device.TryCreateCommandEncoder(&wgpu.CommandEncoderDescriptor{
		Label: "Frame Encoder",
	})
	if err != nil {
		return err
	}
	defer encoder.Release()

	pass := encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		Label: "Frame Pass",
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View:       view,
			LoadOp:     wgpu.LoadOpClear,
			StoreOp:    wgpu.StoreOpStore,
			ClearValue: background,
		}},
	})
	a.rects.Draw(pass, a.config.Width, a.config.Height)
	a.text.Draw(pass, a.panes, a.config.Width, a.config.Height)
	pass.End()
	pass.Release()

	cmd, err := encoder.TryFinish(nil)
	if err != nil {
		return err
	}
	defer cmd.Release()

	a.queue.Submit(cmd)
	a.surface.Present()
	return nil
}

// ignoreTransient swallows the surface errors that are a normal part of
// resizing or losing focus; anything else is a real failure.
func ignoreTransient(err error) error {
	s := err.Error()
	switch {
	case strings.Contains(s, "Surface timed out"),
		strings.Contains(s, "Surface is outdated"),
		strings.Contains(s, "Surface was lost"):
		return nil
	}
	return err
}

// release is idempotent: newApp releases a half-built app on its error paths and
// main releases it again through defer, and a second Destroy would panic inside
// glfw. Terminate needs no guard — it is a no-op once glfw is uninitialised.
func (a *app) release() {
	a.text.release()
	a.text = nil
	a.rects.release()
	a.rects = nil
	if a.queue != nil {
		a.queue.Release()
		a.queue = nil
	}
	if a.device != nil {
		a.device.Release()
		a.device = nil
	}
	if a.surface != nil {
		a.surface.Release()
		a.surface = nil
	}
	if a.instance != nil {
		a.instance.Release()
		a.instance = nil
	}
	if a.window != nil {
		a.window.Destroy()
		a.window = nil
	}
	glfw.Terminate()
}
