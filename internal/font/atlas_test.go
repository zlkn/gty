package font

import (
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/image/font/sfnt"
)

// inkBox returns the bounding box of non-zero coverage inside region, in the
// image's own coordinates. ok is false when nothing was drawn there at all.
func inkBox(img *image.Alpha, region image.Rectangle) (image.Rectangle, bool) {
	region = region.Intersect(img.Bounds())
	minX, minY := region.Max.X, region.Max.Y
	maxX, maxY := region.Min.X-1, region.Min.Y-1

	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if img.AlphaAt(x, y).A == 0 {
				continue
			}
			minX, minY = min(minX, x), min(minY, y)
			maxX, maxY = max(maxX, x), max(maxY, y)
		}
	}
	if maxX < minX {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

// slotInk is the key's ink box in slot coordinates, where (0,0) is the slot's
// top-left and the cell origin sits at (PadLeft, PadTop).
func slotInk(t *testing.T, a *Atlas, k Key) (image.Rectangle, bool) {
	t.Helper()
	slot, ok := a.Slot(k)
	if !ok {
		t.Fatalf("%s glyph %d was not baked", k.Style, k.GID)
	}
	box, inked := inkBox(a.Img, slot)
	return box.Sub(slot.Min), inked
}

func TestAtlasGeometry(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas
	w, h := a.Img.Rect.Dx(), a.Img.Rect.Dy()

	if a.SlotW < fm.CellWidth || a.SlotH < fm.CellHeight {
		t.Errorf("slot %dx%d is smaller than the cell %dx%d",
			a.SlotW, a.SlotH, fm.CellWidth, fm.CellHeight)
	}
	if a.SlotW != fm.CellWidth+a.PadLeft+a.PadRight || a.SlotH != fm.CellHeight+a.PadTop+a.PadBottom {
		t.Errorf("slot %dx%d does not match cell %dx%d plus padding L%d R%d T%d B%d",
			a.SlotW, a.SlotH, fm.CellWidth, fm.CellHeight,
			a.PadLeft, a.PadRight, a.PadTop, a.PadBottom)
	}
	if got := len(a.Glyphs()); a.Cols*a.Rows < got {
		t.Errorf("%d slots for %d glyphs", a.Cols*a.Rows, got)
	}
	if w != a.Cols*a.SlotW || h != a.Rows*a.SlotH {
		t.Errorf("atlas %dx%d, grid says %dx%d", w, h, a.Cols*a.SlotW, a.Rows*a.SlotH)
	}

	// WriteTexture rejects an unaligned BytesPerRow, and Rect must stay the real
	// width or the UVs and the sampler disagree.
	if a.Img.Stride%StrideAlign != 0 || a.Img.Stride < w {
		t.Errorf("stride %d is not a multiple of %d at least as wide as %d", a.Img.Stride, StrideAlign, w)
	}
	if len(a.Img.Pix) != a.Img.Stride*h {
		t.Errorf("len(Pix)=%d, want Stride*H=%d", len(a.Img.Pix), a.Img.Stride*h)
	}

	// Ligatures are drawn in their last cell and reach back over the earlier
	// ones, so the overhang has to be several cells wide.
	if a.PadLeft < fm.CellWidth {
		t.Errorf("PadLeft=%d is under one cell (%d px): ligature overhang would be clipped",
			a.PadLeft, fm.CellWidth)
	}

	t.Logf("%d glyphs, slot %dx%d, atlas %dx%d, stride %d, %d KiB, padding L%d R%d T%d B%d",
		len(a.Glyphs()), a.SlotW, a.SlotH, w, h, a.Img.Stride,
		len(a.Img.Pix)/1024, a.PadLeft, a.PadRight, a.PadTop, a.PadBottom)
}

// TestAtlasInkFitsSlot is the main geometry test: it catches an off-by-one in the
// slot origin and a forgotten +Ascent alike.
func TestAtlasInkFitsSlot(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas
	slotBox := image.Rect(0, 0, a.SlotW, a.SlotH)

	inked := 0
	for _, gid := range a.Glyphs() {
		box, ok := slotInk(t, a, gid)
		if !ok {
			continue // blank glyphs are legal here: space, ligature spacers
		}
		inked++
		if !box.In(slotBox) {
			t.Errorf("glyph %d: ink %v escapes its %v slot", gid, box, slotBox)
		}
	}
	if inked*2 <= len(a.Glyphs()) {
		t.Errorf("only %d of %d slots carry ink", inked, len(a.Glyphs()))
	}
}

func TestEveryASCIIGlyphIsBaked(t *testing.T) {
	fm := newTestManager(t)

	for r := rune(FirstASCII); r <= LastASCII; r++ {
		gid, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			t.Errorf("rune %q: no glyph", r)
			continue
		}
		box, inked := slotInk(t, fm.Atlas, Key{Regular, gid})
		if r == ' ' {
			if inked {
				t.Errorf("the space carries ink at %v", box)
			}
			continue
		}
		if !inked {
			t.Errorf("rune %q (glyph %d) baked blank", r, gid)
		}
	}
}

