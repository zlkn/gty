package main

import (
	"image"
	"testing"
	"time"
)

// cursorPane is a pane placed and gridded the way a layout pass would leave it, with
// enough output behind it that the history is not empty.
func cursorPane(rect image.Rectangle, cols, rows, lines int) *pane {
	p := gridPane(1, rect, cols, rows, numbered(0, lines)...)
	p.cursor.shown = true
	return p
}

// TestCursorAtRidesTheScreen: the cursor lives on the live screen, so its place in the
// view is the history's length plus its screen row — and new output does not move it.
func TestCursorAtRidesTheScreen(t *testing.T) {
	p := cursorPane(image.Rect(0, 0, 400, 300), 20, 10, 100)

	line, col, ok := p.cursorAt()
	if want := p.buf.Len() + p.scr.curRow; !ok || line != want || col != p.scr.curCol {
		t.Fatalf("cursorAt = (%d, %d, %v), want (%d, %d, true)", line, col, ok, want, p.scr.curCol)
	}

	feedText(p, "", "more output")
	if line, _, ok := p.cursorAt(); !ok || line != p.buf.Len()+p.scr.curRow {
		t.Errorf("after more output the cursor is at %d (ok=%v), want %d", line, ok, p.buf.Len()+p.scr.curRow)
	}

	// Wherever it is, it is on screen: that is the whole point of a live grid.
	from, to := p.visible()
	if line, _, _ := p.cursorAt(); line < from || line >= to {
		t.Errorf("cursor at %d is outside the visible window [%d,%d)", line, from, to)
	}
}

func TestCursorAtHidden(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(p *pane)
	}{
		{"not shown", func(p *pane) { p.cursor.shown = false }},
		{"column past the grid", func(p *pane) { p.scr.curCol = p.cols }},
		{"pane with no rows", func(p *pane) { p.setGrid(20, 0) }},
		{"scrolled back into the history", func(p *pane) { p.scrollBy(20) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := cursorPane(image.Rect(0, 0, 400, 300), 20, 10, 100)
			tc.set(p)
			if _, _, ok := p.cursorAt(); ok {
				t.Error("cursorAt reports a visible cursor")
			}
		})
	}
}

// TestCursorCellMatchesTheGlyphGrid pins the cell to the same origin text.Layout lays
// glyphs from, before it backs off by the atlas padding.
func TestCursorCellMatchesTheGlyphGrid(t *testing.T) {
	p := cursorPane(image.Rect(100, 50, 500, 350), 20, 10, 100)
	p.scr.curRow, p.scr.curCol = 7, 3

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
		p.scr.curRow, p.scr.curCol = tc.row, tc.col

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

	for _, shape := range []cursorShape{cursorBlock, cursorBar, cursorUnderline} {
		for _, q := range cursorQuads(cell, shape) {
			if q.Empty() {
				t.Errorf("shape %d produced an empty quad", shape)
			}
			if !q.In(cell) {
				t.Errorf("shape %d: quad %v escapes the cell %v", shape, q, cell)
			}
		}
	}

	if got := cursorQuads(cell, cursorBlock); len(got) != 1 || got[0] != cell {
		t.Errorf("block is %v, want the whole cell %v", got, cell)
	}
	if got := cursorQuads(cell, cursorBar)[0]; got.Min != cell.Min || got.Dx() != cursorBarWidth || got.Dy() != cell.Dy() {
		t.Errorf("bar is %v, want a %d px column down the left of %v", got, cursorBarWidth, cell)
	}
	if got := cursorQuads(cell, cursorUnderline)[0]; got.Max != cell.Max || got.Dy() != cursorUnderlineHeight || got.Dx() != cell.Dx() {
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
	right.cursor.on, right.cursor.shown = false, false
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
