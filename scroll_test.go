package main

import (
	"image"
	"slices"
	"testing"

	"gty/internal/vte"
)

// TestPaneVisibleWindow walks the view over a pane whose output has long since overflowed
// its screen. There is no newline after the last line, so the cursor is left at the end of
// it — where a shell leaves a prompt.
func TestPaneVisibleWindow(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)

	want := []string{"90", "91", "92", "93", "94", "95", "96", "97", "98", "99"}
	if got := viewText(p); !slices.Equal(got, want) {
		t.Errorf("tail shows %v, want %v", got, want)
	}

	p.scrollBy(5)
	p.snap()
	if got, want := viewText(p)[0], "85"; got != want {
		t.Errorf("after scrolling 5 back the top line is %q, want %q", got, want)
	}

	p.scrollBy(1000)
	p.snap()
	if p.scroll != p.term.MaxScroll() {
		t.Errorf("scroll is %d, want it clamped to %d", p.scroll, p.term.MaxScroll())
	}
	if got, want := viewText(p)[0], "0"; got != want {
		t.Errorf("at the top of history the first line is %q, want %q", got, want)
	}
	if p.scrollBy(1) {
		t.Error("scrolling past the top of history reported movement")
	}

	p.scrollBy(-1000)
	p.snap()
	if p.scroll != 0 {
		t.Errorf("scroll is %d, want 0", p.scroll)
	}
	if p.scrollBy(-1) {
		t.Error("scrolling past the tail reported movement")
	}
}

// TestPaneVisibleShorterThanGrid: output that has not filled the screen leaves the history
// empty and nowhere to scroll.
func TestPaneVisibleShorterThanGrid(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 3)...)

	if p.frame.HistLen != 0 {
		t.Errorf("history holds %d lines, want none: nothing has scrolled off yet", p.frame.HistLen)
	}
	if got := len(p.frame.Lines); got != 10 {
		t.Errorf("the window is %d lines, want the whole screen, 10", got)
	}
	if p.term.MaxScroll() != 0 {
		t.Errorf("MaxScroll is %d, want 0", p.term.MaxScroll())
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
	p.snap()
	before := viewText(p)
	feedText(p, "", "later")
	if got := viewText(p); !slices.Equal(got, before) {
		t.Errorf("a scrolled-back pane moved to %v, want it to stay on %v", got, before)
	}
}

// TestPaneFeedKeepsViewAtCapacity is the same rule once the ring evicts: the history stops
// growing, so the anchor has to survive the shift instead. This is why the view tracks lines
// retired rather than the history's length.
func TestPaneFeedKeepsViewAtCapacity(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10)
	fillDemo(p)
	// fillDemo leaves the last screenful on the grid, so the ring stops a few lines short of
	// its bound; push it the rest of the way.
	for p.term.MaxScroll() < vte.MaxScrollback {
		p.term.Feed([]byte("overflow\r\n"))
	}
	p.snap()
	p.scrollBy(20)
	p.snap()

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
	p.snap()

	p.setGrid(80, 25)
	p.snap()
	if p.scroll != p.term.MaxScroll() {
		t.Errorf("scroll is %d, want it clamped to %d", p.scroll, p.term.MaxScroll())
	}
	if got, want := viewText(p)[0], "0"; got != want {
		t.Errorf("top line is %q, want %q", got, want)
	}
}

// TestSetGridKeepsTheScrollRegion: a layout pass runs on every damaged frame, and refitting
// a grid takes a program's DECSTBM region with it — a program that carved one out cannot
// know the grid moved under it. So a refit to the size it already had must do nothing at
// all. vim's status line depends on it.
func TestSetGridKeepsTheScrollRegion(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 10, 4, "a", "b", "c", "STATUS")
	p.term.Feed([]byte("\x1b[1;3r")) // the region is rows 1..3; the status line is outside

	p.setGrid(10, 4) // the layout pass a damaged frame brings

	p.term.Feed([]byte("\x1b[3;1H\r\nd")) // from the region's last row, feed past it
	p.snap()

	if got, want := viewText(p), []string{"b", "c", "d", "STATUS"}; !slices.Equal(got, want) {
		t.Errorf("after a layout pass the screen reads %v, want %v — the region was lost", got, want)
	}
}
