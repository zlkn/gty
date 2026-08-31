package main

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gl/glfw/v3.4/glfw"
)

// keepConfig restores everything a config load writes to after a test has loaded one
// over it. The colours, the font settings and the key bindings are all package state, and
// every other test in here reads them — the font ones included, because the coverage
// exponent is derived from both.
func keepConfig(t *testing.T) {
	t.Helper()
	bg, fg, sel, named := backgroundRGBA, foreground, selectionColor, base16
	tint, shape := cursorTint, cursorShapeDefault
	family, size, gamma, icons := fontFamily, fontSize, fontGamma, fontIconScale
	boxes, frame, blend := fontBoxDrawing, windowDecorations, fontBlend
	binds, bound := maps.Clone(keybinds), maps.Clone(boundKeys)
	t.Cleanup(func() {
		backgroundRGBA, foreground, selectionColor, base16 = bg, fg, sel, named
		cursorTint, cursorShapeDefault = tint, shape
		fontFamily, fontSize, fontGamma, fontIconScale = family, size, gamma, icons
		fontBoxDrawing, windowDecorations = boxes, frame
		fontBlend, blendUsed = blend, blend
		keybinds, boundKeys = binds, bound
		refreshTheme()
	})
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigColors(t *testing.T) {
	keepConfig(t)

	if err := loadConfig(filepath.Join("testdata", "config.toml")); err != nil {
		t.Fatal(err)
	}

	if want := [4]float32{0x10 / 255.0, 0x20 / 255.0, 0x30 / 255.0, 1}; backgroundRGBA != want {
		t.Errorf("background is %v, want %v", backgroundRGBA, want)
	}
	if want := [4]float32{0xa0 / 255.0, 0xb0 / 255.0, 0xc0 / 255.0, 1}; foreground != want {
		t.Errorf("foreground is %v, want %v", foreground, want)
	}
	if want := [4]float32{0x40 / 255.0, 0x50 / 255.0, 0x60 / 255.0, 0x80 / 255.0}; selectionColor != want {
		t.Errorf("selection is %v, want %v", selectionColor, want)
	}

	// The derived colours have to have moved with them. The clear value is derived at
	// the render pass rather than here, because it depends on the surface format: the
	// configured colour verbatim for a target that stores what it is given, decoded for
	// one that applies the sRGB encode itself.
	if got := clearValue(backgroundRGBA, false); got.R != float64(backgroundRGBA[0]) || got.B != float64(backgroundRGBA[2]) {
		t.Errorf("clear value is %v, want the configured background %v", got, backgroundRGBA)
	}
	if got := clearValue(backgroundRGBA, true); got.R >= float64(backgroundRGBA[0]) {
		t.Errorf("clear value for an sRGB target is %v, want it decoded below %v", got, backgroundRGBA)
	}
	if cursorColor != foreground {
		t.Errorf("cursor is %v, want the configured foreground %v", cursorColor, foreground)
	}
	// This fixture is light ink on a dark background, which is the case that wants no
	// bending: linear blending reads heavy enough there on its own.
	if coverageExp != 1 {
		t.Errorf("coverage exponent is %v, want 1 for light text on a dark background", coverageExp)
	}

	// The named end of the palette is the file's; the cube above it is still xterm's.
	if want := [4]float32{0, 0, 2 / 255.0, 1}; palette[1] != want {
		t.Errorf("palette[1] is %v, want %v", palette[1], want)
	}
	if want := [4]float32{0, 0, 0x12 / 255.0, 1}; palette[9] != want {
		t.Errorf("palette[9] is %v, want %v", palette[9], want)
	}
	if want := ([4]float32{0, 0, 0, 1}); palette[16] != want {
		t.Errorf("palette[16] is %v, want the xterm cube's %v", palette[16], want)
	}
}

func TestLoadConfigKeepsDefaultsForAbsentKeys(t *testing.T) {
	keepConfig(t)
	fg, sel, named := foreground, selectionColor, base16

	path := writeConfig(t, "[colors]\nbackground = \"#ffffff\"\n")
	if err := loadConfig(path); err != nil {
		t.Fatal(err)
	}

	if want := [4]float32{1, 1, 1, 1}; backgroundRGBA != want {
		t.Errorf("background is %v, want %v", backgroundRGBA, want)
	}
	if foreground != fg || selectionColor != sel || base16 != named {
		t.Error("a config that only set the background moved something else")
	}
}

func TestColorParsing(t *testing.T) {
	good := []struct {
		in   string
		want rgba
	}{
		{"#000000", rgba{0, 0, 0, 1}},
		{"#ffffff", rgba{1, 1, 1, 1}},
		{"#FFFFFF", rgba{1, 1, 1, 1}},
		{"#f2f2f2", rgba{0xf2 / 255.0, 0xf2 / 255.0, 0xf2 / 255.0, 1}},
		{"#01020304", rgba{1 / 255.0, 2 / 255.0, 3 / 255.0, 4 / 255.0}},
	}
	for _, tc := range good {
		var got rgba
		if err := got.UnmarshalText([]byte(tc.in)); err != nil {
			t.Errorf("%q: %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("%q parsed to %v, want %v", tc.in, got, tc.want)
		}
	}

	bad := []string{"", "#", "f2f2f2", "#f2f2f", "#f2f2f2f", "#gggggg", "#f2f2 2", "#-12345", "red"}
	for _, in := range bad {
		var got rgba
		if err := got.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("%q parsed to %v, want an error", in, got)
		}
	}
}

// TestLoadConfigCursor: the shape a pane starts with, and a cursor colour of its own
// where the theme otherwise takes the foreground.
func TestLoadConfigCursor(t *testing.T) {
	keepConfig(t)

	if err := loadConfig(writeConfig(t,
		"[cursor]\nshape = \" Bar \"\n\n[colors]\ncursor = \"#ff0000\"\n")); err != nil {
		t.Fatal(err)
	}
	if cursorShapeDefault != cursorBar {
		t.Errorf("shape is %d, want the bar %d", cursorShapeDefault, cursorBar)
	}
	if want := [4]float32{1, 0, 0, 1}; cursorColor != want {
		t.Errorf("cursor colour is %v, want %v", cursorColor, want)
	}
	if cursorColor == foreground {
		t.Error("the configured cursor colour still follows the foreground")
	}

	// A pane starts on it, and DECSCUSR 0 and RIS come back to it rather than to a block.
	p := vtPane(20, 3)
	if p.cursor.shape != cursorBar {
		t.Errorf("a fresh pane is shape %d, want the configured %d", p.cursor.shape, cursorBar)
	}
	for _, in := range []string{"\x1b[2 q\x1b[0 q", "\x1b[2 q\x1bc"} {
		p.feed([]byte(in))
		if p.cursor.shape != cursorBar {
			t.Errorf("%q left shape %d, want the configured %d", in, p.cursor.shape, cursorBar)
		}
	}
}

// TestLoadConfigFont covers the [font] section, whose keys are not colours but reach the
// renderer the same way — and whose gamma re-derives a colour-adjacent value.
func TestLoadConfigFont(t *testing.T) {
	keepConfig(t)

	if err := loadConfig(writeConfig(t,
		"[font]\nfamily = \"Iosevka\"\nsize = 13.5\ngamma = 2\nicon_scale = 0.6\nbox_drawing = false\nblend = \"linear\"\n")); err != nil {
		t.Fatal(err)
	}
	if fontFamily != "Iosevka" {
		t.Errorf("family is %q, want %q", fontFamily, "Iosevka")
	}
	if fontSize != 13.5 {
		t.Errorf("size is %v, want 13.5", fontSize)
	}
	if fontGamma != 2 {
		t.Errorf("gamma is %v, want 2", fontGamma)
	}
	// The knob overrides the theme, and the exponent is its reciprocal.
	if coverageExp != 0.5 {
		t.Errorf("coverage exponent is %v, want 0.5", coverageExp)
	}
	if fontIconScale != 0.6 {
		t.Errorf("icon_scale is %v, want 0.6", fontIconScale)
	}
	if fontBoxDrawing {
		t.Error("box_drawing = false left the frames drawn here")
	}
	if fontBlend != blendLinear {
		t.Errorf("blend is %v, want linear", fontBlend)
	}
}

// TestLoadConfigBlendDefaultsToGamma: the default is what keeps antialiased colour
// saturated, so a config that says nothing must not fall back to linear.
func TestLoadConfigBlendDefaultsToGamma(t *testing.T) {
	keepConfig(t)

	if fontBlend != blendGamma {
		t.Fatalf("the default blend is %v, want gamma", fontBlend)
	}
	if err := loadConfig(writeConfig(t, "[font]\nsize = 12\n")); err != nil {
		t.Fatal(err)
	}
	if fontBlend != blendGamma {
		t.Errorf("a config with no blend key left it %v", fontBlend)
	}
	if err := loadConfig(writeConfig(t, "[font]\nblend = \"sideways\"\n")); err == nil {
		t.Error("font.blend = \"sideways\" was accepted")
	} else if !strings.Contains(err.Error(), "gamma") || !strings.Contains(err.Error(), "linear") {
		t.Errorf("the error is %q; it has to list what is allowed", err)
	}
}

// TestLoadConfigWindow covers the [window] section, which is read once at startup
// rather than through refreshTheme.
func TestLoadConfigWindow(t *testing.T) {
	keepConfig(t)

	if !windowDecorations {
		t.Fatal("the system frame is off by default")
	}
	if err := loadConfig(writeConfig(t, "[window]\ndecorations = false\n")); err != nil {
		t.Fatal(err)
	}
	if windowDecorations {
		t.Error("decorations = false left the system frame on")
	}
}

// TestLoadConfigKeys covers the [keys] section: a rebind moves one action and leaves the
// other eight where they were, and an empty string takes a key back out of the window
// manager's hands.
func TestLoadConfigKeys(t *testing.T) {
	keepConfig(t)
	before := maps.Clone(keybinds)

	if err := loadConfig(writeConfig(t,
		"[keys]\nsplit_vertical = \"ALT+D\"\nfocus_next = \"\"\n")); err != nil {
		t.Fatal(err)
	}

	want := chord{glfw.KeyD, glfw.ModAlt}
	if got := keybinds[actionSplitVertical]; got != want {
		t.Errorf("split_vertical is %v, want %v", got, want)
	}
	if act, ok := boundKeys[want]; !ok || act != actionSplitVertical {
		t.Errorf("alt+d maps to %v (found %v), want split_vertical", act, ok)
	}
	// The chord it used to have is nobody's now, so it reaches the shell.
	if act, ok := boundKeys[before[actionSplitVertical]]; ok {
		t.Errorf("the old ctrl+shift+d still runs %s", actionLabel(act))
	}
	if _, ok := keybinds[actionFocusNext]; ok {
		t.Error(`focus_next = "" left the action bound`)
	}
	if act, ok := boundKeys[before[actionFocusNext]]; ok {
		t.Errorf("the unbound ctrl+tab still runs %s", actionLabel(act))
	}

	for act, c := range before {
		if act == actionSplitVertical || act == actionFocusNext {
			continue
		}
		if keybinds[act] != c {
			t.Errorf("%s moved to %v, want the default %v", actionLabel(act), keybinds[act], c)
		}
	}
}

// Swapping two bindings passes through a state where each clashes with the default it is
// replacing. Only the merged table is checked, so it loads.
func TestLoadConfigKeysSwap(t *testing.T) {
	keepConfig(t)

	if err := loadConfig(writeConfig(t,
		"[keys]\nquit = \"ctrl+shift+w\"\nclose_pane = \"ctrl+shift+q\"\n")); err != nil {
		t.Fatal(err)
	}
	if got := keybinds[actionQuit]; got.String() != "ctrl+shift+w" {
		t.Errorf("quit is %v, want ctrl+shift+w", got)
	}
	if got := keybinds[actionClosePane]; got.String() != "ctrl+shift+q" {
		t.Errorf("close_pane is %v, want ctrl+shift+q", got)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"bad colour", "[colors]\nbackground = \"#gg0000\"\n", "bad colour"},
		{"short ansi", "[colors]\nansi = [\"#000000\"]\n", "colors.ansi has 1 entries"},
		{"long bright", "[colors]\nbright = [\"#000000\", \"#000000\", \"#000000\", \"#000000\", \"#000000\", \"#000000\", \"#000000\", \"#000000\", \"#000000\"]\n", "colors.bright has 9 entries"},
		{"not toml", "[colors\nbackground =\n", "expected"},
		{"not a string", "[colors]\nbackground = 7\n", "background"},
		{"gamma at zero", "[font]\ngamma = 0\n", "font.gamma"},
		{"gamma out of range", "[font]\ngamma = 12\n", "font.gamma"},
		{"size out of range", "[font]\nsize = 0\n", "font.size"},
		// Zero is deliberately absent: unlike gamma, it is a legal icon_scale and means
		// "leave icons at the size the face draws them".
		{"icon scale negative", "[font]\nicon_scale = -0.5\n", "font.icon_scale"},
		{"icon scale past one", "[font]\nicon_scale = 1.5\n", "font.icon_scale"},
		{"box_drawing is not a bool", "[font]\nbox_drawing = \"yes\"\n", "box_drawing"},
		{"unknown cursor shape", "[cursor]\nshape = \"beam\"\n", `cursor.shape is "beam", want one of bar block underline`},
		{"cursor shape is not a string", "[cursor]\nshape = 7\n", "shape"},
		{"unknown action", "[keys]\nquti = \"ctrl+q\"\n", "keys.quti is not an action"},
		{"unknown key", "[keys]\nquit = \"ctrl+shift+minus\"\n", `has no key "minus"`},
		{"unknown modifier", "[keys]\nquit = \"meta+q\"\n", `has no modifier "meta"`},
		{"no key after the plus", "[keys]\nquit = \"ctrl+\"\n", "ends on a"},
		{"conflict with a default", "[keys]\nquit = \"ctrl+shift+d\"\n", "keys.quit and keys.split_vertical are both ctrl+shift+d"},
		{"conflict between two entries", "[keys]\nquit = \"alt+1\"\nclose_pane = \"alt+1\"\n", "are both alt+1"},
		{"binding is not a string", "[keys]\nquit = 7\n", "quit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keepConfig(t)
			err := loadConfig(writeConfig(t, tc.body))
			if err == nil {
				t.Fatalf("%q loaded without an error", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error is %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The example file is the documentation, so it has to be a file that loads — including
// every default it spells out for [keys], which is where a rename would show up first.
func TestExampleConfigLoads(t *testing.T) {
	keepConfig(t)
	before := maps.Clone(keybinds)

	if err := loadConfig("config.example.toml"); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(keybinds, before) {
		t.Errorf("the example bindings are %v, want the defaults %v", keybinds, before)
	}
}

// A config that is not there is fs.ErrNotExist and nothing else: main decides whether
// that matters, and it only does for an explicit -config.
func TestLoadConfigMissingFile(t *testing.T) {
	keepConfig(t)
	bg := backgroundRGBA

	err := loadConfig(filepath.Join(t.TempDir(), "absent.toml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing config gave %v, want fs.ErrNotExist", err)
	}
	if backgroundRGBA != bg {
		t.Error("a missing config changed the theme")
	}
}

// An unknown key is a typo, not a failure: the file still loads.
func TestLoadConfigUnknownKey(t *testing.T) {
	keepConfig(t)

	path := writeConfig(t, "[colors]\nforground = \"#ffffff\"\nbackground = \"#000000\"\n")
	if err := loadConfig(path); err != nil {
		t.Fatal(err)
	}
	if want := [4]float32{0, 0, 0, 1}; backgroundRGBA != want {
		t.Errorf("background is %v, want %v", backgroundRGBA, want)
	}
}
