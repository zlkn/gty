// Command gty is a GPU terminal: a glfw window, a WebGPU surface and a tree of split
// panes, each running a shell on its own pseudo-terminal and drawing from a shared
// glyph atlas. The loop is event-driven rather than a game loop, so an idle terminal
// costs no CPU.
//
// The window manager keeps Ctrl+Shift only, because everything else belongs to the
// shell: Ctrl+Shift+D and Ctrl+Shift+E split the focused pane, Ctrl+Shift+W closes it,
// Ctrl+Shift+Q quits, Ctrl+Tab cycles the focus. The wheel scrolls the pane under the
// mouse; Shift+PageUp/PageDown and Ctrl+Shift+Up/Down scroll the focused one. Every
// other key goes to the shell.
package main

import (
	"fmt"
	"image"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gl/glfw/v3.4/glfw"
	"github.com/oliverbestmann/webgpu/wgpu"
	"github.com/oliverbestmann/webgpu/wgpuglfw"
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

	cursorBarWidth        = 2
	cursorUnderlineHeight = 2
	cursorOutlineWidth    = 1
	underlineHeight       = 1

	// cursorShapeDefault stands in for DECSCUSR until a PTY sends one.
	cursorShapeDefault = cursorBlock

	// cursorBlinkPeriod is one full on-off cycle. Blinking only runs while the window
	// holds the focus, so an idle terminal still parks in WaitEvents and costs
	// nothing; see run.
	cursorBlinkPeriod = 1200 * time.Millisecond
)

var (
	// backgroundRGBA is the source of truth: the clear value and the inverted glyph
	// under a block cursor have to be the same colour, and two literals would drift.
	backgroundRGBA = [4]float32{0.09, 0.10, 0.12, 1}
	background     = wgpu.Color{
		R: float64(backgroundRGBA[0]),
		G: float64(backgroundRGBA[1]),
		B: float64(backgroundRGBA[2]),
		A: float64(backgroundRGBA[3]),
	}

	foreground = [4]float32{0.85, 0.87, 0.91, 1}
	divider    = [4]float32{0.20, 0.22, 0.26, 1}

	// A block draws its glyph in the background colour, so the cursor has to be
	// something that reads against it.
	cursorColor = foreground
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

	// Blink state, main thread only. blinkShown is the phase the last relayout baked
	// into the instance buffers, so run can tell a phase flip from an ordinary event.
	windowFocused bool
	blinkEpoch    time.Time
	blinkShown    bool

	// Atomic because the PTY reader will eventually set them from its own goroutine;
	// see Damage. needsLayout is separate from dirty because the quads carry the text
	// and the cursor: re-encoding a frame without relaying it out presents the
	// previous one.
	dirty       atomic.Bool
	needsLayout atomic.Bool

	// Reused between frames: the bytes pumpPTY drains, and the painted cell
	// backgrounds relayout builds.
	ptyBuf []byte
	paint  []quad
}

func (a *app) newPane() *pane {
	a.nextID++
	return newPane(a.nextID)
}

// ensureShell starts a pane's shell, once. It waits for the layout to have given the
// pane a grid: a shell told it has no terminal to speak of writes its first prompt
// into nothing, and it will not print it again.
func (a *app) ensureShell(p *pane) {
	if p.pty != nil || p.noShell || p.cols == 0 || p.rows == 0 {
		return
	}
	s, err := startPTY(p.cols, p.rows, a.Damage)
	if err != nil {
		// A pane with no shell still lays out and draws; better than refusing to open.
		fmt.Fprintln(os.Stderr, "gty:", err)
		p.noShell = true
		return
	}
	p.pty = s
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
	window.SetCharCallback(func(_ *glfw.Window, r rune) {
		a.typed(r)
	})
	window.SetFocusCallback(func(_ *glfw.Window, focused bool) {
		// Come back solid and start the phase here, rather than resuming mid-blink.
		a.windowFocused, a.blinkEpoch = focused, time.Now()
		a.Damage()
	})

	a.windowFocused = window.GetAttrib(glfw.Focused) == glfw.True
	a.blinkEpoch, a.blinkShown = time.Now(), true

	// Lay out once here so a.panes is populated before the first pump: the shell
	// starts writing its prompt the moment it is spawned.
	a.relayout()
	a.Damage()
	return a, nil
}

// blinkOn is the cursor's on phase. Derived from the clock rather than counted, so a
// late or coalesced wakeup cannot desynchronise it. An unfocused window holds the
// cursor solid instead of blinking, which is what lets run go back to a plain
// WaitEvents and cost nothing while idle.
func blinkOn(since time.Duration, focused bool) bool {
	if !focused {
		return true
	}
	return since%cursorBlinkPeriod < cursorBlinkPeriod/2
}

