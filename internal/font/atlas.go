package font

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const (
	FirstASCII = 0x20
	LastASCII  = 0x7E

	// StrideAlign is wgpu's COPY_BYTES_PER_ROW_ALIGNMENT. WriteTexture rejects a
	// BytesPerRow that is not a multiple of it, so Pix is allocated aligned
	// rather than repacked at upload time.
	StrideAlign = 256
)

// Key identifies one baked glyph. The style is part of the key because glyph
// numbering is per face: GID 1167 is a different glyph in Regular and in Bold, and
// a different one again in every face of the fallback chain.
type Key struct {
	Style Style
	GID   GID
}

// Atlas is the baked glyph sheet, keyed by style and glyph ID.
//
// Every style shares one sheet. They agree on the slot size (NewManager rejects
// faces that do not), so the slots tile as a single grid and the renderer needs
// one texture and one bind group for all four.
//
// Pix is padded to StrideAlign while Rect keeps the real width: image/draw and
// the rasterizer honour Stride, so nothing downstream has to know, and UVs
// divide by Rect because the texture is Rect-sized — the stride only pads the
// copy.
//
// The quad is the size of a slot, not of a cell. Cell origins still step by
// CellWidth; a glyph legally overhangs into its neighbours, which is how the
// font draws ligatures and how alpha blending puts them back together. The
// renderer places cell (col,row) at
//
//	(col*CellWidth - PadLeft, row*CellHeight - PadTop)
//
// with quad size (SlotW, SlotH) and uv_cell (SlotW/AtlasW, SlotH/AtlasH).
type Atlas struct {
	Img          *image.Alpha
	SlotW, SlotH int
	Cols, Rows   int

	// Offset of the cell origin inside its slot. PadLeft is large once
	// ligatures are baked: their ink reaches back over the cells they span.
	PadLeft, PadTop     int
	PadRight, PadBottom int

	// Ascent is the baseline's offset from the top of the cell, kept here so a
	// glyph baked later lands on the same line as the ones baked at startup.
	Ascent int

	keys []Key
	slot map[Key]int

	// The sheet is filled as glyphs are asked for. Printable ASCII and everything
	// GSUB can substitute in are baked up front, because a terminal needs all of
	// them within the first frame; the rest of the face — Cyrillic, box drawing,
	// arrows, the icons in a shell prompt — is rasterised on the frame that first
	// wants it.
	//
	// Which is what makes a large face affordable. A Nerd Font-patched JetBrains Mono
	// carries 12,608 glyphs a style: a slot for every one of them across four styles
	// is a sheet 9225 px wide, past what any GPU will allocate. Only the glyphs a
	// session actually reaches are ever rasterised, and the sheet grows rows to suit.
	//
	// No lock guards any of this. Everything that touches the atlas — the parser,
	// the layout pass, the upload — runs on the main thread.
	rast    []*rasterizer
	free    int   // next unbaked slot
	dirty   []int // slots written since the last TakeDirty
	maxRows int   // as many as the device's texture limit allows

	// notdef is the slot holding the hollow box drawn for a rune the face has no
	// glyph for. Drawing nothing would be indistinguishable from a space.
	notdef int
}

// Ensure is the key's position in texture coordinates, rasterising it into the sheet
// if this is the first time it has been asked for.
//
// A glyph the face does not have — GID 0 — comes back as the replacement box, so a
// missing character leaves a visible hole rather than a silent one.
func (a *Atlas) Ensure(k Key) (u, v float32) {
	i := a.notdef
	if k.GID != 0 {
		if j, in := a.slot[k]; in {
			i = j
		} else if j, err := a.bake(k); err == nil {
			i = j
		}
	}
	return a.slotUV(i)
}

// bake rasterises one glyph into the next free slot, adding rows if there are none.
func (a *Atlas) bake(k Key) (int, error) {
	if int(k.Style) >= len(a.rast) {
		return 0, fmt.Errorf("no %s face loaded", k.Style)
	}
	r := a.rast[k.Style]
	if a.free >= a.Cols*a.Rows && !a.grow() {
		return 0, fmt.Errorf("atlas full at %d slots", a.free)
	}
	i := a.free
	slot := a.slotRect(i)
	// The face's own baseline, not the grid's: a fallback is a different design
	// fitted to the cell, and it sits on a line of its own.
	dot := fixed.P(slot.Min.X+a.PadLeft, slot.Min.Y+a.PadTop+r.ascent)
	if err := r.draw(a.Img, k.GID, dot, a.fitBox(slot, r.fit), slot); err != nil {
		return 0, err
	}

	a.free++
	a.slot[k] = i
	a.keys = append(a.keys, k)
	a.dirty = append(a.dirty, i)
	return i, nil
}

