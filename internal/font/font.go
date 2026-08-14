package font

import (
	"fmt"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// GID is a glyph index into one face.
//
// Glyphs are addressed by ID rather than by rune because ligature glyphs have no
// codepoint: JetBrains Mono keeps them in GSUB/calt, reachable only through the
// shaper. x/image and go-text agree on the numbering but name the type
// differently; this is the package's own name for that number.
//
// A GID is only meaningful together with the style it came from — the same
// number is a different glyph in Regular and in Bold. Pair them with Key.
type GID = sfnt.GlyphIndex

// Style selects a face. The two bits are independent so SGR bold and italic map
// straight onto it, and so the four values index a dense array.
type Style uint8

const (
	Bold   Style = 1 << 0
	Italic Style = 1 << 1

	Regular    Style = 0
	BoldItalic Style = Bold | Italic

	NumStyles = 4
)

func (s Style) String() string {
	switch s {
	case Regular:
		return "Regular"
	case Bold:
		return "Bold"
	case Italic:
		return "Italic"
	default:
		return "BoldItalic"
	}
}

// Hinting is applied to metrics only. x/image implements no TrueType
// interpreter, so this quantizes advances and the cell box without touching
// outlines — the atlas rasterizes identically with or without it.
const Hinting = xfont.HintingFull

// face is one loaded variant.
type face struct {
	font   *sfnt.Font
	shaper *Shaper
	buf    sfnt.Buffer // reused by GlyphIndex
}

// FontManager owns everything derived from the faces: the cell geometry the grid
// is sized from, the shared atlas, and a shaper per style.
//
// Not safe for concurrent use — the shapers and buffers are reused.
type FontManager struct {
	Family string
	Size   float64
	PPEM   fixed.Int26_6

	// One geometry for every style; see the check in NewManager.
	CellWidth  int
	CellHeight int
	Ascent     int // Dot is the baseline, not the top of the cell

	Atlas *Atlas

	faces [NumStyles]*face
}

// NewManager builds all four faces at size points and dpi dots per inch, derives
// the terminal cell box, then bakes one atlas covering every style.
//
// ttf is indexed by Style. The bake is unconditional: a couple of MiB and a few
// milliseconds once, which buys away any need for laziness, a per-glyph cache,
// or a mutex.
func NewManager(ttf [NumStyles][]byte, family string, size, dpi float64) (*FontManager, error) {
	// Same arithmetic as opentype.NewFace, so metrics here and any face built
	// from the same file agree on ppem.
	ppem := fixed.Int26_6(0.5 + (size * dpi * 64 / 72))
	fm := &FontManager{Family: family, Size: size, PPEM: ppem}

	for i := range NumStyles {
		style := Style(i)
		f, err := sfnt.Parse(ttf[style])
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", style, err)
		}
		cellW, cellH, ascent, err := faceMetrics(f, ppem)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", style, err)
		}
		if style == Regular {
			fm.CellWidth, fm.CellHeight, fm.Ascent = cellW, cellH, ascent
		} else if cellW != fm.CellWidth || cellH != fm.CellHeight || ascent != fm.Ascent {
			// One grid serves every style, so a variant that disagrees would
			// misalign the whole screen rather than just its own cells.
			return nil, fmt.Errorf("%s geometry %dx%d ascent %d does not match Regular %dx%d ascent %d",
				style, cellW, cellH, ascent, fm.CellWidth, fm.CellHeight, fm.Ascent)
		}
		sh, err := NewShaper(ttf[style])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", style, err)
		}
		fm.faces[style] = &face{font: f, shaper: sh}
	}

	var err error
	if fm.Atlas, err = BakeAtlas(fm); err != nil {
		return nil, err
	}
	return fm, nil
}

// faceMetrics derives the cell box from the font's own metrics.
func faceMetrics(f *sfnt.Font, ppem fixed.Int26_6) (cellW, cellH, ascent int, err error) {
	var buf sfnt.Buffer
	m, err := f.Metrics(&buf, ppem, Hinting)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("face metrics: %w", err)
	}
	// Monospace: every glyph advances identically, so any letter will do.
	gid, err := f.GlyphIndex(&buf, 'M')
	if err != nil {
		return 0, 0, 0, fmt.Errorf("glyph index for 'M': %w", err)
	}
	adv, err := f.GlyphAdvance(&buf, gid, ppem, Hinting)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("advance for 'M': %w", err)
	}
	if adv == 0 {
		return 0, 0, 0, fmt.Errorf("font has no advance for 'M'")
	}
	// fixed.Int26_6 is 1/64 px; never cast directly.
	return adv.Ceil(), m.Height.Ceil(), m.Ascent.Ceil(), nil
}

func (fm *FontManager) Font(s Style) *sfnt.Font { return fm.faces[s].font }
func (fm *FontManager) Shaper(s Style) *Shaper  { return fm.faces[s].shaper }

// Close releases the manager. Nothing here owns an OS handle — the faces are
// byte slices in the binary and the atlas is plain memory — so this only exists
// to keep the window's teardown path honest.
func (fm *FontManager) Close() error {
	fm.Atlas, fm.faces = nil, [NumStyles]*face{}
	return nil
}

// GlyphIndex maps a rune straight through cmap, skipping the shaper. Correct
// only for cells that cannot ligate: substitution is contextual, so a run has to
// go through Shaper.ShapeRow to come out right.
//
// This is the renderer's cheap path for rows with no ligature material, so it
// reuses the face's buffer rather than allocating one per call.
func (fm *FontManager) GlyphIndex(s Style, r rune) (GID, bool) {
	fc := fm.faces[s]
	gid, err := fc.font.GlyphIndex(&fc.buf, r)
	if err != nil || gid == 0 {
		return 0, false
	}
	return gid, true
}
