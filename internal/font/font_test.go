package font

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	testSize = 14.0
	testDPI  = 72.0

	// testMaxTexture is the WebGPU baseline, which every device meets.
	testMaxTexture = 8192

	wantCellWidth  = 8
	wantCellHeight = 19
	wantAscent     = 15

	// testIcon is carried by both the family and the symbol face, so the same icon can be
	// drawn both ways and compared. testDingbat is in the general face only — a fish prompt
	// draws it for a command that succeeded. testMissing is in none of them.
	testIcon    = 0xF015  // Font Awesome's house
	testDingbat = 0x2714  // ✔, where the family has only the light ✓ U+2713
	testMissing = 0x1F600 // an emoji: no glyph anywhere in this build
)

// testFace names the files the tests load. Spelled out rather than derived from
// Style.String(), and a lighter pair than text.go embeds on purpose: the geometry checks
// have to hold for a family whose weights were not the ones the grid was tuned on.
var testFace = [NumStyles]string{
	Regular:    "../../assets/JetBrainsMonoNerdFontMono-Light.ttf",
	Bold:       "../../assets/JetBrainsMonoNerdFontMono-Regular.ttf",
	Italic:     "../../assets/JetBrainsMonoNerdFontMono-LightItalic.ttf",
	BoldItalic: "../../assets/JetBrainsMonoNerdFontMono-Italic.ttf",
}

// The two faces the tests reach for outside the family: a symbol face with the icons and no
// dingbats, and a general monospace face with the dingbats and no icons. The app embeds
// neither, so here they stand in for whatever a Finder would turn up.
const (
	testSymbolFace  = "../../assets/SymbolsNerdFontMono-Regular.ttf"
	testGeneralFace = "../../assets/DejaVuSansMono.ttf"

	// What source() calls them, which is what FaceName reports.
	symbolFaceName  = "SymbolsNerdFontMono-Regular.ttf"
	generalFaceName = "DejaVuSansMono.ttf"
)

func readFace(t *testing.T, path string) []byte {
	t.Helper()
	ttf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return ttf
}

func source(t *testing.T, path string) Source {
	t.Helper()
	return Source{Name: filepath.Base(path), TTF: readFace(t, path)}
}

// fakeFinder stands in for the system's fonts. It offers its faces for every rune,
// including ones they do not have: the manager is supposed to check that itself.
type fakeFinder struct {
	faces []Source
	calls int
}

func (f *fakeFinder) FindRune(rune) []Source {
	f.calls++
	return f.faces
}

// testOptions is the app's own shape: the embedded family, nothing else.
func testOptions(t *testing.T) Options {
	t.Helper()

	var styles [NumStyles]Source
	for i := range NumStyles {
		styles[i] = source(t, testFace[Style(i)])
	}
	return Options{
		Styles: styles, Family: "Monospace",
		Size: testSize, DPI: testDPI, MaxTexture: testMaxTexture,
		IconFill: DefaultIconFill,
		Warn:     func(msg string) { t.Log("warn:", msg) },
	}
}

func newManager(t *testing.T, o Options) *FontManager {
	t.Helper()
	fm, err := NewManager(o)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { fm.Close() })
	return fm
}

