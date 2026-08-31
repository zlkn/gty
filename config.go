package main

// The config file is $XDG_CONFIG_HOME/gty/config.toml, or ~/.config/gty/config.toml.
// Every key in it is optional: an absent one keeps the built-in default
// See config.example.toml.

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ansiColors is the length of each half of the named palette:
// Eight normal colours, then eight bright ones.
const ansiColors = 8

type config struct {
	Colors colorConfig  `toml:"colors"`
	Cursor cursorConfig `toml:"cursor"`
	Font   fontConfig   `toml:"font"`
	Window windowConfig `toml:"window"`

	Keys map[string]string `toml:"keys"`
}

type windowConfig struct {
	// Decorations false hides the system titlebar and frame, leaving the tab bar as
	// the top of the window.
	Decorations *bool `toml:"decorations"`
}

// fontConfig picks the face the grid is drawn with. An absent family means the
// embedded JetBrains Mono, which is also what a family that cannot be found falls back
// to — see newText.
type fontConfig struct {
	Family    *string  `toml:"family"`
	Size      *float64 `toml:"size"`
	Gamma     *float64 `toml:"gamma"`
	IconScale *float64 `toml:"icon_scale"`

	// BoxDrawing false takes the frames and blocks from the face; see internal/font/boxdraw.go.
	BoxDrawing *bool `toml:"box_drawing"`
}

type colorConfig struct {
	Background *rgba  `toml:"background"`
	Foreground *rgba  `toml:"foreground"`
	Selection  *rgba  `toml:"selection"`
	Cursor     *rgba  `toml:"cursor"`
	ANSI       []rgba `toml:"ansi"`
	Bright     []rgba `toml:"bright"`
}

type cursorConfig struct {
	Shape *string `toml:"shape"`
}

// rgba is "#rrggbb" or "#rrggbbaa" in the file and a renderer colour here.
type rgba [4]float32

func (c *rgba) UnmarshalText(text []byte) error {
	s := string(text)
	digits, ok := strings.CutPrefix(s, "#")
	if !ok || (len(digits) != 6 && len(digits) != 8) {
		return badColor(s)
	}
	*c = rgba{0, 0, 0, 1}
	for i := 0; i < len(digits); i += 2 {
		v, err := strconv.ParseUint(digits[i:i+2], 16, 8)
		if err != nil {
			return badColor(s)
		}
		c[i/2] = float32(v) / 255
	}
	return nil
}

func badColor(s string) error {
	return fmt.Errorf("bad colour %q, want #rrggbb or #rrggbbaa", s)
}

// packed is the colour as buildPalette wants it. The palette carries no alpha, so an
// eight-digit entry loses it.
func (c rgba) packed() uint32 {
	q := func(v float32) uint32 { return uint32(min(max(v, 0), 1)*255 + 0.5) }
	return q(c[0])<<16 | q(c[1])<<8 | q(c[2])
}

// configPath is where gty looks when it was not told otherwise. Empty if the OS has no
// answer, which reads as no config at all.
func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "gty", "config.toml")
}

