package font

import (
	"os"
	"testing"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// JetBrains Mono at the config defaults (config.DefaultConfig: 14pt, 72 DPI)
// with hinted metrics. The wanted values are measured, kept as a regression
// anchor: the atlas layout and the whole grid geometry are sized from them.
const (
	testSize = 14.0
	testDPI  = 72.0

	// testMaxTexture is the WebGPU baseline, which every device meets.
	testMaxTexture = 8192

	wantCellWidth  = 8
	wantCellHeight = 19
	wantAscent     = 15
)

// testFace names the files the tests load, mirroring what text.go embeds. Spelled out
// rather than derived from Style.String(), because the mapping is a choice: this build
// takes Light as its regular weight and Regular as its bold.
var testFace = [NumStyles]string{
	Regular:    "../../assets/JetBrainsMonoNerdFontMono-Light.ttf",
	Bold:       "../../assets/JetBrainsMonoNerdFontMono-Regular.ttf",
	Italic:     "../../assets/JetBrainsMonoNerdFontMono-LightItalic.ttf",
	BoldItalic: "../../assets/JetBrainsMonoNerdFontMono-Italic.ttf",
}

func newTestManager(t *testing.T) *FontManager {
	t.Helper()

	var faces [NumStyles][]byte
	for i := range NumStyles {
		ttf, err := os.ReadFile(testFace[Style(i)])
		if err != nil {
			t.Fatal(err)
		}
		faces[Style(i)] = ttf
	}
	fm, err := NewManager(faces, "Monospace", testSize, testDPI, testMaxTexture)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { fm.Close() })
	return fm
}

func TestNewManagerMetrics(t *testing.T) {
	fm := newTestManager(t)

	if fm.CellWidth != wantCellWidth || fm.CellHeight != wantCellHeight || fm.Ascent != wantAscent {
		t.Errorf("cell %dx%d ascent=%d, want %dx%d ascent=%d",
			fm.CellWidth, fm.CellHeight, fm.Ascent,
			wantCellWidth, wantCellHeight, wantAscent)
	}

	// main.go and Window.onResize divide by these; zero here is a panic there.
	if fm.CellWidth <= 0 || fm.CellHeight <= 0 {
		t.Fatalf("cell metrics must be positive, got %dx%d", fm.CellWidth, fm.CellHeight)
	}
	if fm.Ascent <= 0 || fm.Ascent > fm.CellHeight {
		t.Errorf("baseline at y=%d lies outside the %d px cell", fm.Ascent, fm.CellHeight)
	}
	if fm.Atlas == nil || fm.Shaper(Regular) == nil {
		t.Error("NewManager must bake the atlas and build the shaper eagerly")
	}
}

func TestCellWidthMatchesAdvance(t *testing.T) {
	fm := newTestManager(t)

	var buf sfnt.Buffer
	advance := func(r rune) (fixed.Int26_6, bool) {
		gid, err := fm.Font(Regular).GlyphIndex(&buf, r)
		if err != nil || gid == 0 {
			return 0, false
		}
		adv, err := fm.Font(Regular).GlyphAdvance(&buf, gid, fm.PPEM, Hinting)
		if err != nil {
			return 0, false
		}
		return adv, true
	}

	want, ok := advance('M')
	if !ok {
		t.Fatal("face reports no advance for 'M'")
	}
	if want.Ceil() != fm.CellWidth {
		t.Errorf("CellWidth=%d, but advance('M')=%v (%d px)", fm.CellWidth, want, want.Ceil())
	}

	// A terminal grid is only valid if every baked glyph advances identically.
	for r := rune(FirstASCII); r <= LastASCII; r++ {
		adv, ok := advance(r)
		if !ok {
			t.Errorf("rune %q (%#x): face has no glyph", r, r)
			continue
		}
		if adv != want {
			t.Errorf("rune %q: advance %v, want %v — face is not monospaced at this size", r, adv, want)
		}
	}
}

// TestASCIIInkFitsCell is a tripwire on the plain ASCII range, kept separate
// from the atlas geometry tests: it is the number that changes first when the
// face or the size changes.
func TestASCIIInkFitsCell(t *testing.T) {
	fm := newTestManager(t)

	// Measured on JetBrains Mono Regular @14pt/72dpi: ink never leaves the cell
	// vertically, and only 3 of the 95 glyphs ('%' '&' 'W') bleed one column past
	// the advance width. Ligature glyphs bleed far more, which is why the atlas
	// computes its padding over the whole baked set instead of over this range.
	const (
		maxRightBleed  = 1
		wantBleedRunes = 3
	)

	var buf sfnt.Buffer
	var bleeders []rune
	for r := rune(FirstASCII); r <= LastASCII; r++ {
		gid, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			t.Errorf("rune %q: no glyph", r)
			continue
		}
		bounds, _, err := fm.Font(Regular).GlyphBounds(&buf, gid, fm.PPEM, Hinting)
		if err != nil || bounds.Empty() {
			continue // blank glyph, e.g. space
		}
		left := -bounds.Min.X.Floor()
		right := bounds.Max.X.Ceil() - fm.CellWidth
		top := -(fm.Ascent + bounds.Min.Y.Floor())
		bottom := fm.Ascent + bounds.Max.Y.Ceil() - fm.CellHeight

		if left > 0 || top > 0 || bottom > 0 || right > maxRightBleed {
			t.Errorf("rune %q escapes the cell: L%d R%d T%d B%d (right budget %d)",
				r, left, right, top, bottom, maxRightBleed)
		}
		if right > 0 {
			bleeders = append(bleeders, r)
		}
	}

	t.Logf("%d ASCII glyphs bleed one column past the cell: %q", len(bleeders), bleeders)
	if len(bleeders) != wantBleedRunes {
		t.Errorf("%d ASCII glyphs bleed past the cell, want %d", len(bleeders), wantBleedRunes)
	}
}
