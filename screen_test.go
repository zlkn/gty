package main

import (
	"slices"
	"testing"

	"gty/internal/font"
)

func testScreen(cols, rows int) (*screen, *scrollback) {
	out := newScrollback()
	return newScreen(cols, rows, out), out
}

func writeTo(s *screen, str string) {
	for _, r := range str {
		s.put(r)
	}
}

// crlf is what a shell actually sends between lines. LF alone only feeds the line.
func crlf(s *screen) {
	s.carriageReturn()
	s.lineFeed()
}

func TestScreenPutsAndWraps(t *testing.T) {
	s, out := testScreen(4, 3)
	writeTo(s, "abcdefg")

	if got, want := screenText(s), []string{"abcd", "efg", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if out.Len() != 0 {
		t.Errorf("%d lines reached the history; nothing has scrolled off a three-row screen", out.Len())
	}
	if s.curRow != 1 || s.curCol != 3 {
		t.Errorf("cursor at row %d col %d, want row 1 col 3", s.curRow, s.curCol)
	}
}

// TestScreenDefersTheWrap is the classic terminal off-by-one: a character in the last
// column parks the cursor rather than wrapping, so the newline that usually follows
// does not cost a blank line.
func TestScreenDefersTheWrap(t *testing.T) {
	s, _ := testScreen(4, 3)
	writeTo(s, "abcd")

	if !s.wrapNext {
		t.Error("a character in the last column did not park the cursor")
	}
	if s.curRow != 0 {
		t.Errorf("cursor moved to row %d; filling a row must not advance it on its own", s.curRow)
	}

	// The newline after a full-width line lands on the next row, not the one after.
	s.carriageReturn()
	s.lineFeed()
	writeTo(s, "xy")
	if got, want := screenText(s), []string{"abcd", "xy", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q — an eager wrap would leave a blank row between", got, want)
	}
}

func TestScreenSpendsThePendingWrap(t *testing.T) {
	s, _ := testScreen(4, 3)
	writeTo(s, "abcde")

	if got, want := screenText(s), []string{"abcd", "e", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if s.wrapNext {
		t.Error("the pending wrap survived the character that spent it")
	}
}

func TestScreenControls(t *testing.T) {
	s, _ := testScreen(20, 3)

	writeTo(s, "hello")
	s.carriageReturn()
	writeTo(s, "HE")
	if got, want := screenText(s)[0], "HEllo"; got != want {
		t.Errorf("after a carriage return the row reads %q, want %q", got, want)
	}

	s.backspace()
	writeTo(s, "x")
	if got, want := screenText(s)[0], "Hxllo"; got != want {
		t.Errorf("after a backspace the row reads %q, want %q", got, want)
	}

	// LF feeds the line and nothing else: the column is the carriage return's job,
	// which is why every shell sends both.
	col := s.curCol
	s.lineFeed()
	if s.curCol != col {
		t.Errorf("a line feed moved the cursor to column %d, want it left at %d", s.curCol, col)
	}
	if s.curRow != 1 {
		t.Errorf("a line feed left the cursor on row %d, want 1", s.curRow)
	}

	s.carriageReturn()
	s.tab()
	if s.curCol != tabWidth {
		t.Errorf("tab moved to column %d, want %d", s.curCol, tabWidth)
	}
	s.tab()
	if s.curCol != 2*tabWidth {
		t.Errorf("a second tab moved to column %d, want %d", s.curCol, 2*tabWidth)
	}
}

// TestScreenScrollsIntoHistory: the retired line arrives trimmed, which is what keeps
// a history row the size of its text rather than the size of the pane.
func TestScreenScrollsIntoHistory(t *testing.T) {
	s, out := testScreen(20, 2)

	writeTo(s, "first")
	crlf(s)
	writeTo(s, "second")
	crlf(s)
	writeTo(s, "third")

	if out.Len() != 1 {
		t.Fatalf("%d lines reached the history, want 1", out.Len())
	}
	if got, want := rowText(out.Row(0).cells), "first"; got != want {
		t.Errorf("history holds %q, want %q", got, want)
	}
	if got := len(out.Row(0).cells); got != len("first") {
		t.Errorf("the history row is %d cells wide, want %d: the padding was not trimmed", got, len("first"))
	}
	if got, want := screenText(s), []string{"second", "third"}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
}

func TestScreenResizeWidth(t *testing.T) {
	s, _ := testScreen(10, 2)
	writeTo(s, "abcdefghij")

	s.resize(4, 2)
	if got, want := screenText(s)[0], "abcd"; got != want {
		t.Errorf("narrowing left %q, want %q — there is no reflow", got, want)
	}
	if s.curCol > 3 {
		t.Errorf("cursor stayed at column %d, past the new right edge", s.curCol)
	}

	s.resize(8, 2)
	if got := len(s.lines[0].cells); got != 8 {
		t.Errorf("row is %d cells after widening, want 8", got)
	}
	if got, want := screenText(s)[0], "abcd"; got != want {
		t.Errorf("widening left %q, want %q", got, want)
	}
}

// TestScreenResizeShedsFromTheTop: dragging a window shorter keeps the cursor — and so
// the prompt and whatever the shell just printed — on screen.
func TestScreenResizeShedsFromTheTop(t *testing.T) {
	s, out := testScreen(10, 4)
	for i, l := range []string{"one", "two", "three", "four"} {
		if i > 0 {
			crlf(s)
		}
		writeTo(s, l)
	}
	if s.curRow != 3 {
		t.Fatalf("cursor is on row %d, want the last one", s.curRow)
	}

	s.resize(10, 2)
	if got, want := screenText(s), []string{"three", "four"}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if s.curRow != 1 {
		t.Errorf("cursor is on row %d, want 1 — it must stay with its line", s.curRow)
	}
	if got, want := out.Len(), 2; got != want {
		t.Errorf("%d lines went to the history, want %d", got, want)
	}
	if got, want := rowText(out.Row(0).cells), "one"; got != want {
		t.Errorf("history starts at %q, want %q", got, want)
	}
}

func TestScreenResizeGrows(t *testing.T) {
	s, _ := testScreen(10, 1)
	writeTo(s, "only")

	s.resize(10, 3)
	if got, want := screenText(s), []string{"only", "", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if s.curRow != 0 {
		t.Errorf("growing moved the cursor to row %d, want 0", s.curRow)
	}
}

// TestScreenZeroGrid: a pane too small for one cell must absorb writes rather than
// index into nothing.
func TestScreenZeroGrid(t *testing.T) {
	s, _ := testScreen(0, 0)
	writeTo(s, "anything")
	s.lineFeed()
	s.tab()
	s.backspace()

	if s.height() != 0 || s.curRow != 0 || s.curCol != 0 {
		t.Errorf("zero grid ended at %dx%d cursor (%d,%d)", s.cols, s.height(), s.curRow, s.curCol)
	}
}

// TestScreenInvalidatesShapingOnWrite: a live row changes constantly, so its cached
// glyphs have to be dropped the moment it is written to — otherwise the screen would
// have to be reshaped whole every frame.
func TestScreenInvalidatesShapingOnWrite(t *testing.T) {
	s, _ := testScreen(10, 1)
	writeTo(s, "ab")

	calls := 0
	shape := func(cells []cell, dst []font.GID) []font.GID {
		calls++
		return append(dst, font.GID(len(cells)))
	}
	s.row(0).shaped(s.gen, shape)
	s.row(0).shaped(s.gen, shape)
	if calls != 1 {
		t.Errorf("an unchanged row shaped %d times, want 1", calls)
	}

	writeTo(s, "c")
	s.row(0).shaped(s.gen, shape)
	if calls != 2 {
		t.Errorf("shaped %d times after a write, want 2", calls)
	}

	s.resize(20, 1)
	s.row(0).shaped(s.gen, shape)
	if calls != 3 {
		t.Errorf("shaped %d times after a width change, want 3", calls)
	}
}

func TestTrimBlanks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"abc", "abc"},
		{"abc   ", "abc"},
		{"   ", ""},
		{"", ""},
		{"a b ", "a b"},
	} {
		if got := rowText(cellsOf(tc.in)); got != tc.want {
			t.Errorf("trimBlanks(%q) reads %q, want %q", tc.in, got, tc.want)
		}
	}

	// An unwritten cell is blank too: that is what lets clear() blank a row.
	row := make([]cell, 5)
	copy(row, cellsOf("hi"))
	if got := len(trimBlanks(row)); got != 2 {
		t.Errorf("a row of two characters padded with zero cells trimmed to %d, want 2", got)
	}
}

func TestScreenWideRunes(t *testing.T) {
	s, _ := testScreen(10, 1)
	writeTo(s, "héllo")
	if got, want := screenText(s)[0], "héllo"; got != want {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if s.curCol != len([]rune("héllo")) {
		t.Errorf("cursor at column %d, want %d: a rune is one cell", s.curCol, len([]rune("héllo")))
	}
}
