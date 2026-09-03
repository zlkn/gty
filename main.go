package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"image"
	"io/fs"
	"math"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gl/glfw/v3.4/glfw"
	"github.com/oliverbestmann/webgpu/wgpu"
	"github.com/oliverbestmann/webgpu/wgpuglfw"

	"gty/internal/font"
	"gty/internal/vte"
)

// Set from the config file, which is read before the window is built. Defaults here.
var (
	// fontFamily empty means the embedded JetBrains Mono; anything else is looked up
	// among the installed fonts.
	fontFamily = ""
	fontSize   = 16.0

	// fontGamma bends the antialiasing coverage curve; zero derives it from the theme
	// and the blend space. See coverageExponent.
	fontGamma = 0.0

	// fontBlend is the blending asked for. What the surface actually gives is blendUsed,
	// which is what the theme derives from — the driver need not offer either kind.
	fontBlend = blendGamma

	// fontIconScale is the share of the cell's height an icon is scaled to fill; zero
	// leaves icons at the size the face draws them. Read when the renderer is built,
	// not in refreshTheme: it decides the atlas geometry, which is laid out once.
	fontIconScale = font.DefaultIconFill

	// fontBoxDrawing draws the frames and blocks ourselves. Read when the renderer is
	// built, like fontIconScale.
	fontBoxDrawing = true

	// fontHinting runs the face's own bytecode over its outlines, snapping their horizontal
	// edges to whole pixels. Off by default: the bytecode fits nothing sideways, so it
	// sharpens a glyph built from horizontals far more than one built from stems, and the
	// two then read unevenly together. Read when the atlas is baked.
	fontHinting = false

	// windowDecorations keeps the system titlebar and frame. Read once, when the window
	// is created: glfw can retarget the hint but not the window already built from it.
	windowDecorations = true
)

const (
	initialWidth  = 900
	initialHeight = 600
	title         = "gty"

	// Lengths in logical pixels, here and in the cursor block below: px turns them
	// into framebuffer pixels at the display's own scale. See uiScale.
	padding      = 8
	dividerWidth = 1

	wheelLines = 3

	// dimFactor darkens unfocused panes. They have no border of their own, so
	// brightness is the whole focus cue.
	dimFactor = 0.45

	cursorBarWidth        = 2
	cursorUnderlineHeight = 2
	cursorOutlineWidth    = 1
	underlineHeight       = 1

	// cursorBlinkPeriod is one full on-off cycle. Blinking only runs while the window
	// holds the focus, so an idle terminal still parks in WaitEvents and costs
	// nothing; see run.
	cursorBlinkPeriod = 1200 * time.Millisecond
)

// uiScale is how many framebuffer pixels the display puts in a logical one: two on a
// HiDPI laptop panel, one on the monitor plugged into it. Everything the window draws
// is sized in logical units and scaled by this, so the terminal comes out the same
// physical size on either screen. Set from the window before the first frame, and
// again whenever it moves to a display that scales differently; see app.setScale.
var uiScale = 1.0

// px is a logical length in framebuffer pixels. A line never rounds away to nothing —
// a divider or an underline that vanishes at some scale reads as a bug in the renderer.
func px(n int) int {
	if n <= 0 {
		return 0
	}
	return max(1, int(float64(n)*uiScale+0.5))
}

// contentScale is the window's scale as one number. glfw reports the axes separately;
// no display scales them apart, and the larger is the safe one to take.
func contentScale(w *glfw.Window) float64 {
	x, y := w.GetContentScale()
	s := math.Max(float64(x), float64(y))
	if math.IsNaN(s) || math.IsInf(s, 0) || s <= 0 {
		return 1 // a platform with nothing to say reports zero
	}
	return s
}

