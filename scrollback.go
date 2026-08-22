package main

const maxScrollback = 10_000

// scrollback is a pane's line history: the lines that have scrolled off the screen,
// in a ring of at most maxScrollback rows.
//
// The ring grows to its bound rather than being allocated at it. A line of cells is
// far heavier than the string it replaced, and a fresh pane holds none of them.
type scrollback struct {
	rows     []shapedRow
	start, n int

	// pushed counts every line ever appended, evicted ones included. A scrolled-back
	// view tracks it to stay on the same text: whether the ring grew or evicted, one
	// more line pushed means the view steps one line further back. The two cases
	// differ in whether Len moved, which is why Len cannot be used for this.
	pushed uint64

	cols int
	gen  uint32
}

func newScrollback() *scrollback { return &scrollback{gen: 1} }

func (s *scrollback) Len() int { return s.n }

// Gen is the generation a history row's cached glyphs have to match to be reused.
func (s *scrollback) Gen() uint32 { return s.gen }

// Row is the i-th line, oldest first.
func (s *scrollback) Row(i int) *shapedRow { return &s.rows[(s.start+i)%len(s.rows)] }

// Append adds cells as the newest line, evicting the oldest once the ring is full.
//
// The cells are copied: the caller is the screen, which recycles the row it has just
// retired as its new bottom line.
func (s *scrollback) Append(cells []cell) {
	s.pushed++

	var r *shapedRow
	if s.n < maxScrollback {
		// Still growing, so the ring has not wrapped yet: start is 0 and n indexes
		// straight onto the end.
		s.rows = append(s.rows, shapedRow{})
		s.n++
		r = &s.rows[s.n-1]
	} else {
		r = &s.rows[s.start]
		s.start = (s.start + 1) % len(s.rows)
	}
	r.cells = append(r.cells[:0], cells...)
	r.gids, r.gen = r.gids[:0], 0
}

// reset drops the history. ED 3 asks for this — it is what `clear` sends.
func (s *scrollback) reset() {
	s.rows, s.start, s.n = s.rows[:0], 0, 0
	s.gen = nextGen(s.gen)
}

// setCols invalidates every row at once when the grid width changes: the renderer
// clips a history line to the pane's columns, so its glyphs were shaped for a width.
func (s *scrollback) setCols(cols int) {
	if cols != s.cols {
		s.cols, s.gen = cols, nextGen(s.gen)
	}
}
