package font

import (
	"fmt"
	"slices"

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

	// Fallback is the first face a rune none of the four styles covers is drawn
	// from, and the chain runs on from there: Fallback+1, Fallback+2. They are not
	// styles — nothing in SGR reaches them and they have no bold or italic of
	// their own — but they are numbered alongside them so that a Key stays two
	// fields wide and the atlas keeps one rasterizer per index.
	//
	// The chain grows while the terminal runs: Options.Fallback is what it opens
	// with, and Options.Finder appends to it the first time a rune turns up that
	// nothing loaded can draw. One face is never enough — the Nerd Font-patched
	// family carries the icons a prompt draws but 14 of the 192 dingbats, so a fish
	// prompt asking for ✔ (U+2714) needs a face from somewhere else entirely.
	Fallback Style = NumStyles

	// maxFaces is the ceiling on the chain, because a Style is a byte and a Key is
	// addressed by it. Reaching it means something is wrong, not that a session
	// legitimately wanted 252 fallback faces.
	maxFaces = 1 << 8
)

func (s Style) String() string {
	switch s {
	case Regular:
		return "Regular"
	case Bold:
		return "Bold"
	case Italic:
		return "Italic"
	case BoldItalic:
		return "BoldItalic"
	}
	if s >= Fallback {
		return fmt.Sprintf("Fallback%d", s-Fallback)
	}
	return fmt.Sprintf("Style(%d)", uint8(s))
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

	// Where this face draws: the size it is rasterised at and the baseline's
	// offset from the top of the cell. The four styles share the grid's own
	// numbers; a fallback is fitted to the cell instead, so it carries its own.
	ppem   fixed.Int26_6
	ascent int

	// fitted marks a face that was scaled into this grid rather than sized with
	// it. Its glyphs are confined to their cell at bake time, which is what lets
	// one turn up mid-session: the atlas slot was measured without it.
	fitted bool

	// reach is how far outside its cell this face may draw to the left. Only an icon
	// face has any: it is wider than a cell on purpose, and its overhang belongs on
	// both sides so the icon sits centred. See Atlas.fitBox.
	reach int

	name string // for messages; the four styles are named by their Style
}

// Source is one face to load: the file's bytes, which face inside it when the file is
// a collection, and a name for messages.
type Source struct {
	Name  string
	TTF   []byte
	Index uint16 // the face inside a .ttc; 0 for a plain .ttf
}

// parseFont is the one way this package turns bytes into a face. Through the collection
// parser even for a plain file — it reports a collection of one — because the CJK fonts
// a fallback search lands on ship as .ttc, and sfnt.Parse refuses those outright.
func parseFont(src Source) (*sfnt.Font, error) {
	c, err := sfnt.ParseCollection(src.TTF)
	if err != nil {
		return nil, err
	}
	if int(src.Index) >= c.NumFonts() {
		return nil, fmt.Errorf("face %d of a %d-face collection", src.Index, c.NumFonts())
	}
	return c.Font(int(src.Index))
}

// Finder supplies faces for runes nothing loaded covers — in practice the system's
// installed fonts, searched on demand. See Library.
type Finder interface {
	// FindRune is the faces carrying r, best first. Several, because the best one
	// on paper may be undrawable: a colour emoji font is bitmaps, and this package
	// rasterises outlines, so the manager works down the list.
	FindRune(r rune) []Source
}

// Options is what a manager is built from.
type Options struct {
	// Styles is the primary family, indexed by Style. All four are required, and
	// they have to agree on geometry — the grid is sized from them.
	Styles [NumStyles]Source
	Family string // the primary's name, for messages

	// Fallback is searched in order for a rune the primary has no glyph for,
	// before Finder is asked. This is where the embedded family goes when the
	// primary came from the config: it is Nerd Font-patched, so it answers for
	// every icon a prompt draws, and for ligature-free text it is a close match.
	Fallback []Source

	// Finder is the last resort, asked once per rune: any face on the system that
	// has it. nil leaves an uncovered rune as the replacement box.
	Finder Finder

	// Size in points and DPI; PPEM is derived from both.
	Size, DPI float64

	// IconFill is the share of the cell's height an icon's ink is scaled to fill; see
	// iconFit. Zero leaves icons at the size the face drew them, which is half a line
	// on a Nerd Font's Mono variant — DefaultIconFill is what the app passes.
	IconFill float64

	MaxTexture int // the device's largest 2D texture; it bounds the atlas

	// Warn reports what was worked around rather than failed on — a fallback that
	// would not load, a face offered for a rune that cannot be rasterised. nil is
	// silence.
	Warn func(string)
}

