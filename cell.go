package main

import "gty/internal/font"

// attrs are the SGR flags that are not a face and not a colour.
type attrs uint8

const (
	attrInverse attrs = 1 << iota
	attrUnderline
	attrFaint
)

// cell is one grid cell: sixteen bytes.
//
// A zero cell is an unwritten one, which is what makes clear() the way to blank a row
// and what lets rune 0 render as nothing. Colours are packed rather than the
// [4]float32 the renderer wants because a pane holds up to maxScrollback lines of
// these; the renderer resolves them once per cell at layout time.
type cell struct {
	Rune   rune
	FG, BG color
	Style  font.Style
	Attrs  attrs
}

// blank reports whether the cell would render as nothing. A background or an inverse
// makes a cell visible even with no rune in it — that is how a program paints a block
// of colour, and trimming it away would erase the paint.
func (c cell) blank() bool {
	return (c.Rune == 0 || c.Rune == ' ') &&
		c.BG == colorDefault &&
		c.Attrs&(attrInverse|attrUnderline) == 0
}

// trimBlanks drops the trailing run of cells that carry nothing.
//
// This is what keeps the history affordable: a line costs its own length rather than
// the pane's width, which on a wide pane is the difference between a few megabytes and
// forty.
func trimBlanks(cells []cell) []cell {
	end := len(cells)
	for end > 0 && cells[end-1].blank() {
		end--
	}
	return cells[:end]
}

// colors is the cell's foreground and background as the renderer wants them, with
// inverse already applied — nothing downstream should have to remember it.
func (c cell) colors() (fg, bg [4]float32) {
	fg, bg = c.FG.resolve(foreground), c.BG.resolve(backgroundRGBA)
	if c.Attrs&attrInverse != 0 {
		fg, bg = bg, fg
	}
	if c.Attrs&attrFaint != 0 {
		fg = dim(fg)
	}
	return fg, bg
}

// painted reports whether the cell puts anything behind its glyph.
func (c cell) painted() bool { return c.BG != colorDefault || c.Attrs&attrInverse != 0 }

// cellAt is cells[i], or a default cell past the end of the row.
func cellAt(cells []cell, i int) cell {
	if i < len(cells) {
		return cells[i]
	}
	return cell{}
}

// shapedRow is a row of cells with its shaped glyphs cached beside it.
//
// The cache exists because redrawing is otherwise dominated by reshaping — a screenful
// of code costs tens of milliseconds through harfbuzz, and every keystroke is a frame.
//
// gen is what invalidates it, and the two stores drive it differently: the scrollback
// bumps one counter for the whole ring when the width changes, because it may hold ten
// thousand rows; a screen row zeroes its own the moment something is written into it,
// because only a handful change per frame.
type shapedRow struct {
	cells []cell
	gids  []font.GID
	gen   uint32
}

// nextGen advances a generation counter, skipping zero: a row whose gen is zero is
// explicitly invalid, so a counter that wrapped onto it would silently validate stale
// glyphs.
func nextGen(g uint32) uint32 {
	if g++; g == 0 {
		return 1
	}
	return g
}

// shaped is the row's glyphs, run through shape on the first ask since the row was
// last invalidated. shape appends to the row's own slice, so a second pass over an
// unchanged row allocates nothing.
func (r *shapedRow) shaped(gen uint32, shape func(cells []cell, dst []font.GID) []font.GID) []font.GID {
	if r.gen != gen {
		r.gids = shape(r.cells, r.gids[:0])
		r.gen = gen
	}
	return r.gids
}

// fill resets the row to cols copies of c, keeping its allocations. Used when a
// scrolled-off row is recycled as the new bottom line, where c carries the background
// the pen is painting with.
func (r *shapedRow) fill(cols int, c cell) {
	if cap(r.cells) < cols {
		r.cells = make([]cell, cols)
	}
	r.cells = r.cells[:cols]
	for i := range r.cells {
		r.cells[i] = c
	}
	r.gids, r.gen = r.gids[:0], 0
}

// resizeTo pads the row with unwritten cells or clips it to cols.
func (r *shapedRow) resizeTo(cols int) {
	for len(r.cells) < cols {
		r.cells = append(r.cells, cell{})
	}
	r.cells, r.gen = r.cells[:cols], 0
}
