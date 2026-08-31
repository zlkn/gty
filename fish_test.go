package main

import (
	"image"
	"os"
	"slices"
	"testing"

	"gty/internal/font"
)

// TestFishSessionKeepsLigatures: the shaping the renderer does is unaffected by all the
// redrawing fish does — "=>" is still one arrow on the grid it left behind. The session
// itself, and what it puts on the grid, is the model's own test; see internal/vte.
func TestFishSessionKeepsLigatures(t *testing.T) {
	device, queue := newTestGPU(t)
	txt := newTestText(t, device, queue)

	session, err := os.ReadFile("internal/vte/testdata/fish-session.bin")
	if err != nil {
		t.Fatal(err)
	}
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
	// In chunks, the way a pty read arrives: the parser has to carry its state across the
	// seams for the arrow to survive at all.
	for len(session) > 0 {
		k := min(64, len(session))
		p.term.Feed(session[:k])
		session = session[k:]
	}
	p.snap()

	row := p.frame.Lines[2] // the output line, "a => b"
	if got := row.String(); got != "a => b" {
		t.Fatalf("row 2 is %q, want %q", got, "a => b")
	}
	gids := txt.shapeRow(row.Cells, p.cols, nil)

	eq, _ := txt.fm.GlyphIndex(font.Regular, '=')
	gt, _ := txt.fm.GlyphIndex(font.Regular, '>')
	if slices.Contains(gids, eq) && slices.Contains(gids, gt) {
		t.Errorf("the row shaped to %v, which still holds the plain '=' and '>': no ligature", gids)
	}
}
