package main

import (
	"image"
	"testing"
)

// The layout only needs the cell step, so the tests state it instead of loading a
// face.
const (
	testCellW = 10
	testCellH = 22
)

func leafIDs(panes []*pane) []int {
	ids := make([]int, len(panes))
	for i, p := range panes {
		ids[i] = p.id
	}
	return ids
}

// TestSplitRectTiles: the halves and the divider tile the parent exactly.
func TestSplitRectTiles(t *testing.T) {
	full := image.Rect(0, 0, 900, 600)

	for _, tc := range []struct {
		name string
		d    dir
	}{{"vertical", vertical}, {"horizontal", horizontal}} {
		t.Run(tc.name, func(t *testing.T) {
			a, div, b := splitRect(full, tc.d, 0.5)

			if !a.Intersect(b).Empty() || !a.Intersect(div).Empty() || !b.Intersect(div).Empty() {
				t.Errorf("overlapping pieces: %v %v %v", a, div, b)
			}
			if got := a.Union(div).Union(b); got != full {
				t.Errorf("pieces cover %v, want %v", got, full)
			}
			if tc.d == vertical {
				if div.Dx() != dividerWidth {
					t.Errorf("divider is %d px wide, want %d", div.Dx(), dividerWidth)
				}
			} else if div.Dy() != dividerWidth {
				t.Errorf("divider is %d px tall, want %d", div.Dy(), dividerWidth)
			}
		})
	}
}

// TestSplitRectTooSmall: empty halves are fine, negative ones panic downstream.
func TestSplitRectTooSmall(t *testing.T) {
	a, div, b := splitRect(image.Rect(0, 0, 1, 20), vertical, 0.5)
	for _, r := range []image.Rectangle{a, div, b} {
		if r.Dx() < 0 || r.Dy() < 0 {
			t.Errorf("negative rect %v", r)
		}
	}
	if got := a.Union(div).Union(b); got != image.Rect(0, 0, 1, 20) {
		t.Errorf("pieces cover %v, want the whole rect", got)
	}
}

// TestLayoutTreeGrids: each pane's grid comes from its own rect, padded both sides.
func TestLayoutTreeGrids(t *testing.T) {
	root := &node{pane: newPane(1)}
	root.split(root.pane, vertical, newPane(2))

	full := image.Rect(0, 0, 900, 600)
	panes, dividers := layoutTree(root, full, testCellW, testCellH)

	if len(panes) != 2 || len(dividers) != 1 {
		t.Fatalf("got %d panes and %d dividers, want 2 and 1", len(panes), len(dividers))
	}
	for _, p := range panes {
		wantCols := (p.rect.Dx() - 2*padding) / testCellW
		wantRows := (p.rect.Dy() - 2*padding) / testCellH
		if p.cols != wantCols || p.rows != wantRows {
			t.Errorf("pane %d is %dx%d cells, want %dx%d", p.id, p.cols, p.rows, wantCols, wantRows)
		}
	}
	if panes[0].rect.Max.X > panes[1].rect.Min.X {
		t.Errorf("pane 1 (%v) overlaps pane 2 (%v)", panes[0].rect, panes[1].rect)
	}
}

// TestLayoutTreeTinyWindow: a pane with no room reports a zero grid, not a negative
// one.
func TestLayoutTreeTinyWindow(t *testing.T) {
	root := &node{pane: newPane(1)}
	panes, _ := layoutTree(root, image.Rect(0, 0, 20, 20), testCellW, testCellH)

	if len(panes) != 1 {
		t.Fatalf("got %d panes, want 1", len(panes))
	}
	if panes[0].cols != 0 || panes[0].rows != 0 {
		t.Errorf("grid is %dx%d, want 0x0", panes[0].cols, panes[0].rows)
	}
}

// TestSplitOrderAndNesting pins the traversal order the focus cycles through.
func TestSplitOrderAndNesting(t *testing.T) {
	first := newPane(1)
	root := &node{pane: first}
	second := newPane(2)
	root.split(first, vertical, second)

	// Split the right pane horizontally: 1 | (2 over 3).
	third := newPane(3)
	if !root.split(second, horizontal, third) {
		t.Fatal("split did not find the nested pane")
	}

	panes, dividers := layoutTree(root, image.Rect(0, 0, 900, 600), testCellW, testCellH)
	if got := leafIDs(panes); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("traversal order %v, want [1 2 3]", got)
	}
	if len(dividers) != 2 {
		t.Errorf("got %d dividers, want 2", len(dividers))
	}
	if panes[1].rect.Max.Y > panes[2].rect.Min.Y {
		t.Errorf("pane 2 (%v) overlaps pane 3 (%v)", panes[1].rect, panes[2].rect)
	}
	if panes[1].rect.Min.X != panes[2].rect.Min.X {
		t.Errorf("panes 2 and 3 should share a column: %v vs %v", panes[1].rect, panes[2].rect)
	}
}

