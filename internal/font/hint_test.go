package font

import (
	"math"
	"testing"

	"github.com/golang/freetype/truetype"
	xfont "golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// TestHintingSnapsGlyphEdgesToWholePixels is what grid fitting is for: a horizontal edge
// landing mid-pixel is what spreads a "t" bar over three rows instead of two.
func TestHintingSnapsGlyphEdgesToWholePixels(t *testing.T) {
	f := parseHinted(source(t, testFace[Regular]))
	if f == nil {
		t.Fatal("the regular face has no hinted parse")
	}

	count := func(h xfont.Hinting, ppem fixed.Int26_6) (whole, total int) {
		var gb truetype.GlyphBuf
		for r := rune(FirstASCII); r <= LastASCII; r++ {
			if err := gb.Load(f, ppem, f.Index(r), h); err != nil {
				t.Fatalf("load %q: %v", r, err)
			}
			if gb.Bounds.Min.Y == gb.Bounds.Max.Y {
				continue // the space, which has no edges to snap
			}
			total++
			if gb.Bounds.Min.Y%64 == 0 && gb.Bounds.Max.Y%64 == 0 {
				whole++
			}
		}
		return whole, total
	}

	// Two sizes, because the interpreter is handed a different grid at each and an
	// outline that only fits at one of them has not been fitted at all.
	for _, ppemPx := range []float64{testSize * testDPI / 72, 28.33} {
		ppem := fixed.Int26_6(0.5 + ppemPx*64)
		if whole, total := count(xfont.HintingFull, ppem); whole != total {
			t.Errorf("ppem %.2f hinted: %d of %d glyphs sit between pixels", ppemPx, total-whole, total)
		}
		// The unhinted outline is the control: if it snapped by itself, the test above
		// would pass with the interpreter switched off and prove nothing.
		if whole, total := count(xfont.HintingNone, ppem); whole > total/4 {
			t.Errorf("ppem %.2f unhinted: %d of %d glyphs already sit on whole pixels", ppemPx, whole, total)
		}
	}
}

// TestHintedGlyphsCarryTheSameInk guards the contour walk. Hinting nudges edges; it does not
// redraw the glyph, so a walk that dropped a contour or mistook an off-curve point for an
// on-curve one shows up as ink nowhere near what the same glyph carries unhinted.
func TestHintedGlyphsCarryTheSameInk(t *testing.T) {
	bake := func(hinting bool) *FontManager {
		o := testOptions(t)
		o.Hinting = hinting
		fm, err := NewManager(o)
		if err != nil {
			t.Fatal(err)
		}
		return fm
	}
	off, on := bake(false), bake(true)

	if off.faces[Regular].hinted != nil {
		t.Error("Options.Hinting is off and the face still carries a hinted parse")
	}
	if on.faces[Regular].hinted == nil {
		t.Fatal("Options.Hinting is on and the face carries no hinted parse")
	}

	ink := func(fm *FontManager, r rune) float64 {
		fc := fm.faces[Regular]
		gid, err := fc.font.GlyphIndex(&fc.buf, r)
		if err != nil {
			t.Fatalf("glyph index for %q: %v", r, err)
		}
		slot, ok := fm.Atlas.Slot(Key{Style: Regular, GID: gid})
		if !ok {
			t.Fatalf("%q was not baked", r)
		}
		sum := 0.0
		for y := slot.Min.Y; y < slot.Max.Y; y++ {
			for x := slot.Min.X; x < slot.Max.X; x++ {
				sum += float64(fm.Atlas.Img.Pix[y*fm.Atlas.Img.Stride+x]) / 255
			}
		}
		return sum
	}

	// A fifth: grid fitting moves whole pixels of coverage around on a glyph this small —
	// a full stop is four pixels of ink and snapping it costs one of them.
	const tolerance = 0.25
	for r := rune(FirstASCII); r <= LastASCII; r++ {
		a, b := ink(off, r), ink(on, r)
		if a < 1 && b < 1 {
			continue // the space
		}
		if rel := math.Abs(b-a) / math.Max(a, 1); rel > tolerance {
			t.Errorf("%q carries %.2f px of ink unhinted and %.2f hinted, %.0f%% apart", r, a, b, 100*rel)
		}
	}
}
