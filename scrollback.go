package main

import "gty/internal/font"

const maxScrollback = 10_000

// row is one line of history with its shaped glyphs. gids belong to the generation
// they were shaped in, so setCols invalidates every row at once.
type row struct {
	line line
	gids []font.GID
	gen  uint32
}

// scrollback is a pane's line history: a ring of at most maxScrollback rows.
//
// The shaped glyphs live next to the text because scrolling is otherwise dominated
// by reshaping — a screenful of code costs tens of milliseconds through harfbuzz,
// and every wheel notch is a frame.
type scrollback struct {
	rows     []row
	start, n int

	cols int
	gen  uint32
}

func newScrollback() *scrollback {
	return &scrollback{rows: make([]row, maxScrollback), gen: 1}
}

func (s *scrollback) Len() int { return s.n }

// At is the i-th line, oldest first.
func (s *scrollback) At(i int) *line { return &s.rows[(s.start+i)%len(s.rows)].line }

// Append adds l as the newest line, evicting the oldest once the ring is full.
func (s *scrollback) Append(l line) {
	i := (s.start + s.n) % len(s.rows)
	if s.n == len(s.rows) {
		i = s.start
		s.start = (s.start + 1) % len(s.rows)
	} else {
		s.n++
	}
	r := &s.rows[i]
	r.line, r.gids, r.gen = l, r.gids[:0], 0
}

// setCols invalidates the cache when the grid width changes: the cached runs are
// clipped to cols.
func (s *scrollback) setCols(cols int) {
	if cols != s.cols {
		s.cols, s.gen = cols, s.gen+1
	}
}

// shaped is the i-th line's glyphs, run through shape on the first ask since the
// last width change. shape appends to the row's own slice, so a second pass over
// the same history allocates nothing.
func (s *scrollback) shaped(i int, shape func(l *line, dst []font.GID) []font.GID) []font.GID {
	r := &s.rows[(s.start+i)%len(s.rows)]
	if r.gen != s.gen {
		r.gids = shape(&r.line, r.gids[:0])
		r.gen = s.gen
	}
	return r.gids
}
