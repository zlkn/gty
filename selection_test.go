package main

import (
	"image"
	"testing"
	"time"

	"gty/internal/vte"
)

func TestSelectionOrdered(t *testing.T) {
	// 1. Forward character drag on same row
	s := selection{
		anchor: selPos{seq: 10, col: 2},
		head:   selPos{seq: 10, col: 5},
		mode:   selChar,
		active: true,
	}
	lo, hi := s.ordered()
	if lo != (selPos{seq: 10, col: 2}) || hi != (selPos{seq: 10, col: 6}) {
		t.Errorf("forward char ordered = (%v, %v), want ((10, 2), (10, 6))", lo, hi)
	}

	// 2. Backward character drag on same row
	sBack := selection{
		anchor: selPos{seq: 10, col: 5},
		head:   selPos{seq: 10, col: 2},
		mode:   selChar,
		active: true,
	}
	lo, hi = sBack.ordered()
	if lo != (selPos{seq: 10, col: 2}) || hi != (selPos{seq: 10, col: 6}) {
		t.Errorf("backward char ordered = (%v, %v), want ((10, 2), (10, 6))", lo, hi)
	}

	// 3. Multi-row forward drag
	sMulti := selection{
		anchor: selPos{seq: 10, col: 2},
		head:   selPos{seq: 12, col: 5},
		mode:   selChar,
		active: true,
	}
	lo, hi = sMulti.ordered()
	if lo != (selPos{seq: 10, col: 2}) || hi != (selPos{seq: 12, col: 6}) {
		t.Errorf("multi-row ordered = (%v, %v), want ((10, 2), (12, 6))", lo, hi)
	}

	// 4. Block mode drag
	sBlock := selection{
		anchor: selPos{seq: 15, col: 10},
		head:   selPos{seq: 10, col: 5},
		block:  true,
		active: true,
	}
	lo, hi = sBlock.ordered()
	if lo != (selPos{seq: 10, col: 5}) || hi != (selPos{seq: 15, col: 11}) {
		t.Errorf("block ordered = (%v, %v), want ((10, 5), (15, 11))", lo, hi)
	}
}

func TestSelectionContains(t *testing.T) {
	// Inactive selection contains nothing
	sInactive := selection{
		anchor: selPos{seq: 10, col: 2},
		head:   selPos{seq: 10, col: 5},
		mode:   selChar,
		active: false,
	}
	if sInactive.contains(10, 3) {
		t.Errorf("inactive selection should not contain cell")
	}

	// Single-line linear selection: row 10, cols 2..5 (hi.col = 6)
	sLinear := selection{
		anchor: selPos{seq: 10, col: 2},
		head:   selPos{seq: 10, col: 5},
		mode:   selChar,
		active: true,
	}
	for col := 2; col <= 5; col++ {
		if !sLinear.contains(10, col) {
			t.Errorf("expected col %d to be contained", col)
		}
	}
	if sLinear.contains(10, 1) || sLinear.contains(10, 6) {
		t.Errorf("bounds out of range should not be contained")
	}
	if sLinear.contains(9, 3) || sLinear.contains(11, 3) {
		t.Errorf("other rows should not be contained")
	}

	// Multi-line linear selection: row 10 col 5 to row 12 col 3
	sMulti := selection{
		anchor: selPos{seq: 10, col: 5},
		head:   selPos{seq: 12, col: 3},
		mode:   selChar,
		active: true,
	}
	// Row 10: col 4 outside, col 5 inside, col 50 inside
	if sMulti.contains(10, 4) {
		t.Errorf("row 10 col 4 should be outside")
	}
	if !sMulti.contains(10, 5) || !sMulti.contains(10, 50) {
		t.Errorf("row 10 col >= 5 should be inside")
	}
	// Row 11: all columns inside
	if !sMulti.contains(11, 0) || !sMulti.contains(11, 100) {
		t.Errorf("row 11 interior row should be inside everywhere")
	}
	// Row 12: cols 0..3 inside, col 4 outside
	if !sMulti.contains(12, 0) || !sMulti.contains(12, 3) {
		t.Errorf("row 12 col <= 3 should be inside")
	}
	if sMulti.contains(12, 4) {
		t.Errorf("row 12 col 4 should be outside")
	}

	// Block selection: rows 10..12, cols 5..8 (hi.col = 9)
	sBlock := selection{
		anchor: selPos{seq: 10, col: 5},
		head:   selPos{seq: 12, col: 8},
		block:  true,
		active: true,
	}
	for r := uint64(10); r <= 12; r++ {
		for c := 5; c <= 8; c++ {
			if !sBlock.contains(r, c) {
				t.Errorf("block should contain (%d, %d)", r, c)
			}
		}
		if sBlock.contains(r, 4) || sBlock.contains(r, 9) {
			t.Errorf("block should not contain cols outside 5..8 on row %d", r)
		}
	}
	if sBlock.contains(9, 6) || sBlock.contains(13, 6) {
		t.Errorf("block should not contain rows outside 10..12")
	}
}

