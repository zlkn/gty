package main

// tabWidth is the fixed tab stop. Real terminals move them with HTS and TBC; nothing
// gty talks to has asked yet.
const tabWidth = 8

// savedCursor is what DECSC keeps and DECRC puts back.
type savedCursor struct {
	row, col int
	pen      cell
	autowrap bool
}

// screen is the addressable grid a PTY writes to — the terminal's live view, as
// distinct from the history behind it. The cursor goes wherever the escape codes send
// it, and a line leaves for the scrollback only when the screen scrolls.
//
// out is nil on the alternate screen: nothing scrolls off it, because a full-screen
// program owns the grid and its repaints are not history.
type screen struct {
	lines []shapedRow // one per row; every cells slice is cols long
	cols  int
	out   *scrollback

	curRow, curCol int

	// top and bot are the DECSTBM scroll region, inclusive. Full screen unless a
	// program carves one out, which is how vim keeps its status line still.
	top, bot int

	// wrapNext parks the cursor past the last column rather than wrapping straight
	// away. A character written into the last column must not scroll the screen — that
	// happens on the character after it, if one ever comes. Wrapping eagerly instead
	// puts a blank line after every full-width line.
	wrapNext bool
	autowrap bool // DECAWM

	pen   cell // the attributes a written cell takes; SGR sets it
	saved savedCursor

	// gen invalidates every row's shaping at once, for the one thing that affects all
	// of them: a width change. An ordinary write zeroes its own row's gen instead.
	gen uint32
}

func newScreen(cols, rows int, out *scrollback) *screen {
	s := &screen{cols: max(cols, 0), out: out, autowrap: true, gen: 1}
	s.setHeight(max(rows, 0))
	return s
}

func (s *screen) height() int { return len(s.lines) }

// row is the i-th row of the grid, top first.
func (s *screen) row(i int) *shapedRow { return &s.lines[i] }

// erased is what a clear leaves behind: no rune, but the pen's background. Erasing
// paints — colouring a region by setting a background and wiping it is how every
// full-screen program fills space.
func (s *screen) erased() cell { return cell{BG: s.pen.BG} }

// resize refits the grid. There is no reflow: a line keeps its cells from the left and
// is clipped or padded on the right. Rewrapping history to a new width is its own
// problem, and this milestone does not attempt it.
func (s *screen) resize(cols, rows int) {
	if cols = max(cols, 0); cols != s.cols {
		s.cols = cols
		for i := range s.lines {
			s.lines[i].resizeTo(cols)
		}
		s.curCol = min(s.curCol, max(cols-1, 0))
		s.gen = nextGen(s.gen) // every row was shaped against the old width
	}
	s.setHeight(max(rows, 0))
	s.wrapNext = s.wrapNext && s.curCol == s.cols-1
}

// setHeight grows the grid with blank rows, or sheds rows off the top into the history
// and clips whatever is still over from the bottom.
//
// Shedding from the top first keeps the cursor — and so the prompt and whatever the
// shell just printed — on screen, which is what a terminal does when its window is
// dragged shorter.
func (s *screen) setHeight(rows int) {
	for s.height() < rows {
		var r shapedRow
		r.fill(s.cols, cell{})
		s.lines = append(s.lines, r)
	}
	if extra := s.height() - rows; extra > 0 {
		shed := min(extra, s.curRow)
		for i := range shed {
			if s.out != nil {
				s.out.Append(trimBlanks(s.lines[i].cells))
			}
		}
		copy(s.lines, s.lines[shed:])
		s.lines = s.lines[:rows]
		s.curRow -= shed
	}
	s.curRow = min(s.curRow, max(rows-1, 0))
	if rows == 0 {
		s.curCol, s.wrapNext = 0, false
	}
	// A program that carved out a region cannot know the grid changed under it, so the
	// region goes back to the whole screen — which is what xterm does too.
	s.top, s.bot = 0, max(rows-1, 0)
}

// setRegion is DECSTBM. An empty or inverted pair resets to the whole screen, and the
// cursor goes home, both as the standard requires.
func (s *screen) setRegion(top, bot int) {
	if bot <= 0 || bot > s.height() {
		bot = s.height()
	}
	if top < 1 || top >= bot {
		top = 1
	}
	s.top, s.bot = top-1, bot-1
	s.moveTo(0, 0)
}

// moveTo puts the cursor at an absolute position, clamped to the grid. Origin mode
// (DECOM) is not implemented, so this is always screen-absolute.
func (s *screen) moveTo(row, col int) {
	s.curRow = min(max(row, 0), max(s.height()-1, 0))
	s.curCol = min(max(col, 0), max(s.cols-1, 0))
	s.wrapNext = false
}

func (s *screen) moveBy(rows, cols int) { s.moveTo(s.curRow+rows, s.curCol+cols) }

// put writes r at the cursor and advances, spending a pending wrap first.
func (s *screen) put(r rune) {
	if s.cols == 0 || s.height() == 0 {
		return
	}
	if s.wrapNext {
		if s.autowrap {
			s.curCol = 0
			s.lineFeed()
		}
		s.wrapNext = false
	}
	c := s.pen
	c.Rune = r
	s.set(s.curRow, s.curCol, c)
	if s.curCol+1 < s.cols {
		s.curCol++
	} else if s.autowrap {
		s.wrapNext = true
	}
}

// set writes one cell and drops that row's cached glyphs.
func (s *screen) set(row, col int, c cell) {
	l := &s.lines[row]
	l.cells[col] = c
	l.gen = 0
}

// lineFeed moves down a row, scrolling the region when the cursor is on its last line.
func (s *screen) lineFeed() {
	s.wrapNext = false
	switch {
	case s.curRow == s.bot:
		s.scrollUp()
	case s.curRow+1 < s.height():
		s.curRow++
	}
}

