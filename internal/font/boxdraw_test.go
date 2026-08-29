package font

import (
	"image"
	"testing"
)

// boxManager draws the frames itself; testOptions leaves BoxDrawing off, so every other
// test in the package still measures the face's own glyphs.
func boxManager(t *testing.T, size float64) *FontManager {
	t.Helper()
	o := testOptions(t)
	o.BoxDrawing = true
	if size > 0 {
		o.Size = size
	}
	return newManager(t, o)
}

// boxGlyph is the baked glyph's cell, copied out with its origin at (0,0).
func boxGlyph(t *testing.T, fm *FontManager, r rune) *image.Alpha {
	t.Helper()
	cell := boxCell(t, fm, r)
	out := image.NewAlpha(image.Rect(0, 0, cell.Dx(), cell.Dy()))
	for y := range cell.Dy() {
		for x := range cell.Dx() {
			out.SetAlpha(x, y, fm.Atlas.Img.AlphaAt(cell.Min.X+x, cell.Min.Y+y))
		}
	}
	return out
}

func boxCell(t *testing.T, fm *FontManager, r rune) image.Rectangle {
	t.Helper()
	k := fm.Resolve(Regular, 0, r)
	if k.Style != SynthBox {
		t.Fatalf("%q resolved to %s glyph %d, want a synthesized one", r, k.Style, k.GID)
	}
	fm.Atlas.Ensure(k)
	slot, ok := fm.Atlas.Slot(k)
	if !ok {
		t.Fatalf("%q was not baked", r)
	}
	return fm.Atlas.cellBox(slot)
}

// lines slices a glyph into rows or into columns, so a property about a stem can be
// stated once and checked either way.
type lines struct {
	at func(img *image.Alpha, i int) []uint8
	n  func(img *image.Alpha) int
}

var rows = lines{
	at: func(img *image.Alpha, y int) []uint8 {
		out := make([]uint8, img.Rect.Dx())
		for x := range out {
			out[x] = img.AlphaAt(x, y).A
		}
		return out
	},
	n: func(img *image.Alpha) int { return img.Rect.Dy() },
}

var cols = lines{
	at: func(img *image.Alpha, x int) []uint8 {
		out := make([]uint8, img.Rect.Dy())
		for y := range out {
			out[y] = img.AlphaAt(x, y).A
		}
		return out
	},
	n: func(img *image.Alpha) int { return img.Rect.Dx() },
}

// inkRun is the first and last inked index in vals, and whether what lies between is
// unbroken full coverage.
func inkRun(vals []uint8) (lo, hi int, solid, ok bool) {
	lo, hi = -1, -1
	for i, v := range vals {
		if v == 0 {
			continue
		}
		if lo < 0 {
			lo = i
		}
		hi = i + 1
	}
	if lo < 0 {
		return 0, 0, false, false
	}
	solid = true
	for _, v := range vals[lo:hi] {
		if v != 0xFF {
			solid = false
		}
	}
	return lo, hi, solid, true
}

// stemAt is the inked range along line i of r's cell; a negative i counts back from the
// far edge, where the opposite arm leaves.
func stemAt(t *testing.T, fm *FontManager, r rune, ln lines, i int) (lo, hi int) {
	t.Helper()
	img := boxGlyph(t, fm, r)
	if i < 0 {
		i += ln.n(img)
	}
	lo, hi, _, ok := inkRun(ln.at(img, i))
	if !ok {
		t.Fatalf("%q inks nothing along %d", r, i)
	}
	return lo, hi
}

func spansTheCell(img *image.Alpha, ln lines) bool {
	for i := range ln.n(img) {
		vals := ln.at(img, i)
		if lo, hi, solid, ok := inkRun(vals); ok && solid && lo == 0 && hi == len(vals) {
			return true
		}
	}
	return false
}

func allBoxRunes() []rune {
	out := make([]rune, 0, lastBoxRune-firstBoxRune+1)
	for r := rune(firstBoxRune); r <= lastBoxRune; r++ {
		out = append(out, r)
	}
	return out
}

// Ink in the padding would land on the neighbouring column — the overhang that makes the
// face's own glyphs seam.
func TestBoxGlyphsStayInTheCell(t *testing.T) {
	fm := boxManager(t, 0)

	for _, r := range allBoxRunes() {
		k := fm.Resolve(Regular, 0, r)
		fm.Atlas.Ensure(k)
		slot, ok := fm.Atlas.Slot(k)
		if !ok {
			t.Fatalf("%q was not baked", r)
		}
		box, inked := inkBox(fm.Atlas.Img, slot)
		if !inked {
			t.Errorf("%q (%U) drew nothing", r, r)
			continue
		}
		if cell := fm.Atlas.cellBox(slot); !box.In(cell) {
			t.Errorf("%q (%U) inked %v, outside its cell %v", r, r, box, cell)
		}
	}
}