func TestWordAt(t *testing.T) {
	// "git commit -m 'hello world' /usr/bin/go"
	text := "git commit -m 'hello world' /usr/bin/go"
	cells := make([]vte.Cell, len(text))
	for i, r := range text {
		cells[i] = vte.Cell{Rune: r}
	}

	// Double click on "git"
	from, to := wordAt(cells, 1, len(text))
	if from != 0 || to != 3 {
		t.Errorf("wordAt('git') = [%d, %d), want [0, 3)", from, to)
	}

	// Double click on path "/usr/bin/go"
	idx := len(text) - 5 // inside "/usr/bin/go"
	from, to = wordAt(cells, idx, len(text))
	if from != len(text)-11 || to != len(text) {
		t.Errorf("wordAt path = [%d, %d), want [%d, %d)", from, to, len(text)-11, len(text))
	}

	// Double click on quote "'"
	quoteIdx := 14
	from, to = wordAt(cells, quoteIdx, len(text))
	if from != 14 || to != 15 {
		t.Errorf("wordAt quote = [%d, %d), want [14, 15)", from, to)
	}

	// Double click on space between "hello" and "world"
	spaceIdx := 20
	from, to = wordAt(cells, spaceIdx, len(text))
	if from != 20 || to != 21 {
		t.Errorf("wordAt space = [%d, %d), want [20, 21)", from, to)
	}
}

func TestClickTracker(t *testing.T) {
	ct := clickTracker{}
	now := time.Now()
	pos := selPos{seq: 5, col: 10}

	// First click
	if c := ct.click(1, pos, now); c != 1 {
		t.Errorf("click 1 = %d, want 1", c)
	}

	// Second click within 200ms -> double click
	now = now.Add(200 * time.Millisecond)
	if c := ct.click(1, pos, now); c != 2 {
		t.Errorf("click 2 = %d, want 2", c)
	}

	// Third click within 200ms -> triple click
	now = now.Add(200 * time.Millisecond)
	if c := ct.click(1, pos, now); c != 3 {
		t.Errorf("click 3 = %d, want 3", c)
	}

	// Fourth click cycles to 1
	now = now.Add(200 * time.Millisecond)
	if c := ct.click(1, pos, now); c != 1 {
		t.Errorf("click 4 = %d, want 1", c)
	}

	// Click after 500ms resets to 1
	now = now.Add(500 * time.Millisecond)
	if c := ct.click(1, pos, now); c != 1 {
		t.Errorf("click after timeout = %d, want 1", c)
	}

	// Click on another cell resets to 1
	now = now.Add(100 * time.Millisecond)
	otherPos := selPos{seq: 5, col: 11}
	if c := ct.click(1, otherPos, now); c != 1 {
		t.Errorf("click on another cell = %d, want 1", c)
	}

	// Click on another pane resets to 1
	now = now.Add(100 * time.Millisecond)
	if c := ct.click(2, otherPos, now); c != 1 {
		t.Errorf("click on another pane = %d, want 1", c)
	}
}

