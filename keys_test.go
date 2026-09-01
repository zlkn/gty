package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-gl/glfw/v3.4/glfw"
)

func TestParseChord(t *testing.T) {
	good := []struct {
		in   string
		want chord
	}{
		{"q", chord{glfw.KeyQ, 0}},
		{"ctrl+shift+q", chord{glfw.KeyQ, glfw.ModControl | glfw.ModShift}},
		{"shift+pageup", chord{glfw.KeyPageUp, glfw.ModShift}},
		{"ctrl+tab", chord{glfw.KeyTab, glfw.ModControl}},
		{"f11", chord{glfw.KeyF11, 0}},
		{"f25", chord{glfw.KeyF25, 0}},
		{"super+1", chord{glfw.Key1, glfw.ModSuper}},
		{"ctrl+f", chord{glfw.KeyF, glfw.ModControl}}, // the letter, not a function key
		{"alt+space", chord{glfw.KeySpace, glfw.ModAlt}},
		// Case and the spaces around a name are noise; the order of the modifiers is not
		// meaningful either.
		{"CTRL+Shift+Q", chord{glfw.KeyQ, glfw.ModControl | glfw.ModShift}},
		{"shift + ctrl + q", chord{glfw.KeyQ, glfw.ModControl | glfw.ModShift}},
	}
	for _, tc := range good {
		got, err := parseChord(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("%q parsed to %v, want %v", tc.in, got, tc.want)
		}
	}

	bad := []string{"", "ctrl+", "+", "meta+q", "ctrl+shift+nope", "ctrl shift q", "f0", "f26", "f99", "ctrl+q+w", "минус"}
	for _, in := range bad {
		if got, err := parseChord(in); err == nil {
			t.Errorf("%q parsed to %v, want an error", in, got)
		}
	}
}

// Every chord has to print as something parseChord reads back, or the error messages
// name bindings nobody can write down.
func TestChordStringRoundTrips(t *testing.T) {
	for act, c := range keybinds {
		s := c.String()
		got, err := parseChord(s)
		if err != nil {
			t.Errorf("%s prints as %q, which does not parse: %v", actionLabel(act), s, err)
			continue
		}
		if got != c {
			t.Errorf("%s prints as %q, which parses back to %v, not %v", actionLabel(act), s, got, c)
		}
	}
}

// A bound chord has to swallow the character glfw may deliver from the same key event.
// Which modifiers stop that character differs by platform, so the claim is what makes
// Ctrl+Shift+D split a pane without typing a "d" into it as well.
func TestOnKeyClaimsTheCharacter(t *testing.T) {
	a := &app{focused: newPane(1)}

	a.onKey(glfw.KeyUp, glfw.ModControl|glfw.ModShift) // scroll_line_up
	if !a.keyClaimed {
		t.Fatal("a bound chord did not claim its character event")
	}
	a.typed('x')
	if a.keyClaimed {
		t.Error("the claim outlived the character it was there to swallow")
	}

	a.onKey(glfw.KeyF13, 0) // bound to nothing
	if a.keyClaimed {
		t.Error("an unbound key claimed a character event")
	}
}

// The defaults are the documented ones, and no two of them are the same chord.
func TestDefaultKeybinds(t *testing.T) {
	want := map[string]string{
		"quit":             "ctrl+shift+q",
		"split_vertical":   "ctrl+shift+d",
		"split_horizontal": "ctrl+shift+e",
		"close_pane":       "ctrl+shift+w",
		"focus_next":       "alt+o",
		"scroll_page_up":   "shift+pageup",
		"scroll_page_down": "shift+pagedown",
		"scroll_line_up":   "ctrl+shift+up",
		"scroll_line_down": "ctrl+shift+down",
		"new_tab":          "ctrl+shift+t",
		"close_tab":        "ctrl+shift+backspace",
		"next_tab":         "ctrl+tab",
		"prev_tab":         "ctrl+shift+tab",
	}
	// The nine digits, which keys.go binds in a loop for the same reason.
	for i := range numTabKeys {
		want[fmt.Sprintf("goto_tab_%d", i+1)] = fmt.Sprintf("ctrl+shift+%d", i+1)
	}
	if len(actionNames) != int(numActions) || len(keybinds) != int(numActions) {
		t.Errorf("%d actions, %d of them named and %d of them bound", numActions, len(actionNames), len(keybinds))
	}
	if len(want) != int(numActions) {
		t.Errorf("there are %d actions and %d expectations here", numActions, len(want))
	}
	for name, act := range actionNames {
		c, ok := keybinds[act]
		if !ok {
			t.Errorf("%s has no default binding", name)
			continue
		}
		if got := c.String(); got != want[name] {
			t.Errorf("%s defaults to %s, want %s", name, got, want[name])
		}
	}
	if len(boundKeys) != len(keybinds) {
		t.Errorf("%d bindings invert to %d chords, so two of them collide", len(keybinds), len(boundKeys))
	}
}

