package vte

// tabWidth is the spacing of the stops a screen starts with. HTS and TBC move them from
// there, so it is a default rather than the rule.
const tabWidth = 8

// savedCursor is what DECSC keeps and DECRC puts back.
type savedCursor struct {
	row, col int
	pen      Cell
	autowrap bool
	g        [4]charset
	gl       int
}

// screen is the addressable grid a shell writes to, as distinct from the history behind it: a
// line leaves for the scrollback only when the screen scrolls.
//
// out is nil on the alternate screen, where a full-screen program owns the grid.
type screen struct {
	lines []Row // one per row; every Cells slice is cols long
	cols  int
	out   *scrollback

	curRow, curCol int

	// top and bot are the DECSTBM scroll region, inclusive. Full screen unless a
	// program carves one out, which is how vim keeps its status line still.
	top, bot int

	// wrapNext parks the cursor past the last column rather than wrapping straight away: a
	// character in the last column must not scroll, or every full line gets a blank after it.
	wrapNext bool
	autowrap bool // DECAWM

	pen   Cell // the attributes a written cell takes; SGR sets it
	saved savedCursor

	// tabs marks the columns a tab stops on, one flag per column. A fixed step of eight
	// would do for a shell, but ncurses moves the stops with HTS and TBC and then trusts
	// them.
	tabs []bool

	// insert is IRM: a written cell pushes the rest of the line right rather than
	// overwriting what is there.
	insert bool

	// g holds the four character-set slots and gl the one invoked as GL, which SI and SO
	// switch between. Per screen, because DECSC and DECRC carry them along with the cursor.
	g  [4]charset
	gl int

	// last is the rune REP repeats. Only printing sets it, and moving the cursor clears
	// it: a repeat reaching across a control sequence would copy the wrong character.
	last rune
}

func newScreen(cols, rows int, out *scrollback) *screen {
	s := &screen{cols: max(cols, 0), out: out, autowrap: true}
	s.resetTabs()
	s.setHeight(max(rows, 0))
	return s
}

func (s *screen) height() int { return len(s.lines) }

// row is the i-th row of the grid, top first.
func (s *screen) row(i int) *Row { return &s.lines[i] }

// erased is what a clear leaves behind: no rune, but the pen's background. Erasing paints —
// that is how a full-screen program fills space.
func (s *screen) erased() Cell { return Cell{BG: s.pen.BG} }

// resize refits the grid. There is no reflow: a line is clipped or padded on the right.
func (s *screen) resize(cols, rows int) {
	if cols = max(cols, 0); cols != s.cols {
		s.cols = cols
		s.resizeTabs(cols)
		for i := range s.lines {
			s.lines[i].resizeTo(cols)
		}
		s.curCol = min(s.curCol, max(cols-1, 0))
	}
	s.setHeight(max(rows, 0))
	s.wrapNext = s.wrapNext && s.curCol == s.cols-1
}