// TestSharedBaseline uses flat-bottomed glyphs only: round ones (o, u, e)
// overshoot the baseline by a pixel by design, and including them turns this
// into a false alarm.
func TestSharedBaseline(t *testing.T) {
	fm := newTestManager(t)
	want := fm.Atlas.PadTop + fm.Ascent // exclusive bottom of the ink box

	for _, r := range "HELTIZnxm" {
		gid, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			t.Errorf("rune %q: no glyph", r)
			continue
		}
		box, inked := slotInk(t, fm.Atlas, Key{Regular, gid})
		if !inked {
			t.Errorf("rune %q baked blank", r)
			continue
		}
		if box.Max.Y != want {
			t.Errorf("rune %q sits on baseline %d, want %d", r, box.Max.Y, want)
		}
	}
}

// TestDumpPNG covers the debug dump: an *image.Alpha encoded directly would come
// out invisible, so the dump has to go through the image.Gray alias.
func TestDumpPNG(t *testing.T) {
	fm := newTestManager(t)
	path := filepath.Join(t.TempDir(), "atlas.png")

	if err := fm.Atlas.DumpPNG(path); err != nil {
		t.Fatalf("DumpPNG: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open dump: %v", err)
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode dump: %v", err)
	}
	if format != "png" {
		t.Errorf("dump is %q, want png", format)
	}
	if got := img.Bounds(); got != fm.Atlas.Img.Rect {
		t.Errorf("dump bounds %v, want %v", got, fm.Atlas.Img.Rect)
	}

	gray, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("dump decoded as %T, want *image.Gray", img)
	}
	for _, v := range gray.Pix {
		if v != 0 {
			return
		}
	}
	t.Error("the dumped image is entirely black: glyphs would be invisible")
}

func TestGlyphUVRoundTrip(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas
	w, h := float32(a.Img.Rect.Dx()), float32(a.Img.Rect.Dy())

	for _, k := range a.Glyphs() {
		u, v, ok := a.GlyphUV(k)
		if !ok {
			t.Errorf("%s glyph %d: GlyphUV reports a miss for a baked glyph", k.Style, k.GID)
			continue
		}
		slot, _ := a.Slot(k)
		if got := image.Pt(int(u*w+0.5), int(v*h+0.5)); got != slot.Min {
			t.Errorf("%s glyph %d: UV %v,%v -> %v, want slot origin %v", k.Style, k.GID, u, v, got, slot.Min)
		}
	}

	// Not simply NumGlyphs-1: the last glyph in this face is a ligature spacer,
	// which is very much baked.
	unbaked := GID(fm.Font(Regular).NumGlyphs()) // out of range, always a miss
	for gid := range GID(fm.Font(Regular).NumGlyphs()) {
		if _, in := a.Slot(Key{Regular, gid}); !in {
			unbaked = gid
			break
		}
	}
	if _, _, ok := a.GlyphUV(Key{Regular, unbaked}); ok {
		t.Errorf("GlyphUV(%d) reports a hit for an unbaked glyph", unbaked)
	}
}