func (o Options) warn(format string, args ...any) {
	if o.Warn != nil {
		o.Warn(fmt.Sprintf(format, args...))
	}
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

	// The four styles first, then the fallback chain in the order it is searched.
	// Atlas.rast is indexed the same way and grows with it.
	faces []*face

	finder Finder
	warn   func(string)

	// iconFill is Options.IconFill, and iconTwin is the face each face's icons are drawn
	// from — the same file at the bigger size. Built on first use, because most sessions
	// draw icons from one face and none from the rest.
	iconFill  float64
	iconTwins map[Style]Style

	// resolved is every rune the chain has been asked about, hits and misses
	// alike. A miss has to be remembered too: without it, a screen full of a rune
	// nothing has would search the system's fonts again on every frame.
	resolved map[rune]Key
}

// NewManager builds the primary family at o.Size points and o.DPI dots per inch,
// derives the terminal cell box, then lays out one atlas covering every face.
//
// Only the glyphs a terminal needs in its first frame are rasterised here, and only
// from the primary: the fallback chain is baked a glyph at a time, on the frame that
// asks. See Atlas.
func NewManager(o Options) (*FontManager, error) {
	// Same arithmetic as opentype.NewFace, so metrics here and any face built
	// from the same file agree on ppem.
	ppem := fixed.Int26_6(0.5 + (o.Size * o.DPI * 64 / 72))
	fm := &FontManager{
		Family: o.Family, Size: o.Size, PPEM: ppem,
		faces:     make([]*face, NumStyles),
		finder:    o.Finder,
		warn:      o.Warn,
		resolved:  make(map[rune]Key),
		iconFill:  o.IconFill,
		iconTwins: make(map[Style]Style),
	}

	for i := range NumStyles {
		style := Style(i)
		f, err := parseFont(o.Styles[style])
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", style, err)
		}
		cellW, cellH, ascent, err := faceMetrics(f, ppem)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", style, err)
		}
		if style == Regular {
			fm.CellWidth, fm.CellHeight, fm.Ascent = cellW, cellH, ascent
			// The grid is one width for every cell, so a proportional family comes
			// out as text with holes in it — and usually fails the geometry check
			// below, since its bold 'M' is wider than its regular one. Either way
			// the reason wants saying plainly, or it reads as a bug in the terminal.
			if n := proportionalRunes(f, ppem, cellW); n > 0 {
				o.warn("%s is not monospaced: %d of the %d printable ASCII glyphs do not advance by one cell",
					o.Family, n, LastASCII-FirstASCII+1)
			}
		} else if cellW != fm.CellWidth || cellH != fm.CellHeight || ascent != fm.Ascent {
			// One grid serves every style, so a variant that disagrees would
			// misalign the whole screen rather than just its own cells.
			return nil, fmt.Errorf("%s geometry %dx%d ascent %d does not match Regular %dx%d ascent %d",
				style, cellW, cellH, ascent, fm.CellWidth, fm.CellHeight, fm.Ascent)
		}
		sh, err := NewShaper(o.Styles[style])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", style, err)
		}
		fm.faces[style] = &face{font: f, shaper: sh, ppem: ppem, ascent: ascent, name: o.Family}
	}

	// A fallback that will not load is worked around, not fatal: the terminal still
	// runs, with the box in place of whatever that face was carrying.
	for _, fb := range o.Fallback {
		fc, err := fitFace(fb, fm.CellWidth, fm.CellHeight)
		if err != nil {
			o.warn("fallback %s: %v", fb.Name, err)
			continue
		}
		fm.faces = append(fm.faces, fc)
	}

	var err error
	if fm.Atlas, err = BakeAtlas(fm, o.MaxTexture); err != nil {
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

// proportionalRunes counts the printable ASCII glyphs that do not advance by exactly
// one cell — zero for a monospaced face, since the cell is one of their advances.
func proportionalRunes(f *sfnt.Font, ppem fixed.Int26_6, cellW int) int {
	var buf sfnt.Buffer
	want, n := fixed.I(cellW), 0
	for r := rune(FirstASCII); r <= LastASCII; r++ {
		gid, err := f.GlyphIndex(&buf, r)
		if err != nil || gid == 0 {
			continue
		}
		if adv, err := f.GlyphAdvance(&buf, gid, ppem, Hinting); err == nil && adv != want {
			n++
		}
	}
	return n
}

// Nerd Font icons live in the two private-use areas, and they are the reason a terminal
// needs a size policy of its own: the patcher's Mono variant fits every icon to the cell
// *width*, and a cell is about twice as tall as it is wide, so an icon drawn at the size
// the file says comes out half the height of the line. Other terminals fit them to the
// cell height and let the width overhang; that is what iconFit reproduces.
const (
	iconPUALo, iconPUAHi     = 0xE000, 0xF8FF   // the BMP area: powerline, devicons, seti, ...
	iconPlaneLo, iconPlaneHi = 0xF0000, 0xFFFFD // plane 15: Material Design, since Nerd Fonts v3

	// Powerline is excluded. The patcher already stretches those to the full cell so a
	// prompt's arrows tile with no seam, and scaling them would open it back up.
	powerlineLo, powerlineHi = 0xE0A0, 0xE0D7

	// DefaultIconFill is how much of the cell's height an icon is scaled to fill.
	// Measured against a terminal that does this by default: 0.8 of the line, against
	// the 0.45 the patched font draws at.
	DefaultIconFill = 0.8
)

// isIconRune reports whether r is an icon rather than a character — something drawn to be
// looked at rather than read, and which therefore wants the whole height of the line.
//
// Note what is *not* here: the standard-Unicode symbols the patcher also carries, ✔ and ♥
// and the box drawing, which appear in prose and have to stay text-sized.
func isIconRune(r rune) bool {
	if r >= powerlineLo && r <= powerlineHi {
		return false
	}
	return r >= iconPUALo && r <= iconPUAHi || r >= iconPlaneLo && r <= iconPlaneHi
}

// iconSample is what a face is measured on, one rune per icon set: Font Awesome, Octicons,
// Devicons, Codicons, Seti, Material Design. A fixed list, so the size a face is fitted to
// is the same on every run — and a spread, because an icon set's glyphs are not all one
// height and the median of a single set would not stand in for the rest.
var iconSample = [...]rune{
	0xF015, 0xF07B, 0xF00C, 0xF0F3, 0xF09B, 0xF113, 0xF121, // Font Awesome, Octicons
	0xE62B, 0xE712, 0xE725, 0xE73C, 0xE20F, 0xF1D0, // Seti, Devicons, Codicons
	0xF0320, 0xF10FE, 0xF0868, // Material Design, plane 15
}

// iconFit is where an icon face draws: the size it is rasterised at, the baseline that
// centres it in the cell, and how far it reaches out of the cell on each side, which the
// atlas has to reserve room for before the face exists.
//
// The size comes from the ink, not from the metrics: the face is the same file at a bigger
// ppem, so its ascent and descent are bigger than the cell by construction and say nothing
// about where the icons in it sit.
//
// fill is the share of the cell's height the ink is scaled to; zero or less turns the whole
// thing off. ok is also false for a face with no icons to measure, which is most of them —
// a text face gets no twin and its runes keep drawing at their own size.
func iconFit(base *face, cellW, cellH int, fill float64) (ppem fixed.Int26_6, ascent, overhang int, ok bool) {
	if fill <= 0 {
		return 0, 0, 0, false
	}
	above, below, width, ok := medianIconBox(base)
	if !ok {
		return 0, 0, 0, false
	}
	// The box is in font units; these are the same numbers in the pixels the face draws
	// text at. Measured in units and converted here rather than measured in pixels,
	// because a bounding box rounded outward at both ends overstates a ten-pixel icon by
	// a fifth, and the scale derived from it would fall as far short.
	perUnit := float64(base.ppem) / 64 / float64(base.font.UnitsPerEm())
	a, b, w := perUnit*float64(above), perUnit*float64(below), perUnit*float64(width)

	scale := fill * float64(cellH) / (a + b)
	if scale <= 1 {
		return 0, 0, 0, false // nothing to gain, and no room to reserve
	}

	// Centre the scaled median ink in the cell: the baseline is wherever it has to be for
	// that, which is not where the face's own metrics would put it.
	a, b = scale*a, scale*b
	ascent = int(0.5*(float64(cellH)-(a+b)) + a + 0.5)
	overhang = int(0.5*(scale*w-float64(cellW)) + 0.9999)
	return fixed.Int26_6(float64(base.ppem) * scale), ascent, max(overhang, 0), true
}

// medianIconBox is the middle icon's box in font units: how far it reaches above and below
// the baseline, and how wide it is. Medians rather than extremes, so one outsized glyph in
// a set does not decide the size of every other.
//
// Units, not pixels: at ppem = upem a "pixel" is a font unit, so nothing is rounded away
// and the caller can scale the numbers without inheriting the rounding.
func medianIconBox(base *face) (above, below, width int, ok bool) {
	var buf sfnt.Buffer
	em := fixed.I(int(base.font.UnitsPerEm()))
	var as, bs, ws []int
	for _, r := range iconSample {
		gid, err := base.font.GlyphIndex(&buf, r)
		if err != nil || gid == 0 {
			continue
		}
		bounds, _, err := base.font.GlyphBounds(&buf, gid, em, xfont.HintingNone)
		if err != nil || bounds.Empty() {
			continue
		}
		// Bounds are relative to the pen: y grows downward, so Min.Y is the top.
		as = append(as, (-bounds.Min.Y).Round())
		bs = append(bs, bounds.Max.Y.Round())
		ws = append(ws, (bounds.Max.X - bounds.Min.X).Round())
	}
	if len(as) == 0 {
		return 0, 0, 0, false
	}
	slices.Sort(as)
	slices.Sort(bs)
	slices.Sort(ws)
	mid := len(as) / 2
	if as[mid]+bs[mid] <= 0 {
		return 0, 0, 0, false
	}
	return as[mid], bs[mid], ws[mid], true
}

// fitFace loads a face that was not designed for this grid and scales it into it.
//
// It cannot simply be asked for the family's ppem: the two are different designs at
// different em sizes — Symbols Nerd Font Mono advances a full em where JetBrains Mono
// advances 0.6 of one, so at a shared ppem its icons would come out nearly twice a
// cell wide. Its ppem is derived from its own advance instead, which makes one glyph
// one cell.
//
// Nor can it share the family's baseline. The two faces put different fractions of
// their em above it, and an icon drawn on the family's baseline hangs low in the cell
// by the difference; centring the face's own ascent-descent box in the cell puts it
// back where the patched family draws the same icon, within a pixel.
//
// What is left over is per glyph, not per face: on a proportional face — and the
// system will hand us those — the widest glyph is three times the median, so a scale
// that fits every one of them would draw the rest tiny. The face is fitted to its
// median and the outliers are shrunk one at a time, at bake time.
//
// No shaper: a fallback is reached one rune at a time, and nothing in it ligates.
func fitFace(src Source, cellW, cellH int) (*face, error) {
	f, err := parseFont(src)
	if err != nil {
		return nil, err
	}

	var buf sfnt.Buffer
	// At ppem = upem, a "pixel" is a font unit, so this advance is the design's own.
	em := fixed.I(int(f.UnitsPerEm()))
	adv, err := medianAdvance(f, &buf, em)
	if err != nil {
		return nil, err
	}
	ppem := fixed.Int26_6(int64(cellW<<6) * int64(em) / int64(adv))
	if ppem <= 0 {
		return nil, fmt.Errorf("a %v advance does not fit a %d px cell", adv, cellW)
	}

	m, err := f.Metrics(&buf, ppem, Hinting)
	if err != nil {
		return nil, fmt.Errorf("face metrics: %w", err)
	}
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	return &face{
		font: f, ppem: ppem, ascent: (cellH-asc-desc)/2 + asc,
		fitted: true, name: src.Name,
	}, nil
}

// medianAdvance is the middle advance in the face at ppem. Not the advance of some
// representative letter, the way the family is measured — a symbol face carries icons,
// so there is no 'M' to ask — and not the widest either: on Symbola the widest glyph
// is 3 em against a 0.725 em median, and fitting that would leave every ordinary
// symbol at a quarter of its size. On a monospaced face all three are the same number.
func medianAdvance(f *sfnt.Font, buf *sfnt.Buffer, ppem fixed.Int26_6) (fixed.Int26_6, error) {
	advances := make([]fixed.Int26_6, 0, faceGlyphs(f))
	for gid := GID(1); gid < faceGlyphs(f); gid++ {
		adv, err := f.GlyphAdvance(buf, gid, ppem, Hinting)
		if err == nil && adv > 0 {
			advances = append(advances, adv)
		}
	}
	if len(advances) == 0 {
		return 0, fmt.Errorf("no glyph in the face has an advance")
	}
	slices.Sort(advances)
	return advances[len(advances)/2], nil
}

// NumFaces is the four styles plus however long the fallback chain is. Face numbers
// below it are valid; Fallback..NumFaces-1 is the chain.
func (fm *FontManager) NumFaces() int { return len(fm.faces) }

// face is nil past the end of the chain, which is what makes the fallback optional.
func (fm *FontManager) face(s Style) *face {
	if int(s) >= len(fm.faces) {
		return nil
	}
	return fm.faces[s]
}

// Font and Shaper are nil for a face this manager was not given — the fallback chain
// is allowed to be empty, and no face in it has a shaper.
func (fm *FontManager) Font(s Style) *sfnt.Font {
	if fc := fm.face(s); fc != nil {
		return fc.font
	}
	return nil
}

func (fm *FontManager) Shaper(s Style) *Shaper {
	if fc := fm.face(s); fc != nil {
		return fc.shaper
	}
	return nil
}

// FaceMetrics is where the face draws: the size it is rasterised at and the
// baseline's offset from the top of the cell. ok is false for a face that was not
// loaded.
func (fm *FontManager) FaceMetrics(s Style) (ppem fixed.Int26_6, ascent int, ok bool) {
	fc := fm.face(s)
	if fc == nil {
		return 0, 0, false
	}
	return fc.ppem, fc.ascent, true
}

// Close releases the manager. Nothing here owns an OS handle — the faces are
// byte slices in the binary and the atlas is plain memory — so this only exists
// to keep the window's teardown path honest.
func (fm *FontManager) Close() error {
	fm.Atlas, fm.faces = nil, nil
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

// Resolve is the atlas key for the glyph picked for r, walking the fallback chain when
// the family had nothing to pick: both the shaper and GlyphIndex answer GID 0 for a
// rune the face does not cover, and GID 0 draws as the replacement box.
//
// First hit in the chain wins, so its order is its priority — the patched family
// before whatever the system offers, because a face that has an icon should draw the
// icon. Past the end of the chain the finder is asked, once per rune, and whatever it
// turns up joins the chain for every rune after.
//
// Every fallback is one face for all four styles, so a missing glyph comes out upright
// inside a bold or italic run. That is also what keeps it to a single atlas slot
// instead of four copies of the same glyph.
//
// A rune nothing covers comes back as GID 0, which is the box — a hole that reads as
// missing rather than as a space.
func (fm *FontManager) Resolve(s Style, gid GID, r rune) Key {
	k := fm.resolveFace(s, gid, r)
	if k.GID == 0 || !isIconRune(r) {
		return k
	}
	// An icon is drawn from a twin of whichever face turned out to have it: the same
	// file at the size that fills the cell's height. The glyph id carries over
	// unchanged, because it is the same file — that is what makes this cost nothing.
	if twin, ok := fm.iconTwin(k.Style); ok {
		return Key{Style: twin, GID: k.GID}
	}
	return k
}

// iconTwin is the face style's icons are drawn from, built on first use. ok is false when
// the face has no icons to measure, or when the twin would be no bigger than the face.
//
// One twin serves all four styles, like a fallback does: an icon has no italic, and four
// copies of the same glyph would only cost atlas slots.
func (fm *FontManager) iconTwin(s Style) (Style, bool) {
	if s < NumStyles {
		s = Regular
	}
	if twin, in := fm.iconTwins[s]; in {
		return twin, twin != s
	}

	base := fm.face(s)
	if base == nil || len(fm.faces) >= maxFaces {
		return s, false
	}
	ppem, ascent, overhang, ok := iconFit(base, fm.CellWidth, fm.CellHeight, fm.iconFill)
	if !ok {
		fm.iconTwins[s] = s // remembered as "no twin", so the measuring happens once
		return s, false
	}

	twin := Style(len(fm.faces))
	fc := &face{
		font: base.font, ppem: ppem, ascent: ascent,
		fitted: true, reach: overhang, name: base.name + " icons",
	}
	fm.faces = append(fm.faces, fc)
	fm.Atlas.addFace(fc)
	fm.iconTwins[s] = twin
	fm.warnf("icons from %s drawn at %v, %v for text", base.name, ppem, base.ppem)
	return twin, true
}

// resolveFace is Resolve without the icon policy: which face and glyph id carry r.
func (fm *FontManager) resolveFace(s Style, gid GID, r rune) Key {
	if gid != 0 {
		return Key{Style: s, GID: gid}
	}
	k, known := fm.resolved[r]
	if !known {
		k = fm.search(r)
		fm.resolved[r] = k
	}
	if k.GID == 0 {
		// Nothing has it. The box is styleless, but keep the caller's style so the
		// key is the same one a plain miss produces.
		return Key{Style: s}
	}
	return k
}

// search is Resolve's slow half, run once per rune: the loaded chain first, then the
// finder, whose answer is added to the chain.
//
// A face joins the chain rather than being loaded per rune because the atlas holds one
// rasterizer per face and every glyph baked from it has to keep working — and because
// the next unknown rune is usually from the same script as this one.
func (fm *FontManager) search(r rune) Key {
	for i := int(Fallback); i < len(fm.faces); i++ {
		fc := fm.faces[i]
		if gid, err := fc.font.GlyphIndex(&fc.buf, r); err == nil && gid != 0 {
			return Key{Style: Style(i), GID: gid}
		}
	}
	if fm.finder == nil {
		return Key{}
	}
	if len(fm.faces) >= maxFaces {
		return Key{}
	}

	for _, cand := range fm.finder.FindRune(r) {
		fc, err := fitFace(cand, fm.CellWidth, fm.CellHeight)
		if err != nil {
			fm.warnf("%s: %v", cand.Name, err)
			continue
		}
		gid, err := fc.font.GlyphIndex(&fc.buf, r)
		if err != nil || gid == 0 {
			continue // the finder was wrong about the coverage
		}
		if segs, err := fc.font.LoadGlyph(&fc.buf, gid, fc.ppem, nil); err != nil || len(segs) == 0 {
			// No outline to rasterise. A colour emoji face is bitmaps, and this is
			// where that becomes visible: skip it and try the next candidate.
			fm.warnf("%s has %U but no outline for it", cand.Name, r)
			continue
		}

		style := Style(len(fm.faces))
		fm.faces = append(fm.faces, fc)
		fm.Atlas.addFace(fc)
		fm.forgetMisses() // this face may well have a rune an earlier search gave up on
		fm.warnf("%U drawn from %s", r, cand.Name)
		return Key{Style: style, GID: gid}
	}
	return Key{}
}

// forgetMisses drops the remembered boxes, so the next frame asks again now that there
// is one more face to ask. The hits stay: the face that answered has not changed.
//
// A search only offers a handful of candidates per rune, so a face loaded for one rune
// can easily carry another that was given up on before it was there — a CJK collection
// reached for one ideograph holds twenty thousand more.
func (fm *FontManager) forgetMisses() {
	for r, k := range fm.resolved {
		if k.GID == 0 {
			delete(fm.resolved, r)
		}
	}
}

func (fm *FontManager) warnf(format string, args ...any) {
	if fm.warn != nil {
		fm.warn(fmt.Sprintf(format, args...))
	}
}

// FaceName is what the face is called, for a message about it. The four styles carry
// the primary family's name.
func (fm *FontManager) FaceName(s Style) string {
	if fc := fm.face(s); fc != nil {
		return fc.name
	}
	return ""
}