// The theme. These are the defaults; the config file replaces them before the first
// frame, which is why nothing here may be baked into another initialiser.
var (
	// backgroundRGBA is the source of truth: the clear value and the inverted glyph
	// under a block cursor have to be the same colour, and two literals would drift.
	backgroundRGBA = [4]float32{0.949, 0.949, 0.949, 1} // #f2f2f2
	foreground     = [4]float32{0.259, 0.259, 0.259, 1} // #424242

	// selectionColor also paints the dividers between panes.
	selectionColor = [4]float32{0.851, 0.851, 0.851, 1} // #d9d9d9

	// cursorTint is colors.cursor, or nil to follow the foreground. A block draws its
	// glyph in the background colour, so a tint near the background buys a visible cursor
	// and an invisible glyph.
	cursorTint *[4]float32

	// cursorShapeDefault is what a pane starts with and what DECSCUSR 0 and RIS return to.
	cursorShapeDefault = vte.CursorBlock

	// Derived; see refreshTheme.
	cursorColor [4]float32

	// coverageExp is what the text shader raises glyph coverage to. Derived from the
	// theme, because which way the curve has to bend depends on which of the ink and
	// the paper is darker.
	coverageExp float32

	// blendUsed is the space the surface ended up blending in. Set once the format is
	// known; until then the request stands in, which is right for every driver that
	// offers both.
	blendUsed = fontBlend
)

// refreshTheme recomputes everything that follows from the theme's own colours. The
// config file assigns to the sources long after their initialisers have run, so the
// derivation cannot live in one.
// The clear value is not among the derived colours, even though it follows from
// backgroundRGBA: it depends on the surface format too, and this runs from init and from
// the config file, both long before there is an adapter to ask. See clearValue.
func refreshTheme() {
	// A block draws its glyph in the background colour, so the cursor has to be
	// something that reads against it.
	cursorColor = foreground
	if cursorTint != nil {
		cursorColor = *cursorTint
	}
	coverageExp = coverageExponent(foreground, backgroundRGBA, fontGamma, blendUsed)
	palette = buildPalette()
}

func dim(c [4]float32) [4]float32 {
	return [4]float32{c[0] * dimFactor, c[1] * dimFactor, c[2] * dimFactor, c[3]}
}

func glfwBool(b bool) int {
	if b {
		return glfw.True
	}
	return glfw.False
}

// mix blends a fraction of b into a, keeping a's alpha.
func mix(a, b [4]float32, by float32) [4]float32 {
	return [4]float32{
		a[0] + (b[0]-a[0])*by,
		a[1] + (b[1]-a[1])*by,
		a[2] + (b[2]-a[2])*by,
		a[3],
	}
}

func init() {
	// glfw must talk to the window manager from the thread it was initialised on.
	runtime.LockOSThread()
	refreshTheme()
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

	// srgb is whether the surface encodes what is written to it. The shaders decode
	// their own colours; the clear value is the one that has to be decoded here.
	srgb bool

	// Never empty: closing the last tab takes the window with it.
	tabs   []*tab
	active int

	// Aliases of the active tab, written only by syncActive and stashActive.
	root     *node
	panes    []*pane
	dividers []image.Rectangle
	focused  *pane

	windowTitle string // so setWindowTitle can tell a change from a repeat

	nextID int

	// Blink state, main thread only. blinkShown is the phase the last relayout baked
	// into the instance buffers, so run can tell a phase flip from an ordinary event.
	windowFocused bool
	blinkEpoch    time.Time
	blinkShown    bool

	// keyClaimed is set by the key callback and read by the character one; see typed.
	keyClaimed bool

	// Atomic because the PTY reader will eventually set them from its own goroutine;
	// see Damage. needsLayout is separate from dirty because the quads carry the text
	// and the cursor: re-encoding a frame without relaying it out presents the
	// previous one.
	dirty       atomic.Bool
	needsLayout atomic.Bool

	// Reused between frames: the painted cell backgrounds relayout builds.
	paint []quad
}

func (a *app) newPane() *pane {
	a.nextID++
	return newPane(a.nextID)
}

