package font

import (
	"image"
	"testing"

	"golang.org/x/image/font/sfnt"
)

// The machine has to have fonts for any of this to mean anything, and a test box might
// not. Everything here skips rather than fails on an empty index.
func testLibrary(t *testing.T) *Library {
	t.Helper()

	lib := &Library{}
	prints, err := lib.index()
	if err != nil {
		t.Skipf("no font index: %v", err)
	}
	if len(prints) == 0 {
		t.Skip("no fonts installed")
	}
	t.Logf("%d faces indexed", len(prints))
	return lib
}

// TestLibraryFamily is the config half: a family name has to come back as four faces
// this package can load, whatever the family calls its files.
func TestLibraryFamily(t *testing.T) {
	lib := testLibrary(t)

	// DejaVu Sans Mono because it ships with practically every distribution and has
	// all four styles. Skipped rather than failed where it does not.
	const family = "DejaVu Sans Mono"
	styles, missing, err := lib.Family(family)
	if err != nil {
		t.Skipf("%s: %v", family, err)
	}
	if len(missing) != 0 {
		t.Errorf("%s reported %v missing; it has all four", family, missing)
	}
	for i := range NumStyles {
		if styles[i].TTF == nil {
			t.Errorf("%s: no %s face", family, Style(i))
			continue
		}
		if _, err := parseFont(styles[i]); err != nil {
			t.Errorf("%s %s: %v", family, Style(i), err)
		}
	}
	// Four distinct faces, or the aspect matching picked the same one more than once.
	for i := range NumStyles {
		for j := i + 1; j < NumStyles; j++ {
			a, b := styles[i], styles[j]
			if a.Index == b.Index && len(a.TTF) == len(b.TTF) && string(a.TTF[:64]) == string(b.TTF[:64]) {
				t.Errorf("%s and %s are the same face", Style(i), Style(j))
			}
		}
	}
}

func TestLibraryFamilyNotInstalled(t *testing.T) {
	lib := testLibrary(t)

	if _, _, err := lib.Family("No Such Font Family At All"); err == nil {
		t.Error("a family nobody has came back without an error")
	}
}

// TestLibraryFindRune is the fallback half: the dingbat the patched family lacks has to
// be somewhere on the machine, and the candidate offered has to really carry it.
func TestLibraryFindRune(t *testing.T) {
	lib := testLibrary(t)

	cands := lib.FindRune(testDingbat)
	if len(cands) == 0 {
		t.Skipf("no installed face has %U", rune(testDingbat))
	}
	for _, c := range cands {
		f, err := parseFont(c)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		var buf sfnt.Buffer
		gid, err := f.GlyphIndex(&buf, testDingbat)
		if err != nil || gid == 0 {
			t.Errorf("%s was offered for %U but has no glyph for it", c.Name, rune(testDingbat))
		}
	}
	t.Logf("%U offered by %d faces, first %q", rune(testDingbat), len(cands), cands[0].Name)
}

// TestSystemChainDrawsWhatTheFamilyLacks is the whole point, end to end: the embedded
// family, the machine behind it, and the runes a prompt draws coming out as glyphs.
func TestSystemChainDrawsWhatTheFamilyLacks(t *testing.T) {
	lib := testLibrary(t)

	o := testOptions(t)
	o.Finder = lib
	fm := newManager(t, o)
	a := fm.Atlas
	slotBox := image.Rect(0, 0, a.SlotW, a.SlotH)

	// ✔ and ✘ are the reason this exists. ⬢ and 中 are runes nothing embedded has
	// either, and no single fallback file would have covered all four.
	for _, r := range []rune{'✔', '✘', '⬢', '中'} {
		gid, _ := fm.GlyphIndex(Regular, r)
		key := fm.Resolve(Regular, gid, r)
		if key.GID == 0 {
			t.Errorf("%q (%U) resolves to the box; %d faces are installed and none was used",
				r, r, len(lib.prints))
			continue
		}
		a.Ensure(key)
		box, inked := slotInk(t, a, key)
		if !inked {
			t.Errorf("%q (%U) from %s baked blank", r, r, fm.FaceName(key.Style))
			continue
		}
		if !box.In(slotBox) {
			t.Errorf("%q (%U) from %s: ink %v escapes its %v slot",
				r, r, fm.FaceName(key.Style), box, slotBox)
		}
		t.Logf("%q (%U) drawn from %s, ink %v", r, r, fm.FaceName(key.Style), box)
	}
}
