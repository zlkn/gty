package vte

// Attr is the SGR state a cell carries. One bitfield and not a face beside it: the same
// sequences set and clear bold, italic and the rest.
type Attr uint16

const (
	AttrBold Attr = 1 << iota
	AttrItalic
	AttrUnderline
	AttrInverse
	AttrFaint
)

// Color is a packed terminal colour: a kind in the top byte and its argument below, so the
// zero value costs no initialising. Resolving one needs a palette, which is a view's.
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

// Palette is the colour's index into the 256-colour table, if that is what it is.
func (c Color) Palette() (int, bool) { return int(c & 0xFF), c>>kindShift == kindPalette }

// RGB is the colour's channels, if it was set directly.
func (c Color) RGB() (r, g, b uint8, ok bool) {
	return uint8(c >> 16), uint8(c >> 8), uint8(c), c>>kindShift == kindRGB
}

// Cell is one grid cell: sixteen bytes. A zero cell is an unwritten one, which is what makes
// a fill the way to blank a row.
type Cell struct {
	Rune   rune
	FG, BG Color
	Attrs  Attr
}

// Blank reports whether the cell would render as nothing. A background or an inverse makes a
// cell visible with no rune in it — that is how a program paints a block of colour.
func (c Cell) Blank() bool {
	return (c.Rune == 0 || c.Rune == ' ') &&
		c.BG == ColorDefault &&
		c.Attrs&(AttrInverse|AttrUnderline) == 0
}

// Painted reports whether the cell puts anything behind its glyph.
func (c Cell) Painted() bool { return c.BG != ColorDefault || c.Attrs&AttrInverse != 0 }

// TrimBlanks drops the trailing cells that carry nothing, so a line costs its own length
// rather than the grid's width. On a wide grid that is megabytes against tens of them.
func TrimBlanks(cells []Cell) []Cell {
	end := len(cells)
	for end > 0 && cells[end-1].Blank() {
		end--
	}
	return cells[:end]
}

// CellAt is cells[i], or a default cell past the end of the row.
func CellAt(cells []Cell, i int) Cell {
	if i < len(cells) {
		return cells[i]
	}
	return Cell{}
}