// TestAtlasBakesOnDemand: the sheet holds printable ASCII and the GSUB outputs after
// NewManager, and grows into the rest of the face as glyphs are asked for. Everything
// outside that eager set used to render as nothing at all — Cyrillic included.
func TestAtlasBakesOnDemand(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	gid, ok := fm.GlyphIndex(Regular, 'ж')
	if !ok {
		t.Fatal("the face has no Cyrillic; pick another probe")
	}
	k := Key{Regular, gid}

	if _, in := a.Slot(k); in {
		t.Fatal("Cyrillic was baked eagerly; this test proves nothing")
	}
	before := len(a.Glyphs())

	u, v := a.Ensure(k)
	if _, in := a.Slot(k); !in {
		t.Fatal("Ensure did not bake the glyph")
	}
	if len(a.Glyphs()) != before+1 {
		t.Errorf("the sheet went from %d glyphs to %d, want one more", before, len(a.Glyphs()))
	}

	slot, _ := a.Slot(k)
	if box, inked := inkBox(a.Img, slot); !inked {
		t.Error("the glyph baked blank")
	} else if !box.Sub(slot.Min).In(image.Rect(0, 0, a.SlotW, a.SlotH)) {
		t.Errorf("ink %v escapes the slot: the padding was measured over too small a set", box)
	}

	// Asking twice must not bake twice.
	u2, v2 := a.Ensure(k)
	if u != u2 || v != v2 || len(a.Glyphs()) != before+1 {
		t.Error("a second Ensure re-baked the glyph")
	}
}

// TestAtlasNotdefBox: a rune the face cannot draw gets a visible box. Drawing nothing
// makes a missing character indistinguishable from a space, which is how a prompt full
// of Nerd Font icons came to look merely empty.
func TestAtlasNotdefBox(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	u, v := a.Ensure(Key{Regular, 0})
	slot := image.Rect(0, 0, a.SlotW, a.SlotH).
		Add(image.Pt(int(u*float32(a.Img.Rect.Dx())+0.5), int(v*float32(a.Img.Rect.Dy())+0.5)))

	box, inked := inkBox(a.Img, slot)
	if !inked {
		t.Fatal("the replacement slot is blank")
	}
	box = box.Sub(slot.Min)
	if box.Dx() < fm.CellWidth/2 || box.Dy() < fm.CellHeight/2 {
		t.Errorf("the replacement box is %v, want something the size of a cell", box)
	}

	// Hollow, so a run of missing glyphs does not read as a solid bar.
	mid := image.Pt(slot.Min.X+a.PadLeft+fm.CellWidth/2, slot.Min.Y+a.PadTop+fm.CellHeight/2)
	if a.Img.AlphaAt(mid.X, mid.Y).A != 0 {
		t.Error("the replacement box is filled, want it hollow")
	}
}

// TestAtlasLazyInkFitsEverySlot walks whole faces through Ensure: a glyph baked later cannot
// resize the grid, so every one has to fit the slot chosen at startup. The chain is in here
// because each of its faces has a baseline of its own.
func TestAtlasLazyInkFitsEverySlot(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas
	slotBox := image.Rect(0, 0, a.SlotW, a.SlotH)

	// An icon first, so the walk covers its twin — the one face allowed outside its cell.
	if gid, ok := fm.GlyphIndex(Regular, testIcon); ok {
		fm.Resolve(Regular, gid, testIcon)
	}
	faces := []Style{Regular}
	for face := Fallback; int(face) < fm.NumFaces(); face++ {
		faces = append(faces, face)
	}
	for _, style := range faces {
		for gid := range GID(fm.Font(style).NumGlyphs()) {
			k := Key{style, gid}
			a.Ensure(k)
			slot, in := a.Slot(k)
			if !in {
				continue // a glyph the rasterizer refused; bake reports that by not adding it
			}
			if box, inked := inkBox(a.Img, slot); inked && !box.Sub(slot.Min).In(slotBox) {
				t.Fatalf("%s glyph %d: ink %v escapes its %v slot", style, gid, box.Sub(slot.Min), slotBox)
			}
		}
	}
}

