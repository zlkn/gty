// Command gty is a skeleton for a GPU terminal: a glfw window, a WebGPU surface
// and one line of text drawn from a glyph atlas. The loop is event-driven rather
// than a game loop so an idle terminal costs no CPU.
package main

import (
	"fmt"
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
)

var HELLO_WORLD = []string{
	`hello, world`,
	`!= == === -> <- => <=> |> ?? ::`,
	`func f[T any](x T) T { return x }`,
}

// sample repeats HELLO_WORLD once per style so all four can be compared at a
// glance, each block under a label drawn in Regular.
func sample() []line {
	var out []line
	for _, style := range []font.Style{font.Regular, font.Bold, font.Italic, font.BoldItalic} {
		out = append(out, line{Text: style.String() + ":", Style: font.Regular, Color: label})
		for _, txt := range HELLO_WORLD {
			out = append(out, line{Text: txt, Style: style, Color: foreground})
		}
		out = append(out, line{})
	}
	return out
}

var (
	background = wgpu.Color{R: 0.09, G: 0.10, B: 0.12, A: 1}
	foreground = [4]float32{0.85, 0.87, 0.91, 1}
	label      = [4]float32{0.45, 0.62, 0.81, 1}
)

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

	// Atomic because the PTY reader will eventually set it from its own
	// goroutine; see Damage.
	dirty atomic.Bool
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
	a.text.Set(sample(), padding)

	window.SetFramebufferSizeCallback(func(_ *glfw.Window, width, height int) {
		a.resize(width, height)
	})
	window.SetKeyCallback(func(w *glfw.Window, key glfw.Key, _ int, action glfw.Action, mods glfw.ModifierKey) {
		if action != glfw.Press && action != glfw.Repeat {
			return
		}
		if key == glfw.KeyEscape || (key == glfw.KeyQ && mods&glfw.ModControl != 0) {
			w.SetShouldClose(true)
		}
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

func (a *app) resize(width, height int) {
	if width <= 0 || height <= 0 {
		return // iconified
	}
	a.config.Width, a.config.Height = uint32(width), uint32(height)
	a.surface.Configure(a.device, a.config)

	// Repaint synchronously: this runs inside WaitEvents, and deferring to the
	// next iteration shows a stale buffer while the window is dragged.
	if err := a.render(); err != nil {
		fmt.Fprintln(os.Stderr, "gty: resize render:", err)
	}
	a.dirty.Store(false)
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
	a.text.Draw(pass, a.config.Width, a.config.Height)
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

func (a *app) release() {
	a.text.release()
	if a.queue != nil {
		a.queue.Release()
	}
	if a.device != nil {
		a.device.Release()
	}
	if a.surface != nil {
		a.surface.Release()
	}
	if a.instance != nil {
		a.instance.Release()
	}
	if a.window != nil {
		a.window.Destroy()
	}
	glfw.Terminate()
}
