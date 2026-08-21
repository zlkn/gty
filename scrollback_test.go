package main

import (
	"fmt"
	"slices"
	"testing"

	"gty/internal/font"
)

func numbered(from, to int) []line {
	out := make([]line, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, line{Text: fmt.Sprintf("%d", i)})
	}
	return out
}

// texts is what a pane currently shows, oldest first.
func texts(p *pane) []string {
	from, to := p.visible()
	out := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, p.buf.At(i).Text)
	}
	return out
}

func TestScrollbackKeepsOrder(t *testing.T) {
	s := newScrollback()
	for _, l := range numbered(0, 3) {
		s.Append(l)
	}

	if s.Len() != 3 {
		t.Fatalf("Len is %d, want 3", s.Len())
	}
	for i, want := range []string{"0", "1", "2"} {
		if got := s.At(i).Text; got != want {
			t.Errorf("line %d is %q, want %q", i, got, want)
		}
	}
}

// TestScrollbackEvictsOldest: at capacity the ring drops the front instead of growing.
func TestScrollbackEvictsOldest(t *testing.T) {
	s := newScrollback()
	for _, l := range numbered(0, maxScrollback+5) {
		s.Append(l)
	}

	if s.Len() != maxScrollback {
		t.Fatalf("Len is %d, want %d", s.Len(), maxScrollback)
	}
	if got, want := s.At(0).Text, "5"; got != want {
		t.Errorf("oldest line is %q, want %q", got, want)
	}
	if got, want := s.At(s.Len()-1).Text, fmt.Sprint(maxScrollback+4); got != want {
		t.Errorf("newest line is %q, want %q", got, want)
	}
}

// TestScrollbackCachesShaping: a line is shaped once per width, not once per frame.
func TestScrollbackCachesShaping(t *testing.T) {
	s := newScrollback()
	for _, l := range numbered(0, 3) {
		s.Append(l)
	}
	s.setCols(80)

	calls := 0
	shape := func(l *line, dst []font.GID) []font.GID {
		calls++
		return append(dst, font.GID(len(l.Text)))
	}

	s.shaped(0, shape)
	s.shaped(0, shape)
	s.shaped(1, shape)
	if calls != 2 {
		t.Errorf("shaped %d times, want 2 (one per line)", calls)
	}

	s.setCols(80)
	s.shaped(0, shape)
	if calls != 2 {
		t.Errorf("shaped %d times after a no-op setCols, want 2", calls)
	}

	s.setCols(40)
	s.shaped(0, shape)
	s.shaped(1, shape)
	if calls != 4 {
		t.Errorf("shaped %d times after a width change, want 4", calls)
	}

	s.Append(line{Text: "new"})
	s.shaped(s.Len()-1, shape)
	s.shaped(s.Len()-1, shape)
	if calls != 5 {
		t.Errorf("shaped %d times after an append, want 5", calls)
	}
}

func TestPaneVisibleWindow(t *testing.T) {
	p := newPane(1)
	for _, l := range numbered(0, 100) {
		p.Write(l)
	}
	p.setGrid(80, 10)

	if got, want := texts(p), []string{"90", "91", "92", "93", "94", "95", "96", "97", "98", "99"}; !slices.Equal(got, want) {
		t.Errorf("tail shows %v, want %v", got, want)
	}

	p.scrollBy(5)
	if got, want := texts(p)[0], "85"; got != want {
		t.Errorf("after scrolling 5 back the top line is %q, want %q", got, want)
	}

	p.scrollBy(1000)
	if p.scroll != p.maxScroll() {
		t.Errorf("scroll is %d, want it clamped to %d", p.scroll, p.maxScroll())
	}
	if got, want := texts(p)[0], "0"; got != want {
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

// TestPaneVisibleShorterThanGrid: a history that does not fill the pane starts at
// its first line, with no room to scroll.
func TestPaneVisibleShorterThanGrid(t *testing.T) {
	p := newPane(1)
	for _, l := range numbered(0, 3) {
		p.Write(l)
	}
	p.setGrid(80, 10)

	from, to := p.visible()
	if from != 0 || to != 3 {
		t.Errorf("visible window is [%d,%d), want [0,3)", from, to)
	}
	if p.maxScroll() != 0 {
		t.Errorf("maxScroll is %d, want 0", p.maxScroll())
	}
}

// TestPaneWriteKeepsView pins the sticky-tail rule: pinned panes follow new output,
// scrolled-back ones stay on the text the user is reading.
func TestPaneWriteKeepsView(t *testing.T) {
	p := newPane(1)
	for _, l := range numbered(0, 100) {
		p.Write(l)
	}
	p.setGrid(80, 10)

	p.Write(line{Text: "fresh"})
	if got, want := texts(p)[9], "fresh"; got != want {
		t.Errorf("a pinned pane shows %q at the bottom, want %q", got, want)
	}

	p.scrollBy(20)
	before := texts(p)
	p.Write(line{Text: "later"})
	if got := texts(p); !slices.Equal(got, before) {
		t.Errorf("a scrolled-back pane moved to %v, want it to stay on %v", got, before)
	}
}

// TestPaneWriteKeepsViewAtCapacity is the same rule once the ring evicts: the
// history stops growing, so the anchor has to survive the shift instead.
func TestPaneWriteKeepsViewAtCapacity(t *testing.T) {
	p := newPane(1)
	for _, l := range numbered(0, maxScrollback) {
		p.Write(l)
	}
	p.setGrid(80, 10)
	p.scrollBy(20)

	before := texts(p)
	p.Write(line{Text: "later"})
	if got := texts(p); !slices.Equal(got, before) {
		t.Errorf("view moved to %v, want it to stay on %v", got, before)
	}
}

// TestSetGridClampsScroll: growing a pane can leave the view further back than
// there is history for.
func TestSetGridClampsScroll(t *testing.T) {
	p := newPane(1)
	for _, l := range numbered(0, 30) {
		p.Write(l)
	}
	p.setGrid(80, 10)
	p.scrollBy(20)

	p.setGrid(80, 25)
	if p.scroll != 5 {
		t.Errorf("scroll is %d, want 5 (30 lines in a 25-row pane)", p.scroll)
	}
	if got, want := texts(p)[0], "0"; got != want {
		t.Errorf("top line is %q, want %q", got, want)
	}
}