// The point of drawing these ourselves: whole pixels, so neither the coverage bend nor a
// sampler can thicken them. Only arcs, diagonals and shades may hold a value in between.
func TestBoxLinesAreCrisp(t *testing.T) {
	fm := boxManager(t, 0)

	for _, r := range allBoxRunes() {
		switch {
		case r >= firstArcRune && r <= lastDiagRune, r >= 0x2591 && r <= 0x2593:
			continue // curved, slanted, or a shade
		}
		img := boxGlyph(t, fm, r)
		for y := range img.Rect.Dy() {
			for x := range img.Rect.Dx() {
				if a := img.AlphaAt(x, y).A; a != 0 && a != 0xFF {
					t.Fatalf("%q (%U) has coverage %d at (%d,%d), want 0 or 255", r, r, a, x, y)
				}
			}
		}
	}
}

// A line spans its cell exactly, which is what lets a frame run across a row of them
// without a seam at every boundary.
func TestBoxLinesReachTheCellEdge(t *testing.T) {
	fm := boxManager(t, 0)

	for _, r := range []rune{'─', '━', '═'} {
		if !spansTheCell(boxGlyph(t, fm, r), rows) {
			t.Errorf("%q has no row inked solid from edge to edge", r)
		}
	}
	for _, r := range []rune{'│', '┃', '║'} {
		if !spansTheCell(boxGlyph(t, fm, r), cols) {
			t.Errorf("%q has no column inked solid from edge to edge", r)
		}
	}

	// A dashed line is broken on purpose, but a dash still lands on each end: a run of
	// them across a frame must not gap at the cell joins on top of its own gaps.
	for _, tc := range []struct {
		r  rune
		ln lines
	}{{'┄', cols}, {'┅', cols}, {'╌', cols}, {'┆', rows}, {'┊', rows}, {'╎', rows}} {
		img := boxGlyph(t, fm, tc.r)
		for _, i := range []int{0, tc.ln.n(img) - 1} {
			if _, _, _, ok := inkRun(tc.ln.at(img, i)); !ok {
				t.Errorf("%q inks nothing at the %d end of its run", tc.r, i)
			}
		}
	}
}

// Composed the way the renderer will: full coverage across, nothing doubled at a join.
func TestBoxRowHasNoSeams(t *testing.T) {
	fm := boxManager(t, 0)
	a := fm.Atlas

	for _, r := range []rune{'─', '━'} {
		keys := make([]Key, 5)
		for i := range keys {
			keys[i] = fm.Resolve(Regular, 0, r)
			a.Ensure(keys[i])
		}
		row := a.RenderRow(keys, fm.CellWidth)

		// RenderRow offsets each quad by the slot's padding, so cell i starts at
		// i*cellW + PadLeft.
		y, ok := bandRow(a, row, fm.CellWidth)
		if !ok {
			t.Fatalf("%q run: no row is inked across the first cell", r)
		}
		for x := a.PadLeft; x < a.PadLeft+5*fm.CellWidth; x++ {
			if got := row.AlphaAt(x, y).A; got != 0xFF {
				t.Fatalf("%q run: coverage %d at x=%d, want 255 unbroken across the cells", r, got, x)
			}
		}
	}
}

// bandRow is a row the first cell inked solid, which is where the line lies.
func bandRow(a *Atlas, row *image.Alpha, cellW int) (int, bool) {
	for y := range row.Rect.Dy() {
		solid := true
		for x := a.PadLeft; x < a.PadLeft+cellW; x++ {
			if row.AlphaAt(x, y).A != 0xFF {
				solid = false
				break
			}
		}
		if solid {
			return y, true
		}
	}
	return 0, false
}