// TestAtlasTracksDirtySlots: the renderer copies up only what changed, so the sheet has
// to say what that was.
func TestAtlasTracksDirtySlots(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	if n := len(a.TakeDirty()); n == 0 {
		t.Fatal("nothing was reported dirty after the eager bake")
	}
	if n := len(a.TakeDirty()); n != 0 {
		t.Errorf("%d slots still dirty after taking them", n)
	}

	gid, _ := fm.GlyphIndex(Regular, 'д')
	a.Ensure(Key{Regular, gid})
	if got := a.TakeDirty(); len(got) != 1 {
		t.Errorf("one new glyph reported %d dirty slots, want 1", len(got))
	}
}

// TestAtlasGrowsRows: the sheet is laid out for the glyphs a first frame needs, not for
// the whole face — a Nerd Font-patched file has 12,608 glyphs a style, and a slot
// apiece would be wider than any GPU will allocate. It gains rows instead, and nothing
// already on it may move when it does.
func TestAtlasGrowsRows(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	// A glyph baked before the sheet grows, to check it afterwards.
	probe := Key{Regular, mustGlyph(t, fm, 'ж')}
	a.Ensure(probe)
	slotBefore, _ := a.Slot(probe)
	inkBefore, _ := inkBox(a.Img, slotBefore)

	startRows, startCols := a.Rows, a.Cols
	free := a.Cols*a.Rows - len(a.Glyphs())
	for gid := GID(1); gid < faceGlyphs(fm.Font(Regular)) && a.Rows == startRows; gid++ {
		a.Ensure(Key{Regular, gid})
	}
	if a.Rows == startRows {
		t.Fatalf("the face ran out before the sheet did: %d slots free of %d", free, a.Cols*a.Rows)
	}

	if a.Cols != startCols {
		t.Errorf("growing changed the column count from %d to %d; every slot would move", startCols, a.Cols)
	}
	if got, want := a.Img.Rect.Dy(), a.Rows*a.SlotH; got != want {
		t.Errorf("the image is %d px tall for %d rows, want %d", got, a.Rows, want)
	}
	if a.Rows > a.maxRows {
		t.Errorf("grew to %d rows, past the %d the texture limit allows", a.Rows, a.maxRows)
	}

	slotAfter, _ := a.Slot(probe)
	if slotAfter != slotBefore {
		t.Fatalf("the probe moved from %v to %v", slotBefore, slotAfter)
	}
	if inkAfter, _ := inkBox(a.Img, slotAfter); inkAfter != inkBefore {
		t.Errorf("the probe's ink is %v after growing, was %v", inkAfter, inkBefore)
	}
}

// TestAtlasStopsAtTheTextureLimit: when there is genuinely no room left, a glyph falls
// back to the replacement box rather than scribbling outside the sheet.
func TestAtlasStopsAtTheTextureLimit(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	a.maxRows = a.Rows // pin it shut
	a.free = a.Cols * a.Rows

	before := len(a.Glyphs())
	u, v := a.Ensure(Key{Regular, mustGlyph(t, fm, 'ю')})
	nu, nv := a.Ensure(Key{Regular, 0})
	if u != nu || v != nv {
		t.Errorf("a glyph past the limit drew from %v,%v, want the replacement box at %v,%v", u, v, nu, nv)
	}
	if len(a.Glyphs()) != before {
		t.Error("a glyph was recorded as baked with nowhere to put it")
	}
}

func mustGlyph(t *testing.T, fm *FontManager, r rune) GID {
	t.Helper()
	gid, ok := fm.GlyphIndex(Regular, r)
	if !ok {
		t.Fatalf("the face has no glyph for %q", r)
	}
	return gid
}

