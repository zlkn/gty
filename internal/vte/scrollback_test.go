package vte

import (
	"fmt"
	"slices"
	"testing"
)

func TestScrollbackKeepsOrder(t *testing.T) {
	var s scrollback
	for _, l := range numbered(0, 3) {
		s.append(rowOf(l))
	}

	if s.len() != 3 {
		t.Fatalf("len is %d, want 3", s.len())
	}
	for i, want := range []string{"0", "1", "2"} {
		if got := s.row(i).String(); got != want {
			t.Errorf("line %d is %q, want %q", i, got, want)
		}
	}
}

// TestScrollbackEvictsOldest: at capacity the ring drops the front instead of growing.
func TestScrollbackEvictsOldest(t *testing.T) {
	var s scrollback
	for _, l := range numbered(0, MaxScrollback+5) {
		s.append(rowOf(l))
	}

	if s.len() != MaxScrollback {
		t.Fatalf("len is %d, want %d", s.len(), MaxScrollback)
	}
	if got, want := s.row(0).String(), "5"; got != want {
		t.Errorf("oldest line is %q, want %q", got, want)
	}
	if got, want := s.row(s.len()-1).String(), fmt.Sprint(MaxScrollback+4); got != want {
		t.Errorf("newest line is %q, want %q", got, want)
	}
}

// TestScrollbackGrowsLazily: the ring reaches its bound, it is not allocated at it. A line
// of cells is far heavier than the string it replaced, and a fresh terminal holds none.
func TestScrollbackGrowsLazily(t *testing.T) {
	var s scrollback
	if len(s.rows) != 0 {
		t.Errorf("a fresh scrollback holds %d rows, want none", len(s.rows))
	}
	for _, l := range numbered(0, 5) {
		s.append(rowOf(l))
	}
	if len(s.rows) != 5 {
		t.Errorf("after five lines the ring is %d rows, want 5", len(s.rows))
	}
}

// TestScrollbackAppendCopies: the screen recycles the row it retires, so the history must
// not be left aliasing it.
func TestScrollbackAppendCopies(t *testing.T) {
	var s scrollback
	src := rowOf("original")
	s.append(src)

	clear(src.Cells)
	if got := s.row(0).String(); got != "original" {
		t.Errorf("history line became %q after the caller reused its buffer, want %q", got, "original")
	}
}

// TestScrollbackAppendTrims: a line costs its own length, not the grid's width. This is
// what makes ten thousand of them affordable.
func TestScrollbackAppendTrims(t *testing.T) {
	var s scrollback
	wide := &Row{Cells: make([]Cell, 200)}
	copy(wide.Cells, cellsOf("short"))
	s.append(wide)

	if got := len(s.row(0).Cells); got != len("short") {
		t.Errorf("a 200-cell line was stored as %d cells, want %d", got, len("short"))
	}
}

// TestScrollbackAppendKeepsGen: the retired line is the same line it was on screen, so a
// view that shaped it there must keep its glyphs across the move.
func TestScrollbackAppendKeepsGen(t *testing.T) {
	var s scrollback
	src := rowOf("text")
	src.Gen = 42
	s.append(src)

	if got := s.row(0).Gen; got != 42 {
		t.Errorf("the retired line arrived at Gen %d, want the %d it left with", got, 42)
	}
}

// TestScrollbackResetKeepsNumbering: ED 3 drops the lines, never the numbering. A Seq that
// has been handed out must never be handed out again, or a view's cache would take a new
// line for one it had already shaped.
func TestScrollbackResetKeepsNumbering(t *testing.T) {
	var s scrollback
	for _, l := range numbered(0, 4) {
		s.append(rowOf(l))
	}
	s.reset()

	if s.len() != 0 {
		t.Errorf("the ring holds %d lines after a reset, want none", s.len())
	}
	if s.retired != 4 {
		t.Errorf("retired is %d after a reset, want the 4 lines that have gone through", s.retired)
	}
	s.append(rowOf("after"))
	if s.retired != 5 {
		t.Errorf("retired is %d, want 5", s.retired)
	}
}