// newTestManager is the default: the family, plus a finder holding the general face,
// which is how the app runs — an empty chain and the machine behind it.
func newTestManager(t *testing.T) *FontManager {
	t.Helper()
	o := testOptions(t)
	o.Finder = &fakeFinder{faces: []Source{source(t, testGeneralFace)}}
	return newManager(t, o)
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

// TestFallbackFacesFitTheGrid: each face in the chain is scaled into the grid by its median
// glyph — fitting the widest instead would draw a proportional face at a quarter size.
func TestFallbackFacesFitTheGrid(t *testing.T) {
	o := testOptions(t)
	o.Fallback = []Source{source(t, testSymbolFace), source(t, testGeneralFace)}
	fm := newManager(t, o)

	if got, want := fm.NumFaces(), NumStyles+len(o.Fallback); got != want {
		t.Fatalf("%d faces loaded, want %d", got, want)
	}
	for face := Fallback; int(face) < fm.NumFaces(); face++ {
		ppem, ascent, ok := fm.FaceMetrics(face)
		if !ok {
			t.Fatalf("%s was not loaded", face)
		}
		if ascent <= 0 || ascent > fm.CellHeight {
			t.Errorf("the %s baseline at y=%d lies outside the %d px cell", face, ascent, fm.CellHeight)
		}

		f := fm.Font(face)
		var buf sfnt.Buffer
		var advances []fixed.Int26_6
		for gid := GID(1); gid < faceGlyphs(f); gid++ {
			if adv, err := f.GlyphAdvance(&buf, gid, ppem, Hinting); err == nil && adv > 0 {
				advances = append(advances, adv)
			}
		}
		slices.Sort(advances)
		if got, want := advances[len(advances)/2], fixed.I(fm.CellWidth); got != want {
			t.Errorf("%s: the median glyph advances %v, want one cell (%v)", face, got, want)
		}
		t.Logf("%s (%s): ppem %v, ascent %d, advances %v..%v",
			face, fm.FaceName(face), ppem, ascent, advances[0], advances[len(advances)-1])
	}
}

// TestResolveWalksTheFallbackChain covers which face a cell is drawn from and in what order
// the question is asked: the icon is in the loaded chain, the dingbat only in the finder's.
func TestResolveWalksTheFallbackChain(t *testing.T) {
	o := testOptions(t)
	o.Fallback = []Source{source(t, testSymbolFace)}
	finder := &fakeFinder{faces: []Source{source(t, testGeneralFace)}}
	o.Finder = finder
	fm := newManager(t, o)

	gid, ok := fm.GlyphIndex(Regular, 'A')
	if !ok {
		t.Fatal("the family has no 'A'")
	}
	if got, want := fm.Resolve(Regular, gid, 'A'), (Key{Regular, gid}); got != want {
		t.Errorf("Resolve(%v) = %v, want the shaper's own glyph untouched", want, got)
	}
	if finder.calls != 0 {
		t.Errorf("the finder was asked %d times for a rune the family has", finder.calls)
	}

	// The name says which link answered; the index no longer does, because an icon comes
	// from a twin appended after the chain.
	icon := fm.Resolve(Italic, 0, testIcon)
	if icon.GID == 0 || !strings.HasPrefix(fm.FaceName(icon.Style), symbolFaceName) {
		t.Errorf("%U resolved to %s (%q) glyph %d, want the symbol face in the loaded chain",
			rune(testIcon), icon.Style, fm.FaceName(icon.Style), icon.GID)
	}
	if finder.calls != 0 {
		t.Errorf("the finder was asked for %U, which the chain already had", rune(testIcon))
	}

	// Past the end of the chain: the finder is asked, and its face joins the chain.
	dingbat := fm.Resolve(Regular, 0, testDingbat)
	if dingbat.GID == 0 || fm.FaceName(dingbat.Style) != generalFaceName {
		t.Errorf("%U resolved to %s (%q) glyph %d, want the face the finder offered",
			rune(testDingbat), dingbat.Style, fm.FaceName(dingbat.Style), dingbat.GID)
	}
	// The family, the symbol face, its icon twin, and the finder's face.
	if fm.NumFaces() != NumStyles+3 {
		t.Errorf("%d faces after the finder answered, want %d", fm.NumFaces(), NumStyles+3)
	}

	// One face for all four styles: an icon in a bold run costs the same slot as in a
	// regular one, and comes out upright in both.
	if bold := fm.Resolve(Bold, 0, testIcon); bold != icon {
		t.Errorf("the chain answered %v for bold and %v for italic; it has no styles", bold, icon)
	}

	if _, in := fm.GlyphIndex(Regular, testMissing); in {
		t.Fatalf("%U is in the family; this probe needs a rune nothing has", rune(testMissing))
	}
	if got := fm.Resolve(Regular, 0, testMissing); got.GID != 0 {
		t.Errorf("%U resolved to %v, want GID 0 — the replacement box", rune(testMissing), got)
	}
}

// TestResolveAsksTheFinderOncePerRune: a search walks the machine's fonts, so hits and
// misses alike are remembered — otherwise an uncovered rune is searched sixty times a
// second.
func TestResolveAsksTheFinderOncePerRune(t *testing.T) {
	o := testOptions(t)
	finder := &fakeFinder{faces: []Source{source(t, testGeneralFace)}}
	o.Finder = finder
	fm := newManager(t, o)

	for range 3 {
		if got := fm.Resolve(Regular, 0, testDingbat); got.GID == 0 {
			t.Fatalf("%U did not resolve", rune(testDingbat))
		}
	}
	if finder.calls != 1 {
		t.Errorf("the finder was asked %d times for one hit, want 1", finder.calls)
	}

	for range 3 {
		if got := fm.Resolve(Regular, 0, testMissing); got.GID != 0 {
			t.Fatalf("%U resolved to %v, want the box", rune(testMissing), got)
		}
	}
	if finder.calls != 2 {
		t.Errorf("the finder was asked %d times in total, want 2 — the miss was searched again",
			finder.calls)
	}
}

// TestResolveSkipsAFaceItCannotDraw: a finder answers with candidates, not an answer. A face
// that will not parse, or lacks the rune after all, is stepped over — in the app that is a
// colour emoji font, which has the rune and no outline.
func TestResolveSkipsAFaceItCannotDraw(t *testing.T) {
	o := testOptions(t)
	o.Finder = &fakeFinder{faces: []Source{
		{Name: "not-a-font", TTF: []byte("this is not a font")},
		{Name: "symbols-no-dingbats", TTF: readFace(t, testSymbolFace)},
		{Name: "general", TTF: readFace(t, testGeneralFace)},
	}}
	fm := newManager(t, o)

	key := fm.Resolve(Regular, 0, testDingbat)
	if key.GID == 0 {
		t.Fatalf("%U fell through to the box; the last candidate has it", rune(testDingbat))
	}
	if got := fm.FaceName(key.Style); got != "general" {
		t.Errorf("%U came from %q, want the general face", rune(testDingbat), got)
	}
	// Only the face that answered joins the chain.
	if fm.NumFaces() != NumStyles+1 {
		t.Errorf("%d faces loaded, want %d — a candidate that could not draw was kept",
			fm.NumFaces(), NumStyles+1)
	}
}

// TestPromptRunesResolve is the regression the chain was added for: the first two lines are
// every non-ASCII rune a real fish prompt draws, including the two dingbats that used to come
// out as hollow boxes. GID 0 here is a box on screen.
func TestPromptRunesResolve(t *testing.T) {
	fm := newTestManager(t)

	for _, r := range []rune{
		'\uf113', '\U000f0320', '\U000f10fe', '\U000f0868', // git branch, and three Material Design icons
		'✔', '✘', // _exit_status
		'✓', '✗', // the light pair, which the family does have
		'\ue0b0', '\ue0b2', '\ue0a0', // powerline separators and the git branch glyph
		'☸',                                              // a kubernetes prompt
		'→', '±', '≈', '…', '·', '│', '─', '█', '░', '●', // odds and ends a TUI draws
		'⣿', // braille, which spinners and graphs are made of
	} {
		gid, _ := fm.GlyphIndex(Regular, r)
		if key := fm.Resolve(Regular, gid, r); key.GID == 0 {
			t.Errorf("%q (%U) resolves to the replacement box: no face in this build draws it", r, r)
		}
	}
}

// TestManagerWithoutFallback: both are optional, and leaving them out costs nothing else.
func TestManagerWithoutFallback(t *testing.T) {
	fm := newManager(t, testOptions(t))

	if fm.NumFaces() != NumStyles {
		t.Errorf("%d faces loaded with no chain, want %d", fm.NumFaces(), NumStyles)
	}
	if fm.Font(Fallback) != nil || fm.Shaper(Fallback) != nil {
		t.Error("a fallback face appeared out of nowhere")
	}
	if _, _, ok := fm.FaceMetrics(Fallback); ok {
		t.Error("FaceMetrics reports a face that was never loaded")
	}
	if got := fm.Resolve(Regular, 0, testIcon); got.GID != 0 {
		t.Errorf("%U resolved to %v with nothing to fall back to, want the box", rune(testIcon), got)
	}
	if fm.CellWidth != wantCellWidth || fm.CellHeight != wantCellHeight || fm.Ascent != wantAscent {
		t.Errorf("cell %dx%d ascent=%d without a fallback, want %dx%d ascent=%d",
			fm.CellWidth, fm.CellHeight, fm.Ascent, wantCellWidth, wantCellHeight, wantAscent)
	}
}

// TestEmbeddedFamilyIsMonospaced: the not-monospaced warning has to stay quiet for the face
// this binary ships with.
func TestEmbeddedFamilyIsMonospaced(t *testing.T) {
	fm := newTestManager(t)

	if n := proportionalRunes(fm.Font(Regular), fm.PPEM, fm.CellWidth); n != 0 {
		t.Errorf("%d of the printable ASCII glyphs do not advance by one %d px cell",
			n, fm.CellWidth)
	}
}

// TestIsIconRune: the boundaries are the whole of it. Get one wrong and either a letter
// is drawn as an icon or a prompt's icons stay half-height.
func TestIsIconRune(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    rune
		want bool
	}{
		{"below the private-use area", 0xDFFF, false},
		{"first of the BMP area", 0xE000, true},
		{"last before powerline", 0xE09F, true},
		{"first powerline", powerlineLo, false},
		{"last powerline", powerlineHi, false},
		{"first after powerline", 0xE0D8, true},
		{"last of the BMP area", 0xF8FF, true},
		{"between the two areas", 0xF900, false},
		{"first of plane 15", 0xF0000, true},
		{"last of plane 15", 0xFFFFD, true},
		{"past plane 15", 0x100000, false},
		{"the heavy check mark", testDingbat, false},
		{"a heart", 0x2665, false},
		{"a letter", 'a', false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isIconRune(tc.r); got != tc.want {
				t.Errorf("isIconRune(%U) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// TestIconFaceIsOnlyTheTwin: the renderer asks by face which glyphs to leave at the
// coverage they were rasterised with, so nothing but the twin may answer yes — a fallback
// draws text and wants the same darkening as the family.
func TestIconFaceIsOnlyTheTwin(t *testing.T) {
	o := testOptions(t)
	o.Fallback = []Source{source(t, testGeneralFace)}
	fm := newManager(t, o)

	gid, ok := fm.GlyphIndex(Regular, testIcon)
	if !ok {
		t.Fatalf("the family has no %U", rune(testIcon))
	}
	twin := fm.Resolve(Regular, gid, testIcon).Style
	if !fm.IconFace(twin) {
		t.Errorf("the twin %s says it is not an icon face", twin)
	}
	// The dingbat pulls the fallback into the chain, so the loop sees a fitted face that
	// is not a twin — the case a check on fitted alone would get wrong.
	if got := fm.Resolve(Regular, 0, testDingbat).Style; got == twin {
		t.Fatalf("%U came from the twin %s", rune(testDingbat), got)
	}
	for s := Style(0); int(s) < fm.NumFaces(); s++ {
		if s != twin && fm.IconFace(s) {
			t.Errorf("%s (%s) says it is an icon face", s, fm.FaceName(s))
		}
	}
	if fm.IconFace(Style(fm.NumFaces())) {
		t.Error("a style past the chain says it is an icon face")
	}
}

// TestIconTwinIsSharedAndStyleless: an icon has no italic, so all four styles share one
// twin, built once.
func TestIconTwinIsSharedAndStyleless(t *testing.T) {
	fm := newManager(t, testOptions(t))

	gid, ok := fm.GlyphIndex(Regular, testIcon)
	if !ok {
		t.Fatalf("the family has no %U", rune(testIcon))
	}
	regular := fm.Resolve(Regular, gid, testIcon)
	if regular.Style < Fallback {
		t.Fatalf("%U came from %s, want a twin appended past the styles", rune(testIcon), regular.Style)
	}
	for _, style := range []Style{Bold, Italic, BoldItalic} {
		if got := fm.Resolve(style, gid, testIcon); got != regular {
			t.Errorf("%s draws %U from %v, want the same %v as Regular", style, rune(testIcon), got, regular)
		}
	}

	// A second icon from the same face reuses the twin rather than adding another.
	before := fm.NumFaces()
	other, ok := fm.GlyphIndex(Regular, 0xF07B)
	if !ok {
		t.Fatal("the family has no U+F07B")
	}
	if got := fm.Resolve(Regular, other, 0xF07B); got.Style != regular.Style {
		t.Errorf("a second icon came from %s, want the same twin %s", got.Style, regular.Style)
	}
	if fm.NumFaces() != before {
		t.Errorf("%d faces after a second icon, want %d", fm.NumFaces(), before)
	}
	if name := fm.FaceName(regular.Style); !strings.HasSuffix(name, "icons") {
		t.Errorf("the twin is called %q, want it named after the face it doubles", name)
	}
}

// TestNonIconRunesAreNotScaled: the standard Unicode symbols the family also carries are read
// as text, so only the private-use areas count as icons.
func TestNonIconRunesAreNotScaled(t *testing.T) {
	o := testOptions(t)
	o.Finder = &fakeFinder{faces: []Source{source(t, testGeneralFace)}}
	fm := newManager(t, o)

	// In the family: the key comes back exactly as the shaper made it.
	for _, r := range []rune{'a', '=', 0x2665, 0x2713, '─', '█'} {
		gid, ok := fm.GlyphIndex(Regular, r)
		if !ok {
			t.Errorf("%q (%U): the family has no glyph", r, r)
			continue
		}
		if got, want := fm.Resolve(Regular, gid, r), (Key{Regular, gid}); got != want {
			t.Errorf("%q (%U) resolved to %v, want %v untouched", r, r, got, want)
		}
	}

	// From a fallback face: still the face itself, not a twin of it.
	dingbat := fm.Resolve(Regular, 0, testDingbat)
	if got := fm.FaceName(dingbat.Style); got != generalFaceName {
		t.Errorf("%U came from %q, want the fallback face %q itself",
			rune(testDingbat), got, generalFaceName)
	}
}
