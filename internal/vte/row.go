package vte

import "strings"

// Row is one line of the terminal: its cells, the identity a view's cache keys on, and the
// version that cache compares.
type Row struct {
	Cells []Cell

	// Seq is the line's absolute position in the session, so it survives being retired into
	// the history. Stamped on the way out; the live grid derives it from position.
	Seq uint64

	// Gen counts edits and never goes back. Zero is a line never written, which a view can
	// read as nothing to reuse.
	Gen uint64
}

// touch marks the line edited. A caller writing a run of cells touches once at the end.
func (r *Row) touch() { r.Gen++ }

// fill resets the row to cols copies of c, keeping its allocation. c carries the background
// the pen is painting with, for a scrolled-off row recycled as the new bottom line.
func (r *Row) fill(cols int, c Cell) {
	if cap(r.Cells) < cols {
		r.Cells = make([]Cell, cols)
	}
	r.Cells = r.Cells[:cols]
	for i := range r.Cells {
		r.Cells[i] = c
	}
	r.touch()
}

// resizeTo pads the row with unwritten cells or clips it to cols.
func (r *Row) resizeTo(cols int) {
	for len(r.Cells) < cols {
		r.Cells = append(r.Cells, Cell{})
	}
	r.Cells = r.Cells[:cols]
	r.touch()
}

// String is the line's text, trailing blanks dropped and unwritten cells read as spaces.
func (r Row) String() string {
	var b strings.Builder
	for _, c := range TrimBlanks(r.Cells) {
		if c.Rune == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(c.Rune)
	}
	return b.String()
}
