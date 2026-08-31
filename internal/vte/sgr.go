package vte

// sgr applies a Select Graphic Rendition sequence to the pen — the attributes every cell
// written after it will carry.
func (t *Terminal) sgr(c CSI) {
	pen := &t.scr.pen
	if len(c.Params) == 0 {
		*pen = Cell{} // a bare CSI m is a reset
		return
	}

	for i := 0; i < len(c.Params); i++ {
		switch v := c.Params[i]; {
		case v == 0:
			*pen = Cell{}
		case v == 1:
			pen.Attrs |= AttrBold
		case v == 2:
			pen.Attrs |= AttrFaint
		case v == 3:
			pen.Attrs |= AttrItalic
		case v == 4:
			pen.Attrs |= AttrUnderline
		case v == 7:
			pen.Attrs |= AttrInverse
		case v == 22: // normal intensity is neither of them
			pen.Attrs &^= AttrBold | AttrFaint
		case v == 23:
			pen.Attrs &^= AttrItalic
		case v == 24:
			pen.Attrs &^= AttrUnderline
		case v == 27:
			pen.Attrs &^= AttrInverse
		case v >= 30 && v <= 37:
			pen.FG = PaletteColor(v - 30)
		case v == 38:
			pen.FG, i = extendedColor(c, i)
		case v == 39:
			pen.FG = ColorDefault
		case v >= 40 && v <= 47:
			pen.BG = PaletteColor(v - 40)
		case v == 48:
			pen.BG, i = extendedColor(c, i)
		case v == 49:
			pen.BG = ColorDefault
		case v >= 90 && v <= 97:
			pen.FG = PaletteColor(v - 90 + 8)
		case v >= 100 && v <= 107:
			pen.BG = PaletteColor(v - 100 + 8)
		}
	}
}

// extendedColor reads an SGR 38 or 48 argument at index i, returning the colour and the index
// of its last parameter.
//
// Two forms are in the wild: 38;5;n and 38;2;r;g;b, and 38:2::r:g:b with an empty colour-space
// slot. Only the separator tells them apart.
func extendedColor(c CSI, i int) (Color, int) {
	if i+1 >= len(c.Params) {
		return ColorDefault, i
	}
	switch c.Params[i+1] {
	case 5: // palette index
		if i+2 < len(c.Params) {
			return PaletteColor(c.Params[i+2]), i + 2
		}
	case 2: // direct RGB
		j := i + 2
		if c.Sub(i+1) && j+3 < len(c.Params) {
			j++ // step over the empty colour space
		}
		if j+2 < len(c.Params) {
			return RGBColor(c.Params[j], c.Params[j+1], c.Params[j+2]), j + 2
		}
	}
	return ColorDefault, i + 1
}
