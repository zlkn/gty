package font

import (
	"image"
	"strings"
	"testing"
)

// TestOneGlyphPerCell is the load-bearing assumption of the whole design: if a
// realistic line of code shapes into a different number of glyphs than runes, a
// per-cell renderer cannot draw it and the atlas would need multi-cell quads.
func TestOneGlyphPerCell(t *testing.T) {
	fm := newTestManager(t)

	corpus := []string{
		"if x != y && a <= b || c >= d { return -> nil }",
		"x := []int{1, 2, 3} // <<= >>= ::= =~ !~ ?. ?:",
		"/* <!-- --> <=> <==> ===> |=> ~~> */",
		`fmt.Printf("%d%%\n", n) www 0x1F <> ...`,
		"|| && ++ -- ** // /* */ __ ~- -~ =/= <-> <~>",
		"a=>b !=c ->d <-e ==f ===g <=h >=i <>j ::k",
	}

	var gids []GID
	for _, line := range corpus {
		runes := []rune(line)
		gids, ok := fm.Shaper(Regular).ShapeRow(gids[:0], runes, true)
		if !ok {
			t.Errorf("%q broke the one-glyph-per-cell contract", line)
			continue
		}
		if len(gids) != len(runes) {
			t.Errorf("%q: %d glyphs for %d cells", line, len(gids), len(runes))
			continue
		}
		// A glyph the shaper can emit but the atlas never baked renders as a
		// hole on screen. This is the safety net on the GSUB walk in GlyphSet.
		for i, gid := range gids {
			if _, baked := fm.Atlas.Slot(Key{Regular, gid}); !baked {
				t.Errorf("%q cell %d (%q): glyph %d is not in the atlas", line, i, runes[i], gid)
			}
		}
	}
}

// TestLigatureShape pins the thing that was missing before shaping existed: "=>"
// must come out as a blank cell plus a wide glyph that reaches back into it.
func TestLigatureShape(t *testing.T) {
	fm := newTestManager(t)

	gids, ok := fm.Shaper(Regular).ShapeRow(nil, []rune("=>"), true)
	if !ok || len(gids) != 2 {
		t.Fatalf(`"=>" shaped into %v (ok=%v), want 2 glyphs`, gids, ok)
	}

	plainEq, _ := fm.GlyphIndex(Regular, '=')
	if gids[0] == plainEq {
		t.Errorf(`"=>" was not substituted: first cell is still the plain '=' (glyph %d)`, plainEq)
	}

	if _, inked := slotInk(t, fm.Atlas, Key{Regular, gids[0]}); inked {
		t.Errorf("first cell (glyph %d) carries ink, want a blank spacer", gids[0])
	}
	box, inked := slotInk(t, fm.Atlas, Key{Regular, gids[1]})
	if !inked {
		t.Fatalf("second cell (glyph %d) is blank, the ligature is missing", gids[1])
	}
	// Ink starting left of PadLeft is ink outside the glyph's own cell — the
	// overhang that makes the arrow continuous across the two cells.
	if box.Min.X >= fm.Atlas.PadLeft {
		t.Errorf("ligature ink starts at x=%d, its own cell starts at %d: no overhang",
			box.Min.X, fm.Atlas.PadLeft)
	}
	if box.Dx() <= fm.CellWidth {
		t.Errorf("ligature ink is %d px wide, not wider than the %d px cell", box.Dx(), fm.CellWidth)
	}
}

// TestLigaturesDisabled covers the cursor and selection case: a run that must not
// join up. It is also the cheap path — calt is where essentially all of the
// shaping cost lives.
func TestLigaturesDisabled(t *testing.T) {
	fm := newTestManager(t)

	gids, ok := fm.Shaper(Regular).ShapeRow(nil, []rune("=>"), false)
	if !ok || len(gids) != 2 {
		t.Fatalf(`"=>" shaped into %v (ok=%v), want 2 glyphs`, gids, ok)
	}

	wantEq, _ := fm.GlyphIndex(Regular, '=')
	wantGt, _ := fm.GlyphIndex(Regular, '>')
	if gids[0] != wantEq || gids[1] != wantGt {
		t.Errorf("got glyphs %v, want the plain %v — calt was not disabled", gids, []GID{wantEq, wantGt})
	}
}

// TestShapeRowAppends checks the buffer-reuse contract: ShapeRow appends, so a
// caller can keep one slice for the whole frame.
func TestShapeRowAppends(t *testing.T) {
	fm := newTestManager(t)

	first, ok := fm.Shaper(Regular).ShapeRow(nil, []rune("ab"), true)
	if !ok {
		t.Fatal("shaping \"ab\" failed")
	}
	both, ok := fm.Shaper(Regular).ShapeRow(first, []rune("cd"), true)
	if !ok {
		t.Fatal("shaping \"cd\" failed")
	}
	if len(both) != 4 {
		t.Fatalf("got %d glyphs after two appends, want 4", len(both))
	}

	want, _ := fm.Shaper(Regular).ShapeRow(nil, []rune("abcd"), true)
	for i := range both {
		if both[i] != want[i] {
			t.Errorf("glyph %d: %d, want %d", i, both[i], want[i])
		}
	}
}

// TestRenderRowComposesLigature composes a row out of per-cell quads on the CPU
// and asserts the ligature reads as one connected shape: between the two cells of
// "=>" there is no empty pixel column where the join should be.
func TestRenderRowComposesLigature(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	gids, ok := fm.Shaper(Regular).ShapeRow(nil, []rune("=>"), true)
	if !ok {
		t.Fatal(`shaping "=>" failed`)
	}
	row := a.RenderRow(keysOf(gids), fm.CellWidth)

	box, inked := inkBox(row, row.Bounds())
	if !inked {
		t.Fatal("the composed row is empty")
	}
	if box.Dx() <= fm.CellWidth {
		t.Errorf("composed ink is %d px wide, want more than one %d px cell", box.Dx(), fm.CellWidth)
	}
	for x := box.Min.X; x < box.Max.X; x++ {
		if _, colInked := inkBox(row, image.Rect(x, box.Min.Y, x+1, box.Max.Y)); !colInked {
			t.Errorf("column %d inside the ligature is empty: the two quads do not join", x)
			break
		}
	}

	if testing.Verbose() {
		for y := row.Bounds().Min.Y; y < row.Bounds().Max.Y; y++ {
			var line strings.Builder
			for x := row.Bounds().Min.X; x < row.Bounds().Max.X; x++ {
				switch v := row.AlphaAt(x, y).A; {
				case v > 128:
					line.WriteByte('#')
				case v > 0:
					line.WriteByte('+')
				default:
					line.WriteByte('.')
				}
			}
			t.Log(line.String())
		}
	}
}

// keysOf tags a shaped row with the style it was shaped in.
func keysOf(gids []GID) []Key {
	keys := make([]Key, len(gids))
	for i, gid := range gids {
		keys[i] = Key{Regular, gid}
	}
	return keys
}
