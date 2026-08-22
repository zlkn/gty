package font

import (
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
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

// TestAtlasLazyInkFitsEverySlot walks the whole face through Ensure. This is what the
// padding measurement is for: a glyph baked later cannot resize the grid it lands in,
// so every one of them has to fit the slot chosen at startup.
func TestAtlasLazyInkFitsEverySlot(t *testing.T) {
	fm := newTestManager(t)
	a := fm.Atlas
	slotBox := image.Rect(0, 0, a.SlotW, a.SlotH)

	for gid := range GID(fm.Font(Regular).NumGlyphs()) {
		k := Key{Regular, gid}
		a.Ensure(k)
		slot, in := a.Slot(k)
		if !in {
			continue // a glyph the rasterizer refused; bake reports that by not adding it
		}
		if box, inked := inkBox(a.Img, slot); inked && !box.Sub(slot.Min).In(slotBox) {
			t.Fatalf("glyph %d: ink %v escapes its %v slot", gid, box.Sub(slot.Min), slotBox)
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
