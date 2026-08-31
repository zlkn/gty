package main

import (
	"image"
	"testing"
)

// TestPaintedCellsBecomeQuads: a background is not a glyph, so it has to reach the rect
// renderer. Runs of one colour are coalesced, or a full screen would be one quad a cell.
func TestPaintedCellsBecomeQuads(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 400, 100), 20, 2, "\x1b[41mRRRR\x1b[44mBB\x1b[0mplain")

	quads := paintRects(nil, p, testCellW, testCellH)
	if len(quads) != 2 {
		t.Fatalf("got %d quads, want two runs: %v", len(quads), quads)
	}
	if got, want := quads[0].color, palette[1]; got != want {
		t.Errorf("first run is %v, want red %v", got, want)
	}
	if got, want := quads[0].rect.Dx(), 4*testCellW; got != want {
		t.Errorf("the red run is %d px wide, want %d — four cells coalesced", got, want)
	}
	if got, want := quads[1].rect.Dx(), 2*testCellW; got != want {
		t.Errorf("the blue run is %d px wide, want %d", got, want)
	}
}

// TestUnderlineBecomesAQuad: an underline is drawn, not shaped — the font has no glyph for
// it.
func TestUnderlineBecomesAQuad(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 400, 100), 20, 2, "\x1b[4mup\x1b[24mplain")

	quads := paintRects(nil, p, testCellW, testCellH)
	if len(quads) != 2 {
		t.Fatalf("got %d quads, want one under each of the two underlined cells: %v", len(quads), quads)
	}
	for i, q := range quads {
		if q.rect.Dy() != underlineHeight {
			t.Errorf("quad %d is %d px tall, want %d", i, q.rect.Dy(), underlineHeight)
		}
		if q.rect.Max.Y != padding+testCellH {
			t.Errorf("quad %d sits at y=%d, want it on the cell's baseline edge %d",
				i, q.rect.Max.Y, padding+testCellH)
		}
	}
}