// TestFallbackMatchesTheFamily is what the fitted ppem and baseline are for: the family and
// the symbol face carry the same glyphs, so the same rune drawn both ways has to land in the
// same place. Both sides go through Resolve, so both get the icon policy.
//
// Powerline is left out: the patcher stretches those to the full cell, the standalone symbol
// face does not.
func TestFallbackMatchesTheFamily(t *testing.T) {
	o := testOptions(t)
	o.Fallback = []Source{source(t, testSymbolFace)}
	fm := newManager(t, o)
	a := fm.Atlas

	// Two pixels: each face is fitted by its own median, so one icon can land a pixel either
	// side of where the other face puts it.
	const tolerance = 2

	// One rune per icon set the fallback is likely to be asked for: Font Awesome,
	// Octicons, Devicons, Codicons, Material Design, and a plain Unicode symbol.
	for _, r := range []rune{0xF015, 0xF07B, 0xF00C, 0xF0F3, 0xE62B, 0xE712, 0xE20F, 0xF1D0, 0xF09B, 0x2665} {
		fam, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			t.Errorf("%U: the family has no glyph, so there is nothing to compare against", r)
			continue
		}
		famKey := fm.Resolve(Regular, fam, r)
		fb := fm.Resolve(Regular, 0, r)
		if fb == famKey || fb.GID == 0 {
			t.Errorf("%U: the fallback has no glyph of its own for it", r)
			continue
		}
		a.Ensure(famKey)
		a.Ensure(fb)

		want, inked := slotInk(t, a, famKey)
		if !inked {
			t.Errorf("%U: the family baked it blank; pick another probe", r)
			continue
		}
		got, inked := slotInk(t, a, fb)
		if !inked {
			t.Errorf("%U: the fallback baked blank", r)
			continue
		}
		if !within(got, want, tolerance) {
			t.Errorf("%U: fallback ink %v against the family's %v, off by more than %d px",
				r, got, want, tolerance)
		}
	}
}

// within reports whether every edge of got sits inside tol px of want's.
func within(got, want image.Rectangle, tol int) bool {
	near := func(a, b int) bool { return a-b <= tol && b-a <= tol }
	return near(got.Min.X, want.Min.X) && near(got.Min.Y, want.Min.Y) &&
		near(got.Max.X, want.Max.X) && near(got.Max.Y, want.Max.Y)
}

// TestFallbackDingbatSitsInItsCell: the second link is a text face, not an icon face,
// and it is fitted by the same rule. Its glyphs have to land inside the cell they are
// drawn in — a dingbat that overhangs would paint over the character next door, and
// one drawn at the wrong scale is the bug this face was added to fix.
func TestFallbackDingbatSitsInItsCell(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas

	key := fm.Resolve(Regular, 0, testDingbat)
	if key.Style < Fallback {
		t.Fatalf("%U came from %s, want a face from the chain", rune(testDingbat), key.Style)
	}
	a.Ensure(key)

	box, inked := slotInk(t, a, key)
	if !inked {
		t.Fatalf("%U baked blank", rune(testDingbat))
	}
	// The cell inside the slot, widened by the bleed the family itself is allowed.
	cell := image.Rect(a.PadLeft, a.PadTop, a.PadLeft+fm.CellWidth+a.PadRight, a.PadTop+fm.CellHeight)
	if !box.In(cell) {
		t.Errorf("%U: ink %v leaves the cell %v", rune(testDingbat), box, cell)
	}
	// Against the family's own light ✓, which is the same mark at the same size: a
	// face scaled by the wrong em fits the cell just as well and reads as nothing.
	light, ok := fm.GlyphIndex(Regular, 0x2713)
	if !ok {
		t.Fatal("the family has no ✓ U+2713 to compare against")
	}
	a.Ensure(Key{Regular, light})
	want, inked := slotInk(t, a, Key{Regular, light})
	if !inked {
		t.Fatal("the family baked ✓ blank")
	}
	if !within(box, want, 2) {
		t.Errorf("%U from the fallback is %v against the family's ✓ at %v", rune(testDingbat), box, want)
	}
	t.Logf("%U ink %v, the family's ✓ %v, cell %v", rune(testDingbat), box, want, cell)
}

