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
