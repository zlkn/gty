package main

import (
	"image"
	"testing"
	"time"

	"gty/internal/vte"
)

// cursorPane is a pane placed and gridded the way a layout pass would leave it, with
// enough output behind it that the history is not empty.
func cursorPane(rect image.Rectangle, cols, rows, lines int) *pane {
	return gridPane(1, rect, cols, rows, numbered(0, lines)...)
}

// TestCursorRowRidesTheScreen: the cursor lives on the live screen, so while the view is
// pinned its row in the window is its row on the screen — and new output does not move it.
func TestCursorRowRidesTheScreen(t *testing.T) {
	p := cursorPane(image.Rect(0, 0, 400, 300), 20, 10, 100)

	row, col, ok := p.cursorRow()
	if c := p.frame.Cursor; !ok || row != c.Row || col != c.Col {
		t.Fatalf("cursorRow = (%d, %d, %v), want (%d, %d, true)", row, col, ok, c.Row, c.Col)
	}

	feedText(p, "", "more output")
	row, _, ok = p.cursorRow()
	if !ok || row != p.frame.Cursor.Row {
		t.Errorf("after more output the cursor is on row %d (ok=%v), want %d", row, ok, p.frame.Cursor.Row)
	}
	// Wherever it is, it is in the window: that is the whole point of a live grid.
	if row < 0 || row >= len(p.frame.Lines) {
		t.Errorf("cursor on row %d is outside the %d-line window", row, len(p.frame.Lines))
	}
}

// TestCursorRowFollowsAScrolledView: scrolling back does not move the cursor on the screen,
// it moves the window — so the cursor slides down the window and eventually out of it.
func TestCursorRowFollowsAScrolledView(t *testing.T) {
	p := cursorPane(image.Rect(0, 0, 400, 300), 20, 10, 100)
	// Off the bottom row, or the first line scrolled back would already take it out of view.
	placeCursor(p, 2, 0)
	onScreen := p.frame.Cursor.Row

	p.scrollBy(3)
	p.snap()
	if row, _, ok := p.cursorRow(); !ok || row != onScreen+3 {
		t.Errorf("scrolled 3 back the cursor is on row %d (ok=%v), want %d", row, ok, onScreen+3)
	}

	p.scrollBy(20)
	p.snap()
	if _, _, ok := p.cursorRow(); ok {
		t.Error("the cursor is still reported after the view scrolled past it")
	}
}

func TestCursorRowHidden(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(p *pane)
	}{
		{"not shown", func(p *pane) { p.shown = false }},
		// A grid narrower than the terminal's is what a view looks like for the frame
		// between a resize and the terminal hearing about it.
		{"column past the grid", func(p *pane) { p.cols = 1 }},
		{"pane with no rows", func(p *pane) { p.setGrid(20, 0); p.snap() }},
		{"scrolled back into the history", func(p *pane) { p.scrollBy(20); p.snap() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := cursorPane(image.Rect(0, 0, 400, 300), 20, 10, 100)
			tc.set(p)
			if _, _, ok := p.cursorRow(); ok {
				t.Error("cursorRow reports a visible cursor")
			}
		})
	}
}

// TestCursorCellMatchesTheGlyphGrid pins the cell to the same origin text.Layout lays
// glyphs from, before it backs off by the atlas padding.
func TestCursorCellMatchesTheGlyphGrid(t *testing.T) {
	p := cursorPane(image.Rect(100, 50, 500, 350), 20, 10, 100)
	placeCursor(p, 7, 3)

	cell, ok := p.cursorCell(testCellW, testCellH)
	if !ok {
		t.Fatal("cursorCell reports the cursor hidden")
	}
	want := image.Rect(
		100+padding+3*testCellW, 50+padding+7*testCellH,
		100+padding+4*testCellW, 50+padding+8*testCellH)
	if cell != want {
		t.Errorf("cell is %v, want %v", cell, want)
	}
}

// TestCursorCellStaysInsidePane is the invariant that lets the cursor skip the
// scissor: padding + cols*cellW never reaches past the pane.
func TestCursorCellStaysInsidePane(t *testing.T) {
	// Deliberately not a whole number of cells in either direction.
	full := image.Rect(0, 0, 397, 293)
	cols := max(0, (full.Dx()-2*padding)/testCellW)
	rows := max(0, (full.Dy()-2*padding)/testCellH)

	for _, tc := range []struct{ row, col int }{{0, 0}, {0, cols - 1}, {rows - 1, cols - 1}} {
		p := cursorPane(full, cols, rows, 100)
		placeCursor(p, tc.row, tc.col)

		cell, ok := p.cursorCell(testCellW, testCellH)
		if !ok {
			t.Fatalf("row %d col %d: cursorCell reports the cursor hidden", tc.row, tc.col)
		}
		if !cell.In(full) {
			t.Errorf("row %d col %d: cell %v escapes the pane %v", tc.row, tc.col, cell, full)
		}
	}
}