// The error for a misspelt action has to say what the spellings are, since the file
// cannot be told by toml that the key went undecoded.
func TestActionListNamesEveryAction(t *testing.T) {
	list := actionList()
	for name := range actionNames {
		if !strings.Contains(list, name) {
			t.Errorf("actionList() is %q, missing %q", list, name)
		}
	}
}

// TestKeyBytesApplicationCursor: the keys smkx moves go out as SS3 in application cursor mode
// and CSI outside it. terminfo promises kcuu1=\EOA and ncurses matches nothing else, which is
// why htop and ncdu saw no arrow keys at all while the mode was ignored.
func TestKeyBytesApplicationCursor(t *testing.T) {
	for _, tc := range []struct {
		key         glfw.Key
		normal, app string
	}{
		{glfw.KeyUp, "\x1b[A", "\x1bOA"},
		{glfw.KeyDown, "\x1b[B", "\x1bOB"},
		{glfw.KeyRight, "\x1b[C", "\x1bOC"},
		{glfw.KeyLeft, "\x1b[D", "\x1bOD"},
		{glfw.KeyHome, "\x1b[H", "\x1bOH"},
		{glfw.KeyEnd, "\x1b[F", "\x1bOF"},
	} {
		if got := string(keyBytes(tc.key, 0, false, false)); got != tc.normal {
			t.Errorf("key %v sends %q in normal mode, want %q", tc.key, got, tc.normal)
		}
		if got := string(keyBytes(tc.key, 0, true, false)); got != tc.app {
			t.Errorf("key %v sends %q in application mode, want %q", tc.key, got, tc.app)
		}
	}

	// Everything else stays CSI: terminfo has kpp=\E[5~ and knp=\E[6~ whatever smkx did.
	for _, tc := range []struct {
		key  glfw.Key
		want string
	}{
		{glfw.KeyPageUp, "\x1b[5~"},
		{glfw.KeyPageDown, "\x1b[6~"},
		{glfw.KeyDelete, "\x1b[3~"},
	} {
		for _, app := range []bool{false, true} {
			if got := string(keyBytes(tc.key, 0, app, false)); got != tc.want {
				t.Errorf("key %v with appCursor=%v sends %q, want %q", tc.key, app, got, tc.want)
			}
		}
	}
}

// TestXtermMod is the modifier parameter's arithmetic: one plus shift, alt, ctrl and super as
// bits one, two, four and eight. terminfo names every combination, from kUP=\E[1;2A up.
func TestXtermMod(t *testing.T) {
	for _, tc := range []struct {
		mods glfw.ModifierKey
		want int
	}{
		{0, 0},
		{glfw.ModShift, 2},
		{glfw.ModAlt, 3},
		{glfw.ModShift | glfw.ModAlt, 4},
		{glfw.ModControl, 5},
		{glfw.ModControl | glfw.ModShift, 6},
		{glfw.ModControl | glfw.ModAlt, 7},
		{glfw.ModControl | glfw.ModAlt | glfw.ModShift, 8},
		{glfw.ModSuper, 9},
	} {
		if got := xtermMod(tc.mods); got != tc.want {
			t.Errorf("mods %v give parameter %d, want %d", tc.mods, got, tc.want)
		}
	}
}