// ensureShell starts a pane's shell, once. It waits for the layout to have given the pane a
// grid: a shell told it has no terminal to speak of writes its first prompt into nothing,
// and it will not print it again.
func (a *app) ensureShell(p *pane) {
	if p.noShell || p.cols == 0 || p.rows == 0 {
		return
	}
	if err := p.term.Attach(vte.Options{
		CursorShape: cursorShapeDefault,
		Wake:        a.Damage,
		ReportColor: reportThemeColor,
	}); err != nil {
		// A pane with no shell still lays out and draws; better than refusing to open.
		fmt.Fprintln(os.Stderr, "gty:", err)
		p.noShell = true
	}
}

// reportThemeColor answers the OSC 10 and 11 queries out of the theme. The terminal knows
// the protocol and the host knows the colours, which is the whole of what it is told.
func reportThemeColor(code int) (r, g, b uint16, ok bool) {
	var c [4]float32
	switch code {
	case 10:
		c = foreground
	case 11:
		c = backgroundRGBA
	default:
		return 0, 0, 0, false
	}
	q := func(v float32) uint16 { return uint16(min(max(v, 0), 1) * 0xFFFF) }
	return q(c[0]), q(c[1]), q(c[2]), true
}

func main() {
	configFile := flag.String("config", "", "TOML config file (default "+configPath()+")")
	flag.Parse()

	if err := loadConfig(cmp.Or(*configFile, configPath())); err != nil {
		// An absent config is only an error when it was asked for by name.
		if *configFile != "" || !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "gty:", err)
			os.Exit(1)
		}
	}

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
	// Undecorated, the tab bar becomes the top of the window: closing is then the quit
	// binding, and moving and resizing are whatever the window manager offers without a
	// frame — on most, Super and a drag.
	glfw.WindowHint(glfw.Decorated, glfwBool(windowDecorations))
	glfw.WindowHintString(glfw.WaylandAppID, "gty")
	window, err := glfw.CreateWindow(initialWidth, initialHeight, title, nil, nil)
	if err != nil {
		glfw.Terminate()
		return nil, fmt.Errorf("create window: %w", err)
	}

	// Before anything is sized: the atlas is rasterised at the scale the display
	// draws at, and the grid is sized from the atlas.
	uiScale = contentScale(window)

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
	if len(caps.Formats) == 0 || len(caps.AlphaModes) == 0 {
		a.release()
		return nil, errors.New("the surface offers no texture format to draw in")
	}
	format := pickFormat(caps.Formats, fontBlend)
	a.srgb = isSrgbFormat(format)

	// The theme was derived from the requested space; only now is the real one known,
	// and the coverage curve depends on it.
	if blendUsed = spaceOf(format); blendUsed != fontBlend {
		fmt.Fprintf(os.Stderr, "gty: no surface format blends in %v space; using %v, which blends in %v\n",
			fontBlend, format, blendUsed)
	}
	refreshTheme()
	a.config = &wgpu.SurfaceConfiguration{
		Usage:       wgpu.TextureUsageRenderAttachment,
		Format:      format,
		Width:       uint32(w),
		Height:      uint32(h),
		PresentMode: wgpu.PresentModeFifo, // vsync
		AlphaMode:   caps.AlphaModes[0],
	}
	a.surface.Configure(a.device, a.config)

	if a.text, err = newText(a.device, a.queue, a.config.Format, fontSize, uiScale); err != nil {
		a.release()
		return nil, fmt.Errorf("text renderer: %w", err)
	}
	if a.rects, err = newRects(a.device, a.queue, a.config.Format); err != nil {
		a.release()
		return nil, fmt.Errorf("rect renderer: %w", err)
	}

	first := a.newPane()
	a.tabs = []*tab{{root: &node{pane: first}, focused: first}}
	a.syncActive()

	window.SetFramebufferSizeCallback(func(_ *glfw.Window, width, height int) {
		a.resize(width, height)
	})
	window.SetContentScaleCallback(func(w *glfw.Window, _, _ float32) {
		a.setScale(contentScale(w))
	})
	window.SetKeyCallback(func(_ *glfw.Window, key glfw.Key, _ int, act glfw.Action, mods glfw.ModifierKey) {
		if act != glfw.Press && act != glfw.Repeat {
			return
		}
		a.onKey(key, mods)
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
	return a.windowFocused && a.focused != nil && a.focused.frame.Cursor.Visible
}

// untilNextPhase is how long the current blink phase has left to run.
func (a *app) untilNextPhase() time.Duration {
	half := cursorBlinkPeriod / 2
	return half - time.Since(a.blinkEpoch)%half
}

func (a *app) run() error {
	for !a.window.ShouldClose() {
		a.pumpPTY()
		// A shell that just exited can have taken the last tab, and the window with it.
		// Laying out or drawing what is left would reach through released panes.
		if a.window.ShouldClose() {
			break
		}
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

// pumpPTY parses whatever each pane's shell has written since the last frame.
//
// This is the only place a terminal is written, and it runs on the main thread: the reader
// goroutines only move bytes.
func (a *app) pumpPTY() {
	// Every tab: a shell whose output nobody drains stops as soon as its pipe buffer fills,
	// so a build in a background tab would hang rather than finish.
	for _, t := range a.tabs {
		for _, p := range t.panes {
			changed, err := p.term.Pump()
			if changed {
				a.needsLayout.Store(true)
				a.dirty.Store(true)
			}
			if err != nil {
				// closePane rebuilds the tree and may drop a whole tab, so stop walking
				// what this was derived from.
				a.closePane(p)
				return
			}
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

// onKey runs the action a key is bound to, and sends everything it is not to the focused
// pane's shell. The table is keybinds; what the defaults reserve, and why so little, is
// documented there.
func (a *app) onKey(key glfw.Key, mods glfw.ModifierKey) {
	// Typing restarts the phase, so the cursor stays solid under the hands.
	a.blinkEpoch = time.Now()

	act, ok := boundKeys[chord{key, mods & bindableMods}]

	// glfw runs the character callback from this same event, and which modifiers stop it
	// from running differs by platform — X11 and Wayland suppress on Ctrl or Alt, Win32
	// on Alt, Cocoa on Super. So the character cannot be relied on to stay away: claim
	// it here, or Ctrl+Shift+D would split the pane and type a "d" into it as well.
	a.keyClaimed = ok
	if ok {
		a.dispatch(act)
		return
	}
	b := keyBytes(key, mods, a.focused.term.AppCursor(), a.focused.term.AppKeypad())
	// Claim whatever was encoded here too: a keypad digit reaches the character callback as
	// well, and the shell would see it twice.
	a.keyClaimed = len(b) > 0
	a.toShell(b)
}

// typed sends a character the window manager did not claim. Printable input arrives
// here rather than through onKey because glfw has already applied the layout, the
// shift state and any dead keys by this point.
//
// The flag is cleared on the way through as well as set by every key press, so a binding
// that produces no character at all cannot leave it standing to eat the next one.
func (a *app) typed(r rune) {
	if a.keyClaimed {
		a.keyClaimed = false
		return
	}
	a.toShell([]byte(string(r)))
}

// toShell writes to the focused pane's shell and snaps its view back to the live screen — a
// key that reaches the shell means the user wants to see what it does.
func (a *app) toShell(b []byte) {
	if len(b) == 0 {
		return
	}
	a.focused.term.Write(b)
	if a.focused.scroll != 0 {
		a.focused.scroll = 0
		a.Damage()
	}
}

// xtermMod is the modifier parameter xterm puts in a key's sequence: one plus a bitmask, so
// Ctrl alone is 5. Zero means nothing was held and the key keeps its bare form.
func xtermMod(mods glfw.ModifierKey) int {
	var m int
	if mods&glfw.ModShift != 0 {
		m |= 1
	}
	if mods&glfw.ModAlt != 0 {
		m |= 2
	}
	if mods&glfw.ModControl != 0 {
		m |= 4
	}
	if mods&glfw.ModSuper != 0 {
		m |= 8
	}
	if m == 0 {
		return 0
	}
	return m + 1
}

// cursorKey is an arrow, Home or End. A held modifier goes into a CSI parameter and takes the
// sequence back to CSI form whatever DECCKM said — terminfo has kUP5=\E[1;5A.
func cursorKey(final byte, mods glfw.ModifierKey, appCursor bool) []byte {
	if m := xtermMod(mods); m != 0 {
		return fmt.Appendf(nil, "\x1b[1;%d%c", m, final)
	}
	if appCursor {
		return []byte{0x1B, 'O', final}
	}
	return []byte{0x1B, '[', final}
}

// tildeKey is a CSI n ~ key: Delete, Page Up, Page Down. Never SS3, in either cursor mode.
func tildeKey(n int, mods glfw.ModifierKey) []byte {
	if m := xtermMod(mods); m != 0 {
		return fmt.Appendf(nil, "\x1b[%d;%d~", n, m)
	}
	return fmt.Appendf(nil, "\x1b[%d~", n)
}

// keypadFinal is the SS3 final byte a keypad key sends in application mode, after terminfo's
// kpZRO=\EOp and its neighbours. Enter is not here: it is the one with a fallback.
func keypadFinal(key glfw.Key) (byte, bool) {
	if key >= glfw.KeyKP0 && key <= glfw.KeyKP9 {
		return byte('p' + key - glfw.KeyKP0), true
	}
	switch key {
	case glfw.KeyKPDecimal:
		return 'n', true
	case glfw.KeyKPDivide:
		return 'o', true
	case glfw.KeyKPMultiply:
		return 'j', true
	case glfw.KeyKPSubtract:
		return 'm', true
	case glfw.KeyKPAdd:
		return 'k', true
	case glfw.KeyKPEqual:
		return 'X', true
	}
	return 0, false
}

// keyBytes is what an unclaimed key sends to the shell. Printable characters are not here:
// glfw reports those to the character callback instead.
//
// appCursor is DECCKM and appKeypad DECKPAM — the two modes smkx turns on. ncurses matches
// only what terminfo promised, so ignoring them leaves htop and ncdu with dead keys.
func keyBytes(key glfw.Key, mods glfw.ModifierKey, appCursor, appKeypad bool) []byte {
	// These carry their modifiers in a CSI parameter, so they answer here and skip the escape
	// prefix below: xterm puts Alt in the parameter for them rather than in front.
	switch key {
	case glfw.KeyUp:
		return cursorKey('A', mods, appCursor)
	case glfw.KeyDown:
		return cursorKey('B', mods, appCursor)
	case glfw.KeyRight:
		return cursorKey('C', mods, appCursor)
	case glfw.KeyLeft:
		return cursorKey('D', mods, appCursor)
	case glfw.KeyHome:
		return cursorKey('H', mods, appCursor)
	case glfw.KeyEnd:
		return cursorKey('F', mods, appCursor)
	case glfw.KeyDelete:
		return tildeKey(3, mods)
	case glfw.KeyPageUp:
		return tildeKey(5, mods)
	case glfw.KeyPageDown:
		return tildeKey(6, mods)
	}

	var b []byte
	switch key {
	case glfw.KeyEnter:
		b = []byte{'\r'}
	case glfw.KeyKPEnter:
		if b = []byte{'\r'}; appKeypad {
			b = []byte{0x1B, 'O', 'M'} // kent
		}
	case glfw.KeyBackspace:
		b = []byte{0x7F}
	case glfw.KeyTab:
		b = []byte{'\t'}
	case glfw.KeyEscape:
		b = []byte{0x1B}
	default:
		switch final, keypad := keypadFinal(key); {
		case keypad && appKeypad:
			b = []byte{0x1B, 'O', final}
		case keypad:
			// Numeric mode prints the digit on the key, and glfw reports that to the
			// character callback; there is nothing to send from here.
		case mods&glfw.ModControl != 0 && key >= glfw.KeyA && key <= glfw.KeyZ:
			// Ctrl+letter is the letter with its top three bits cleared: Ctrl+C is 0x03.
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

// closePane works on the tab's own fields, syncing only when that tab is on display —
// pumpPTY reaches every tab. The last pane of a tab takes the tab with it.
func (a *app) closePane(p *pane) {
	i := a.tabOf(p)
	if i < 0 {
		return
	}
	if i == a.active {
		a.stashActive()
	}
	t := a.tabs[i]
	next, found := t.root.close(p)
	if !found {
		return
	}
	p.release()
	if next == nil {
		a.closeTabAt(i)
		return
	}
	if t.focused == p {
		t.focused = next
	}
	if i == a.active {
		a.syncActive()
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

	bar, content := splitBar(surface, len(a.tabs), cellH)

	// Every tab against the same rect, so none needs a Setsize when it comes forward.
	a.stashActive()
	for _, t := range a.tabs {
		t.panes, t.dividers = layoutTree(t.root, content, cellW, cellH)
	}
	a.syncActive()

	// A pane's shell starts here rather than at newPane, because only now does the pane have
	// a grid to tell it about. Every tab, because a background pane still has to be running.
	for _, t := range a.tabs {
		for _, p := range t.panes {
			a.ensureShell(p)
		}
	}
	// Only the tab on display is drawn, so only its panes need a frame read. Both consumers
	// below take the same one, and shown is resolved into it here: text.Layout and the quads
	// both read it, and if they disagreed the block would vanish while its glyph stayed
	// inverted — a blank cell.
	for _, p := range a.panes {
		p.snap()
		p.shown = p.frame.Cursor.Visible && (p != a.focused || a.blinkShown)
	}
	// After the stash, so every tab's focus — and so its label — is current.
	barFills, barLabels := layoutBar(a.tabs, a.active, bar, cellW, cellH)
	a.setWindowTitle()
	a.text.Layout(a.panes, a.focused, barLabels)

	// Order matters inside the one buffer: the tab bar, the cells a program painted,
	// the dividers, then the cursor. The glyphs land over all of it.
	a.rects.Reset()
	a.rects.AddQuads(barFills)
	a.paint = a.paint[:0]
	for _, p := range a.panes {
		a.paint = paintRects(a.paint, p, cellW, cellH)
	}
	a.rects.AddQuads(a.paint)
	a.rects.Add(a.dividers, selectionColor)
	fills, rims := cursorRects(a.panes, a.focused, cellW, cellH)
	a.rects.Add(fills, cursorColor)
	a.rects.Add(rims, dim(cursorColor))
	a.rects.Upload()
}

// setScale rebakes the atlas for a display that scales differently — the window was
// dragged onto the other monitor, or the compositor changed its mind about this one.
// Glyphs are rasterised at the exact size they are drawn at and the sampler does not
// filter, so a new scale needs a new sheet rather than a bigger quad.
//
// A relayout is enough to pick up the new cell size and the scaled padding; the
// framebuffer callback that usually follows this one only changes the surface.
func (a *app) setScale(scale float64) {
	if scale == uiScale {
		return
	}
	if err := a.text.SetScale(fontSize, scale); err != nil {
		// Keep the sheet that is on the GPU. Half applied, the grid would be laid out
		// for a cell the glyphs are not drawn at.
		fmt.Fprintln(os.Stderr, "gty: rescale font:", err)
		return
	}
	uiScale = scale
	a.Damage()
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
			ClearValue: clearValue(backgroundRGBA, a.srgb),
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
	for _, t := range a.tabs {
		for _, p := range t.root.leaves(nil) {
			p.release()
		}
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
