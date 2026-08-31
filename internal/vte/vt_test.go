package vte

import (
	"slices"
	"strings"
	"testing"
)

// vtTerm is a terminal driven the way a program drives it: bytes in, grid out.
func vtTerm(cols, rows int, input ...string) *Terminal {
	tm := New(cols, rows)
	for _, s := range input {
		tm.Feed([]byte(s))
	}
	return tm
}

func TestSGRColors(t *testing.T) {
	tm := vtTerm(40, 2, "\x1b[31mR\x1b[92mB\x1b[38;5;123mP\x1b[38;2;10;20;30mT\x1b[39mD")
	cells := tm.scr.row(0).Cells

	for i, want := range []Color{
		PaletteColor(1),      // 31: red
		PaletteColor(8 + 2),  // 92: bright green
		PaletteColor(123),    // 38;5;n
		RGBColor(10, 20, 30), // 38;2;r;g;b
		ColorDefault,         // 39
	} {
		if got := cells[i].FG; got != want {
			t.Errorf("Cell %d foreground is %#x, want %#x", i, got, want)
		}
	}
}

// TestSGRTrueColorForms: both spellings of a direct colour have to land on the same
// value, and only the separator distinguishes them.
func TestSGRTrueColorForms(t *testing.T) {
	tm := vtTerm(40, 2, "\x1b[38;2;1;2;3mA\x1b[38:2::1:2:3mB\x1b[38:2:1:2:3mC")
	cells := tm.scr.row(0).Cells

	want := RGBColor(1, 2, 3)
	for i, name := range []string{"semicolons", "colons with a colour space", "colons without"} {
		if got := cells[i].FG; got != want {
			t.Errorf("%s gave %#x, want %#x", name, got, want)
		}
	}
}

func TestSGRAttributes(t *testing.T) {
	tm := vtTerm(40, 2, "\x1b[1mb\x1b[3mi\x1b[22mI\x1b[0mp\x1b[7mv\x1b[4mu")
	cells := tm.scr.row(0).Cells

	for i, want := range []Attr{
		AttrBold,
		AttrBold | AttrItalic,
		AttrItalic, // 22 drops bold and leaves italic
		0,
		AttrInverse,
		AttrInverse | AttrUnderline,
	} {
		if got := cells[i].Attrs; got != want {
			t.Errorf("cell %d is attrs %d, want %d", i, got, want)
		}
	}
}

// TestSGRNormalIntensity: 22 is "neither bold nor faint", and they are one field now, so
// dropping one must not leave the other standing.
func TestSGRNormalIntensity(t *testing.T) {
	tm := vtTerm(40, 2, "\x1b[1;2;3mx\x1b[22my")
	cells := tm.scr.row(0).Cells

	if want := AttrBold | AttrFaint | AttrItalic; cells[0].Attrs != want {
		t.Errorf("bold and faint together gave attrs %d, want %d", cells[0].Attrs, want)
	}
	if want := AttrItalic; cells[1].Attrs != want {
		t.Errorf("after 22 the pen is attrs %d, want %d — italic is not an intensity", cells[1].Attrs, want)
	}
}

// TestSGRInverseIsRecordedNotApplied: the cell keeps the colours the pen had and a flag
// saying to swap them. Performing the swap needs a palette, which is a view's to hold.
func TestSGRInverseIsRecordedNotApplied(t *testing.T) {
	tm := vtTerm(40, 2, "\x1b[31;44;7mX")
	c := tm.scr.row(0).Cells[0]

	if want := PaletteColor(1); c.FG != want {
		t.Errorf("foreground is %#x, want the red the pen was given, %#x", c.FG, want)
	}
	if want := PaletteColor(4); c.BG != want {
		t.Errorf("background is %#x, want the blue the pen was given, %#x", c.BG, want)
	}
	if c.Attrs&AttrInverse == 0 {
		t.Error("the inverse flag was not recorded")
	}
	if !c.Painted() {
		t.Error("an inverse cell reports nothing to paint; its block would not be drawn")
	}
}

// TestErasePaintsTheBackground: a program colours a region by setting a background and
// wiping — an erase that dropped the pen would leave the region blank instead.
func TestErasePaintsTheBackground(t *testing.T) {
	tm := vtTerm(10, 3, "\x1b[44m\x1b[2J")

	for r := range 3 {
		for _, c := range tm.scr.row(r).Cells {
			if c.BG != PaletteColor(4) {
				t.Fatalf("row %d holds %#x after a blue erase, want %#x", r, c.BG, PaletteColor(4))
			}
		}
	}
	if TrimBlanks(tm.scr.row(0).Cells) == nil {
		t.Error("the painted row trimmed away; the paint would never be drawn")
	}
}

