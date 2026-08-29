package main

import (
	"image"
	"slices"
	"strings"
	"testing"

	"gty/internal/font"
)

// vtPane is a pane driven the way a program drives it: bytes in, grid out.
func vtPane(cols, rows int, input ...string) *pane {
	p := gridPane(1, image.Rect(0, 0, 1200, 900), cols, rows)
	for _, s := range input {
		p.feed([]byte(s))
	}
	return p
}

func TestSGRColors(t *testing.T) {
	p := vtPane(40, 2, "\x1b[31mR\x1b[92mB\x1b[38;5;123mP\x1b[38;2;10;20;30mT\x1b[39mD")
	cells := p.scr.row(0).cells

	for i, want := range []color{
		paletteColor(1),      // 31: red
		paletteColor(8 + 2),  // 92: bright green
		paletteColor(123),    // 38;5;n
		rgbColor(10, 20, 30), // 38;2;r;g;b
		colorDefault,         // 39
	} {
		if got := cells[i].FG; got != want {
			t.Errorf("cell %d foreground is %#x, want %#x", i, got, want)
		}
	}
}

// TestSGRTrueColorForms: both spellings of a direct colour have to land on the same
// value, and only the separator distinguishes them.
func TestSGRTrueColorForms(t *testing.T) {
	p := vtPane(40, 2, "\x1b[38;2;1;2;3mA\x1b[38:2::1:2:3mB\x1b[38:2:1:2:3mC")
	cells := p.scr.row(0).cells

	want := rgbColor(1, 2, 3)
	for i, name := range []string{"semicolons", "colons with a colour space", "colons without"} {
		if got := cells[i].FG; got != want {
			t.Errorf("%s gave %#x, want %#x", name, got, want)
		}
	}
}

func TestSGRAttributes(t *testing.T) {
	p := vtPane(40, 2, "\x1b[1mb\x1b[3mi\x1b[22mI\x1b[0mp\x1b[7mv\x1b[4mu")
	cells := p.scr.row(0).cells

	for i, tc := range []struct {
		style font.Style
		attrs attrs
	}{
		{font.Bold, 0},
		{font.Bold | font.Italic, 0},
		{font.Italic, 0}, // 22 drops bold and leaves italic
		{font.Regular, 0},
		{font.Regular, attrInverse},
		{font.Regular, attrInverse | attrUnderline},
	} {
		if cells[i].Style != tc.style || cells[i].Attrs != tc.attrs {
			t.Errorf("cell %d is style %v attrs %d, want style %v attrs %d",
				i, cells[i].Style, cells[i].Attrs, tc.style, tc.attrs)
		}
	}
}

// TestSGRInverseSwapsColors: inverse is resolved once, in the cell, so neither the
// glyph pass nor the background pass has to remember it.
func TestSGRInverseSwapsColors(t *testing.T) {
	p := vtPane(40, 2, "\x1b[31;44;7mX")
	c := p.scr.row(0).cells[0]

	fg, bg := c.colors()
	if want := palette[1]; bg != want {
		t.Errorf("inverse paints with %v, want the red the pen had as its foreground %v", bg, want)
	}
	if want := palette[4]; fg != want {
		t.Errorf("inverse inks with %v, want the blue the pen had as its background %v", fg, want)
	}
	if !c.painted() {
		t.Error("an inverse cell reports nothing to paint; its block would not be drawn")
	}
}

// TestErasePaintsTheBackground: a program colours a region by setting a background and
// wiping — an erase that dropped the pen would leave the region blank instead.
func TestErasePaintsTheBackground(t *testing.T) {
	p := vtPane(10, 3, "\x1b[44m\x1b[2J")

	for r := range 3 {
		for _, c := range p.scr.row(r).cells {
			if c.BG != paletteColor(4) {
				t.Fatalf("row %d holds %#x after a blue erase, want %#x", r, c.BG, paletteColor(4))
			}
		}
	}
	if trimBlanks(p.scr.row(0).cells) == nil {
		t.Error("the painted row trimmed away; the paint would never be drawn")
	}
}

func TestCursorAddressing(t *testing.T) {
	p := vtPane(20, 5,
		"\x1b[3;5Hmid", // CUP row 3 col 5
		"\x1b[HTL",     // home
		"\x1b[2;2H\x1b[3CX",
	)
	want := []string{"TL", "    X", "    mid", "", ""}
	if got := screenText(p.scr); !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
}