// reverseIndex is RI: up a row, scrolling the region back when already at its top.
func (s *screen) reverseIndex() {
	s.wrapNext = false
	switch {
	case s.curRow == s.top:
		s.scrollDownAt(s.top)
	case s.curRow > 0:
		s.curRow--
	}
}

// scrollUp moves the region up one line. It reaches the history only when the region
// is the whole screen: a program that carved one out is managing its own window, and
// what leaves it is a repaint, not history.
func (s *screen) scrollUp() {
	s.scrollUpAt(s.top, s.top == 0 && s.bot == s.height()-1)
}

func (s *screen) scrollUpAt(top int, toHistory bool) {
	if s.height() == 0 || top > s.bot {
		return
	}
	row := s.lines[top]
	if toHistory && s.out != nil {
		s.out.Append(trimBlanks(row.cells)) // Append copies; row is about to be reused
	}
	copy(s.lines[top:s.bot], s.lines[top+1:s.bot+1])
	row.fill(s.cols, s.erased())
	s.lines[s.bot] = row
}

func (s *screen) scrollDownAt(top int) {
	if s.height() == 0 || top > s.bot {
		return
	}
	row := s.lines[s.bot]
	copy(s.lines[top+1:s.bot+1], s.lines[top:s.bot])
	row.fill(s.cols, s.erased())
	s.lines[top] = row
}

// insertLines and deleteLines work inside the region and never reach the history: they
// are a program rearranging its own screen.
func (s *screen) insertLines(n int) {
	if s.curRow < s.top || s.curRow > s.bot {
		return
	}
	for range min(n, s.bot-s.curRow+1) {
		s.scrollDownAt(s.curRow)
	}
	s.curCol, s.wrapNext = 0, false
}

func (s *screen) deleteLines(n int) {
	if s.curRow < s.top || s.curRow > s.bot {
		return
	}
	for range min(n, s.bot-s.curRow+1) {
		s.scrollUpAt(s.curRow, false)
	}
	s.curCol, s.wrapNext = 0, false
}

func (s *screen) insertChars(n int) {
	if s.height() == 0 || s.cols == 0 {
		return
	}
	l := &s.lines[s.curRow]
	n = min(n, s.cols-s.curCol)
	copy(l.cells[s.curCol+n:], l.cells[s.curCol:])
	s.fillRange(s.curRow, s.curCol, s.curCol+n)
}

func (s *screen) deleteChars(n int) {
	if s.height() == 0 || s.cols == 0 {
		return
	}
	l := &s.lines[s.curRow]
	n = min(n, s.cols-s.curCol)
	copy(l.cells[s.curCol:], l.cells[s.curCol+n:])
	s.fillRange(s.curRow, s.cols-n, s.cols)
}

func (s *screen) eraseChars(n int) { s.fillRange(s.curRow, s.curCol, s.curCol+n) }

// eraseInLine is EL: 0 to the end of the row, 1 from its start, 2 the whole row.
func (s *screen) eraseInLine(mode int) {
	if s.height() == 0 {
		return
	}
	switch mode {
	case 0:
		s.fillRange(s.curRow, s.curCol, s.cols)
	case 1:
		s.fillRange(s.curRow, 0, s.curCol+1)
	case 2:
		s.fillRange(s.curRow, 0, s.cols)
	}
}

// eraseInDisplay is ED: 0 to the end of the screen, 1 from its start, 2 all of it,
// 3 all of it and the history with it.
func (s *screen) eraseInDisplay(mode int) {
	if s.height() == 0 {
		return
	}
	switch mode {
	case 0:
		s.fillRange(s.curRow, s.curCol, s.cols)
		for r := s.curRow + 1; r < s.height(); r++ {
			s.fillRange(r, 0, s.cols)
		}
	case 1:
		for r := range s.curRow {
			s.fillRange(r, 0, s.cols)
		}
		s.fillRange(s.curRow, 0, s.curCol+1)
	case 2, 3:
		for r := range s.height() {
			s.fillRange(r, 0, s.cols)
		}
		if mode == 3 && s.out != nil {
			s.out.reset()
		}
	}
}

func (s *screen) fillRange(row, from, to int) {
	l := &s.lines[row]
	e := s.erased()
	for i := max(from, 0); i < min(to, len(l.cells)); i++ {
		l.cells[i] = e
	}
	l.gen = 0
}

func (s *screen) carriageReturn() { s.curCol, s.wrapNext = 0, false }

// backspace moves the cursor left without erasing — the shell redraws whatever it
// wants there. A pending wrap is spent instead of a column.
func (s *screen) backspace() {
	if s.wrapNext {
		s.wrapNext = false
		return
	}
	s.curCol = max(s.curCol-1, 0)
}

// tab moves to the next stop, or to the last column.
func (s *screen) tab() {
	s.wrapNext = false
	s.curCol = min((s.curCol/tabWidth+1)*tabWidth, max(s.cols-1, 0))
}

func (s *screen) save() {
	s.saved = savedCursor{row: s.curRow, col: s.curCol, pen: s.pen, autowrap: s.autowrap}
}

func (s *screen) restore() {
	s.pen, s.autowrap = s.saved.pen, s.saved.autowrap
	s.moveTo(s.saved.row, s.saved.col)
}

// reset returns the screen to a fresh state, as entering the alternate screen wants.
func (s *screen) reset() {
	s.pen = cell{}
	for r := range s.height() {
		s.fillRange(r, 0, s.cols)
	}
	s.top, s.bot = 0, max(s.height()-1, 0)
	s.autowrap, s.wrapNext = true, false
	s.moveTo(0, 0)
}