// TestFitShrinksAWideGlyph: a face fitted by its median glyph still has outliers too big for
// the slot, and those are shrunk as they are baked. The probe is the family loaded as a
// fallback — its ligatures reach four cells, the same problem a system face brings.
func TestFitShrinksAWideGlyph(t *testing.T) {
	o := testOptions(t)
	o.Fallback = []Source{source(t, testFace[Regular])}
	fm := newManager(t, o)
	a := fm.Atlas

	f := fm.Font(Fallback)
	ppem, _, _ := fm.FaceMetrics(Fallback)
	var buf sfnt.Buffer
	var wide GID
	var wideW int
	for gid := GID(1); gid < faceGlyphs(f); gid++ {
		b, _, err := f.GlyphBounds(&buf, gid, ppem, Hinting)
		if err != nil {
			continue
		}
		if w := (b.Max.X - b.Min.X).Ceil(); w > 2*fm.CellWidth {
			wide, wideW = gid, w
			break
		}
	}
	if wide == 0 {
		t.Skip("no glyph in the face is wider than two cells")
	}

	k := Key{Fallback, wide}
	a.Ensure(k)
	box, inked := slotInk(t, a, k)
	if !inked {
		t.Fatalf("glyph %d baked blank", wide)
	}

	// The slot without the ligature reach-back, which a fallback has no business using.
	fit := image.Rect(a.PadLeft, 0, a.SlotW, a.SlotH)
	if !box.In(fit) {
		t.Errorf("glyph %d: ink %v is outside %v", wide, box, fit)
	}
	if box.Dx() >= wideW {
		t.Errorf("glyph %d: ink is %d px wide, was %d px unfitted — it was not shrunk",
			wide, box.Dx(), wideW)
	}
	t.Logf("glyph %d: %d px wide unfitted, %v baked, cell %d px", wide, wideW, box, fm.CellWidth)
}

// TestIconFillsTheCellHeight is the point of the icon twin: the Mono variant fits icons to
// the cell's width, so they come out half the line's height. The twin fills the height
// instead, which makes them wider than a cell — into the room the atlas reserved.
func TestIconFillsTheCellHeight(t *testing.T) {
	fm := newManager(t, testOptions(t))
	a := fm.Atlas

	// The box a twin is allowed, in slot coordinates.
	fit := image.Rect(0, 0, a.SlotW, a.SlotH)
	cell := image.Rect(a.PadLeft, a.PadTop, a.PadLeft+fm.CellWidth, a.PadTop+fm.CellHeight)

	var heights []int
	for _, r := range iconSample {
		gid, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			continue
		}
		plain, icon := Key{Regular, gid}, fm.Resolve(Regular, gid, r)
		if icon.Style < Fallback {
			t.Fatalf("%U was not routed to a twin; it came from %s", r, icon.Style)
		}
		a.Ensure(plain)
		a.Ensure(icon)

		was, inked := slotInk(t, a, plain)
		if !inked {
			continue
		}
		got, inked := slotInk(t, a, icon)
		if !inked {
			t.Errorf("%U baked blank from the twin", r)
			continue
		}
		if got.Dy() <= was.Dy() {
			t.Errorf("%U is %d px tall from the twin against %d untwinned; it did not grow",
				r, got.Dy(), was.Dy())
		}
		if !got.In(fit) {
			t.Errorf("%U: ink %v leaves its %v slot", r, got, fit)
		}
		// Centred on the cell: a wide icon overhangs both sides, not one.
		if off := (got.Min.X + got.Max.X) - (cell.Min.X + cell.Max.X); off < -2 || off > 2 {
			t.Errorf("%U: ink %v is off the cell's centre %v by %d px", r, got, cell, off)
		}
		heights = append(heights, got.Dy())
	}
	if len(heights) == 0 {
		t.Fatal("no icons in the sample were drawn")
	}

	// Fitted by the median, so that is what lands on the target; the rest spread around it.
	slices.Sort(heights)
	median, want := heights[len(heights)/2], DefaultIconFill*float64(fm.CellHeight)
	if float64(median) < want-2 || float64(median) > want+2 {
		t.Errorf("the median icon is %d px tall in a %d px cell, want about %.1f",
			median, fm.CellHeight, want)
	}
	t.Logf("%d icons, heights %v, cell %dx%d", len(heights), heights, fm.CellWidth, fm.CellHeight)
}