// TestFrameWindowFollowsTheTail: a pinned view is exactly the live screen.
func TestFrameWindowFollowsTheTail(t *testing.T) {
	tm := gridTerm(80, 10, numbered(0, 100)...)

	want := []string{"90", "91", "92", "93", "94", "95", "96", "97", "98", "99"}
	if got := frameText(tm, 0); !slices.Equal(got, want) {
		t.Errorf("the tail shows %v, want %v", got, want)
	}
	if got := frameText(tm, 5); got[0] != "85" {
		t.Errorf("scrolled 5 back the top line is %q, want %q", got[0], "85")
	}
}

// TestFrameClampsScroll: a view that asks for more history than there is gets the top of
// it, and is told where it really ended up.
func TestFrameClampsScroll(t *testing.T) {
	tm := gridTerm(80, 10, numbered(0, 100)...)

	f := tm.Frame(nil, 1000)
	if f.Scroll != f.HistLen {
		t.Errorf("scroll came back as %d, want it clamped to the %d lines of history", f.Scroll, f.HistLen)
	}
	if got := f.Lines[0].String(); got != "0" {
		t.Errorf("at the top of history the first line is %q, want %q", got, "0")
	}
	if f := tm.Frame(nil, -5); f.Scroll != 0 {
		t.Errorf("a negative scroll came back as %d, want 0", f.Scroll)
	}
}

// TestFrameShorterThanGrid: output that has not filled the screen leaves the history empty
// and nowhere to scroll.
func TestFrameShorterThanGrid(t *testing.T) {
	tm := gridTerm(80, 10, numbered(0, 3)...)

	f := tm.Frame(nil, 0)
	if f.HistLen != 0 {
		t.Errorf("history holds %d lines, want none: nothing has scrolled off yet", f.HistLen)
	}
	if len(f.Lines) != 10 {
		t.Errorf("the window is %d lines, want the whole screen, 10", len(f.Lines))
	}
	if tm.MaxScroll() != 0 {
		t.Errorf("MaxScroll is %d, want 0", tm.MaxScroll())
	}
}

// TestFrameNumbersLinesAcrossTheHistory is the property a view's cache rests on: one
// numbering over the history and the screen, and a line keeps its number when the screen
// retires it. Otherwise every line of output would invalidate the whole screen.
func TestFrameNumbersLinesAcrossTheHistory(t *testing.T) {
	tm := gridTerm(80, 4, numbered(0, 10)...)

	before := tm.Frame(nil, 0)
	seqs := map[string]uint64{}
	for _, r := range before.Lines {
		seqs[r.String()] = r.Seq
	}

	// One more line: the top of the window retires into the history, and the rest step up.
	feedText(tm, "", "fresh")

	for _, r := range tm.Frame(nil, 0).Lines {
		if was, ok := seqs[r.String()]; ok && was != r.Seq {
			t.Errorf("line %q was Seq %d and is now %d; a line must keep its number", r.String(), was, r.Seq)
		}
	}
	// And the retired line keeps it too, now that it is read out of the ring.
	for _, r := range tm.Frame(nil, 2).Lines {
		if was, ok := seqs[r.String()]; ok && was != r.Seq {
			t.Errorf("retired line %q was Seq %d and is now %d", r.String(), was, r.Seq)
		}
	}
}

// TestFrameSeqsAreStrictlyIncreasing: the numbering is a position in the session, so it
// rises through the window whatever the window is showing.
func TestFrameSeqsAreStrictlyIncreasing(t *testing.T) {
	tm := gridTerm(80, 6, numbered(0, 40)...)
	for _, back := range []int{0, 3, 17, 1000} {
		lines := tm.Frame(nil, back).Lines
		for i := 1; i < len(lines); i++ {
			if lines[i].Seq != lines[i-1].Seq+1 {
				t.Fatalf("scrolled %d back, Seq goes %d then %d", back, lines[i-1].Seq, lines[i].Seq)
			}
		}
	}
}

// TestFrameBumpsGenOnAWrite: the version moves when the line does, and only then. A view
// redraws on it.
func TestFrameBumpsGenOnAWrite(t *testing.T) {
	tm := gridTerm(80, 4, "one", "two")

	first := tm.Frame(nil, 0)
	gens := make([]uint64, len(first.Lines))
	for i, r := range first.Lines {
		gens[i] = r.Gen
	}

	if second := tm.Frame(nil, 0); !slices.Equal(gens, gensOf(second.Lines)) {
		t.Errorf("two reads with nothing written between them gave %v then %v", gens, gensOf(second.Lines))
	}

	tm.Feed([]byte("X"))
	after := gensOf(tm.Frame(nil, 0).Lines)
	if after[1] == gens[1] {
		t.Errorf("the written line is still Gen %d; a view would keep its stale glyphs", after[1])
	}
	if after[0] != gens[0] {
		t.Errorf("an untouched line moved from Gen %d to %d", gens[0], after[0])
	}
}