// A stem sits on the same pixels whatever glyph carries it, or a frame steps sideways
// where a tee meets a line. A heavier one grows around that same middle.
func TestBoxStemsAlign(t *testing.T) {
	fm := boxManager(t, 0)

	stemLo, stemHi := stemAt(t, fm, '│', rows, 0)
	for _, r := range []rune{'├', '┤', '┴', '┼', '╵', '╽'} { // a light arm reaching up
		if lo, hi := stemAt(t, fm, r, rows, 0); lo != stemLo || hi != stemHi {
			t.Errorf("%q stem is columns [%d,%d), want [%d,%d) like │", r, lo, hi, stemLo, stemHi)
		}
	}
	for _, r := range []rune{'├', '┤', '┬', '┼', '╷', '╿'} { // and reaching down
		if lo, hi := stemAt(t, fm, r, rows, -1); lo != stemLo || hi != stemHi {
			t.Errorf("%q stem is columns [%d,%d), want [%d,%d) like │", r, lo, hi, stemLo, stemHi)
		}
	}

	bandLo, bandHi := stemAt(t, fm, '─', cols, 0)
	for _, r := range []rune{'┬', '┴', '┤', '┼', '╴', '╼'} { // a light arm reaching left
		if lo, hi := stemAt(t, fm, r, cols, 0); lo != bandLo || hi != bandHi {
			t.Errorf("%q band is rows [%d,%d), want [%d,%d) like ─", r, lo, hi, bandLo, bandHi)
		}
	}
	for _, r := range []rune{'┬', '┴', '├', '┼', '╶', '╾'} { // and reaching right
		if lo, hi := stemAt(t, fm, r, cols, -1); lo != bandLo || hi != bandHi {
			t.Errorf("%q band is rows [%d,%d), want [%d,%d) like ─", r, lo, hi, bandLo, bandHi)
		}
	}

	for _, tc := range []struct {
		heavy rune
		ln    lines
		lo    int
		hi    int
	}{
		{'┃', rows, stemLo, stemHi},
		{'━', cols, bandLo, bandHi},
	} {
		lo, hi := stemAt(t, fm, tc.heavy, tc.ln, 0)
		if lo > tc.lo || hi < tc.hi {
			t.Errorf("%q spans [%d,%d), want it to contain the light [%d,%d)",
				tc.heavy, lo, hi, tc.lo, tc.hi)
		}
	}
}

// An arc may hold partial coverage, but the run it turns into is a stem like any other
// and the turn still reaches the cell edge.
func TestBoxArcsJoinTheirArms(t *testing.T) {
	fm := boxManager(t, 0)
	stemLo, stemHi := stemAt(t, fm, '│', rows, 0)
	bandLo, bandHi := stemAt(t, fm, '─', cols, 0)

	for _, tc := range []struct {
		r    rune
		tail int // the row its straight run leaves by
		edge int // the column its turn leaves by
	}{
		{'╭', -1, -1},
		{'╮', -1, 0},
		{'╯', 0, 0},
		{'╰', 0, -1},
	} {
		if lo, hi := stemAt(t, fm, tc.r, rows, tc.tail); lo != stemLo || hi != stemHi {
			t.Errorf("%q tail is columns [%d,%d), want [%d,%d) like │", tc.r, lo, hi, stemLo, stemHi)
		}
		// Within a pixel: the arc meets the edge on a tangent, so the sample there is
		// a fraction of one.
		lo, hi := stemAt(t, fm, tc.r, cols, tc.edge)
		if lo < bandLo-1 || hi > bandHi+1 {
			t.Errorf("%q meets the edge at rows [%d,%d), want it on the band [%d,%d)",
				tc.r, lo, hi, bandLo, bandHi)
		}
	}
}

// Light grows with the size, heavy is thicker, and a double is two hairlines with the gap
// that makes it read as one line.
func TestBoxThickness(t *testing.T) {
	for _, tc := range []struct {
		size float64
		thin int
	}{{testSize, 1}, {34, 2}} {
		fm := boxManager(t, tc.size)
		if got := lightThickness(fm.PPEM); got != tc.thin {
			t.Fatalf("at %v pt the light stem is %d px, want %d", tc.size, got, tc.thin)
		}

		lo, hi := stemAt(t, fm, '│', rows, 0)
		if hi-lo != tc.thin {
			t.Errorf("at %v pt │ is %d px, want %d", tc.size, hi-lo, tc.thin)
		}
		if hlo, hhi := stemAt(t, fm, '┃', rows, 0); hhi-hlo <= hi-lo {
			t.Errorf("at %v pt ┃ is %d px and │ is %d; heavy must be thicker",
				tc.size, hhi-hlo, hi-lo)
		}

		vals := rows.at(boxGlyph(t, fm, '║'), 0)
		dlo, dhi, solid, _ := inkRun(vals)
		if solid {
			t.Errorf("at %v pt ║ is a solid %d px band, want two stems with a gap",
				tc.size, dhi-dlo)
		}
		if dhi-dlo != 3*tc.thin {
			t.Errorf("at %v pt ║ spans %d px, want %d", tc.size, dhi-dlo, 3*tc.thin)
		}
	}
}