// setHeight grows the grid with blank rows, or sheds them off the top into the history and
// clips the rest from the bottom. Top first, so the cursor and the prompt stay in view.
func (s *screen) setHeight(rows int) {
	for s.height() < rows {
		var r Row
		r.fill(s.cols, Cell{})
		s.lines = append(s.lines, r)
	}
	if extra := s.height() - rows; extra > 0 {
		shed := min(extra, s.curRow)
		for i := range shed {
			if s.out != nil {
				s.out.append(&s.lines[i])
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
	s.wrapNext, s.last = false, 0
}

func (s *screen) moveBy(rows, cols int) { s.moveTo(s.curRow+rows, s.curCol+cols) }

// put writes r at the cursor and advances, spending a pending wrap first.
func (s *screen) put(r rune) {
	if s.cols == 0 || s.height() == 0 {
		return
	}
	if s.wrapNext {
		if s.autowrap {
			s.lines[s.curRow].Wrapped = true
			s.curCol = 0
			s.lineFeed()
		}
		s.wrapNext = false
	}
	c := s.pen
	c.Rune = r
	if s.insert {
		s.insertChars(1)
	}
	s.set(s.curRow, s.curCol, c)
	s.last = r
	if s.curCol+1 < s.cols {
		s.curCol++
	} else if s.autowrap {
		s.wrapNext = true
	}
}

// set writes one cell and bumps the row's version.
func (s *screen) set(row, col int, c Cell) {
	l := &s.lines[row]
	l.Cells[col] = c
	l.touch()
}

// lineFeed moves down a row, scrolling the region when the cursor is on its last line.
func (s *screen) lineFeed() {
	s.wrapNext, s.last = false, 0
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

// scrollUp moves the region up one line, reaching the history only when the region is the
// whole screen: what leaves a carved-out region is a repaint, not history.
func (s *screen) scrollUp() {
	s.scrollUpAt(s.top, s.top == 0 && s.bot == s.height()-1)
}

func (s *screen) scrollUpAt(top int, toHistory bool) {
	if s.height() == 0 || top > s.bot {
		return
	}
	row := s.lines[top]
	if toHistory && s.out != nil {
		s.out.append(&row) // append copies; row is about to be reused
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
	copy(l.Cells[s.curCol+n:], l.Cells[s.curCol:])
	s.fillRange(s.curRow, s.curCol, s.curCol+n)
}

func (s *screen) deleteChars(n int) {
	if s.height() == 0 || s.cols == 0 {
		return
	}
	l := &s.lines[s.curRow]
	n = min(n, s.cols-s.curCol)
	copy(l.Cells[s.curCol:], l.Cells[s.curCol+n:])
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
	if to >= s.cols {
		l.Wrapped = false
	}
	e := s.erased()
	for i := max(from, 0); i < min(to, len(l.Cells)); i++ {
		l.Cells[i] = e
	}
	l.touch()
}

func (s *screen) carriageReturn() { s.curCol, s.wrapNext, s.last = 0, false, 0 }

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
func (s *screen) tab() { s.tabForward(1) }

// tabForward is CHT: n stops to the right, the last column when the stops run out.
func (s *screen) tabForward(n int) {
	s.wrapNext = false
	for range max(n, 1) {
		s.curCol = s.nextStop(s.curCol)
	}
}

// tabBack is CBT: n stops to the left, the first column when they run out.
func (s *screen) tabBack(n int) {
	s.wrapNext = false
	for range max(n, 1) {
		s.curCol = s.prevStop(s.curCol)
	}
}

func (s *screen) nextStop(col int) int {
	for i := col + 1; i < len(s.tabs); i++ {
		if s.tabs[i] {
			return i
		}
	}
	return max(s.cols-1, 0)
}

func (s *screen) prevStop(col int) int {
	for i := min(col, len(s.tabs)) - 1; i > 0; i-- {
		if s.tabs[i] {
			return i
		}
	}
	return 0
}

// setTab is HTS: the cursor's column becomes a stop.
func (s *screen) setTab() {
	if s.curCol < len(s.tabs) {
		s.tabs[s.curCol] = true
	}
}

// clearTab is TBC: 0 drops the stop under the cursor, 3 drops every one of them.
func (s *screen) clearTab(mode int) {
	switch mode {
	case 0:
		if s.curCol < len(s.tabs) {
			s.tabs[s.curCol] = false
		}
	case 3:
		clear(s.tabs)
	}
}

// repeat is REP. Nothing printed yet means nothing to copy.
func (s *screen) repeat(n int) {
	r := s.last
	if r == 0 {
		return
	}
	for range max(n, 1) {
		s.put(r)
	}
}

// resetTabs puts the stops back every eight columns, where a terminal starts. Column one
// is not one of them: a tab from there lands on column nine.
func (s *screen) resetTabs() {
	s.tabs = make([]bool, s.cols)
	for i := tabWidth; i < s.cols; i += tabWidth {
		s.tabs[i] = true
	}
}

// resizeTabs refits the table to a new width. The stops inside the old width are the
// program's and are kept; the columns beyond it are new and take the default.
func (s *screen) resizeTabs(cols int) {
	old := s.tabs
	s.tabs = make([]bool, cols)
	n := min(len(old), cols)
	copy(s.tabs, old[:n])
	for i := max(((n+tabWidth-1)/tabWidth)*tabWidth, tabWidth); i < cols; i += tabWidth {
		s.tabs[i] = true
	}
}

func (s *screen) save() {
	s.saved = savedCursor{
		row: s.curRow, col: s.curCol, pen: s.pen, autowrap: s.autowrap,
		g: s.g, gl: s.gl,
	}
}

func (s *screen) restore() {
	s.pen, s.autowrap = s.saved.pen, s.saved.autowrap
	s.g, s.gl = s.saved.g, s.saved.gl
	s.moveTo(s.saved.row, s.saved.col)
}

// softReset is DECSTR: the modes a program can change go back to their defaults, while the
// grid, the history and the cursor's position stay as they are. The saved cursor goes home,
// which is the one thing the standard does move.
func (s *screen) softReset() {
	s.pen = Cell{}
	s.insert = false
	s.g, s.gl = [4]charset{}, 0
	s.autowrap, s.wrapNext = true, false
	s.top, s.bot = 0, max(s.height()-1, 0)
	s.saved = savedCursor{autowrap: true}
}

// reset returns the screen to a fresh state, as entering the alternate screen wants.
func (s *screen) reset() {
	s.pen = Cell{}
	s.insert = false
	s.g, s.gl = [4]charset{}, 0
	s.last = 0
	s.resetTabs()
	for r := range s.height() {
		s.fillRange(r, 0, s.cols)
	}
	s.top, s.bot = 0, max(s.height()-1, 0)
	s.autowrap, s.wrapNext = true, false
	s.moveTo(0, 0)
}
