package main

import "gty/internal/font"

// sgr applies a Select Graphic Rendition sequence to the pen — the attributes every
// cell written after it will carry.
func (p *pane) sgr(c csi) {
	pen := &p.scr.pen
	if len(c.params) == 0 {
		*pen = cell{} // a bare CSI m is a reset
		return
	}

	for i := 0; i < len(c.params); i++ {
		switch v := c.params[i]; {
		case v == 0:
			*pen = cell{}
		case v == 1:
			pen.Style |= font.Bold
		case v == 2:
			pen.Attrs |= attrFaint
		case v == 3:
			pen.Style |= font.Italic
		case v == 4:
			pen.Attrs |= attrUnderline
		case v == 7:
			pen.Attrs |= attrInverse
		case v == 22:
			pen.Style &^= font.Bold
			pen.Attrs &^= attrFaint
		case v == 23:
			pen.Style &^= font.Italic
		case v == 24:
			pen.Attrs &^= attrUnderline
		case v == 27:
			pen.Attrs &^= attrInverse
		case v >= 30 && v <= 37:
			pen.FG = paletteColor(v - 30)
		case v == 38:
			pen.FG, i = extendedColor(c, i)
		case v == 39:
			pen.FG = colorDefault
		case v >= 40 && v <= 47:
			pen.BG = paletteColor(v - 40)
		case v == 48:
			pen.BG, i = extendedColor(c, i)
		case v == 49:
			pen.BG = colorDefault
		case v >= 90 && v <= 97:
			pen.FG = paletteColor(v - 90 + 8)
		case v >= 100 && v <= 107:
			pen.BG = paletteColor(v - 100 + 8)
		}
	}
}

// extendedColor reads the argument of an SGR 38 or 48 starting at index i and returns
// the colour together with the index of its last parameter.
//
// Two forms are in the wild. Semicolons — 38;5;n and 38;2;r;g;b — and colons, where the
// truecolour form carries an extra colour-space slot everyone leaves empty:
// 38:2::r:g:b. Only the separator tells them apart, which is why the parser keeps it.
func extendedColor(c csi, i int) (color, int) {
	if i+1 >= len(c.params) {
		return colorDefault, i
	}
	switch c.params[i+1] {
	case 5: // palette index
		if i+2 < len(c.params) {
			return paletteColor(c.params[i+2]), i + 2
		}
	case 2: // direct RGB
		j := i + 2
		if c.sub(i+1) && j+3 < len(c.params) {
			j++ // step over the empty colour space
		}
		if j+2 < len(c.params) {
			return rgbColor(c.params[j], c.params[j+1], c.params[j+2]), j + 2
		}
	}
	return colorDefault, i + 1
}