func TestNextPaneCycles(t *testing.T) {
	one, two, three := newPane(1), newPane(2), newPane(3)
	panes := []*pane{one, two, three}

	for _, tc := range []struct{ from, want *pane }{{one, two}, {two, three}, {three, one}} {
		if got := nextPane(panes, tc.from); got != tc.want {
			t.Errorf("after pane %d the focus went to %v, want pane %d", tc.from.id, got, tc.want.id)
		}
	}
	if got := nextPane([]*pane{one}, one); got != one {
		t.Errorf("a lone pane cycled to %v, want itself", got)
	}
}

// TestDeepTreeTiles walks a window through six splits, checking the invariant the
// renderer rests on: pane rects stay inside the window and never overlap.
func TestDeepTreeTiles(t *testing.T) {
	full := image.Rect(0, 0, 900, 600)
	first := newPane(1)
	root := &node{pane: first}

	panes, _ := layoutTree(root, full, testCellW, testCellH)
	focus := first
	for i := range 6 {
		d := vertical
		if i%2 == 1 {
			d = horizontal
		}
		nu := newPane(i + 2)
		if !root.split(focus, d, nu) {
			t.Fatalf("split %d did not find pane %d", i, focus.id)
		}
		panes, _ = layoutTree(root, full, testCellW, testCellH)
		focus = nextPane(panes, nu)
	}

	if len(panes) != 7 {
		t.Fatalf("got %d panes, want 7", len(panes))
	}
	for i, p := range panes {
		if !p.rect.In(full) {
			t.Errorf("pane %d at %v escapes the window", p.id, p.rect)
		}
		for _, q := range panes[i+1:] {
			if !p.rect.Intersect(q.rect).Empty() {
				t.Errorf("pane %d (%v) overlaps pane %d (%v)", p.id, p.rect, q.id, q.rect)
			}
		}
	}

	for len(panes) > 1 {
		next, found := root.close(panes[0])
		if !found || next == nil {
			t.Fatalf("close refused with %d panes left (next=%v found=%v)", len(panes), next, found)
		}
		panes, _ = layoutTree(root, full, testCellW, testCellH)
	}
	if panes[0].rect != full {
		t.Errorf("last pane is %v, want the whole window", panes[0].rect)
	}
	if next, found := root.close(panes[0]); next != nil || !found {
		t.Errorf("closing the last pane returned (%v, %v), want (nil, true)", next, found)
	}
}

// TestCloseCollapsesIntoSibling: closing hands the space to the sibling, and the
// last pane refuses.
func TestCloseCollapsesIntoSibling(t *testing.T) {
	first := newPane(1)
	root := &node{pane: first}
	second := newPane(2)
	root.split(first, vertical, second)

	next, found := root.close(second)
	if !found || next != first {
		t.Fatalf("focus went to %v (found=%v), want pane 1", next, found)
	}
	panes, dividers := layoutTree(root, image.Rect(0, 0, 900, 600), testCellW, testCellH)
	if len(panes) != 1 || panes[0] != first {
		t.Fatalf("got %v panes, want just pane 1", leafIDs(panes))
	}
	if len(dividers) != 0 {
		t.Errorf("got %d dividers, want none", len(dividers))
	}
	if panes[0].rect != image.Rect(0, 0, 900, 600) {
		t.Errorf("surviving pane is %v, want the whole window", panes[0].rect)
	}

	if next, found := root.close(first); next != nil || !found {
		t.Errorf("closing the last pane returned (%v, %v), want (nil, true)", next, found)
	}

	// A pane that is not in the tree must not read as "the last one": conflating the
	// two would close the window on a lookup miss.
	if next, found := root.close(newPane(99)); next != nil || found {
		t.Errorf("closing a stranger returned (%v, %v), want (nil, false)", next, found)
	}
}

// TestCloseNestedFocusesSibling: the focus lands in the subtree that took the space.
func TestCloseNestedFocusesSibling(t *testing.T) {
	first := newPane(1)
	root := &node{pane: first}
	second, third := newPane(2), newPane(3)
	root.split(first, vertical, second)
	root.split(second, horizontal, third)

	if next, found := root.close(third); !found || next != second {
		t.Fatalf("focus went to %v (found=%v), want pane 2", next, found)
	}
	panes, _ := layoutTree(root, image.Rect(0, 0, 900, 600), testCellW, testCellH)
	if got := leafIDs(panes); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("traversal order %v, want [1 2]", got)
	}
}
