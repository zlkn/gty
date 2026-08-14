package font

import (
	"fmt"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// GID is a glyph index into the compiled-in face.
//
// Glyphs are addressed by ID rather than by rune because ligature glyphs have no
// codepoint: JetBrains Mono keeps them in GSUB/calt, reachable only through the
// shaper. x/image and go-text agree on the numbering but name the type
// differently; this is the package's own name for that number.
type GID = sfnt.GlyphIndex

// Hinting is applied to metrics only. x/image implements no TrueType
// interpreter, so this quantizes advances and the cell box without touching
// outlines — the atlas rasterizes identically with or without it.
const Hinting = xfont.HintingFull

// FontManager owns everything derived from the face: the cell geometry the grid
// is sized from, the baked atlas, and the shaper that maps a row of cells to
// glyph IDs.
//
// Not safe for concurrent use — Shaper holds a reusable buffer.
type FontManager struct {
	Family string
	Size   float64

	// Font rasterizes by glyph ID; PPEM is the size it was measured at.
	Font *sfnt.Font
	PPEM fixed.Int26_6

	CellWidth  int
	CellHeight int
	Ascent     int // Dot is the baseline, not the top of the cell

	Atlas  *Atlas
	Shaper *Shaper

	buf sfnt.Buffer // reused by GlyphIndex; the reason this type is not concurrent-safe
}

// NewManager builds the face at size points and dpi dots per inch, derives the
// terminal cell box from the font's own metrics, then bakes the atlas.
//
// The bake is unconditional: it costs a few hundred KiB and a few milliseconds
// once, which buys away any need for laziness, a per-rune cache, or a mutex.
// family is recorded for reporting only.
func NewManager(ttf []byte, family string, size, dpi float64) (*FontManager, error) {
	f, err := sfnt.Parse(ttf)
	if err != nil {
		return nil, fmt.Errorf("parse font: %w", err)
	}

	// Same arithmetic as opentype.NewFace, so metrics here and any face built
	// from the same file agree on ppem.
	ppem := fixed.Int26_6(0.5 + (size * dpi * 64 / 72))

	var buf sfnt.Buffer
	m, err := f.Metrics(&buf, ppem, Hinting)
	if err != nil {
		return nil, fmt.Errorf("face metrics: %w", err)
	}

	// Monospace: every glyph advances identically, so any letter will do.
	gid, err := f.GlyphIndex(&buf, 'M')
	if err != nil {
		return nil, fmt.Errorf("glyph index for 'M': %w", err)
	}
	adv, err := f.GlyphAdvance(&buf, gid, ppem, Hinting)
	if err != nil {
		return nil, fmt.Errorf("advance for 'M': %w", err)
	}
	if adv == 0 {
		return nil, fmt.Errorf("font has no advance for 'M'")
	}

	fm := &FontManager{
		Family:     family,
		Size:       size,
		Font:       f,
		PPEM:       ppem,
		CellWidth:  adv.Ceil(), // fixed.Int26_6 is 1/64 px; never cast directly
		CellHeight: m.Height.Ceil(),
		Ascent:     m.Ascent.Ceil(),
	}

	fm.Shaper, err = NewShaper(ttf)
	if err != nil {
		return nil, err
	}
	fm.Atlas, err = BakeAtlas(fm, fm.Shaper.GlyphSet(f.NumGlyphs()))
	if err != nil {
		return nil, err
	}
	return fm, nil
}

// Close releases the manager. Nothing here owns an OS handle — the face is a
// byte slice in the binary and the atlas is plain memory — so this only exists
// to keep the window's teardown path honest.
func (fm *FontManager) Close() error {
	fm.Atlas, fm.Shaper, fm.Font = nil, nil, nil
	return nil
}

// GlyphIndex maps a rune straight through cmap, skipping the shaper. Correct
// only for cells that cannot ligate: substitution is contextual, so a run has to
// go through Shaper.ShapeRow to come out right.
//
// This is the renderer's cheap path for rows with no ligature material, so it
// reuses fm.buf rather than allocating one per call.
func (fm *FontManager) GlyphIndex(r rune) (GID, bool) {
	gid, err := fm.Font.GlyphIndex(&fm.buf, r)
	if err != nil || gid == 0 {
		return 0, false
	}
	return gid, true
}