// TestKeyBytesModifiedCursor: a modifier goes into a CSI parameter, which is also what takes
// the sequence out of SS3 form — terminfo has kUP5=\E[1;5A, never \EO-anything.
func TestKeyBytesModifiedCursor(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  glfw.Key
		mods glfw.ModifierKey
		want string
	}{
		{"kUP, shift+up", glfw.KeyUp, glfw.ModShift, "\x1b[1;2A"},
		{"kUP5, ctrl+up", glfw.KeyUp, glfw.ModControl, "\x1b[1;5A"},
		{"kLFT3, alt+left", glfw.KeyLeft, glfw.ModAlt, "\x1b[1;3D"},
		{"kRIT3, alt+right", glfw.KeyRight, glfw.ModAlt, "\x1b[1;3C"},
		{"kHOM5, ctrl+home", glfw.KeyHome, glfw.ModControl, "\x1b[1;5H"},
		{"kEND5, ctrl+end", glfw.KeyEnd, glfw.ModControl, "\x1b[1;5F"},
		{"kDC5, ctrl+delete", glfw.KeyDelete, glfw.ModControl, "\x1b[3;5~"},
		{"kPRV5, ctrl+pageup", glfw.KeyPageUp, glfw.ModControl, "\x1b[5;5~"},
		{"kNXT5, ctrl+pagedown", glfw.KeyPageDown, glfw.ModControl, "\x1b[6;5~"},
	} {
		// Both cursor modes: a modified key is CSI either way.
		for _, app := range []bool{false, true} {
			if got := string(keyBytes(tc.key, tc.mods, app, false)); got != tc.want {
				t.Errorf("%s with appCursor=%v sends %q, want %q", tc.name, app, got, tc.want)
			}
		}
	}
}

// TestKeyBytesAltIsNotPrefixedOnCursorKeys: Alt is a bare escape in front of an ordinary key
// but a parameter inside a cursor key. Prefixing as well would send two escapes.
func TestKeyBytesAltIsNotPrefixedOnCursorKeys(t *testing.T) {
	if got, want := string(keyBytes(glfw.KeyUp, glfw.ModAlt, false, false)), "\x1b[1;3A"; got != want {
		t.Errorf("alt+up sends %q, want %q", got, want)
	}
	// An ordinary key still takes the prefix, which is what shells expect of Alt.
	if got, want := string(keyBytes(glfw.KeyEnter, glfw.ModAlt, false, false)), "\x1b\r"; got != want {
		t.Errorf("alt+enter sends %q, want %q", got, want)
	}
}

// TestKeyBytesKeypad: in application keypad mode the keypad sends SS3, after terminfo's
// kpZRO=\EOp and its neighbours. In numeric mode it sends nothing from here — the digit
// printed on the key reaches the character callback instead, and encoding it twice would
// double every keystroke.
func TestKeyBytesKeypad(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  glfw.Key
		want string
	}{
		{"kpZRO", glfw.KeyKP0, "\x1bOp"},
		{"kc1, KP1", glfw.KeyKP1, "\x1bOq"},
		{"kb2, KP5", glfw.KeyKP5, "\x1bOu"},
		{"ka1, KP7", glfw.KeyKP7, "\x1bOw"},
		{"ka3, KP9", glfw.KeyKP9, "\x1bOy"},
		{"kpDOT", glfw.KeyKPDecimal, "\x1bOn"},
		{"kpDIV", glfw.KeyKPDivide, "\x1bOo"},
		{"kpMUL", glfw.KeyKPMultiply, "\x1bOj"},
		{"kpSUB", glfw.KeyKPSubtract, "\x1bOm"},
		{"kpADD", glfw.KeyKPAdd, "\x1bOk"},
	} {
		if got := string(keyBytes(tc.key, 0, false, true)); got != tc.want {
			t.Errorf("%s in application mode sends %q, want %q", tc.name, got, tc.want)
		}
		if got := keyBytes(tc.key, 0, false, false); got != nil {
			t.Errorf("%s in numeric mode sends %q, want nothing", tc.name, got)
		}
	}

	// Enter is the exception: it has no character to fall back on, so numeric mode still owes
	// a carriage return.
	if got, want := string(keyBytes(glfw.KeyKPEnter, 0, false, true)), "\x1bOM"; got != want {
		t.Errorf("keypad enter in application mode sends %q, want %q (kent)", got, want)
	}
	if got, want := string(keyBytes(glfw.KeyKPEnter, 0, false, false)), "\r"; got != want {
		t.Errorf("keypad enter in numeric mode sends %q, want %q", got, want)
	}
}
