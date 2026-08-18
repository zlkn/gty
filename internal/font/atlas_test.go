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