func TestCellAt(t *testing.T) {
	p := gridPane(1, image.Rect(100, 50, 500, 300), 10, 5, "hello", "world")
	cellW, cellH := 10, 20
	pad := px(padding)

	// Exact cell (0, 0)
	row, col := p.cellAt(image.Pt(100+pad+5, 50+pad+10), cellW, cellH)
	if row != 0 || col != 0 {
		t.Errorf("cellAt(0, 0) = (%d, %d), want (0, 0)", row, col)
	}

	// Inside cell (1, 3)
	row, col = p.cellAt(image.Pt(100+pad+35, 50+pad+30), cellW, cellH)
	if row != 1 || col != 3 {
		t.Errorf("cellAt(1, 3) = (%d, %d), want (1, 3)", row, col)
	}

	// Before padding on X and Y (should clamp to 0, 0)
	row, col = p.cellAt(image.Pt(50, 20), cellW, cellH)
	if row != 0 || col != 0 {
		t.Errorf("cellAt before padding = (%d, %d), want (0, 0)", row, col)
	}

	// Past last col (should clamp to p.cols = 10)
	row, col = p.cellAt(image.Pt(100+pad+200, 50+pad+10), cellW, cellH)
	if col != 10 {
		t.Errorf("cellAt past last col = %d, want 10", col)
	}

	// Past last row (should clamp to len(Lines)-1 = 4)
	row, col = p.cellAt(image.Pt(100+pad+5, 50+pad+500), cellW, cellH)
	if row != len(p.frame.Lines)-1 {
		t.Errorf("cellAt past last row = %d, want %d", row, len(p.frame.Lines)-1)
	}
}

func TestSelectionRects(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 400, 200), 20, 5, "line zero", "line one", "line two")
	cellW, cellH := 10, 20
	pad := px(padding)

	seq0 := p.frame.Lines[0].Seq
	seq1 := p.frame.Lines[1].Seq

	// Single row selection
	p.sel = selection{
		anchor: selPos{seq: seq0, col: 2},
		head:   selPos{seq: seq0, col: 6},
		mode:   selChar,
		active: true,
	}
	rects := selectionRects(nil, p, cellW, cellH)
	if len(rects) != 1 {
		t.Fatalf("want 1 rect for single row selection, got %d", len(rects))
	}
	wantRect := image.Rect(pad+2*cellW, pad, pad+7*cellW, pad+cellH)
	if rects[0] != wantRect {
		t.Errorf("single row rect = %v, want %v", rects[0], wantRect)
	}

	// Multi-row selection
	p.sel = selection{
		anchor: selPos{seq: seq0, col: 5},
		head:   selPos{seq: seq1, col: 3},
		mode:   selChar,
		active: true,
	}
	rects = selectionRects(nil, p, cellW, cellH)
	if len(rects) != 2 {
		t.Fatalf("want 2 rects for two-row selection, got %d", len(rects))
	}
	// Row 0: cols 5..20
	want0 := image.Rect(pad+5*cellW, pad, pad+20*cellW, pad+cellH)
	// Row 1: cols 0..4 (col 3 + 1)
	want1 := image.Rect(pad, pad+cellH, pad+4*cellW, pad+2*cellH)
	if rects[0] != want0 || rects[1] != want1 {
		t.Errorf("multi-row rects = %v, want [%v, %v]", rects, want0, want1)
	}

	// Inactive selection produces no rects
	p.sel.active = false
	if r := selectionRects(nil, p, cellW, cellH); len(r) != 0 {
		t.Errorf("inactive selection produced %d rects, want 0", len(r))
	}
}

func TestSetGridClearsSelection(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 400, 200), 20, 5, "test")
	p.sel = selection{
		anchor: selPos{seq: p.frame.Lines[0].Seq, col: 0},
		head:   selPos{seq: p.frame.Lines[0].Seq, col: 4},
		mode:   selWord,
		active: true,
	}

	// Resizing grid without dragging should clear selection
	p.setGrid(30, 5)
	if p.sel.active {
		t.Errorf("setGrid should clear active selection when not dragging")
	}

	// Resizing grid while dragging should preserve selection
	p.sel.active = true
	p.sel.dragging = true
	p.setGrid(40, 5)
	if !p.sel.active {
		t.Errorf("setGrid should preserve active selection when dragging")
	}
}