// Where two doubles cross each is cut between the other's stems; filling that gap would
// turn ╬ into a blob.
func TestBoxDoubleFrames(t *testing.T) {
	fm := boxManager(t, 0)
	thin := lightThickness(fm.PPEM)
	v0, v1, _ := axisStems(0, fm.CellWidth, double, thin)
	h0, h1, _ := axisStems(0, fm.CellHeight, double, thin)

	cross := boxGlyph(t, fm, '╬')
	if got := cross.AlphaAt(v0.hi, h0.hi).A; got != 0 {
		t.Errorf("╬ inks (%d,%d) with %d; the middle of the crossing must stay open",
			v0.hi, h0.hi, got)
	}
	for _, pt := range [][2]int{{v0.lo, h0.lo}, {v1.lo, h1.lo}} {
		if got := cross.AlphaAt(pt[0], pt[1]).A; got != 0xFF {
			t.Errorf("╬ leaves (%d,%d) at %d, want a stem there", pt[0], pt[1], got)
		}
	}

	// ╠ keeps its outer stem whole and breaks the inner one, where the branch turns out.
	tee := boxGlyph(t, fm, '╠')
	if _, _, solid, ok := inkRun(cols.at(tee, v0.lo)); !ok || !solid {
		t.Error("╠ breaks its outer stem; the frame's edge has to run through")
	}
	if _, _, solid, _ := inkRun(cols.at(tee, v1.lo)); solid {
		t.Error("╠ runs its inner stem straight through; the branch has to open it")
	}

	// ╔ turns both: outer stem to outer row, inner to inner.
	corner := boxGlyph(t, fm, '╔')
	for _, pt := range [][2]int{{v0.lo, h0.lo}, {v1.lo, h1.lo}} {
		if got := corner.AlphaAt(pt[0], pt[1]).A; got != 0xFF {
			t.Errorf("╔ leaves (%d,%d) at %d, want the corner closed there", pt[0], pt[1], got)
		}
	}
	if got := corner.AlphaAt(0, 0).A; got != 0 {
		t.Errorf("╔ inks its top-left corner with %d; nothing reaches that way", got)
	}
}

// The eighths grow monotonically and fill the cell at eight; a shade is flat, between
// paper and ink.
func TestBoxBlocks(t *testing.T) {
	fm := boxManager(t, 0)

	area := func(r rune) int {
		img := boxGlyph(t, fm, r)
		n := 0
		for y := range img.Rect.Dy() {
			for x := range img.Rect.Dx() {
				if img.AlphaAt(x, y).A != 0 {
					n++
				}
			}
		}
		return n
	}

	for _, series := range [][]rune{
		{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'},
		{'▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'},
	} {
		last := 0
		for _, r := range series {
			got := area(r)
			if got <= last {
				t.Errorf("%q covers %d px, want more than the step before it (%d)", r, got, last)
			}
			last = got
		}
		if full := fm.CellWidth * fm.CellHeight; last != full {
			t.Errorf("█ covers %d px, want the whole %d px cell", last, full)
		}
	}

	prev := uint8(0)
	for _, r := range []rune{'░', '▒', '▓'} {
		img := boxGlyph(t, fm, r)
		want := img.AlphaAt(0, 0).A
		if want <= prev {
			t.Errorf("%q is coverage %d, want more than the shade before it (%d)", r, want, prev)
		}
		prev = want
		for y := range img.Rect.Dy() {
			for x := range img.Rect.Dx() {
				if got := img.AlphaAt(x, y).A; got != want {
					t.Fatalf("%q is %d at (%d,%d) and %d at the origin; a shade is flat",
						r, got, x, y, want)
				}
			}
		}
	}
	if prev >= 0xFF {
		t.Errorf("▓ is coverage %d; a shade has to stay under solid ink", prev)
	}
}

// The block goes to the synthesized style whatever the face carries, and nothing else.
func TestResolveRoutesBoxRunes(t *testing.T) {
	fm := boxManager(t, 0)

	for _, r := range []rune{'─', '╭', '╬', '█', '░', rune(firstBoxRune), rune(lastBoxRune)} {
		gid, _ := fm.GlyphIndex(Regular, r)
		k := fm.Resolve(Regular, gid, r)
		if k.Style != SynthBox || rune(k.GID) != r {
			t.Errorf("%q (%U) resolved to %s glyph %d, want SynthBox glyph %d", r, r, k.Style, k.GID, r)
		}
		// One slot for all four styles: a frame in a bold run is the same glyph.
		if bold := fm.Resolve(Bold, gid, r); bold != k {
			t.Errorf("%q in bold resolved to %v, want the same key as regular %v", r, bold, k)
		}
	}

	for _, r := range []rune{'a', rune(firstBoxRune - 1), rune(lastBoxRune + 1), '█' + 0x100} {
		gid, _ := fm.GlyphIndex(Regular, r)
		if k := fm.Resolve(Regular, gid, r); k.Style == SynthBox {
			t.Errorf("%q (%U) was drawn as a frame", r, r)
		}
	}

	// With the option off the face's own glyphs come back.
	plain := newManager(t, testOptions(t))
	for _, r := range []rune{'─', '╭', '█'} {
		gid, _ := plain.GlyphIndex(Regular, r)
		if k := plain.Resolve(Regular, gid, r); k.Style == SynthBox {
			t.Errorf("%q was synthesized with BoxDrawing off", r)
		}
	}
}