// blinks reports whether the loop has to wake itself to drive the blink.
func (a *app) blinks() bool {
	return a.windowFocused && a.focused != nil && a.focused.cursor.on
}

// untilNextPhase is how long the current blink phase has left to run.
func (a *app) untilNextPhase() time.Duration {
	half := cursorBlinkPeriod / 2
	return half - time.Since(a.blinkEpoch)%half
}

func (a *app) run() error {
	for !a.window.ShouldClose() {
		a.pumpPTY()
		if on := blinkOn(time.Since(a.blinkEpoch), a.windowFocused); on != a.blinkShown {
			a.blinkShown = on
			a.needsLayout.Store(true)
			a.dirty.Store(true)
		}
		if a.needsLayout.Swap(false) {
			a.relayout()
		}
		// Draw before blocking: the window starts dirty, and WaitEvents would
		// otherwise sit there until some unrelated event arrives, leaving the
		// surface with nothing presented. Test the size before the swap, or an
		// iconified window eats the flag and wakes up with nothing to redraw.
		if a.config.Width > 0 && a.config.Height > 0 && a.dirty.Swap(false) {
			if err := a.render(); err != nil {
				return err
			}
		}
		// Only a focused window with a live cursor needs a timer. Everything else
		// blocks outright, so an idle terminal really is idle.
		if a.blinks() {
			glfw.WaitEventsTimeout(a.untilNextPhase().Seconds())
		} else {
			glfw.WaitEvents()
		}
	}
	return nil
}

// pumpPTY hands each pane whatever its shell has written since the last frame.
//
// This is the only place a screen is written, and it runs on the main thread: the
// reader goroutines only move bytes, so nothing in the terminal state needs a lock.
func (a *app) pumpPTY() {
	for _, p := range a.panes {
		if p.pty == nil {
			continue
		}
		var err error
		a.ptyBuf, err = p.pty.take(a.ptyBuf)
		if len(a.ptyBuf) > 0 {
			p.pty.write(p.feed(a.ptyBuf)) // answers to any queries in this chunk
			a.needsLayout.Store(true)
			a.dirty.Store(true)
		}
		if err != nil {
			// The shell exited or the pty went away. closePane rebuilds the tree, so
			// stop walking the slice it was derived from.
			a.closePane(p)
			return
		}
	}
}

// Damage marks the window as needing a fresh layout and a repaint, and wakes the
// event loop. Safe to call from any goroutine — this is how the PTY reader will drive
// redraws.
func (a *app) Damage() {
	a.needsLayout.Store(true)
	a.dirty.Store(true)
	glfw.PostEmptyEvent()
}

// onKey claims the window-management keys and sends everything else to the focused
// pane's shell.
//
// Only Ctrl+Shift is reserved. Escape, the bare arrows and Ctrl+Q all mean something
// to a terminal — Ctrl+Q is XON — so the window cannot have them. Shift+PageUp is the
// exception: scrolling the pane's own history is what xterm does with it too.
func (a *app) onKey(w *glfw.Window, key glfw.Key, mods glfw.ModifierKey) {
	ctrl := mods&glfw.ModControl != 0
	shift := mods&glfw.ModShift != 0

	// Typing restarts the phase, so the cursor stays solid under the hands.
	a.blinkEpoch = time.Now()

	switch {
	case key == glfw.KeyQ && ctrl && shift:
		w.SetShouldClose(true)
	case key == glfw.KeyD && ctrl && shift:
		a.splitFocused(vertical)
	case key == glfw.KeyE && ctrl && shift:
		a.splitFocused(horizontal)
	case key == glfw.KeyW && ctrl && shift:
		a.closePane(a.focused)
	case key == glfw.KeyTab && ctrl:
		a.focusNext()
	case key == glfw.KeyPageUp && shift:
		a.scrollFocused(a.page())
	case key == glfw.KeyPageDown && shift:
		a.scrollFocused(-a.page())
	case key == glfw.KeyUp && ctrl && shift:
		a.scrollFocused(1)
	case key == glfw.KeyDown && ctrl && shift:
		a.scrollFocused(-1)
	default:
		a.toShell(keyBytes(key, mods))
	}
}

// typed sends a character the window manager did not claim. Printable input arrives
// here rather than through onKey because glfw has already applied the layout, the
// shift state and any dead keys by this point.
func (a *app) typed(r rune) { a.toShell([]byte(string(r))) }

// toShell writes to the focused pane's shell and snaps its view back to the live
// screen — a key that reaches the shell means the user wants to see what it does.
func (a *app) toShell(b []byte) {
	if len(b) == 0 || a.focused.pty == nil {
		return
	}
	a.focused.pty.write(b)
	if a.focused.scroll != 0 {
		a.focused.scroll = 0
		a.Damage()
	}
}

