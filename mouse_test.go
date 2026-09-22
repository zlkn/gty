package main

import (
	"testing"

	"github.com/go-gl/glfw/v3.4/glfw"

	"gty/internal/vte"
)

// TestMouseCode: glfw numbers the buttons in its own order, and a report does not.
func TestMouseCode(t *testing.T) {
	for _, tc := range []struct {
		button glfw.MouseButton
		want   int
		ok     bool
	}{
		{glfw.MouseButtonLeft, vte.MouseLeft, true},
		{glfw.MouseButtonMiddle, vte.MouseMiddle, true},
		{glfw.MouseButtonRight, vte.MouseRight, true},
		{glfw.MouseButton4, 0, false},
	} {
		got, ok := mouseCode(tc.button)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("button %v gave (%d, %v), want (%d, %v)", tc.button, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMouseMods(t *testing.T) {
	for _, tc := range []struct {
		mods glfw.ModifierKey
		want vte.Mods
	}{
		{0, 0},
		{glfw.ModShift, vte.ModShift},
		{glfw.ModAlt, vte.ModAlt},
		{glfw.ModControl, vte.ModCtrl},
		{glfw.ModControl | glfw.ModShift, vte.ModCtrl | vte.ModShift},
		{glfw.ModSuper, 0}, // no report carries it
	} {
		if got := mouseMods(tc.mods); got != tc.want {
			t.Errorf("%v translated to %d, want %d", tc.mods, got, tc.want)
		}
	}
}

// TestHeldCode: a motion report names the lowest button held, or none at all.
func TestHeldCode(t *testing.T) {
	a := &app{held: map[glfw.MouseButton]bool{}}
	if code, held := a.heldCode(); held || code != vte.MouseNone {
		t.Errorf("an empty hand gave (%d, %v), want (%d, false)", code, held, vte.MouseNone)
	}

	a.held[glfw.MouseButtonRight] = true
	if code, held := a.heldCode(); !held || code != vte.MouseRight {
		t.Errorf("the right button gave (%d, %v), want (%d, true)", code, held, vte.MouseRight)
	}

	a.held[glfw.MouseButtonLeft] = true
	if code, _ := a.heldCode(); code != vte.MouseLeft {
		t.Errorf("two buttons held gave %d, want the lowest (%d)", code, vte.MouseLeft)
	}
}

// TestNotches: a mouse reports whole notches and a touchpad a fraction of one, and a
// fraction still has to send an event.
func TestNotches(t *testing.T) {
	for _, tc := range []struct {
		yoff float64
		want int
	}{{1, 1}, {-1, 1}, {0.2, 1}, {-0.2, 1}, {3, 3}, {-2.5, 2}} {
		if got := notches(tc.yoff); got != tc.want {
			t.Errorf("notches(%v) = %d, want %d", tc.yoff, got, tc.want)
		}
	}
}

// TestArrowRun is what the wheel sends on the alternate screen: one cursor key per line,
// in whichever form DECCKM asked for.
func TestArrowRun(t *testing.T) {
	for _, tc := range []struct {
		lines     int
		appCursor bool
		want      string
	}{
		{2, false, "\x1b[A\x1b[A"},
		{-2, false, "\x1b[B\x1b[B"},
		{1, true, "\x1bOA"},
		{-1, true, "\x1bOB"},
	} {
		if got := string(arrowRun(tc.lines, tc.appCursor)); got != tc.want {
			t.Errorf("arrowRun(%d, %v) = %q, want %q", tc.lines, tc.appCursor, got, tc.want)
		}
	}
}