func gensOf(rows []Row) []uint64 {
	out := make([]uint64, len(rows))
	for i, r := range rows {
		out[i] = r.Gen
	}
	return out
}

// TestFrameReusesDst is the allocation contract: a view draws every frame, and the rows it
// hands back keep their cell arrays between calls.
func TestFrameReusesDst(t *testing.T) {
	tm := gridTerm(80, 10, numbered(0, 100)...)

	dst := tm.Frame(nil, 0).Lines
	caps := make([]int, len(dst))
	ptrs := make([]*Cell, len(dst))
	for i := range dst {
		caps[i] = cap(dst[i].Cells)
		ptrs[i] = &dst[i].Cells[0]
	}

	again := tm.Frame(dst, 0).Lines
	if len(again) != len(dst) {
		t.Fatalf("the window changed length, %d then %d", len(dst), len(again))
	}
	for i := range again {
		if cap(again[i].Cells) != caps[i] || &again[i].Cells[0] != ptrs[i] {
			t.Errorf("row %d was reallocated; a steady stream of frames must not allocate", i)
		}
	}
}

// TestFrameCopiesRows: the terminal goes on writing to its own rows, so a view that holds a
// frame must not see them change under it.
func TestFrameCopiesRows(t *testing.T) {
	tm := gridTerm(80, 4, "one", "two")

	held := tm.Frame(nil, 0)
	was := held.Lines[1].String()
	tm.Feed([]byte("\rZZZ"))

	if got := held.Lines[1].String(); got != was {
		t.Errorf("the held frame changed from %q to %q under the caller", was, got)
	}
}

// TestGetViewportRange reads an explicit slice of the view rather than the drawn window.
func TestGetViewportRange(t *testing.T) {
	tm := gridTerm(80, 4, numbered(0, 20)...)

	rows := tm.GetViewport(2, 5)
	want := []string{"2", "3", "4"}
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.String()
	}
	if !slices.Equal(got, want) {
		t.Errorf("GetViewport(2, 5) is %v, want %v", got, want)
	}

	// Out of range is clamped, not fatal: a view's arithmetic lags a resize by a frame.
	if got := tm.GetViewport(-5, 1000); len(got) != tm.hist.len()+4 {
		t.Errorf("a range past both ends gave %d lines, want %d", len(got), tm.hist.len()+4)
	}
	if got := tm.GetViewport(10, 3); len(got) != 0 {
		t.Errorf("an inverted range gave %d lines, want none", len(got))
	}
}

// TestAltScreenNumbersApart: the two grids have their own rows and so their own Gens, and a
// view caching by Seq must not be handed one grid's version for the other's line.
func TestAltScreenNumbersApart(t *testing.T) {
	tm := gridTerm(80, 4, numbered(0, 20)...)
	primary := tm.Frame(nil, 0).Lines[0].Seq

	tm.Feed([]byte("\x1b[?1049h"))
	if alt := tm.Frame(nil, 0).Lines[0].Seq; alt == primary {
		t.Errorf("the alternate screen numbers its first line %d, the same as the primary's", alt)
	}

	tm.Feed([]byte("\x1b[?1049l"))
	if back := tm.Frame(nil, 0).Lines[0].Seq; back != primary {
		t.Errorf("back on the primary the first line is Seq %d, want the %d it had", back, primary)
	}
}

// TestAltScreenHasNoHistory: scrolling back into what the primary left behind would be
// nonsense while a full-screen program owns the grid.
func TestAltScreenHasNoHistory(t *testing.T) {
	tm := gridTerm(80, 4, numbered(0, 20)...)
	tm.Feed([]byte("\x1b[?1049h"))

	if got := tm.MaxScroll(); got != 0 {
		t.Errorf("the alternate screen offers %d lines to scroll back, want none", got)
	}
	if f := tm.Frame(nil, 50); f.Scroll != 0 || f.HistLen != 0 {
		t.Errorf("a scrolled-back view of the alternate screen came back at %d over %d lines of history, want 0 over 0", f.Scroll, f.HistLen)
	}
}