func TestEraseInLineAndDisplay(t *testing.T) {
	p := vtPane(10, 3, "abcdefghij\r\n0123456789\r\nXXXXXXXXXX")

	p.feed([]byte("\x1b[1;4H\x1b[K")) // row 1, col 4, erase to the end
	if got, want := screenText(p.scr)[0], "abc"; got != want {
		t.Errorf("after EL 0 row 0 is %q, want %q", got, want)
	}

	p.feed([]byte("\x1b[2;4H\x1b[1K")) // erase from the start of row 2
	if got, want := screenText(p.scr)[1], "    456789"; got != want {
		t.Errorf("after EL 1 row 1 is %q, want %q", got, want)
	}

	p.feed([]byte("\x1b[2;1H\x1b[J")) // erase from row 2 to the end of the screen
	if got, want := screenText(p.scr), []string{"abc", "", ""}; !slices.Equal(got, want) {
		t.Errorf("after ED 0 the screen is %q, want %q", got, want)
	}
}

func TestInsertAndDeleteLines(t *testing.T) {
	p := vtPane(10, 4, "one\r\ntwo\r\nthree")

	p.feed([]byte("\x1b[2;1H\x1b[L")) // open a line above "two"
	if got, want := screenText(p.scr), []string{"one", "", "two", "three"}; !slices.Equal(got, want) {
		t.Errorf("after IL the screen is %q, want %q", got, want)
	}

	p.feed([]byte("\x1b[2;1H\x1b[M")) // and take it back out
	if got, want := screenText(p.scr), []string{"one", "two", "three", ""}; !slices.Equal(got, want) {
		t.Errorf("after DL the screen is %q, want %q", got, want)
	}

	// Neither may reach the history: this is a program rearranging its own screen.
	if p.buf.Len() != 0 {
		t.Errorf("%d lines leaked into the history", p.buf.Len())
	}
}

func TestInsertAndDeleteChars(t *testing.T) {
	p := vtPane(10, 2, "abcdef")

	p.feed([]byte("\x1b[1;3H\x1b[2@")) // two blanks before 'c'
	if got, want := screenText(p.scr)[0], "ab  cdef"; got != want {
		t.Errorf("after ICH the row is %q, want %q", got, want)
	}

	p.feed([]byte("\x1b[1;3H\x1b[2P"))
	if got, want := screenText(p.scr)[0], "abcdef"; got != want {
		t.Errorf("after DCH the row is %q, want %q", got, want)
	}
}

// TestScrollRegion is what keeps a status line still while the text above it moves.
func TestScrollRegion(t *testing.T) {
	p := vtPane(10, 4, "a\r\nb\r\nc\r\nSTATUS")

	p.feed([]byte("\x1b[1;3r"))      // region is rows 1..3, the status line is outside
	p.feed([]byte("\x1b[3;1H\r\nd")) // from the region's last row, feed past it

	if got, want := screenText(p.scr), []string{"b", "c", "d", "STATUS"}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q — the status line must not move", got, want)
	}
	// A scroll inside a region is a repaint, not history.
	if p.buf.Len() != 0 {
		t.Errorf("%d lines went to the history from inside a scroll region", p.buf.Len())
	}
}

func TestSaveAndRestoreCursor(t *testing.T) {
	p := vtPane(20, 4, "\x1b[2;5H\x1b[31m\x1b7\x1b[4;1Hlow\x1b8X")

	if got, want := screenText(p.scr)[1], "    X"; got != want {
		t.Errorf("row 1 is %q, want %q — the cursor did not come back", got, want)
	}
	if got := p.scr.row(1).cells[4].FG; got != paletteColor(1) {
		t.Errorf("the restored pen is %#x, want the red it was saved with", got)
	}
}

// TestAlternateScreen is what vim runs in: a second grid that keeps no history and
// hands the prompt back untouched on the way out.
func TestAlternateScreen(t *testing.T) {
	p := vtPane(20, 3, "prompt$ vim\r\n")
	before := screenText(p.scr)

	p.feed([]byte("\x1b[?1049h")) // enter
	if p.scr != p.alt {
		t.Fatal("1049h did not switch to the alternate screen")
	}
	p.feed([]byte("~\r\n~\r\nEDITOR"))
	if got, want := screenText(p.scr), []string{"~", "~", "EDITOR"}; !slices.Equal(got, want) {
		t.Errorf("the alternate screen reads %q, want %q", got, want)
	}
	if p.histLen() != 0 {
		t.Errorf("the alternate screen shows %d history rows, want none", p.histLen())
	}

	p.feed([]byte("\x1b[?1049l")) // leave
	if p.scr != p.pri {
		t.Fatal("1049l did not switch back")
	}
	if got := screenText(p.scr); !slices.Equal(got, before) {
		t.Errorf("the primary screen came back as %q, want %q", got, before)
	}
}

