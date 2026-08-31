package main

import (
	"image"
	"slices"
	"testing"

	"gty/internal/font"
	"gty/internal/vte"
)

// counter is a shaper that records how many lines it was asked to shape, and returns
// something cheap enough to compare.
type counter struct{ calls int }

func (c *counter) shape(cells []vte.Cell, dst []font.GID) []font.GID {
	c.calls++
	return append(dst, font.GID(len(cells)))
}

// shapeFrame runs every line of the pane's frame past the cache, the way a draw does.
func shapeFrame(p *pane, c *counter) {
	for i := range p.frame.Lines {
		p.cache.shaped(&p.frame.Lines[i], c.shape)
	}
}

// TestRowCacheShapesOncePerLine: a line is shaped once, however many frames read it. This is
// what the cache is for — reshaping a screenful costs tens of milliseconds, and every
// keystroke is a frame.
func TestRowCacheShapesOncePerLine(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)
	var c counter

	shapeFrame(p, &c)
	if c.calls != len(p.frame.Lines) {
		t.Fatalf("a fresh cache shaped %d of %d lines", c.calls, len(p.frame.Lines))
	}

	first := c.calls
	shapeFrame(p, &c)
	if c.calls != first {
		t.Errorf("a second pass over an unchanged frame shaped %d more lines, want none", c.calls-first)
	}
}

// TestRowCacheSurvivesAScroll is the property the whole split rests on: one line of output
// costs one line of shaping, not a screenful. A line's Seq does not move when the screen
// scrolls it, so every line still on screen keeps its entry.
func TestRowCacheSurvivesAScroll(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)
	var c counter
	shapeFrame(p, &c)
	c.calls = 0

	feedText(p, "", "one more line")
	shapeFrame(p, &c)
	if c.calls != 1 {
		t.Errorf("one line of output cost %d reshapes, want 1 — the new line and nothing else", c.calls)
	}
}

// TestRowCacheReshapesAWrittenLine: the version moves when the line does, and that is the
// whole of what tells the cache its glyphs are stale.
func TestRowCacheReshapesAWrittenLine(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, "hello")
	var c counter
	shapeFrame(p, &c)
	c.calls = 0

	p.term.Feed([]byte("X"))
	p.snap()
	shapeFrame(p, &c)
	if c.calls != 1 {
		t.Errorf("writing one cell cost %d reshapes, want 1", c.calls)
	}
}

// TestRowCacheResetsOnAGridChange: every cached line was shaped against the old width, and
// the renderer clips a line to the grid.
func TestRowCacheResetsOnAGridChange(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)
	var c counter
	shapeFrame(p, &c)
	c.calls = 0

	p.setGrid(40, 10)
	p.snap()
	shapeFrame(p, &c)
	if c.calls != len(p.frame.Lines) {
		t.Errorf("a width change reshaped %d of %d lines, want all of them", c.calls, len(p.frame.Lines))
	}
}

// TestRowCacheDoesNotConfuseLinesSharingASlot: a slot is shared by every line congruent to
// it, so the line's own number has to be checked and not only its version.
func TestRowCacheDoesNotConfuseLinesSharingASlot(t *testing.T) {
	var cache rowCache
	cache.fit(4)

	// Same version, four apart, so they want the same slot.
	a := vte.Row{Cells: cellsOf("aaa"), Seq: 1, Gen: 1}
	b := vte.Row{Cells: cellsOf("bb"), Seq: 5, Gen: 1}
	var c counter

	if got := cache.shaped(&a, c.shape); !slices.Equal(got, []font.GID{3}) {
		t.Fatalf("line a shaped to %v, want [3]", got)
	}
	if got := cache.shaped(&b, c.shape); !slices.Equal(got, []font.GID{2}) {
		t.Errorf("line b was handed %v, want [2] — it took line a's entry", got)
	}
	if got := cache.shaped(&a, c.shape); !slices.Equal(got, []font.GID{3}) {
		t.Errorf("line a was handed %v, want [3]", got)
	}
}

// TestRowCacheHoldsEveryLineOnScreen: sized to the grid, the lines of one frame cannot
// evict each other — their numbers are consecutive, and a collision needs a screenful
// between them.
func TestRowCacheHoldsEveryLineOnScreen(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 80, 10, numbered(0, 100)...)
	var c counter
	shapeFrame(p, &c)

	slots := map[uint64]bool{}
	for i := range p.frame.Lines {
		slots[p.frame.Lines[i].Seq%uint64(len(p.cache.slots))] = true
	}
	if len(slots) != len(p.frame.Lines) {
		t.Errorf("%d lines share %d slots; some evict each other every frame", len(p.frame.Lines), len(slots))
	}
}