// keyBytes is what an unclaimed key sends to the shell. Printable characters are not
// here: glfw reports those to the character callback instead.
func keyBytes(key glfw.Key, mods glfw.ModifierKey) []byte {
	var b []byte
	switch key {
	case glfw.KeyEnter, glfw.KeyKPEnter:
		b = []byte{'\r'}
	case glfw.KeyBackspace:
		b = []byte{0x7F}
	case glfw.KeyTab:
		b = []byte{'\t'}
	case glfw.KeyEscape:
		b = []byte{0x1B}
	case glfw.KeyUp:
		b = []byte("\x1b[A")
	case glfw.KeyDown:
		b = []byte("\x1b[B")
	case glfw.KeyRight:
		b = []byte("\x1b[C")
	case glfw.KeyLeft:
		b = []byte("\x1b[D")
	case glfw.KeyHome:
		b = []byte("\x1b[H")
	case glfw.KeyEnd:
		b = []byte("\x1b[F")
	case glfw.KeyDelete:
		b = []byte("\x1b[3~")
	case glfw.KeyPageUp:
		b = []byte("\x1b[5~")
	case glfw.KeyPageDown:
		b = []byte("\x1b[6~")
	default:
		// Ctrl+letter is the letter with its top three bits cleared: Ctrl+C is 0x03.
		if mods&glfw.ModControl != 0 && key >= glfw.KeyA && key <= glfw.KeyZ {
			b = []byte{byte(key-glfw.KeyA) + 1}
		}
	}
	if b != nil && mods&glfw.ModAlt != 0 {
		// Alt sends the key prefixed with escape, which is what shells expect of it.
		return append([]byte{0x1B}, b...)
	}
	return b
}

func (a *app) scrollFocused(lines int) { a.scroll(a.focused, lines) }

// scroll moves one pane's view and asks for a repaint. Both scroll paths go through
// here so neither can forget the Damage.
func (a *app) scroll(p *pane, lines int) {
	if p.scrollBy(lines) {
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
			a.scroll(p, lines)
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
	a.Damage()
}

// closePane drops a pane and hands the focus to whatever takes its space. The last
// pane takes the window with it.
func (a *app) closePane(p *pane) {
	next, found := a.root.close(p)
	if !found {
		return
	}
	p.release()
	if next == nil {
		a.window.SetShouldClose(true)
		return
	}
	if a.focused == p {
		a.focused = next
	}
	a.Damage()
}

func (a *app) focusNext() {
	next := nextPane(a.panes, a.focused)
	if next == a.focused {
		return
	}
	a.focused = next
	a.Damage() // the dimming and the cursor are baked into the instance buffers
}

// page is a Shift+PageUp step. Never zero: a pane with one row would otherwise take
// the key and do nothing, and a pane with none would scroll backwards.
func (a *app) page() int { return max(1, a.focused.rows-1) }

// relayout recomputes pane rects from the surface size and refills both instance
// buffers.
func (a *app) relayout() {
	cellW, cellH := a.text.CellSize()
	surface := image.Rect(0, 0, int(a.config.Width), int(a.config.Height))
	a.panes, a.dividers = layoutTree(a.root, surface, cellW, cellH)

	// A pane's shell starts here rather than at newPane, because only now does the
	// pane have a grid to tell it about. The cursor is resolved once here too:
	// text.Layout and the quads below both read cursor.shown, and if they disagreed
	// the block would vanish while its glyph stayed inverted — a blank cell.
	for _, p := range a.panes {
		a.ensureShell(p)
		p.cursor.shown = p.cursor.on && (p != a.focused || a.blinkShown)
	}
	a.text.Layout(a.panes, a.focused)

	// Order matters inside the one buffer: the cells a program painted, then the
	// dividers, then the cursor on top of its own cell. The glyphs land over all of it.
	a.rects.Reset()
	a.paint = a.paint[:0]
	for _, p := range a.panes {
		a.paint = paintRects(a.paint, p, cellW, cellH)
	}
	a.rects.AddQuads(a.paint)
	a.rects.Add(a.dividers, divider)
	fills, rims := cursorRects(a.panes, a.focused, cellW, cellH)
	a.rects.Add(fills, cursorColor)
	a.rects.Add(rims, dim(cursorColor))
	a.rects.Upload()
}

func (a *app) resize(width, height int) {
	if width <= 0 || height <= 0 {
		return // iconified
	}
	a.config.Width, a.config.Height = uint32(width), uint32(height)
	a.surface.Configure(a.device, a.config)
	a.needsLayout.Store(false)
	a.relayout()

	// Repaint synchronously: this runs inside WaitEvents, and deferring to the
	// next iteration shows a stale buffer while the window is dragged. Clear the
	// flags before the frame, not after, so Damage arriving mid-frame survives
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
	for _, p := range a.panes {
		p.release()
	}
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