// grow adds rows to the sheet, up to what the device's texture limit allows.
//
// Cols never changes, so every slot keeps its position and everything already
// rasterised stays exactly where it was: the new buffer opens with the old one's
// bytes. Only the renderer has work to do, and it notices by the height changing.
func (a *Atlas) grow() bool {
	if a.Rows >= a.maxRows {
		return false
	}
	rows := min(max(2*a.Rows, a.Rows+1), a.maxRows)
	pix := make([]uint8, a.Img.Stride*rows*a.SlotH)
	copy(pix, a.Img.Pix)

	a.Rows = rows
	a.Img = &image.Alpha{
		Pix:    pix,
		Stride: a.Img.Stride,
		Rect:   image.Rect(0, 0, a.Img.Rect.Dx(), rows*a.SlotH),
	}
	return true
}

// addFace appends a rasterizer for a face that joined the fallback chain mid-session,
// keeping a.rast indexed exactly like FontManager.faces.
//
// The sheet is not re-measured, and does not have to be: a fitted face draws inside
// its cell by construction, which is what fitBox is for. Nothing already baked moves.
func (a *Atlas) addFace(fc *face) {
	a.rast = append(a.rast, &rasterizer{
		f: fc.font, ppem: fc.ppem, ascent: fc.ascent, fit: fc.fitted,
	})
}

// fitBox is the box a glyph from a fitted face has to stay inside: its own cell, plus
// the bleed the family itself is allowed on the other three sides.
//
// Not the whole slot. PadLeft is about three cells wide — the reach-back a ligature is
// drawn with — and a fallback glyph spilling that far would be painted over the
// characters before it. The small allowance is there because the shrink is decided from
// the outline's control points, which sit a fraction outside the ink.
//
// The zero rectangle means "do not shrink": that is the primary, whose overhang the
// atlas was measured for.
func (a *Atlas) fitBox(slot image.Rectangle, fit bool) image.Rectangle {
	if !fit {
		return image.Rectangle{}
	}
	// Which is the slot with the reach-back trimmed off its left edge.
	return image.Rect(slot.Min.X+a.PadLeft, slot.Min.Y, slot.Max.X, slot.Max.Y)
}

// TakeDirty is the slots written since the last call, for the renderer to copy up.
// The whole sheet is dirty after the eager bake.
func (a *Atlas) TakeDirty() []int {
	d := a.dirty
	a.dirty = nil
	return d
}

// SlotRect is slot i in atlas pixels.
func (a *Atlas) SlotRect(i int) image.Rectangle { return a.slotRect(i) }

func (a *Atlas) slotRect(i int) image.Rectangle {
	col, row := i%a.Cols, i/a.Cols
	return image.Rect(col*a.SlotW, row*a.SlotH, (col+1)*a.SlotW, (row+1)*a.SlotH)
}

func (a *Atlas) slotUV(i int) (u, v float32) {
	r := a.slotRect(i)
	return float32(r.Min.X) / float32(a.Img.Rect.Dx()), float32(r.Min.Y) / float32(a.Img.Rect.Dy())
}

// Slot returns the key's slot in atlas pixels, if it has been baked.
func (a *Atlas) Slot(k Key) (image.Rectangle, bool) {
	i, ok := a.slot[k]
	if !ok {
		return image.Rectangle{}, false
	}
	return a.slotRect(i), true
}

// GlyphUV returns the top-left corner of the key's slot in texture coordinates.
// ok is false for a glyph that was never baked.
func (a *Atlas) GlyphUV(k Key) (u, v float32, ok bool) {
	slot, ok := a.Slot(k)
	if !ok {
		return 0, 0, false
	}
	return float32(slot.Min.X) / float32(a.Img.Rect.Dx()),
		float32(slot.Min.Y) / float32(a.Img.Rect.Dy()), true
}

// Glyphs is the baked set, in slot order.
func (a *Atlas) Glyphs() []Key { return a.keys }

// DumpPNG writes the atlas as grayscale, for eyeballing the baked sheet by hand.
//
// png.Encode on an *image.Alpha yields an invisible image — color.Alpha is
// premultiplied white, so the glyphs are white on transparent. image.Gray has an
// identical byte layout, so aliasing the same Pix costs nothing and makes them
// visible.
func (a *Atlas) DumpPNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gray := &image.Gray{Pix: a.Img.Pix, Stride: a.Img.Stride, Rect: a.Img.Rect}
	if err := png.Encode(f, gray); err != nil {
		f.Close()
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return f.Close()
}

