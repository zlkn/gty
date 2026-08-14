package font

// Shaping turns a row of cells into glyph IDs, applying GSUB. It is required for
// ligatures and it cannot be done with x/image: font/sfnt parses no GSUB at all,
// and xfont.Face is rune-keyed end to end, so it can neither pick the ligature
// glyph nor draw one.
//
// The grid survives this untouched. JetBrains Mono's calt is monospace
// preserving: every glyph keeps the plain advance, and every cluster is exactly
// one rune and one glyph. A ligature is either a blank spacer plus a wide glyph
// that reaches back over it ("=>" -> 1167, 1015) or per-cell tiles that join up
// ("<====>" -> 714, 711, 711, 711, 711, 713). Both are one glyph per cell, so
// "one cell, one quad" holds; only Atlas.PadLeft grows to fit the overhang.
//
// This goes through harfbuzz rather than the shaping package on top of it: for a
// 70-cell row of code the low-level buffer is ~1.6x faster with ~2.4x less
// garbage (309 vs 502 us, 78 vs 188 KB), because shaping.Output computes
// per-glyph extents and bounds that the atlas already accounts for.

import (
	"bytes"
	"fmt"
	"slices"

	gotext "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	"github.com/go-text/typesetting/harfbuzz"
	"github.com/go-text/typesetting/language"
)

// noLigatures switches calt off for a whole shaping call.
//
// End matters: it defaults to 0, which is an empty cluster range, and the
// override is then silently ignored.
var noLigatures = []harfbuzz.Feature{{
	Tag:   opentype.MustNewTag("calt"),
	Value: 0,
	Start: harfbuzz.FeatureGlobalStart,
	End:   harfbuzz.FeatureGlobalEnd,
}}

// Shaper maps runes to glyph IDs. One instance per FontManager, reusing a single
// harfbuzz buffer, so it is not safe for concurrent use.
type Shaper struct {
	face  *gotext.Face
	font  *harfbuzz.Font
	buf   *harfbuzz.Buffer
	props harfbuzz.SegmentProperties
}

func NewShaper(ttf []byte) (*Shaper, error) {
	face, err := gotext.ParseTTF(bytes.NewReader(ttf))
	if err != nil {
		return nil, fmt.Errorf("shaper parse face: %w", err)
	}
	return &Shaper{
		face: face,
		font: harfbuzz.NewFont(face),
		buf:  harfbuzz.NewBuffer(),
		props: harfbuzz.SegmentProperties{
			Direction: harfbuzz.LeftToRight,
			Script:    language.Latin,
			Language:  "en",
		},
	}, nil
}

// ShapeRow appends one glyph ID per rune of row to dst and returns it.
//
// ok is false when the font breaks the one-glyph-per-cell contract, which a
// per-cell renderer cannot draw; the caller should fall back to
// FontManager.GlyphIndex for that row rather than render garbage.
//
// Shape whole runs of uniform cells, not single cells: substitution is
// contextual, so an isolated '=' is never a ligature. Pass ligatures=false for
// runs that must not join up — the cell under the cursor, or across a selection
// edge. That path is also ~17x cheaper (18 vs 314 us for 70 cells), since calt
// is where essentially all of the shaping cost lives.
func (s *Shaper) ShapeRow(dst []GID, row []rune, ligatures bool) ([]GID, bool) {
	if len(row) == 0 {
		return dst, true
	}

	s.buf.Clear()
	s.buf.Props = s.props
	s.buf.AddRunes(row, 0, len(row))
	if ligatures {
		s.buf.Shape(s.font, nil)
	} else {
		s.buf.Shape(s.font, noLigatures)
	}

	if len(s.buf.Info) != len(row) {
		return dst, false
	}
	for i, info := range s.buf.Info {
		if info.Cluster != i {
			return dst, false
		}
		dst = append(dst, GID(info.Glyph))
	}
	return dst, true
}

// GlyphSet is every glyph the atlas has to hold: printable ASCII plus anything
// GSUB can substitute in.
//
// Enumerating by shaping n-grams does not converge — 3-grams over the 52
// characters that take part in substitutions are 140k shapes and ~16 s, and
// still only a lower bound, because this font has 4- and 5-character ligatures.
// Walking the lookup list is exact and instant instead. Contextual and chained
// lookups need no special case: they only delegate to other lookups in the same
// list, whose outputs are collected here anyway.
//
// The result is a safe superset — it also picks up frac, sups, subs, ordn and
// zero, which a terminal never asks for. At a few hundred KiB that is not worth
// narrowing; doing so would mean walking calt's nested SeqLookupRecords.
func (s *Shaper) GlyphSet(numGlyphs int) []GID {
	seen := make(map[GID]bool, 512)
	ids := make([]GID, 0, 512)
	add := func(gid GID) {
		if int(gid) < numGlyphs && !seen[gid] {
			seen[gid] = true
			ids = append(ids, gid)
		}
	}

	// ASCII first, so the familiar block stays contiguous at the start of the
	// atlas and a debug dump stays readable.
	for r := rune(FirstASCII); r <= LastASCII; r++ {
		if gid, ok := s.face.Cmap.Lookup(r); ok {
			add(GID(gid))
		}
	}
	ascii := len(ids)

	subst := func(g tables.GlyphID) { add(GID(g)) }
	for _, lookup := range s.face.GSUB.Lookups {
		for _, sub := range lookup.Subtables {
			switch sub := sub.(type) {
			case tables.SingleSubs:
				switch d := sub.Data.(type) {
				case tables.SingleSubstData1:
					// Coverage plus a delta: there is no output list to read, so
					// probe the coverage across the glyph space.
					for gid := range numGlyphs {
						if _, covered := d.Coverage.Index(tables.GlyphID(gid)); covered {
							if out := gid + int(d.DeltaGlyphID); out >= 0 {
								subst(tables.GlyphID(out))
							}
						}
					}
				case tables.SingleSubstData2:
					for _, g := range d.SubstituteGlyphIDs {
						subst(g)
					}
				}
			case tables.MultipleSubs:
				for _, seq := range sub.Sequences {
					for _, g := range seq.SubstituteGlyphIDs {
						subst(g)
					}
				}
			case tables.AlternateSubs:
				for _, set := range sub.AlternateSets {
					for _, g := range set.AlternateGlyphIDs {
						subst(g)
					}
				}
			case tables.LigatureSubs:
				for _, set := range sub.LigatureSets {
					for _, lig := range set.Ligatures {
						subst(lig.LigatureGlyph)
					}
				}
			case tables.ReverseChainSingleSubs:
				for _, g := range sub.SubstituteGlyphIDs {
					subst(g)
				}
			}
		}
	}

	// ASCII keeps its cmap order; sort the rest so the layout is reproducible
	// whatever order the lookups were walked in.
	slices.Sort(ids[ascii:])
	return ids
}
