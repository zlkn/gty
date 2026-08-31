package vte

// MaxScrollback is how many retired lines a terminal keeps.
const MaxScrollback = 10_000

// scrollback is the lines that have scrolled off the screen, in a ring of at most
// MaxScrollback rows. The ring grows to its bound rather than being allocated at it.
type scrollback struct {
	rows     []Row
	start, n int

	// retired counts every line ever appended, evicted ones included, so it is the absolute
	// number of the next line to arrive. This is what gives a Row its Seq.
	retired uint64
}

func (s *scrollback) len() int { return s.n }

// row is the i-th line, oldest first.
func (s *scrollback) row(i int) *Row { return &s.rows[(s.start+i)%len(s.rows)] }

// append adds src as the newest line, evicting the oldest once the ring is full.
//
// Cells are copied trimmed, because the screen recycles the row it has just retired. Gen
// comes with them: it is the same line, so a view keeps the glyphs it shaped on screen.
func (s *scrollback) append(src *Row) {
	s.retired++

	var r *Row
	if s.n < MaxScrollback {
		// Still growing, so the ring has not wrapped: start is 0 and n indexes onto the end.
		s.rows = append(s.rows, Row{})
		s.n++
		r = &s.rows[s.n-1]
	} else {
		r = &s.rows[s.start]
		s.start = (s.start + 1) % len(s.rows)
	}
	r.Cells = append(r.Cells[:0], TrimBlanks(src.Cells)...)
	r.Gen = src.Gen
}

// reset drops the history, as ED 3 asks. retired survives it: a line number that has been
// handed out must never be handed out again.
func (s *scrollback) reset() { s.rows, s.start, s.n = s.rows[:0], 0, 0 }