func TestCursorVisibilityMode(t *testing.T) {
	p := vtPane(20, 3)
	if !p.cursor.on {
		t.Fatal("a fresh pane starts with the cursor hidden")
	}
	p.feed([]byte("\x1b[?25l"))
	if p.cursor.on {
		t.Error("DECTCEM reset left the cursor visible")
	}
	p.feed([]byte("\x1b[?25h"))
	if !p.cursor.on {
		t.Error("DECTCEM set left the cursor hidden")
	}
}

// TestCursorStyle: neovim marks its modes with DECSCUSR alone, so a terminal that drops
// the sequence shows a block cursor in insert mode.
func TestCursorStyle(t *testing.T) {
	p := vtPane(20, 3)
	if p.cursor.shape != cursorShapeDefault {
		t.Fatalf("a fresh pane is shape %d, want the default %d", p.cursor.shape, cursorShapeDefault)
	}

	for _, tc := range []struct {
		in   string
		want cursorShape
	}{
		{"\x1b[6 q", cursorBar}, // steady bar: neovim's insert mode
		{"\x1b[5 q", cursorBar}, // blinking bar, same shape
		{"\x1b[3 q", cursorUnderline},
		{"\x1b[2 q", cursorBlock}, // and back to normal mode
		{"\x1b[6 q", cursorBar},
		{"\x1b[0 q", cursorShapeDefault},
		{"\x1b[6 q", cursorBar},
		{"\x1b[9 q", cursorBar},       // out of range: the shape stands
		{"\x1b[>0q", cursorBar},       // a private q is not a cursor style
		{"\x1bc", cursorShapeDefault}, // RIS
	} {
		p.feed([]byte(tc.in))
		if p.cursor.shape != tc.want {
			t.Errorf("%q left shape %d, want %d", tc.in, p.cursor.shape, tc.want)
		}
	}
}

func TestAutowrapMode(t *testing.T) {
	p := vtPane(4, 2, "\x1b[?7l", "abcdef")
	if got, want := screenText(p.scr), []string{"abcf", ""}; !slices.Equal(got, want) {
		t.Errorf("with autowrap off the screen reads %q, want %q — the last cell is overwritten", got, want)
	}
}

// TestCursorPositionReport: fish asks where the cursor is on every redraw, and a
// terminal that does not answer leaves it guessing.
func TestCursorPositionReport(t *testing.T) {
	p := vtPane(20, 5, "\x1b[3;7H")
	if got, want := string(p.feed([]byte("\x1b[6n"))), "\x1b[3;7R"; got != want {
		t.Errorf("CPR answered %q, want %q", got, want)
	}
	if got, want := string(p.feed([]byte("\x1b[5n"))), "\x1b[0n"; got != want {
		t.Errorf("DSR answered %q, want %q", got, want)
	}
}

func TestReverseIndexAndNextLine(t *testing.T) {
	p := vtPane(10, 3, "one\r\ntwo")

	p.feed([]byte("\x1bM")) // RI, up a row
	if p.scr.curRow != 0 {
		t.Errorf("RI left the cursor on row %d, want 0", p.scr.curRow)
	}
	col := p.scr.curCol
	p.feed([]byte("\x1bM")) // RI at the top scrolls the region down instead
	if p.scr.curCol != col {
		t.Errorf("RI moved the cursor to column %d; like LF it only feeds a line", p.scr.curCol)
	}

	p.feed([]byte("\rtop"))
	if got, want := screenText(p.scr), []string{"top", "one", "two"}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
}

// TestPaintedCellsBecomeQuads: a background is not a glyph, so it has to reach the rect
// renderer. Runs of one colour are coalesced, or a full screen would be one quad a cell.
func TestPaintedCellsBecomeQuads(t *testing.T) {
	p := vtPane(20, 2, "\x1b[41mRRRR\x1b[44mBB\x1b[0mplain")
	p.rect = image.Rect(0, 0, 400, 100)

	quads := paintRects(nil, p, testCellW, testCellH)
	if len(quads) != 2 {
		t.Fatalf("got %d quads, want two runs: %v", len(quads), quads)
	}
	if got, want := quads[0].color, palette[1]; got != want {
		t.Errorf("first run is %v, want red %v", got, want)
	}
	if got, want := quads[0].rect.Dx(), 4*testCellW; got != want {
		t.Errorf("the red run is %d px wide, want %d — four cells coalesced", got, want)
	}
	if got, want := quads[1].rect.Dx(), 2*testCellW; got != want {
		t.Errorf("the blue run is %d px wide, want %d", got, want)
	}
}