// loadConfig reads path into the theme.
//
// A missing file comes back as fs.ErrNotExist rather than as nothing to do, because
// whether that is an error belongs to the caller: the default path is allowed to be
// absent, an explicit -config is not.
func loadConfig(path string) error {
	var cfg config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			// The plain message gives the line; this one draws it, caret and all.
			return fmt.Errorf("%s:\n%s", path, pe.ErrorWithPosition())
		}
		return err
	}

	// A key nobody decoded is a typo, and silently ignoring it is how you spend an
	// evening wondering why the file does nothing.
	for _, key := range md.Undecoded() {
		fmt.Fprintf(os.Stderr, "gty: %s: unknown key %s\n", path, key)
	}

	if err := cfg.apply(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// apply moves the file's settings into the globals they override.
func (c config) apply() error {
	if err := c.applyKeys(); err != nil {
		return err
	}

	if f := c.Font.Family; f != nil {
		fontFamily = strings.TrimSpace(*f)
	}
	if sz := c.Font.Size; sz != nil {
		if *sz <= 0 || *sz > 400 {
			return fmt.Errorf("font.size is %v, want a size in points", *sz)
		}
		fontSize = *sz
	}
	if g := c.Font.Gamma; g != nil {
		// The range coverageExponent clamps to, rejected here instead: a value outside
		// it is a typo, and silently drawing something else is how a config file stops
		// being trustworthy.
		if *g < 0.25 || *g > 4 {
			return fmt.Errorf("font.gamma is %v, want a value between 0.25 and 4", *g)
		}
		fontGamma = *g
	}
	if sc := c.Font.IconScale; sc != nil {
		// Zero is legal and means "leave icons at the size the face draws them"; past 1
		// the ink would leave the cell vertically, where nothing is reserved for it.
		if *sc < 0 || *sc > 1 {
			return fmt.Errorf("font.icon_scale is %v, want a share of the cell's height "+
				"between 0 and 1, or 0 to leave icons at the size the face draws them", *sc)
		}
		fontIconScale = *sc
	}
	if bd := c.Font.BoxDrawing; bd != nil {
		fontBoxDrawing = *bd
	}

	if d := c.Window.Decorations; d != nil {
		windowDecorations = *d
	}

	if s := c.Cursor.Shape; s != nil {
		shape, ok := cursorShapeNames[strings.ToLower(strings.TrimSpace(*s))]
		if !ok {
			return fmt.Errorf("cursor.shape is %q, want one of %s", *s, cursorShapeList())
		}
		cursorShapeDefault = shape
	}

	cl := c.Colors
	if n := len(cl.ANSI); n != 0 && n != ansiColors {
		return fmt.Errorf("colors.ansi has %d entries, want %d", n, ansiColors)
	}
	if n := len(cl.Bright); n != 0 && n != ansiColors {
		return fmt.Errorf("colors.bright has %d entries, want %d", n, ansiColors)
	}

	if cl.Background != nil {
		backgroundRGBA = [4]float32(*cl.Background)
	}
	if cl.Foreground != nil {
		foreground = [4]float32(*cl.Foreground)
	}
	if cl.Selection != nil {
		selectionColor = [4]float32(*cl.Selection)
	}
	if cl.Cursor != nil {
		tint := [4]float32(*cl.Cursor)
		cursorTint = &tint
	}
	for i, c := range cl.ANSI {
		base16[i] = c.packed()
	}
	for i, c := range cl.Bright {
		base16[ansiColors+i] = c.packed()
	}

	refreshTheme()
	return nil
}

// applyKeys merges the file's [keys] over the default bindings. Absent actions keep
// their default, an empty string unbinds one, and anything the window manager does not
// claim goes on reaching the shell.
func (c config) applyKeys() error {
	binds := maps.Clone(keybinds)

	// Sorted, so a file with two mistakes in it reports the same one every run.
	for _, name := range slices.Sorted(maps.Keys(c.Keys)) {
		act, ok := actionNames[name]
		if !ok {
			return fmt.Errorf("keys.%s is not an action; want one of %s", name, actionList())
		}
		spec := strings.TrimSpace(c.Keys[name])
		if spec == "" {
			delete(binds, act)
			continue
		}
		ch, err := parseChord(spec)
		if err != nil {
			return fmt.Errorf("keys.%s: %w", name, err)
		}
		binds[act] = ch
	}

	// Conflicts are looked for once the whole section has been merged, not entry by
	// entry: a file that swaps two bindings passes through a state where each clashes
	// with the default it is replacing, and that is not a mistake.
	bound := make(map[chord]action, len(binds))
	for _, act := range slices.Sorted(maps.Keys(binds)) {
		if other, dup := bound[binds[act]]; dup {
			return fmt.Errorf("keys.%s and keys.%s are both %s", actionLabel(other), actionLabel(act), binds[act])
		}
		bound[binds[act]] = act
	}

	keybinds, boundKeys = binds, bound
	return nil
}
