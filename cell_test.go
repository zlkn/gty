package main

import (
	"testing"

	"gty/internal/vte"
)

// TestCellColorsFaintFadesTowardsPaper: SGR 2 asks for less contrast, so the ink has to move
// towards the paper. Multiplying it towards black instead leaves faint text heavier than
// plain text on a light theme, which is how claude's dimmed status line came out bolder
// than the output around it.
func TestCellColorsFaintFadesTowardsPaper(t *testing.T) {
	ink, paper := vte.RGBColor(0, 0, 0), vte.RGBColor(255, 255, 255)

	plain, _ := cellColors(vte.Cell{FG: ink, BG: paper})
	faint, bg := cellColors(vte.Cell{FG: ink, BG: paper, Attrs: vte.AttrFaint})

	if relLuminance(faint) <= relLuminance(plain) {
		t.Errorf("faint ink on light paper is %v, no lighter than plain %v", faint, plain)
	}
	if relLuminance(faint) >= relLuminance(bg) {
		t.Errorf("faint ink %v reached the paper %v; it has to stay readable", faint, bg)
	}
}

// TestCellColorsFaintUnderInverse: inverse trades the colours after faint has faded the ink,
// so the faded colour is what ends up behind the glyph rather than in it.
func TestCellColorsFaintUnderInverse(t *testing.T) {
	ink, paper := vte.RGBColor(0, 0, 0), vte.RGBColor(255, 255, 255)

	faded, _ := cellColors(vte.Cell{FG: ink, BG: paper, Attrs: vte.AttrFaint})
	fg, bg := cellColors(vte.Cell{FG: ink, BG: paper, Attrs: vte.AttrFaint | vte.AttrInverse})

	if want := [4]float32{1, 1, 1, 1}; fg != want {
		t.Errorf("inverse drew the glyph in %v, want the paper %v", fg, want)
	}
	if bg != faded {
		t.Errorf("inverse put %v behind the glyph, want the faded ink %v", bg, faded)
	}
}
