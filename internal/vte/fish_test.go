package vte

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// A real fish session, captured byte for byte: startup queries, the prompt, `echo "a => b"`
// typed one key at a time with a redraw after each, and the result.
//
// fish is the shell this terminal was found to be broken on, and it is the demanding one: it
// interrogates the terminal before drawing anything, then repaints its whole command line on
// every keystroke with absolute cursor moves and erases. A shell that only ever appends —
// bash, sh — exercises almost none of that, which is exactly how the breakage went
// unnoticed.
//
// Re-record with the recorder in the git history if fish's behaviour changes; the point of a
// fixture is that this test does not need fish installed to run.
func fishSession(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/fish-session.bin")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fishTerm is a terminal that has been through the whole recorded session.
func fishTerm(t *testing.T) (*Terminal, []byte) {
	t.Helper()
	tm := New(80, 24)
	tm.reportColor = func(int) (r, g, b uint16, ok bool) { return 0, 0, 0, true }
	return tm, feedInChunks(tm, fishSession(t), 64)
}

func TestFishSessionRendersCleanly(t *testing.T) {
	tm, replies := fishTerm(t)

	rows := screenText(tm.scr)
	// The two private-use runes are the Nerd Font separators in this prompt theme; spelled
	// out so the fixture cannot be broken by an editor normalising them.
	want := []string{
		"Welcome to fish, the friendly interactive shell!",
		"gty \U000f10fe preprod-us  main echo \"a => b\"",
		"a => b",
	}
	if got := rows[:3]; !slices.Equal(got, want) {
		t.Errorf("screen reads\n  %q\nwant\n  %q", got, want)
	}
	if !strings.HasPrefix(rows[3], "gty") {
		t.Errorf("row 3 is %q, want the next prompt", rows[3])
	}

	// Nothing below the prompt: a missing erase leaves the old line's tail behind, and that
	// is precisely what the terminal used to do.
	for i, l := range rows[4:] {
		if l != "" {
			t.Errorf("row %d holds %q, want nothing", i+4, l)
		}
	}

	// The cursor sits after the prompt, not wherever the last glyph landed.
	if tm.scr.curRow != 3 {
		t.Errorf("cursor is on row %d, want 3 — the prompt fish just drew", tm.scr.curRow)
	}
	if tm.scr.curCol == 0 {
		t.Error("cursor is at column 0; fish placed it past the prompt")
	}

	// The queries it opens with have to be answered or it never draws at all.
	if !strings.Contains(string(replies), "\x1b[?62;22c") {
		t.Error("no DA1 report went back; fish blocks on that one")
	}
	if !strings.Contains(string(replies), "R") {
		t.Error("no cursor position report went back")
	}
}

// TestFishSessionIsColoured: the prompt is not one flat colour, and neither is the
// syntax-highlighted command line.
func TestFishSessionIsColoured(t *testing.T) {
	tm, _ := fishTerm(t)

	// This prompt theme paints no backgrounds and asks for no bold: it colours text and
	// leans on Nerd Font separators. Several distinct foregrounds is the thing to hold it
	// to. The attribute paths have their own tests, which do not depend on whatever theme
	// happened to be installed when this was recorded.
	seen := map[Color]bool{}
	for r := range tm.scr.height() {
		for _, c := range tm.scr.row(r).Cells {
			if c.Rune != 0 {
				seen[c.FG] = true
			}
		}
	}
	if len(seen) < 3 {
		t.Errorf("the whole session used %d foreground colours, want several", len(seen))
	}
}

// feedInChunks splits the stream the way a pty read would, so the parser has to carry its
// state across the seams, and collects everything the terminal answered on the way.
func feedInChunks(tm *Terminal, b []byte, n int) []byte {
	var replies []byte
	for len(b) > 0 {
		k := min(n, len(b))
		tm.Feed(b[:k])
		replies = append(replies, tm.answers...)
		b = b[k:]
	}
	return replies
}
