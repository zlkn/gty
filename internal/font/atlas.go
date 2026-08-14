package font

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The atlas covers printable ASCII plus every glyph GSUB can substitute in.
const (
	FirstASCII = 0x20
	LastASCII  = 0x7E

	// AtlasCols keeps the sheet roughly square at any cell size.
	AtlasCols = 16

	// StrideAlign is wgpu's COPY_BYTES_PER_ROW_ALIGNMENT. WriteTexture rejects a
	// BytesPerRow that is not a multiple of it, so Pix is allocated aligned
	// rather than repacked at upload time.
	StrideAlign = 256
)

// Key identifies one baked glyph. The style is part of the key because glyph
// numbering is per face: GID 1167 is a different glyph in Regular and in Bold.
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

	keys []Key
	slot map[Key]int
}

// Slot returns the key's slot in atlas pixels.
func (a *Atlas) Slot(k Key) (image.Rectangle, bool) {
	i, ok := a.slot[k]
	if !ok {
		return image.Rectangle{}, false
	}
	col, row := i%a.Cols, i/a.Cols
	return image.Rect(col*a.SlotW, row*a.SlotH, (col+1)*a.SlotW, (row+1)*a.SlotH), true
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

// DumpPNG writes the atlas as grayscale, for eyeballing under config.Debug.
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
	f    *sfnt.Font
	ppem fixed.Int26_6
	buf  sfnt.Buffer
	rast vector.Rasterizer
	mask image.Alpha
}

// draw composites gid onto dst with its pen (the baseline origin) at dot.
func (r *rasterizer) draw(dst *image.Alpha, gid GID, dot fixed.Point26_6) error {
	segs, err := r.f.LoadGlyph(&r.buf, gid, r.ppem, nil)
	if err != nil {
		return err
	}
	db := segs.Bounds().Add(dot)
	dr := image.Rect(db.Min.X.Floor(), db.Min.Y.Floor(), db.Max.X.Ceil(), db.Max.Y.Ceil())
	if dr.Empty() {
		return nil // blank by design: the space, and the ligature spacer glyphs
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
	for _, seg := range segs {
		switch seg.Op {
		case sfnt.SegmentOpMoveTo:
			r.rast.MoveTo(float32(seg.Args[0].X+biasX)/64, float32(seg.Args[0].Y+biasY)/64)
		case sfnt.SegmentOpLineTo:
			r.rast.LineTo(float32(seg.Args[0].X+biasX)/64, float32(seg.Args[0].Y+biasY)/64)
		case sfnt.SegmentOpQuadTo:
			r.rast.QuadTo(
				float32(seg.Args[0].X+biasX)/64, float32(seg.Args[0].Y+biasY)/64,
				float32(seg.Args[1].X+biasX)/64, float32(seg.Args[1].Y+biasY)/64)
		case sfnt.SegmentOpCubeTo:
			r.rast.CubeTo(
				float32(seg.Args[0].X+biasX)/64, float32(seg.Args[0].Y+biasY)/64,
				float32(seg.Args[1].X+biasX)/64, float32(seg.Args[1].Y+biasY)/64,
				float32(seg.Args[2].X+biasX)/64, float32(seg.Args[2].Y+biasY)/64)
		}
	}
	r.rast.Draw(&r.mask, r.mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(dst, dr, image.Opaque, image.Point{}, &r.mask, r.mask.Rect.Min, draw.Over)
	return nil
}

// glyphPadding measures how far ink escapes the cell box across the baked set.
//
// It is computed, never hardcoded. On this face the ASCII range alone comes out
// L0 R1 T0 B0, and a literal 1 would have survived a font swap and then quietly
// clipped every ligature: with GSUB glyphs in the set PadLeft jumps to ~3 cells,
// because a 4-cell ligature is drawn entirely in its last cell.
func glyphPadding(rs []*rasterizer, keys []Key, cellW, cellH, ascent int) (l, t, right, b int) {
	for _, k := range keys {
		r := rs[k.Style]
		bounds, _, err := r.f.GlyphBounds(&r.buf, k.GID, r.ppem, Hinting)
		if err != nil || bounds.Empty() {
			continue
		}
		// Bounds are relative to the pen: x=0 at the cell's left edge, y=0 at
		// the baseline, which sits ascent px below the top of the cell.
		l = max(l, -bounds.Min.X.Floor())
		right = max(right, bounds.Max.X.Ceil()-cellW)
		t = max(t, -(ascent + bounds.Min.Y.Floor()))
		b = max(b, ascent+bounds.Max.Y.Ceil()-cellH)
	}
	return l, t, right, b // seeded at 0, so every side is already clamped
}

// BakeAtlas rasterizes every style's glyph set into one sheet sized from fm's
// cell metrics.
func BakeAtlas(fm *FontManager) (*Atlas, error) {
	rasterizers := make([]*rasterizer, NumStyles)
	var keys []Key
	for i := range NumStyles {
		style := Style(i)
		f := fm.Font(style)
		rasterizers[style] = &rasterizer{f: f, ppem: fm.PPEM}
		for _, gid := range fm.Shaper(style).GlyphSet(f.NumGlyphs()) {
			keys = append(keys, Key{Style: style, GID: gid})
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("bake atlas: empty glyph set")
	}
	padL, padT, padR, padB := glyphPadding(rasterizers, keys, fm.CellWidth, fm.CellHeight, fm.Ascent)

	a := &Atlas{
		SlotW: fm.CellWidth + padL + padR,
		SlotH: fm.CellHeight + padT + padB,
		Cols:  AtlasCols,
		Rows:  (len(keys) + AtlasCols - 1) / AtlasCols,

		PadLeft: padL, PadTop: padT, PadRight: padR, PadBottom: padB,

		keys: keys,
		slot: make(map[Key]int, len(keys)),
	}
	for i, k := range keys {
		a.slot[k] = i
	}

	w, h := a.Cols*a.SlotW, a.Rows*a.SlotH
	stride := (w + StrideAlign - 1) / StrideAlign * StrideAlign
	a.Img = &image.Alpha{
		Pix:    make([]uint8, stride*h),
		Stride: stride,
		Rect:   image.Rect(0, 0, w, h),
	}

	for _, k := range keys {
		slot, _ := a.Slot(k)
		dot := fixed.P(slot.Min.X+padL, slot.Min.Y+padT+fm.Ascent)
		if err := rasterizers[k.Style].draw(a.Img, k.GID, dot); err != nil {
			return nil, fmt.Errorf("bake %s glyph %d: %w", k.Style, k.GID, err)
		}
	}
	return a, nil
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
