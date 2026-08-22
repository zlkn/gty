package main

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"gty/internal/font"
)

func TestScrollbackKeepsOrder(t *testing.T) {
	s := newScrollback()
	for _, l := range numbered(0, 3) {
		s.Append(cellsOf(l))
	}

	if s.Len() != 3 {
		t.Fatalf("Len is %d, want 3", s.Len())
	}
	for i, want := range []string{"0", "1", "2"} {
		if got := rowText(s.Row(i).cells); got != want {
			t.Errorf("line %d is %q, want %q", i, got, want)
		}
	}
}

// TestScrollbackEvictsOldest: at capacity the ring drops the front instead of growing.
func TestScrollbackEvictsOldest(t *testing.T) {
	s := newScrollback()
	for _, l := range numbered(0, maxScrollback+5) {
		s.Append(cellsOf(l))
	}

	if s.Len() != maxScrollback {
		t.Fatalf("Len is %d, want %d", s.Len(), maxScrollback)
	}
	if got, want := rowText(s.Row(0).cells), "5"; got != want {
		t.Errorf("oldest line is %q, want %q", got, want)
	}
	if got, want := rowText(s.Row(s.Len()-1).cells), fmt.Sprint(maxScrollback+4); got != want {
		t.Errorf("newest line is %q, want %q", got, want)
	}
}

// TestScrollbackGrowsLazily: the ring reaches its bound, it is not allocated at it. A
// line of cells is far heavier than the string it replaced, and a fresh pane holds
// none of them.
func TestScrollbackGrowsLazily(t *testing.T) {
	s := newScrollback()
	if len(s.rows) != 0 {
		t.Errorf("a fresh scrollback holds %d rows, want none", len(s.rows))
	}
	for _, l := range numbered(0, 5) {
		s.Append(cellsOf(l))
	}
	if len(s.rows) != 5 {
		t.Errorf("after five lines the ring is %d rows, want 5", len(s.rows))
	}
}

// TestScrollbackAppendCopies: the screen recycles the row it retires, so the history
// must not be left aliasing it.
func TestScrollbackAppendCopies(t *testing.T) {
	s := newScrollback()
	cells := cellsOf("original")
	s.Append(cells)

	clear(cells)
	if got := rowText(s.Row(0).cells); got != "original" {
		t.Errorf("history line became %q after the caller reused its buffer, want %q", got, "original")
	}
}

// TestScrollbackCachesShaping: a line is shaped once per width, not once per frame.
func TestScrollbackCachesShaping(t *testing.T) {
	s := newScrollback()
	for _, l := range numbered(0, 3) {
		s.Append(cellsOf(l))
	}
	s.setCols(80)

	calls := 0
	shape := func(cells []cell, dst []font.GID) []font.GID {
		calls++
		return append(dst, font.GID(len(cells)))
	}
	shaped := func(i int) { s.Row(i).shaped(s.Gen(), shape) }

	shaped(0)
	shaped(0)
	shaped(1)
	if calls != 2 {
		t.Errorf("shaped %d times, want 2 (one per line)", calls)
	}

	s.setCols(80)
	shaped(0)
	if calls != 2 {
		t.Errorf("shaped %d times after a no-op setCols, want 2", calls)
	}

	s.setCols(40)
	shaped(0)
	shaped(1)
	if calls != 4 {
		t.Errorf("shaped %d times after a width change, want 4", calls)
	}

	s.Append(cellsOf("new"))
	shaped(s.Len() - 1)
	shaped(s.Len() - 1)
	if calls != 5 {
		t.Errorf("shaped %d times after an append, want 5", calls)
	}
}

// TestPaneVisibleWindow walks the view over a pane whose output has long since
// overflowed its screen. There is no newline after the last line, so the cursor is
// left at the end of it — where a shell leaves a prompt.
func TestPaneVisibleWindow(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)

	want := []string{"90", "91", "92", "93", "94", "95", "96", "97", "98", "99"}
	if got := viewText(p); !slices.Equal(got, want) {
		t.Errorf("tail shows %v, want %v", got, want)
	}

	p.scrollBy(5)
	if got, want := viewText(p)[0], "85"; got != want {
		t.Errorf("after scrolling 5 back the top line is %q, want %q", got, want)
	}

	p.scrollBy(1000)
	if p.scroll != p.maxScroll() {
		t.Errorf("scroll is %d, want it clamped to %d", p.scroll, p.maxScroll())
	}
	if got, want := viewText(p)[0], "0"; got != want {
		t.Errorf("at the top of history the first line is %q, want %q", got, want)
	}
	if p.scrollBy(1) {
		t.Error("scrolling past the top of history reported movement")
	}

	p.scrollBy(-1000)
	if p.scroll != 0 {
		t.Errorf("scroll is %d, want 0", p.scroll)
	}
	if p.scrollBy(-1) {
		t.Error("scrolling past the tail reported movement")
	}
}

// TestPaneVisibleShorterThanGrid: output that has not filled the screen leaves the
// history empty and nowhere to scroll.
func TestPaneVisibleShorterThanGrid(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 3)...)

	if p.buf.Len() != 0 {
		t.Errorf("history holds %d lines, want none: nothing has scrolled off yet", p.buf.Len())
	}
	from, to := p.visible()
	if from != 0 || to != 10 {
		t.Errorf("visible window is [%d,%d), want the whole screen [0,10)", from, to)
	}
	if p.maxScroll() != 0 {
		t.Errorf("maxScroll is %d, want 0", p.maxScroll())
	}
}

// TestPaneFeedKeepsView pins the sticky-tail rule: a pinned pane follows new output, a
// scrolled-back one stays on the text the user is reading.
func TestPaneFeedKeepsView(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)

	feedText(p, "", "fresh")
	if got, want := viewText(p)[9], "fresh"; got != want {
		t.Errorf("a pinned pane shows %q at the bottom, want %q", got, want)
	}

	p.scrollBy(20)
	before := viewText(p)
	feedText(p, "", "later")
	if got := viewText(p); !slices.Equal(got, before) {
		t.Errorf("a scrolled-back pane moved to %v, want it to stay on %v", got, before)
	}
}

// TestPaneFeedKeepsViewAtCapacity is the same rule once the ring evicts: the history
// stops growing, so the anchor has to survive the shift instead. This is why the view
// tracks lines pushed rather than the history's length.
func TestPaneFeedKeepsViewAtCapacity(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10)
	fillDemo(p)
	// fillDemo leaves the last screenful on the grid, so the ring stops a few lines
	// short of its bound; push it the rest of the way.
	for p.buf.Len() < maxScrollback {
		p.feed([]byte("overflow\r\n"))
	}
	p.scrollBy(20)

	before := viewText(p)
	feedText(p, "", "later")
	if got := viewText(p); !slices.Equal(got, before) {
		t.Errorf("view moved to %v, want it to stay on %v", got, before)
	}
}

// TestSetGridClampsScroll: growing a pane can leave the view further back than there is
// history for.
func TestSetGridClampsScroll(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 30)...)
	p.scrollBy(20)

	p.setGrid(80, 25)
	if p.scroll != p.maxScroll() {
		t.Errorf("scroll is %d, want it clamped to %d", p.scroll, p.maxScroll())
	}
	if got, want := viewText(p)[0], "0"; got != want {
		t.Errorf("top line is %q, want %q", got, want)
	}
}
