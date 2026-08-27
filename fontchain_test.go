package main

import (
	"strings"
	"testing"

	"gty/internal/font"
)

// The chain the config asks for, built without a GPU: newFontManager needs a size and
// a texture ceiling, nothing else. testMaxTexture is the WebGPU baseline.
const testMaxTexture = 8192

func withFamily(t *testing.T, family string) {
	t.Helper()
	was := fontFamily
	fontFamily = family
	t.Cleanup(func() { fontFamily = was })
}

// TestFontChainDefault: no family in the config means the embedded one, and nothing
// else is loaded up front — the machine is behind it, asked only when a rune misses.
func TestFontChainDefault(t *testing.T) {
	withFamily(t, "")

	fm, err := newFontManager(fontSize, 1, testMaxTexture)
	if err != nil {
		t.Fatal(err)
	}
	defer fm.Close()

	if fm.Family != embeddedFamily {
		t.Errorf("primary is %q, want the embedded %q", fm.Family, embeddedFamily)
	}
	if fm.NumFaces() != font.NumStyles {
		t.Errorf("%d faces loaded, want %d — the chain should start empty", fm.NumFaces(), font.NumStyles)
	}
	// The finder is wired: the dingbat is in none of the four faces.
	if key := fm.Resolve(font.Regular, 0, '✔'); key.GID == 0 {
		t.Error("✔ resolves to the box; the system finder is not wired up")
	} else if key.Style < font.Fallback {
		t.Errorf("✔ came from %s, want a fallback face", key.Style)
	}
}

// TestFontChainFromConfig: a family that is installed becomes the primary, and the
// embedded family drops in behind it — which is what keeps the icons working under a
// font that has none of them.
func TestFontChainFromConfig(t *testing.T) {
	// DejaVu Sans Mono ships nearly everywhere and has all four styles. It is also a
	// good probe: no icons at all, so the embedded fallback has to answer for them.
	const family = "DejaVu Sans Mono"
	if _, _, err := (&font.Library{}).Family(family); err != nil {
		t.Skipf("%s not installed: %v", family, err)
	}
	withFamily(t, family)

	fm, err := newFontManager(fontSize, 1, testMaxTexture)
	if err != nil {
		t.Fatal(err)
	}
	defer fm.Close()

	if fm.Family != family {
		t.Fatalf("primary is %q, want %q from the config", fm.Family, family)
	}
	if fm.NumFaces() != font.NumStyles+1 {
		t.Errorf("%d faces loaded, want %d — the embedded family should be first in the chain",
			fm.NumFaces(), font.NumStyles+1)
	}
	if got := fm.FaceName(font.Fallback); got != embeddedFamily {
		t.Errorf("the head of the chain is %q, want %q", got, embeddedFamily)
	}

	// An icon this family has never heard of, drawn from the embedded one — from that
	// face's icon twin, in fact, which is why this looks at the name rather than the
	// index: an icon is drawn at the size that fills the cell's height, from a twin of
	// whichever face turned out to have it.
	icon := fm.Resolve(font.Regular, 0, '\uf015') // Font Awesome's house
	if !strings.HasPrefix(fm.FaceName(icon.Style), embeddedFamily) {
		t.Errorf("U+F015 came from %s (%s), want the embedded family",
			icon.Style, fm.FaceName(icon.Style))
	}
	// And the cell is this family's, not the embedded family's.
	if fm.CellWidth <= 0 || fm.CellHeight <= 0 {
		t.Errorf("cell %dx%d", fm.CellWidth, fm.CellHeight)
	}
	t.Logf("%s at %gpt: cell %dx%d ascent %d", family, fontSize, fm.CellWidth, fm.CellHeight, fm.Ascent)
}

// TestFontChainFallsBackOnABadFamily: a name nothing matches is a warning and the
// embedded family, not a terminal that will not start.
func TestFontChainFallsBackOnABadFamily(t *testing.T) {
	withFamily(t, "No Such Font Family At All")

	fm, err := newFontManager(fontSize, 1, testMaxTexture)
	if err != nil {
		t.Fatal(err)
	}
	defer fm.Close()

	if fm.Family != embeddedFamily {
		t.Errorf("primary is %q, want the embedded %q", fm.Family, embeddedFamily)
	}
}

// TestFontChainIgnoresTheEmbeddedNameCase: naming the embedded family in the config is
// not a reason to go looking for it on disk.
func TestFontChainIgnoresTheEmbeddedNameCase(t *testing.T) {
	withFamily(t, strings.ToLower(embeddedFamily))

	fm, err := newFontManager(fontSize, 1, testMaxTexture)
	if err != nil {
		t.Fatal(err)
	}
	defer fm.Close()

	if fm.Family != embeddedFamily || fm.NumFaces() != font.NumStyles {
		t.Errorf("primary %q with %d faces, want the embedded family with %d",
			fm.Family, fm.NumFaces(), font.NumStyles)
	}
}

// TestFontScaleSizesTheCell: the display's scale reaches the cell through the DPI, so
// the grid on a 2x panel is twice the pixels for the same point size — and the same
// physical size as on the 1x monitor beside it.
func TestFontScaleSizesTheCell(t *testing.T) {
	withFamily(t, "")

	one, err := newFontManager(fontSize, 1, testMaxTexture)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()

	two, err := newFontManager(fontSize, 2, testMaxTexture)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()

	// Not exactly twice: the cell is hinted metrics rounded to whole pixels, and
	// rounding twice is not the same as rounding once.
	if got, want := two.CellWidth, 2*one.CellWidth; got < want-2 || got > want+2 {
		t.Errorf("cell is %d px wide at 2x, want about %d", got, want)
	}
	if got, want := two.CellHeight, 2*one.CellHeight; got < want-2 || got > want+2 {
		t.Errorf("cell is %d px tall at 2x, want about %d", got, want)
	}
}