// TestPowerlineIsNotScaled: the patcher already stretches the separators to the full cell so
// arrows tile with no seam, so they are excluded from the icon range.
func TestPowerlineIsNotScaled(t *testing.T) {
	fm := newManager(t, testOptions(t))
	a := fm.Atlas

	for _, r := range []rune{0xE0A0, 0xE0B0, 0xE0B2} {
		gid, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			t.Errorf("%U: the family has no glyph", r)
			continue
		}
		key := fm.Resolve(Regular, gid, r)
		if key != (Key{Regular, gid}) {
			t.Errorf("%U resolved to %v, want the family's own glyph %v untouched",
				r, key, Key{Regular, gid})
			continue
		}
		a.Ensure(key)
		box, inked := slotInk(t, a, key)
		if !inked {
			t.Errorf("%U baked blank", r)
			continue
		}
		// Already the full height of the cell, which is why it needs no help.
		if box.Dy() < fm.CellHeight {
			t.Errorf("%U is %d px tall in a %d px cell; the patched face used to fill it",
				r, box.Dy(), fm.CellHeight)
		}
	}
}

// TestPowerlineWouldMoveIfScaled proves the exclusion earns its place: forced through the
// twin, the separator does move.
func TestPowerlineWouldMoveIfScaled(t *testing.T) {
	fm := newManager(t, testOptions(t))
	a := fm.Atlas

	gid, ok := fm.GlyphIndex(Regular, 0xE0B0)
	if !ok {
		t.Skip("the family has no powerline separator")
	}
	twin, ok := fm.iconTwin(Regular)
	if !ok {
		t.Fatal("the family got no icon twin to compare against")
	}
	plain, forced := Key{Regular, gid}, Key{twin, gid}
	a.Ensure(plain)
	a.Ensure(forced)

	was, _ := slotInk(t, a, plain)
	got, _ := slotInk(t, a, forced)
	if got == was {
		t.Errorf("the separator is %v either way; the exclusion is not doing anything", was)
	}
	t.Logf("U+E0B0 as drawn %v, scaled as an icon it would be %v (cell %dx%d)",
		was, got, fm.CellWidth, fm.CellHeight)
}

// TestIconScaleOff: the knob's other end gives back the old rendering, slot included.
func TestIconScaleOff(t *testing.T) {
	on := newManager(t, testOptions(t))

	o := testOptions(t)
	o.IconFill = 0
	off := newManager(t, o)

	if off.NumFaces() != NumStyles {
		t.Errorf("%d faces with icon scaling off, want %d — a twin was built anyway",
			off.NumFaces(), NumStyles)
	}
	gid, ok := off.GlyphIndex(Regular, testIcon)
	if !ok {
		t.Fatalf("the family has no %U", rune(testIcon))
	}
	if got, want := off.Resolve(Regular, gid, testIcon), (Key{Regular, gid}); got != want {
		t.Errorf("%U resolved to %v, want the family's own glyph %v", rune(testIcon), got, want)
	}

	// And the sheet is the one the family alone asks for: no room reserved for an
	// overhang that cannot happen.
	if off.Atlas.PadRight > on.Atlas.PadRight {
		t.Errorf("PadRight is %d with scaling off against %d with it on", off.Atlas.PadRight, on.Atlas.PadRight)
	}
	t.Logf("off: slot %dx%d padding R%d | on: slot %dx%d padding R%d",
		off.Atlas.SlotW, off.Atlas.SlotH, off.Atlas.PadRight,
		on.Atlas.SlotW, on.Atlas.SlotH, on.Atlas.PadRight)
}