func TestCursorAddressing(t *testing.T) {
	tm := vtTerm(20, 5,
		"\x1b[3;5Hmid", // CUP row 3 col 5
		"\x1b[HTL",     // home
		"\x1b[2;2H\x1b[3CX",
	)
	want := []string{"TL", "    X", "    mid", "", ""}
	if got := screenText(tm.scr); !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
}

func TestEraseInLineAndDisplay(t *testing.T) {
	tm := vtTerm(10, 3, "abcdefghij\r\n0123456789\r\nXXXXXXXXXX")

	tm.Feed([]byte("\x1b[1;4H\x1b[K")) // row 1, col 4, erase to the end
	if got, want := screenText(tm.scr)[0], "abc"; got != want {
		t.Errorf("after EL 0 row 0 is %q, want %q", got, want)
	}

	tm.Feed([]byte("\x1b[2;4H\x1b[1K")) // erase from the start of row 2
	if got, want := screenText(tm.scr)[1], "    456789"; got != want {
		t.Errorf("after EL 1 row 1 is %q, want %q", got, want)
	}

	tm.Feed([]byte("\x1b[2;1H\x1b[J")) // erase from row 2 to the end of the screen
	if got, want := screenText(tm.scr), []string{"abc", "", ""}; !slices.Equal(got, want) {
		t.Errorf("after ED 0 the screen is %q, want %q", got, want)
	}
}

func TestInsertAndDeleteLines(t *testing.T) {
	tm := vtTerm(10, 4, "one\r\ntwo\r\nthree")

	tm.Feed([]byte("\x1b[2;1H\x1b[L")) // open a line above "two"
	if got, want := screenText(tm.scr), []string{"one", "", "two", "three"}; !slices.Equal(got, want) {
		t.Errorf("after IL the screen is %q, want %q", got, want)
	}

	tm.Feed([]byte("\x1b[2;1H\x1b[M")) // and take it back out
	if got, want := screenText(tm.scr), []string{"one", "two", "three", ""}; !slices.Equal(got, want) {
		t.Errorf("after DL the screen is %q, want %q", got, want)
	}

	// Neither may reach the history: this is a program rearranging its own screen.
	if tm.hist.len() != 0 {
		t.Errorf("%d lines leaked into the history", tm.hist.len())
	}
}

func TestInsertAndDeleteChars(t *testing.T) {
	tm := vtTerm(10, 2, "abcdef")

	tm.Feed([]byte("\x1b[1;3H\x1b[2@")) // two blanks before 'c'
	if got, want := screenText(tm.scr)[0], "ab  cdef"; got != want {
		t.Errorf("after ICH the row is %q, want %q", got, want)
	}

	tm.Feed([]byte("\x1b[1;3H\x1b[2P"))
	if got, want := screenText(tm.scr)[0], "abcdef"; got != want {
		t.Errorf("after DCH the row is %q, want %q", got, want)
	}
}

// TestScrollRegion is what keeps a status line still while the text above it moves.
func TestScrollRegion(t *testing.T) {
	tm := vtTerm(10, 4, "a\r\nb\r\nc\r\nSTATUS")

	tm.Feed([]byte("\x1b[1;3r"))      // region is rows 1..3, the status line is outside
	tm.Feed([]byte("\x1b[3;1H\r\nd")) // from the region's last row, feed past it

	if got, want := screenText(tm.scr), []string{"b", "c", "d", "STATUS"}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q — the status line must not move", got, want)
	}
	// A scroll inside a region is a repaint, not history.
	if tm.hist.len() != 0 {
		t.Errorf("%d lines went to the history from inside a scroll region", tm.hist.len())
	}
}

func TestSaveAndRestoreCursor(t *testing.T) {
	tm := vtTerm(20, 4, "\x1b[2;5H\x1b[31m\x1b7\x1b[4;1Hlow\x1b8X")

	if got, want := screenText(tm.scr)[1], "    X"; got != want {
		t.Errorf("row 1 is %q, want %q — the cursor did not come back", got, want)
	}
	if got := tm.scr.row(1).Cells[4].FG; got != PaletteColor(1) {
		t.Errorf("the restored pen is %#x, want the red it was saved with", got)
	}
}

