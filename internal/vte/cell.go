package vte

// The Attr type (uint16) functions as a bitmask for ANSI SGR (Select Graphic Rendition) flags.
// Each style occupies exactly 1 bit
// AttrBold (1) — Bold text
// AttrItalic (2) — Italic text
// AttrUnderline (4) — Underlined text
// AttrInverse (8) — Inverted colors (swaps foreground and background)
// AttrFaint (16) — Dim/faint text
type Attr uint16

const (
	AttrBold Attr = 1 << iota
	AttrItalic
	AttrUnderline
	AttrInverse
	AttrFaint
)

// Top byte (bits 24–31): Color type (kindDefault, kindPalette, or kindRGB).
// Lower 3 bytes (bits 0–23): Color payload.
type Color uint32

const (
	kindDefault = iota
	kindPalette
	kindRGB

	kindShift = 24

	ColorDefault Color = kindDefault << kindShift
)

func PaletteColor(i int) Color { return kindPalette<<kindShift | Color(i&0xFF) }

func RGBColor(r, g, b int) Color {
	return kindRGB<<kindShift | Color(uint8(r))<<16 | Color(uint8(g))<<8 | Color(uint8(b))
}

func (c Color) Palette() (int, bool) { return int(c & 0xFF), c>>kindShift == kindPalette }

func (c Color) RGB() (r, g, b uint8, ok bool) {
	return uint8(c >> 16), uint8(c >> 8), uint8(c), c>>kindShift == kindRGB
}

type Cell struct {
	Rune   rune
	FG, BG Color
	Attrs  Attr
}

func (c Cell) Blank() bool {
	return (c.Rune == 0 || c.Rune == ' ') &&
		c.BG == ColorDefault &&
		c.Attrs&(AttrInverse|AttrUnderline) == 0
}

func (c Cell) Painted() bool { return c.BG != ColorDefault || c.Attrs&AttrInverse != 0 }

func TrimBlanks(cells []Cell) []Cell {
	end := len(cells)
	for end > 0 && cells[end-1].Blank() {
		end--
	}
	return cells[:end]
}

func CellAt(cells []Cell, i int) Cell {
	if i < len(cells) {
		return cells[i]
	}
	return Cell{}
}