// rasterizer draws glyphs by ID — the one thing xfont.Face cannot do, since its
// whole API is keyed by rune.
//
// The arithmetic is opentype.Face.Glyph's, kept identical deliberately: it makes
// this path produce pixel-for-pixel the same ASCII glyphs as the rune-keyed one
// it replaces.
type rasterizer struct {
	f      *sfnt.Font
	ppem   fixed.Int26_6
	ascent int  // the baseline this face draws on, from the top of the cell
	fit    bool // shrink a glyph that does not fit its cell; see Atlas.fitBox
	buf    sfnt.Buffer
	rast   vector.Rasterizer
	mask   image.Alpha
}

// draw composites gid onto dst with its pen (the baseline origin) at dot.
//
// fit, when not the zero rectangle, is a box the ink is scaled down to fit and then
// centred horizontally in — how a face that was not designed for this grid is kept
// inside its cell. clip is the hard boundary: whatever would land outside the glyph's
// own slot is dropped rather than scribbled over a neighbour's.
func (r *rasterizer) draw(dst *image.Alpha, gid GID, dot fixed.Point26_6, fit, clip image.Rectangle) error {
	segs, err := r.f.LoadGlyph(&r.buf, gid, r.ppem, nil)
	if err != nil {
		return err
	}
	db := segs.Bounds().Add(dot)
	dr := image.Rect(db.Min.X.Floor(), db.Min.Y.Floor(), db.Max.X.Ceil(), db.Max.Y.Ceil())
	if dr.Empty() {
		return nil // blank by design: the space, and the ligature spacer glyphs
	}

	// Shrink to the box, then move what is left into the middle of it: an outline three
	// cells wide comes back one cell wide and centred, instead of one cell of it and
	// two cells of its neighbours. Whole pixels, so the ink and the rectangle tracking
	// it cannot drift apart.
	scale, shift := float32(1), fixed.Point26_6{}
	if !fit.Empty() && !dr.In(fit) {
		scale = fitScale(dr, fit)
		dr = scaleRect(dr, dot, scale)
		dx, dy := center(dr, fit)
		shift, dr = fixed.P(dx, dy), dr.Add(image.Pt(dx, dy))
	}
	if dr = dr.Intersect(clip); dr.Empty() {
		return nil
	}

	// Bias the outline into rasterizer space, whose origin is dr.Min.
	biasX := dot.X - fixed.Int26_6(dr.Min.X<<6)
	biasY := dot.Y - fixed.Int26_6(dr.Min.Y<<6)
	w, h := dr.Dx(), dr.Dy()
	if n := w * h; cap(r.mask.Pix) < n {
		r.mask.Pix = make([]uint8, 2*n)
	}
	r.mask.Pix = r.mask.Pix[:w*h]
	r.mask.Stride = w
	r.mask.Rect = image.Rect(0, 0, w, h)

	r.rast.Reset(w, h)
	r.rast.DrawOp = draw.Src
	// One control point in rasterizer space. Segment coordinates are relative to the
	// pen, so scaling is a plain multiply; the bias carries the pen and the slot.
	at := func(p fixed.Point26_6) (x, y float32) {
		return (float32(p.X)*scale + float32(biasX+shift.X)) / 64,
			(float32(p.Y)*scale + float32(biasY+shift.Y)) / 64
	}
	for _, seg := range segs {
		switch seg.Op {
		case sfnt.SegmentOpMoveTo:
			x, y := at(seg.Args[0])
			r.rast.MoveTo(x, y)
		case sfnt.SegmentOpLineTo:
			x, y := at(seg.Args[0])
			r.rast.LineTo(x, y)
		case sfnt.SegmentOpQuadTo:
			x1, y1 := at(seg.Args[0])
			x2, y2 := at(seg.Args[1])
			r.rast.QuadTo(x1, y1, x2, y2)
		case sfnt.SegmentOpCubeTo:
			x1, y1 := at(seg.Args[0])
			x2, y2 := at(seg.Args[1])
			x3, y3 := at(seg.Args[2])
			r.rast.CubeTo(x1, y1, x2, y2, x3, y3)
		}
	}
	r.rast.Draw(&r.mask, r.mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(dst, dr, image.Opaque, image.Point{}, &r.mask, r.mask.Rect.Min, draw.Over)
	return nil
}

// fitScale is how much a glyph has to shrink for its ink to fit inside box, never above
// 1: a glyph smaller than its cell is left at its own size.
//
// Sizes, not distances from the pen. Scaling about the pen alone cannot fix a glyph
// whose ink reaches to the left of it — and most do, by a fraction — because the pen
// sits on the cell's own left edge: there is no room on that side to scale into, and
// the arithmetic collapses to zero.
func fitScale(ink, box image.Rectangle) float32 {
	// A pixel of slack on each axis. Scaling to the box exactly still lands a pixel
	// outside it: the edges come out fractional, and coverage spreads into whichever
	// pixel an edge touches.
	w, h := max(box.Dx()-1, 1), max(box.Dy()-1, 1)
	s := float32(1)
	if ink.Dx() > w {
		s = min(s, float32(w)/float32(ink.Dx()))
	}
	if ink.Dy() > h {
		s = min(s, float32(h)/float32(ink.Dy()))
	}
	return s
}

// center is the whole-pixel shift that puts ink in the middle of box.
func center(ink, box image.Rectangle) (dx, dy int) {
	return (box.Min.X + box.Max.X - ink.Min.X - ink.Max.X) / 2,
		(box.Min.Y + box.Max.Y - ink.Min.Y - ink.Max.Y) / 2
}

// scaleRect is ink scaled about dot, rounded outward so the box still contains the ink.
func scaleRect(ink image.Rectangle, dot fixed.Point26_6, scale float32) image.Rectangle {
	x, y := float64(dot.X)/64, float64(dot.Y)/64
	at := func(v int, origin float64) float64 { return origin + (float64(v)-origin)*float64(scale) }
	return image.Rect(
		int(math.Floor(at(ink.Min.X, x))), int(math.Floor(at(ink.Min.Y, y))),
		int(math.Ceil(at(ink.Max.X, x))), int(math.Ceil(at(ink.Max.Y, y))),
	)
}

// glyphPadding measures how far ink escapes the cell box.
//
// It has to hold for every glyph in every face, not only the ones baked at startup: a
// glyph rasterised later cannot resize the grid it lands in. The font's own bounding
// box is exactly that maximum and costs one lookup, where walking the glyphs costs
// 46ms a face on a Nerd Font-patched file.
//
// It is computed, never hardcoded. The ASCII range alone comes out L0 R1 T0 B0, and a
// literal 1 would have survived a font swap and then quietly clipped every ligature:
// with GSUB glyphs in the set PadLeft jumps to about three cells, because a four-cell
// ligature is drawn entirely in its last one.
func glyphPadding(rs []*rasterizer, cellW, cellH int) (l, t, right, b int) {
	for _, r := range rs {
		if r.fit {
			// A fitted face is shrunk into its cell at bake time, so it needs no
			// room of its own — and must not ask for any: its face bounding box is
			// wide, and a face that joins the chain mid-session cannot resize a
			// sheet that already has glyphs in it.
			continue
		}
		bounds, err := r.f.Bounds(&r.buf, r.ppem, Hinting)
		if err != nil {
			continue
		}
		// Bounds are relative to the pen: x=0 at the cell's left edge, y=0 at the
		// baseline, which sits r.ascent px below the top of the cell — each face
		// measured against its own, because the fallback's is not the grid's.
		l = max(l, -bounds.Min.X.Floor())
		right = max(right, bounds.Max.X.Ceil()-cellW)
		t = max(t, -(r.ascent + bounds.Min.Y.Floor()))
		b = max(b, r.ascent+bounds.Max.Y.Ceil()-cellH)
	}
	return l, t, right, b // seeded at 0, so every side is already clamped
}

// faceGlyphs is the face's glyph count as a GID, clamped: a GID is sixteen bits and
// nothing addressable lives past that.
func faceGlyphs(f *sfnt.Font) GID { return GID(min(f.NumGlyphs(), 0xFFFF)) }

// spareRows is how much room the sheet keeps beyond the eager bake, so the first few
// hundred glyphs a session reaches do not each cost a reallocation.
const spareRows = 8

// BakeAtlas lays out the sheet and rasterises the glyphs a terminal needs in its first
// frame. The rest of the faces are baked by Ensure, on the frame that first asks.
//
// maxTexture is the device's largest 2D texture dimension, and it is a hard ceiling:
// the sheet is laid out as wide as it allows and grows downward within it.
func BakeAtlas(fm *FontManager, maxTexture int) (*Atlas, error) {
	rs := make([]*rasterizer, fm.NumFaces())
	var eager []Key
	for i, fc := range fm.faces {
		style := Style(i)
		rs[style] = &rasterizer{f: fc.font, ppem: fc.ppem, ascent: fc.ascent, fit: fc.fitted}
		if style >= Fallback {
			// Nothing in a first frame comes from the chain, and its faces run to
			// tens of thousands of glyphs. Ensure bakes them on the frame that asks.
			continue
		}
		for _, gid := range fc.shaper.GlyphSet(fc.font.NumGlyphs()) {
			eager = append(eager, Key{Style: style, GID: gid})
		}
	}
	if len(eager) == 0 {
		return nil, fmt.Errorf("bake atlas: empty glyph set")
	}
	padL, padT, padR, padB := glyphPadding(rs, fm.CellWidth, fm.CellHeight)

	a := &Atlas{
		SlotW: fm.CellWidth + padL + padR,
		SlotH: fm.CellHeight + padT + padB,

		PadLeft: padL, PadTop: padT, PadRight: padR, PadBottom: padB,
		Ascent: fm.Ascent,

		slot: make(map[Key]int, len(eager)),
		rast: rs,
	}
	if a.SlotW > maxTexture || a.SlotH > maxTexture {
		return nil, fmt.Errorf("a %dx%d glyph slot does not fit a %d px texture; the font size is too large",
			a.SlotW, a.SlotH, maxTexture)
	}

	// As wide as the device allows, and only as tall as the eager bake needs. Cols is
	// fixed for the life of the sheet so a slot never moves; height is what grows.
	// Laying out a slot per glyph instead would be a 9225 px sheet on a Nerd
	// Font-patched face — wider than any GPU will allocate.
	a.Cols = maxTexture / a.SlotW
	a.maxRows = maxTexture / a.SlotH
	a.Rows = min((len(eager)+1+a.Cols-1)/a.Cols+spareRows, a.maxRows)
	if a.Cols*a.Rows < len(eager)+1 {
		return nil, fmt.Errorf("a %d px texture holds %d glyph slots; the eager set needs %d",
			maxTexture, a.Cols*a.maxRows, len(eager)+1)
	}

	w, h := a.Cols*a.SlotW, a.Rows*a.SlotH
	stride := (w + StrideAlign - 1) / StrideAlign * StrideAlign
	a.Img = &image.Alpha{
		Pix:    make([]uint8, stride*h),
		Stride: stride,
		Rect:   image.Rect(0, 0, w, h),
	}

	// Slot zero, so a miss is cheap to reach and a debug dump opens with it.
	a.notdef = a.free
	a.drawNotdef()
	a.free++
	a.dirty = append(a.dirty, a.notdef)

	for _, k := range eager {
		if _, err := a.bake(k); err != nil {
			return nil, fmt.Errorf("bake %s glyph %d: %w", k.Style, k.GID, err)
		}
	}
	return a, nil
}

// drawNotdef paints the replacement box: a hollow rectangle the size of a cell. A rune
// the face has no glyph for is drawn with this instead of with nothing, so a missing
// character reads as missing rather than as a space.
func (a *Atlas) drawNotdef() {
	cellW := a.SlotW - a.PadLeft - a.PadRight
	cellH := a.SlotH - a.PadTop - a.PadBottom
	slot := a.slotRect(a.notdef)
	box := image.Rect(0, 0, cellW, cellH).Inset(1).
		Add(image.Pt(slot.Min.X+a.PadLeft, slot.Min.Y+a.PadTop))
	if box.Dx() < 2 || box.Dy() < 2 {
		return
	}

	ink := image.NewUniform(color.Alpha{A: 0xB0})
	for _, side := range []image.Rectangle{
		image.Rect(box.Min.X, box.Min.Y, box.Max.X, box.Min.Y+1),
		image.Rect(box.Min.X, box.Max.Y-1, box.Max.X, box.Max.Y),
		image.Rect(box.Min.X, box.Min.Y, box.Min.X+1, box.Max.Y),
		image.Rect(box.Max.X-1, box.Min.Y, box.Max.X, box.Max.Y),
	} {
		draw.Draw(a.Img, side, ink, image.Point{}, draw.Src)
	}
}

// RenderRow composes a row of cells on the CPU the way the renderer will on the
// GPU: one quad per cell, quad = slot, quad origin = cell origin - (PadLeft,
// PadTop), blended together. Used by the tests and by the debug dump — the GPU
// path does not go through it.
func (a *Atlas) RenderRow(keys []Key, cellW int) *image.Alpha {
	dst := image.NewAlpha(image.Rect(0, 0, a.PadLeft+len(keys)*cellW+a.PadRight, a.SlotH))
	for i, k := range keys {
		slot, ok := a.Slot(k)
		if !ok {
			continue
		}
		dr := image.Rect(i*cellW, 0, i*cellW+a.SlotW, a.SlotH)
		draw.DrawMask(dst, dr, image.Opaque, image.Point{}, a.Img, slot.Min, draw.Over)
	}
	return dst
}
