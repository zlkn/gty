package main

import (
	"image"
	"os"
	"slices"
	"strings"
	"testing"

	"gty/internal/font"
)

// A real fish session, captured byte for byte: startup queries, the prompt, `echo
// "a => b"` typed one key at a time with a redraw after each, and the result.
//
// fish is the shell this terminal was found to be broken on, and it is the demanding
// one: it interrogates the terminal before drawing anything, then repaints its whole
// command line on every keystroke with absolute cursor moves and erases. A shell that
// only ever appends — bash, sh — exercises almost none of that, which is exactly how
// the breakage went unnoticed.
//
// Re-record with the recorder in the git history if fish's behaviour changes; the
// point of a fixture is that this test does not need fish installed to run.
func fishSession(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/fish-session.bin")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFishSessionRendersCleanly(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
	replies := feedInChunks(p, fishSession(t), 64)

	rows := screenText(p.scr)
	// The two private-use runes are the Nerd Font separators in this prompt theme;
	// spelled out so the fixture cannot be broken by an editor normalising them.
	want := []string{
		"Welcome to fish, the friendly interactive shell!",
		"gty \U000f10fe preprod-us \uf113 main echo \"a => b\"",
		"a => b",
	}
	if got := rows[:3]; !slices.Equal(got, want) {
		t.Errorf("screen reads\n  %q\nwant\n  %q", got, want)
	}
	if !strings.HasPrefix(rows[3], "gty") {
		t.Errorf("row 3 is %q, want the next prompt", rows[3])
	}

	// Nothing below the prompt: a missing erase leaves the old line's tail behind, and
	// that is precisely what the terminal used to do.
	for i, l := range rows[4:] {
		if l != "" {
			t.Errorf("row %d holds %q, want nothing", i+4, l)
		}
	}

	// The cursor sits after the prompt, not wherever the last glyph landed.
	if p.scr.curRow != 3 {
		t.Errorf("cursor is on row %d, want 3 — the prompt fish just drew", p.scr.curRow)
	}
	if p.scr.curCol == 0 {
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
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
	feedInChunks(p, fishSession(t), 64)

	// This prompt theme paints no backgrounds and asks for no bold: it colours text and
	// leans on Nerd Font separators. Several distinct foregrounds is the thing to hold
	// it to. The attribute paths have their own tests, which do not depend on whatever
	// theme happened to be installed when this was recorded.
	seen := map[color]bool{}
	for r := range p.scr.height() {
		for _, c := range p.scr.row(r).cells {
			if c.Rune != 0 {
				seen[c.FG] = true
			}
		}
	}
	if len(seen) < 3 {
		t.Errorf("the whole session used %d foreground colours, want several", len(seen))
	}
}

// TestFishSessionKeepsLigatures: the shaping the renderer does is unaffected by all the
// redrawing — "=>" is still one arrow on the grid fish left behind.
func TestFishSessionKeepsLigatures(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
	feedInChunks(p, fishSession(t), 64)

	row := p.scr.row(2) // the output line, "a => b"
	if got := rowText(row.cells); got != "a => b" {
		t.Fatalf("row 2 is %q, want %q", got, "a => b")
	}
	gids := txt.shapeRow(row.cells, p.cols, nil)

	eq, _ := txt.fm.GlyphIndex(font.Regular, '=')
	gt, _ := txt.fm.GlyphIndex(font.Regular, '>')
	if slices.Contains(gids, eq) && slices.Contains(gids, gt) {
		t.Errorf("the row shaped to %v, which still holds the plain '=' and '>': no ligature", gids)
	}
}

// feedInChunks splits the stream the way a pty read would, so the parser has to carry
// its state across the seams.
func feedInChunks(p *pane, b []byte, n int) []byte {
	var replies []byte
	for len(b) > 0 {
		k := min(n, len(b))
		replies = append(replies, p.feed(b[:k])...)
		b = b[k:]
	}
	return replies
}
