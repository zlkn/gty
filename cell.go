package main

import (
	"gty/internal/font"
	"gty/internal/vte"
)

// How a terminal cell becomes something drawable. What a colour resolves to and which face a
// style picks are the theme's and the font's, and neither belongs near the parser.

// cellColors is the cell's foreground and background as the renderer wants them, with
// inverse already applied — nothing downstream should have to remember it.
func cellColors(c vte.Cell) (fg, bg [4]float32) {
	fg, bg = resolveColor(c.FG, foreground), resolveColor(c.BG, backgroundRGBA)
	if c.Attrs&vte.AttrInverse != 0 {
		fg, bg = bg, fg
	}
	if c.Attrs&vte.AttrFaint != 0 {
		fg = dim(fg)
	}
	return fg, bg
}

// styleOf is the face a cell is drawn in. The terminal records bold and italic as attribute
// bits, because that is how the escape codes set them; which face they mean is the font's.
func styleOf(c vte.Cell) font.Style {
	var s font.Style
	if c.Attrs&vte.AttrBold != 0 {
		s |= font.Bold
	}
	if c.Attrs&vte.AttrItalic != 0 {
		s |= font.Italic
	}
	return s
}
