package main

import (
	"gty/internal/font"
	"gty/internal/vte"
)

// shapedRow is one line's shaped glyphs, beside the line and the version they were shaped
// from.
type shapedRow struct {
	seq, gen uint64
	gids     []font.GID
}

// rowCache holds the shaped glyphs of the lines a pane has drawn. It exists because a
// screenful of code costs tens of milliseconds through harfbuzz, and every keystroke is a frame.
//
// A line takes the slot its Seq picks, and Seq does not move when the screen scrolls, so one
// line of output leaves every other entry where it was. fit covers every line a view can reach.
type rowCache struct{ slots []shapedRow }

// shaped is the row's glyphs, run through shape on the first ask since the line changed. shape
// appends to the slot's own slice, so an unchanged line costs no allocation.
func (c *rowCache) shaped(r *vte.Row, shape func(cells []vte.Cell, dst []font.GID) []font.GID) []font.GID {
	if len(c.slots) == 0 {
		return shape(r.Cells, nil)
	}
	// Gen zero is an unwritten line and also an empty slot; either way there is nothing to
	// reuse. Seq is checked too, because a slot is shared by every line congruent to it.
	s := &c.slots[r.Seq%uint64(len(c.slots))]
	if r.Gen == 0 || s.seq != r.Seq || s.gen != r.Gen {
		s.gids = shape(r.Cells, s.gids[:0])
		s.seq, s.gen = r.Seq, r.Gen
	}
	return s.gids
}

// fit widens the cache to hold lines of them, dropping what it held when it reallocates. It
// only grows, and the history it must cover stops at the scrollback's bound.
func (c *rowCache) fit(lines int) {
	if lines <= len(c.slots) {
		return
	}
	n := max(len(c.slots), 1)
	for n < lines {
		n *= 2
	}
	c.slots = make([]shapedRow, n)
}

// reset drops every entry, for the changes that reach past the grid: a new font, or a new
// display scale.
func (c *rowCache) reset() { clear(c.slots) }