func TestCursorQuads(t *testing.T) {
	cell := image.Rect(10, 20, 10+testCellW, 20+testCellH)

	for _, shape := range []vte.CursorShape{vte.CursorBlock, vte.CursorBar, vte.CursorUnderline} {
		for _, q := range cursorQuads(cell, shape) {
			if q.Empty() {
				t.Errorf("shape %d produced an empty quad", shape)
			}
			if !q.In(cell) {
				t.Errorf("shape %d: quad %v escapes the cell %v", shape, q, cell)
			}
		}
	}

	if got := cursorQuads(cell, vte.CursorBlock); len(got) != 1 || got[0] != cell {
		t.Errorf("block is %v, want the whole cell %v", got, cell)
	}
	if got := cursorQuads(cell, vte.CursorBar)[0]; got.Min != cell.Min || got.Dx() != cursorBarWidth || got.Dy() != cell.Dy() {
		t.Errorf("bar is %v, want a %d px column down the left of %v", got, cursorBarWidth, cell)
	}
	if got := cursorQuads(cell, vte.CursorUnderline)[0]; got.Max != cell.Max || got.Dy() != cursorUnderlineHeight || got.Dx() != cell.Dx() {
		t.Errorf("underline is %v, want a %d px row along the bottom of %v", got, cursorUnderlineHeight, cell)
	}
}

// TestCursorOutlineIsHollow: the rim covers every edge pixel and none of the middle,
// which is what leaves an unfocused pane's glyph readable without inverting it.
func TestCursorOutlineIsHollow(t *testing.T) {
	cell := image.Rect(10, 20, 10+testCellW, 20+testCellH)
	rim := cursorOutline(cell)

	covered := func(pt image.Point) bool {
		for _, q := range rim {
			if pt.In(q) {
				return true
			}
		}
		return false
	}

	for _, q := range rim {
		if !q.In(cell) {
			t.Errorf("rim %v escapes the cell %v", q, cell)
		}
	}
	for x := cell.Min.X; x < cell.Max.X; x++ {
		for _, pt := range []image.Point{{x, cell.Min.Y}, {x, cell.Max.Y - 1}} {
			if !covered(pt) {
				t.Errorf("edge pixel %v is not on the rim", pt)
			}
		}
	}
	for y := cell.Min.Y; y < cell.Max.Y; y++ {
		for _, pt := range []image.Point{{cell.Min.X, y}, {cell.Max.X - 1, y}} {
			if !covered(pt) {
				t.Errorf("edge pixel %v is not on the rim", pt)
			}
		}
	}
	if mid := image.Pt(cell.Min.X+cell.Dx()/2, cell.Min.Y+cell.Dy()/2); covered(mid) {
		t.Errorf("the rim covers the middle %v: it is not hollow", mid)
	}
}

// TestCursorRectsSplitsByFocus: the focused pane fills, the rest get rims. They are
// separate groups because they are drawn in different colours.
func TestCursorRectsSplitsByFocus(t *testing.T) {
	left := cursorPane(image.Rect(0, 0, 400, 300), 20, 10, 100)
	right := cursorPane(image.Rect(400, 0, 800, 300), 20, 10, 100)
	right.id = 2

	fills, rims := cursorRects([]*pane{left, right}, left, testCellW, testCellH)
	if len(fills) != 1 {
		t.Errorf("focused pane contributed %d fills, want 1 block", len(fills))
	}
	if len(rims) != 4 {
		t.Errorf("unfocused pane contributed %d rim quads, want 4", len(rims))
	}

	// A hidden cursor contributes nothing at all.
	right.shown = false
	if _, rims := cursorRects([]*pane{left, right}, left, testCellW, testCellH); len(rims) != 0 {
		t.Errorf("a hidden cursor still drew %d quads", len(rims))
	}
}

func TestBlinkOn(t *testing.T) {
	half := cursorBlinkPeriod / 2

	for _, tc := range []struct {
		since time.Duration
		want  bool
	}{
		{0, true},
		{half - time.Millisecond, true},
		{half, false},
		{cursorBlinkPeriod - time.Millisecond, false},
		{cursorBlinkPeriod, true},
		{cursorBlinkPeriod + half, false},
	} {
		if got := blinkOn(tc.since, true); got != tc.want {
			t.Errorf("blinkOn(%v, focused) = %v, want %v", tc.since, got, tc.want)
		}
	}

	// An unfocused window holds the cursor solid; that is what lets run drop the timer
	// and park in WaitEvents.
	for _, since := range []time.Duration{0, half, cursorBlinkPeriod + half} {
		if !blinkOn(since, false) {
			t.Errorf("blinkOn(%v, unfocused) = false, want the cursor held solid", since)
		}
	}
}

// TestCursorScaled: on a scaled display the cursor steps over the scaled padding and
// its bar grows with it — a two-pixel bar on a 2x panel is the hairline it is not
// meant to be.
func TestCursorScaled(t *testing.T) {
	withScale(t, 2)

	p := cursorPane(image.Rect(100, 50, 500, 350), 20, 10, 100)
	placeCursor(p, 7, 3)

	cell, ok := p.cursorCell(testCellW, testCellH)
	if !ok {
		t.Fatal("cursorCell reports the cursor hidden")
	}
	want := image.Rect(
		100+2*padding+3*testCellW, 50+2*padding+7*testCellH,
		100+2*padding+4*testCellW, 50+2*padding+8*testCellH)
	if cell != want {
		t.Errorf("cell is %v, want %v", cell, want)
	}

	if got := cursorQuads(cell, vte.CursorBar)[0]; got.Dx() != 2*cursorBarWidth {
		t.Errorf("bar is %d px wide, want %d", got.Dx(), 2*cursorBarWidth)
	}
	if got := cursorOutline(cell)[0]; got.Dy() != 2*cursorOutlineWidth {
		t.Errorf("rim is %d px thick, want %d", got.Dy(), 2*cursorOutlineWidth)
	}
}