// TestAlternateScreen is what vim runs in: a second grid that keeps no history and
// hands the prompt back untouched on the way out.
func TestAlternateScreen(t *testing.T) {
	tm := vtTerm(20, 3, "prompt$ vim\r\n")
	before := screenText(tm.scr)

	tm.Feed([]byte("\x1b[?1049h")) // enter
	if tm.scr != tm.alt {
		t.Fatal("1049h did not switch to the alternate screen")
	}
	tm.Feed([]byte("~\r\n~\r\nEDITOR"))
	if got, want := screenText(tm.scr), []string{"~", "~", "EDITOR"}; !slices.Equal(got, want) {
		t.Errorf("the alternate screen reads %q, want %q", got, want)
	}
	if tm.histLen() != 0 {
		t.Errorf("the alternate screen shows %d history rows, want none", tm.histLen())
	}

	tm.Feed([]byte("\x1b[?1049l")) // leave
	if tm.scr != tm.pri {
		t.Fatal("1049l did not switch back")
	}
	if got := screenText(tm.scr); !slices.Equal(got, before) {
		t.Errorf("the primary screen came back as %q, want %q", got, before)
	}
}

func TestCursorVisibilityMode(t *testing.T) {
	tm := vtTerm(20, 3)
	if !tm.visible {
		t.Fatal("a fresh terminal starts with the cursor hidden")
	}
	tm.Feed([]byte("\x1b[?25l"))
	if tm.visible {
		t.Error("DECTCEM reset left the cursor visible")
	}
	tm.Feed([]byte("\x1b[?25h"))
	if !tm.visible {
		t.Error("DECTCEM set left the cursor hidden")
	}
}

// TestCursorStyle: neovim marks its modes with DECSCUSR alone, so a terminal that drops
// the sequence shows a block cursor in insert mode.
func TestCursorStyle(t *testing.T) {
	tm := vtTerm(20, 3)
	if tm.shape != tm.shapeDefault {
		t.Fatalf("a fresh terminal is shape %d, want the default %d", tm.shape, tm.shapeDefault)
	}

	for _, tc := range []struct {
		in   string
		want CursorShape
	}{
		{"\x1b[6 q", CursorBar}, // steady bar: neovim's insert mode
		{"\x1b[5 q", CursorBar}, // blinking bar, same shape
		{"\x1b[3 q", CursorUnderline},
		{"\x1b[2 q", CursorBlock}, // and back to normal mode
		{"\x1b[6 q", CursorBar},
		{"\x1b[0 q", tm.shapeDefault},
		{"\x1b[6 q", CursorBar},
		{"\x1b[9 q", CursorBar},    // out of range: the shape stands
		{"\x1b[>0q", CursorBar},    // a private q is not a cursor style
		{"\x1bc", tm.shapeDefault}, // RIS
	} {
		tm.Feed([]byte(tc.in))
		if tm.shape != tc.want {
			t.Errorf("%q left shape %d, want %d", tc.in, tm.shape, tc.want)
		}
	}
}

func TestAutowrapMode(t *testing.T) {
	tm := vtTerm(4, 2, "\x1b[?7l", "abcdef")
	if got, want := screenText(tm.scr), []string{"abcf", ""}; !slices.Equal(got, want) {
		t.Errorf("with autowrap off the screen reads %q, want %q — the last Cell is overwritten", got, want)
	}
}

// TestCursorPositionReport: fish asks where the cursor is on every redraw, and a
// terminal that does not answer leaves it guessing.
func TestCursorPositionReport(t *testing.T) {
	tm := vtTerm(20, 5, "\x1b[3;7H")
	if got, want := answer(tm, "\x1b[6n"), "\x1b[3;7R"; got != want {
		t.Errorf("CPR answered %q, want %q", got, want)
	}
	if got, want := answer(tm, "\x1b[5n"), "\x1b[0n"; got != want {
		t.Errorf("DSR answered %q, want %q", got, want)
	}
}