// TestUnderlineBecomesAQuad: an underline is drawn, not shaped — the font has no glyph
// for it.
func TestUnderlineBecomesAQuad(t *testing.T) {
	p := vtPane(20, 2, "\x1b[4mup\x1b[24mplain")
	p.rect = image.Rect(0, 0, 400, 100)

	quads := paintRects(nil, p, testCellW, testCellH)
	if len(quads) != 2 {
		t.Fatalf("got %d quads, want one under each of the two underlined cells: %v", len(quads), quads)
	}
	for i, q := range quads {
		if q.rect.Dy() != underlineHeight {
			t.Errorf("quad %d is %d px tall, want %d", i, q.rect.Dy(), underlineHeight)
		}
		if q.rect.Max.Y != padding+testCellH {
			t.Errorf("quad %d sits at y=%d, want it on the cell's baseline edge %d",
				i, q.rect.Max.Y, padding+testCellH)
		}
	}
}

// TestPaneParsesOntoTheScreen closes the loop: bytes in, text on the grid, no escape
// residue.
func TestPaneParsesOntoTheScreen(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 40, 3)
	p.feed([]byte("\x1b]0;title\x07\x1b[32mgreen\x1b[0m text\r\nsecond line"))

	if got, want := screenText(p.scr), []string{"green text", "second line", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if p.scr.curRow != 1 || p.scr.curCol != len("second line") {
		t.Errorf("cursor at row %d col %d, want row 1 col %d", p.scr.curRow, p.scr.curCol, len("second line"))
	}
}

// fishStartup is what fish actually sends the moment it starts: capability queries and
// nothing else. It draws no prompt until they are answered, which is why a terminal
// that only drops sequences looks to the user like a shell that never started.
const fishStartup = "\x1b[?u" + // kitty keyboard flags
	"\x1b[>0q" + // XTVERSION
	"\x1b]11;?\x1b\\" + // what is your background
	"\x1b[?1049h" + "\x1bP+q696e646e\x1b\\" + "\x1bP+q71756572792d6f732d6e616d65\x1b\\" + "\x1b[?1049l" +
	"\x1b[0c" // DA1, last: the barrier the rest hang on

func TestPaneAnswersFishStartup(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)

	got := string(p.feed([]byte(fishStartup)))
	if !strings.Contains(got, "\x1b[?62;22c") {
		t.Errorf("answered %q, want a DA1 report in it — fish blocks on this one", got)
	}
	if !strings.Contains(got, "\x1b]11;rgb:") {
		t.Errorf("answered %q, want the background colour it asked for", got)
	}
	if screen := strings.Join(trimTrailing(screenText(p.scr)), ""); screen != "" {
		t.Errorf("the query batch put %q on screen, want nothing", screen)
	}
}

func TestPaneAnswersDeviceAttributes(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"DA1 with no parameter", "\x1b[c", "\x1b[?62;22c"},
		{"DA1 with an explicit zero", "\x1b[0c", "\x1b[?62;22c"},
		{"DA2", "\x1b[>c", "\x1b[>0;0;0c"},
		{"DA3 is not answered", "\x1b[=c", ""},
		{"an unrelated final is not answered", "\x1b[0n", ""},
		{"kitty keyboard is left unanswered on purpose", "\x1b[?u", ""},
		{"XTVERSION is left unanswered on purpose", "\x1b[>0q", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
			if got := string(p.feed([]byte(tc.in))); got != tc.want {
				t.Errorf("%q answered %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPaneAnswersColorQueries(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)

	// Derived from the theme rather than spelled out, so recolouring gty does not
	// come back as a failure here.
	if got, want := string(p.feed([]byte("\x1b]11;?\x1b\\"))), oscColor(11, backgroundRGBA); got != want {
		t.Errorf("background query answered %q, want %q", got, want)
	}
	if got := string(p.feed([]byte("\x1b]10;?\x1b\\"))); !strings.HasPrefix(got, "\x1b]10;rgb:") {
		t.Errorf("foreground query answered %q, want an OSC 10 report", got)
	}
	// Setting a colour is not a query and must not be answered.
	if got := string(p.feed([]byte("\x1b]11;#000000\x1b\\"))); got != "" {
		t.Errorf("a colour assignment was answered with %q, want nothing", got)
	}
}

func trimTrailing(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
