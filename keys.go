package main

// The window manager's hotkeys. Everything here is a default: the [keys] section of the
// config file rebinds any of them by name, and an empty string unbinds. See
// config.example.toml.

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/go-gl/glfw/v3.4/glfw"
)

// action is one of the things the window manager does itself, as opposed to the far
// larger set of keys it hands to the shell untouched.
//
// One more of these means four edits in this file: the constant, actionNames, keybinds
// and the switch in dispatch. numActions holds the first three to each other; the fourth
// is why dispatch is kept in the same order as the constants.
type action int

const (
	actionQuit action = iota
	actionSplitVertical
	actionSplitHorizontal
	actionClosePane
	actionFocusNext
	actionScrollPageUp
	actionScrollPageDown
	actionScrollLineUp
	actionScrollLineDown

	numActions
)

// actionNames is what a [keys] entry may be called, and the only such list — the config
// struct decodes that section as a map rather than a field per action, so there is no
// second copy of these names to drift out of step with this one.
var actionNames = map[string]action{
	"quit":             actionQuit,
	"split_vertical":   actionSplitVertical,
	"split_horizontal": actionSplitHorizontal,
	"close_pane":       actionClosePane,
	"focus_next":       actionFocusNext,
	"scroll_page_up":   actionScrollPageUp,
	"scroll_page_down": actionScrollPageDown,
	"scroll_line_up":   actionScrollLineUp,
	"scroll_line_down": actionScrollLineDown,
}

// keybinds is the live table, and these are the defaults. Ctrl+Shift is nearly all of it
// on purpose: Escape, the bare arrows and Ctrl+Q all mean something to a terminal —
// Ctrl+Q is XON — so the window cannot have them. Shift+PageUp is the exception, since
// scrolling the pane's own history is what xterm does with it too.
//
// An action absent from the map is unbound, which is what an empty string in the file
// leaves behind.
var keybinds = map[action]chord{
	actionQuit:            {glfw.KeyQ, glfw.ModControl | glfw.ModShift},
	actionSplitVertical:   {glfw.KeyD, glfw.ModControl | glfw.ModShift},
	actionSplitHorizontal: {glfw.KeyE, glfw.ModControl | glfw.ModShift},
	actionClosePane:       {glfw.KeyW, glfw.ModControl | glfw.ModShift},
	actionFocusNext:       {glfw.KeyTab, glfw.ModControl},
	actionScrollPageUp:    {glfw.KeyPageUp, glfw.ModShift},
	actionScrollPageDown:  {glfw.KeyPageDown, glfw.ModShift},
	actionScrollLineUp:    {glfw.KeyUp, glfw.ModControl | glfw.ModShift},
	actionScrollLineDown:  {glfw.KeyDown, glfw.ModControl | glfw.ModShift},
}

// boundKeys is keybinds inverted, which is the direction a keystroke asks in. Rebuilt
// whenever the config file replaces a binding; see config.applyKeys.
var boundKeys = invertKeybinds()

func invertKeybinds() map[chord]action {
	m := make(map[chord]action, len(keybinds))
	for act, c := range keybinds {
		m[c] = act
	}
	return m
}

// dispatch runs a bound action: the window manager's whole vocabulary, in one place.
func (a *app) dispatch(act action) {
	switch act {
	case actionQuit:
		a.window.SetShouldClose(true)
	case actionSplitVertical:
		a.splitFocused(vertical)
	case actionSplitHorizontal:
		a.splitFocused(horizontal)
	case actionClosePane:
		a.closePane(a.focused)
	case actionFocusNext:
		a.focusNext()
	case actionScrollPageUp:
		a.scrollFocused(a.page())
	case actionScrollPageDown:
		a.scrollFocused(-a.page())
	case actionScrollLineUp:
		a.scrollFocused(1)
	case actionScrollLineDown:
		a.scrollFocused(-1)
	}
}

// chord is a key and the modifiers held with it. Matching is exact: ctrl+shift+pageup is
// a different chord from shift+pageup, and may be bound to something else.
type chord struct {
	key  glfw.Key
	mods glfw.ModifierKey
}

// bindableMods is everything a chord may carry. Masked off the incoming modifiers so
// that a lock key — which glfw only reports when asked to, but might be one day — cannot
// stop a binding from matching.
const bindableMods = glfw.ModShift | glfw.ModControl | glfw.ModAlt | glfw.ModSuper

// modNames is spelled in the order chords print in, so the table doubles as that order.
var modNames = []struct {
	name string
	mask glfw.ModifierKey
}{
	{"ctrl", glfw.ModControl},
	{"alt", glfw.ModAlt},
	{"shift", glfw.ModShift},
	{"super", glfw.ModSuper},
}

