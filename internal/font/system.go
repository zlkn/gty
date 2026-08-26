package font

// The system's fonts, for the two things a terminal cannot carry in its binary: the
// family the config asked for, and a face for a rune nothing loaded has.
//
// This goes through go-text's fontscan rather than fontconfig itself. It is already a
// dependency — the shaper is its harfbuzz — and it reads fontconfig's own directory
// list, so it looks in the same places fc-match would. What it gives back is a
// footprint per face: the family, the aspect (bold, italic), the file, and the set of
// runes the face covers, which is exactly the index a fallback search needs.
//
// The index is built on first use and cached on disk by fontscan, because building it
// means parsing every font on the machine: 2946 faces cost 712 ms here, and 0.2 ms
// once cached. Nothing touches it until the config names a family or a rune goes
// missing, so a session that needs neither never pays for it.

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	gotext "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/fontscan"
)

// Library is the machine's installed fonts, indexed once and searched by family or by
// rune. The zero value is ready to use; the index is built on the first call.
//
// Not safe for concurrent use.
type Library struct {
	// Warn is told, once, if the index could not be built — the one thing about the
	// machine's fonts a caller can act on. nil is silence.
	Warn func(string)

	once   sync.Once
	prints []fontscan.Footprint
	err    error
	told   bool // Warn has had the index error

	// Files already read, so a family whose four styles live in one collection, or
	// a face that answers for a second rune, is not read from disk twice.
	loaded map[string][]byte
}

// index builds the footprint list, once, whatever happens.
func (l *Library) index() ([]fontscan.Footprint, error) {
	l.once.Do(func() {
		// The empty cache dir means fontscan's own default under the user's cache.
		l.prints, l.err = fontscan.SystemFonts(silent{}, "")
		l.loaded = make(map[string][]byte)
	})
	if l.err != nil && !l.told {
		l.told = true
		if l.Warn != nil {
			l.Warn(fmt.Sprintf("cannot index the installed fonts: %v", l.err))
		}
	}
	return l.prints, l.err
}

// silent swallows fontscan's own commentary, which is about the state of the machine's
// fontconfig files — unresolvable includes, a config path that is a directory. A
// terminal writing that to its own stderr on the first frame that wants a dingbat is
// noise the user cannot act on; the one thing they can act on is an index that would
// not build, and that comes back as an error.
type silent struct{}

func (silent) Printf(string, ...any) {}

// Family is the four styles of the named family, as NewManager wants them. Every
// style is filled: a family with no italic of its own gets its regular there, because
// one grid serves all four and a substitute from somewhere else would not fit it.
//
// missing names the styles that were substituted, for the caller to report.
func (l *Library) Family(name string) (styles [NumStyles]Source, missing []Style, err error) {
	prints, err := l.index()
	if err != nil {
		return styles, nil, err
	}

	want := gotext.NormalizeFamily(name)
	var found []fontscan.Footprint
	for _, fp := range prints {
		if fp.Family == want {
			found = append(found, fp)
		}
	}
	if len(found) == 0 {
		return styles, nil, fmt.Errorf("no font family %q installed", name)
	}

	// Regular first, so it is what a missing style falls back to.
	for _, style := range []Style{Regular, Bold, Italic, BoldItalic} {
		fp, ok := pickAspect(found, style)
		if !ok {
			missing = append(missing, style)
			styles[style] = styles[Regular]
			continue
		}
		ttf, err := l.read(fp)
		if err != nil {
			return styles, nil, fmt.Errorf("%s %s: %w", name, style, err)
		}
		styles[style] = Source{Name: name, TTF: ttf, Index: fp.Location.Index}
	}
	if styles[Regular].TTF == nil {
		return styles, nil, fmt.Errorf("family %q has no regular face", name)
	}
	return styles, missing, nil
}

// FindRune is the Finder half: faces that carry r, best first.
//
// Best is a monospaced-looking face before a proportional one and a smaller coverage
// before a larger one. Both are about looking right in a grid: a face whose glyphs are
// all one width needs no shrinking to fit a cell, and among the rest the specialised
// symbol face is a better answer than the 40,000-glyph pan-Unicode one, which tends to
// draw a symbol as an afterthought.
//
// The list is capped: the caller only walks it until a face rasterises, and reading
// every font on the machine that happens to have some rune is not worth it.
func (l *Library) FindRune(r rune) []Source {
	prints, err := l.index()
	if err != nil {
		return nil
	}

	var cands []fontscan.Footprint
	for _, fp := range prints {
		if fp.Runes.Contains(r) {
			cands = append(cands, fp)
		}
	}
	slices.SortStableFunc(cands, func(a, b fontscan.Footprint) int {
		if c := cmp.Compare(monoRank(a.Family), monoRank(b.Family)); c != 0 {
			return c
		}
		return cmp.Compare(a.Runes.Len(), b.Runes.Len())
	})

	const maxCandidates = 4
	var out []Source
	for _, fp := range cands {
		if len(out) == maxCandidates {
			break
		}
		ttf, err := l.read(fp)
		if err != nil {
			continue
		}
		out = append(out, Source{Name: fp.Family, TTF: ttf, Index: fp.Location.Index})
	}
	return out
}

// monoRank sorts monospaced families first. The name is all there is to go on:
// a footprint carries no such flag, and reading the file to find out would defeat
// the point of having an index.
func monoRank(family string) int {
	if strings.Contains(family, "mono") { // families are stored normalized
		return 0
	}
	return 1
}

func (l *Library) read(fp fontscan.Footprint) ([]byte, error) {
	if ttf, in := l.loaded[fp.Location.File]; in {
		return ttf, nil
	}
	ttf, err := os.ReadFile(fp.Location.File)
	if err != nil {
		return nil, err
	}
	l.loaded[fp.Location.File] = ttf
	return ttf, nil
}

// pickAspect is the footprint whose aspect matches the style: bold means weight 700 or
// more, italic means either italic or oblique. A family with several matches keeps the
// first, which is the scan's own order.
func pickAspect(prints []fontscan.Footprint, style Style) (fontscan.Footprint, bool) {
	wantBold, wantItalic := style&Bold != 0, style&Italic != 0
	for _, fp := range prints {
		isBold := fp.Aspect.Weight >= gotext.WeightBold
		isItalic := fp.Aspect.Style != gotext.StyleNormal
		if isBold == wantBold && isItalic == wantItalic {
			return fp, true
		}
	}
	return fontscan.Footprint{}, false
}