func TestReverseIndexAndNextLine(t *testing.T) {
	tm := vtTerm(10, 3, "one\r\ntwo")

	tm.Feed([]byte("\x1bM")) // RI, up a row
	if tm.scr.curRow != 0 {
		t.Errorf("RI left the cursor on row %d, want 0", tm.scr.curRow)
	}
	col := tm.scr.curCol
	tm.Feed([]byte("\x1bM")) // RI at the top scrolls the region down instead
	if tm.scr.curCol != col {
		t.Errorf("RI moved the cursor to column %d; like LF it only feeds a line", tm.scr.curCol)
	}

	tm.Feed([]byte("\rtop"))
	if got, want := screenText(tm.scr), []string{"top", "one", "two"}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
}

// TestPaneParsesOntoTheScreen closes the loop: bytes in, text on the grid, no escape
// residue.
func TestTerminalParsesOntoTheScreen(t *testing.T) {
	tm := New(40, 3)
	tm.Feed([]byte("\x1b]0;title\x07\x1b[32mgreen\x1b[0m text\r\nsecond line"))

	if got, want := screenText(tm.scr), []string{"green text", "second line", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if tm.scr.curRow != 1 || tm.scr.curCol != len("second line") {
		t.Errorf("cursor at row %d col %d, want row 1 col %d", tm.scr.curRow, tm.scr.curCol, len("second line"))
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

func TestTerminalAnswersFishStartup(t *testing.T) {
	tm := New(80, 24)
	tm.reportColor = func(int) (r, g, b uint16, ok bool) { return 0, 0, 0, true }

	got := answer(tm, fishStartup)
	if !strings.Contains(got, "\x1b[?62;22c") {
		t.Errorf("answered %q, want a DA1 report in it — fish blocks on this one", got)
	}
	if !strings.Contains(got, "\x1b]11;rgb:") {
		t.Errorf("answered %q, want the background colour it asked for", got)
	}
	if screen := strings.Join(trimTrailing(screenText(tm.scr)), ""); screen != "" {
		t.Errorf("the query batch put %q on screen, want nothing", screen)
	}
}

func TestTerminalAnswersDeviceAttributes(t *testing.T) {
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
			tm := New(80, 24)
			if got := answer(tm, tc.in); got != tc.want {
				t.Errorf("%q answered %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTerminalAnswersColorQueries: the terminal knows the protocol, the host knows the
// colours. An app asks so it can pick a light theme or a dark one.
func TestTerminalAnswersColorQueries(t *testing.T) {
	tm := New(80, 24)
	tm.reportColor = func(code int) (r, g, b uint16, ok bool) {
		switch code {
		case 10:
			return 0x1111, 0x2222, 0x3333, true
		case 11:
			return 0xffff, 0xeeee, 0xdddd, true
		}
		return 0, 0, 0, false
	}

	if got, want := answer(tm, "\x1b]11;?\x1b\\"), "\x1b]11;rgb:ffff/eeee/dddd\x1b\\"; got != want {
		t.Errorf("background query answered %q, want %q", got, want)
	}
	if got, want := answer(tm, "\x1b]10;?\x1b\\"), "\x1b]10;rgb:1111/2222/3333\x1b\\"; got != want {
		t.Errorf("foreground query answered %q, want %q", got, want)
	}
	// Setting a colour is not a query and must not be answered.
	if got := answer(tm, "\x1b]11;#000000\x1b\\"); got != "" {
		t.Errorf("a colour assignment was answered with %q, want nothing", got)
	}
}

// TestTerminalWithoutColorsAnswersNothing: a host that does not report its colours leaves
// the query unanswered, which is what a terminal that does not know them should do.
func TestTerminalWithoutColorsAnswersNothing(t *testing.T) {
	tm := New(80, 24)
	if got := answer(tm, "\x1b]11;?\x1b\\"); got != "" {
		t.Errorf("answered %q with no ReportColor set, want nothing", got)
	}
}

// TestCursorStyleReturnsToTheConfiguredDefault: DECSCUSR 0 and RIS go back to whatever the
// host asked for, which is only the same as a block when nobody configured otherwise.
func TestCursorStyleReturnsToTheConfiguredDefault(t *testing.T) {
	for _, in := range []string{"\x1b[2 q\x1b[0 q", "\x1b[2 q\x1bc"} {
		tm := New(20, 3)
		tm.shape, tm.shapeDefault = CursorBar, CursorBar

		tm.Feed([]byte(in))
		if tm.shape != CursorBar {
			t.Errorf("%q left shape %d, want the configured %d", in, tm.shape, CursorBar)
		}
	}
}