// namedKeys are the keys that spell nothing on their own. Letters, digits and function
// keys are not here; parseKeyName derives those, since glfw numbers each run contiguously.
var namedKeys = map[string]glfw.Key{
	"backspace": glfw.KeyBackspace,
	"delete":    glfw.KeyDelete,
	"down":      glfw.KeyDown,
	"end":       glfw.KeyEnd,
	"enter":     glfw.KeyEnter,
	"escape":    glfw.KeyEscape,
	"home":      glfw.KeyHome,
	"insert":    glfw.KeyInsert,
	"left":      glfw.KeyLeft,
	"pagedown":  glfw.KeyPageDown,
	"pageup":    glfw.KeyPageUp,
	"right":     glfw.KeyRight,
	"space":     glfw.KeySpace,
	"tab":       glfw.KeyTab,
	"up":        glfw.KeyUp,
}

var keyLabels = func() map[glfw.Key]string {
	m := make(map[glfw.Key]string, len(namedKeys))
	for name, k := range namedKeys {
		m[k] = name
	}
	return m
}()

// parseChord reads "ctrl+shift+q": the modifiers first, then exactly one key.
//
// The names are glfw's physical keys rather than whatever the layout prints on them, so a
// binding still works with a Cyrillic layout active — which is the point of binding by
// key at all.
func parseChord(s string) (chord, error) {
	var c chord
	fields := strings.Split(strings.ToLower(s), "+")
	for _, f := range fields[:len(fields)-1] {
		mod, ok := lookupMod(strings.TrimSpace(f))
		if !ok {
			return chord{}, fmt.Errorf("%q has no modifier %q; want %s", s, f, modList())
		}
		c.mods |= mod
	}

	name := strings.TrimSpace(fields[len(fields)-1])
	if name == "" {
		if len(fields) == 1 {
			return chord{}, errors.New("a chord needs a key")
		}
		return chord{}, fmt.Errorf("%q ends on a %q; want a key after it", s, "+")
	}
	key, ok := parseKeyName(name)
	if !ok {
		return chord{}, fmt.Errorf("%q has no key %q; want a letter, a digit, f1 to f25, or one of %s",
			s, name, strings.Join(slices.Sorted(maps.Keys(namedKeys)), " "))
	}
	c.key = key
	return c, nil
}

func lookupMod(name string) (glfw.ModifierKey, bool) {
	for _, m := range modNames {
		if m.name == name {
			return m.mask, true
		}
	}
	return 0, false
}

func modList() string {
	names := make([]string, len(modNames))
	for i, m := range modNames {
		names[i] = m.name
	}
	return strings.Join(names, " ")
}

func parseKeyName(s string) (glfw.Key, bool) {
	if len(s) == 1 {
		switch r := s[0]; {
		case r >= 'a' && r <= 'z':
			return glfw.KeyA + glfw.Key(r-'a'), true
		case r >= '0' && r <= '9':
			return glfw.Key0 + glfw.Key(r-'0'), true
		}
	}
	if digits, ok := strings.CutPrefix(s, "f"); ok {
		// Bounded by hand: glfw stops at F25, and Key(F1+99) is some other key entirely.
		if n, err := strconv.Atoi(digits); err == nil && n >= 1 && n <= 25 {
			return glfw.KeyF1 + glfw.Key(n-1), true
		}
	}
	k, ok := namedKeys[s]
	return k, ok
}

func (c chord) String() string {
	var b strings.Builder
	for _, m := range modNames {
		if c.mods&m.mask != 0 {
			b.WriteString(m.name)
			b.WriteByte('+')
		}
	}
	b.WriteString(keyName(c.key))
	return b.String()
}

func keyName(k glfw.Key) string {
	switch {
	case k >= glfw.KeyA && k <= glfw.KeyZ:
		return string(rune('a' + k - glfw.KeyA))
	case k >= glfw.Key0 && k <= glfw.Key9:
		return string(rune('0' + k - glfw.Key0))
	case k >= glfw.KeyF1 && k <= glfw.KeyF25:
		return "f" + strconv.Itoa(int(k-glfw.KeyF1)+1)
	}
	if name, ok := keyLabels[k]; ok {
		return name
	}
	return "key(" + strconv.Itoa(int(k)) + ")"
}

// actionLabel is actionNames backwards, for error messages about a binding the file did
// not necessarily name — the other half of a conflict is usually a default.
func actionLabel(act action) string {
	for name, a := range actionNames {
		if a == act {
			return name
		}
	}
	return "action(" + strconv.Itoa(int(act)) + ")"
}

func actionList() string {
	return strings.Join(slices.Sorted(maps.Keys(actionNames)), " ")
}
