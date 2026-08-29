package font

import (
	"image"
	"math"

	"golang.org/x/image/math/fixed"
)

// The box-drawing and block-element glyphs are drawn here rather than taken from the
// face. Nothing hints an outline (see Hinting), so the face's own spill across two pixel
// columns and overhang the cell, which seams at every boundary. These are whole pixels
// and stop at the cell edge.

const (
	firstBoxRune   = 0x2500
	lastBoxRune    = 0x259F
	firstBlockRune = 0x2580

	firstArcRune = 0x256D
	lastArcRune  = 0x2570

	firstDiagRune = 0x2571
	lastDiagRune  = 0x2573
)

func boxRune(r rune) bool { return r >= firstBoxRune && r <= lastBoxRune }

// lightRatio is a light line's share of the em: 1 px at 16, 2 at 34, 4 at 68.
const lightRatio = 1.0 / 16

func lightThickness(ppem fixed.Int26_6) int {
	return max(1, int(math.Round(float64(ppem)/64*lightRatio)))
}

// weight is how a line glyph draws one arm, ordered by width so wider can compare them.
type weight uint8

const (
	blank weight = iota
	light
	heavy
	double
)

func width(w weight, thin int) int {
	switch w {
	case light:
		return thin
	case heavy:
		return max(thin+1, 2*thin)
	case double:
		return 3 * thin // two stems and the gap between them
	}
	return 0
}

func wider(a, b weight) weight { return max(a, b) }

// arms is a line glyph: a weight per arm, and the dashes the line breaks into — zero for
// a solid one. No dashed glyph has a corner.
type arms struct {
	up, right, down, left weight
	dash                  uint8
}

// lineGlyphs is U+2500..257F by index, less the arcs and diagonals, which are not made
// of arms and are drawn by rune. The four Unicode names of an arm — "light down and
// right heavy" and the like — are exactly these fields.
var lineGlyphs = [0x80]arms{
	0x00: {right: light, left: light},              // ─
	0x01: {right: heavy, left: heavy},              // ━
	0x02: {up: light, down: light},                 // │
	0x03: {up: heavy, down: heavy},                 // ┃
	0x04: {right: light, left: light, dash: 3},     // ┄
	0x05: {right: heavy, left: heavy, dash: 3},     // ┅
	0x06: {up: light, down: light, dash: 3},        // ┆
	0x07: {up: heavy, down: heavy, dash: 3},        // ┇
	0x08: {right: light, left: light, dash: 4},     // ┈
	0x09: {right: heavy, left: heavy, dash: 4},     // ┉
	0x0A: {up: light, down: light, dash: 4},        // ┊
	0x0B: {up: heavy, down: heavy, dash: 4},        // ┋
	0x0C: {down: light, right: light},              // ┌
	0x0D: {down: light, right: heavy},              // ┍
	0x0E: {down: heavy, right: light},              // ┎
	0x0F: {down: heavy, right: heavy},              // ┏
	0x10: {down: light, left: light},               // ┐
	0x11: {down: light, left: heavy},               // ┑
	0x12: {down: heavy, left: light},               // ┒
	0x13: {down: heavy, left: heavy},               // ┓
	0x14: {up: light, right: light},                // └
	0x15: {up: light, right: heavy},                // ┕
	0x16: {up: heavy, right: light},                // ┖
	0x17: {up: heavy, right: heavy},                // ┗
	0x18: {up: light, left: light},                 // ┘
	0x19: {up: light, left: heavy},                 // ┙
	0x1A: {up: heavy, left: light},                 // ┚
	0x1B: {up: heavy, left: heavy},                 // ┛
	0x1C: {up: light, down: light, right: light},   // ├
	0x1D: {up: light, down: light, right: heavy},   // ┝
	0x1E: {up: heavy, down: light, right: light},   // ┞
	0x1F: {up: light, down: heavy, right: light},   // ┟
	0x20: {up: heavy, down: heavy, right: light},   // ┠
	0x21: {up: heavy, down: light, right: heavy},   // ┡
	0x22: {up: light, down: heavy, right: heavy},   // ┢
	0x23: {up: heavy, down: heavy, right: heavy},   // ┣
	0x24: {up: light, down: light, left: light},    // ┤
	0x25: {up: light, down: light, left: heavy},    // ┥
	0x26: {up: heavy, down: light, left: light},    // ┦
	0x27: {up: light, down: heavy, left: light},    // ┧
	0x28: {up: heavy, down: heavy, left: light},    // ┨
	0x29: {up: heavy, down: light, left: heavy},    // ┩
	0x2A: {up: light, down: heavy, left: heavy},    // ┪
	0x2B: {up: heavy, down: heavy, left: heavy},    // ┫
	0x2C: {down: light, left: light, right: light}, // ┬
	0x2D: {down: light, left: heavy, right: light}, // ┭
	0x2E: {down: light, left: light, right: heavy}, // ┮
	0x2F: {down: light, left: heavy, right: heavy}, // ┯
	0x30: {down: heavy, left: light, right: light}, // ┰
	0x31: {down: heavy, left: heavy, right: light}, // ┱
	0x32: {down: heavy, left: light, right: heavy}, // ┲
	0x33: {down: heavy, left: heavy, right: heavy}, // ┳
	0x34: {up: light, left: light, right: light},   // ┴
	0x35: {up: light, left: heavy, right: light},   // ┵
	0x36: {up: light, left: light, right: heavy},   // ┶
	0x37: {up: light, left: heavy, right: heavy},   // ┷
	0x38: {up: heavy, left: light, right: light},   // ┸
	0x39: {up: heavy, left: heavy, right: light},   // ┹
	0x3A: {up: heavy, left: light, right: heavy},   // ┺
	0x3B: {up: heavy, left: heavy, right: heavy},   // ┻

	0x3C: {up: light, down: light, left: light, right: light}, // ┼
	0x3D: {up: light, down: light, left: heavy, right: light}, // ┽
	0x3E: {up: light, down: light, left: light, right: heavy}, // ┾
	0x3F: {up: light, down: light, left: heavy, right: heavy}, // ┿
	0x40: {up: heavy, down: light, left: light, right: light}, // ╀
	0x41: {up: light, down: heavy, left: light, right: light}, // ╁
	0x42: {up: heavy, down: heavy, left: light, right: light}, // ╂
	0x43: {up: heavy, down: light, left: heavy, right: light}, // ╃
	0x44: {up: heavy, down: light, left: light, right: heavy}, // ╄
	0x45: {up: light, down: heavy, left: heavy, right: light}, // ╅
	0x46: {up: light, down: heavy, left: light, right: heavy}, // ╆
	0x47: {up: heavy, down: light, left: heavy, right: heavy}, // ╇
	0x48: {up: light, down: heavy, left: heavy, right: heavy}, // ╈
	0x49: {up: heavy, down: heavy, left: heavy, right: light}, // ╉
	0x4A: {up: heavy, down: heavy, left: light, right: heavy}, // ╊
	0x4B: {up: heavy, down: heavy, left: heavy, right: heavy}, // ╋

	0x4C: {right: light, left: light, dash: 2}, // ╌
	0x4D: {right: heavy, left: heavy, dash: 2}, // ╍
	0x4E: {up: light, down: light, dash: 2},    // ╎
	0x4F: {up: heavy, down: heavy, dash: 2},    // ╏

	0x50: {right: double, left: double},                           // ═
	0x51: {up: double, down: double},                              // ║
	0x52: {down: light, right: double},                            // ╒
	0x53: {down: double, right: light},                            // ╓
	0x54: {down: double, right: double},                           // ╔
	0x55: {down: light, left: double},                             // ╕
	0x56: {down: double, left: light},                             // ╖
	0x57: {down: double, left: double},                            // ╗
	0x58: {up: light, right: double},                              // ╘
	0x59: {up: double, right: light},                              // ╙
	0x5A: {up: double, right: double},                             // ╚
	0x5B: {up: light, left: double},                               // ╛
	0x5C: {up: double, left: light},                               // ╜
	0x5D: {up: double, left: double},                              // ╝
	0x5E: {up: light, down: light, right: double},                 // ╞
	0x5F: {up: double, down: double, right: light},                // ╟
	0x60: {up: double, down: double, right: double},               // ╠
	0x61: {up: light, down: light, left: double},                  // ╡
	0x62: {up: double, down: double, left: light},                 // ╢
	0x63: {up: double, down: double, left: double},                // ╣
	0x64: {down: light, left: double, right: double},              // ╤
	0x65: {down: double, left: light, right: light},               // ╥
	0x66: {down: double, left: double, right: double},             // ╦
	0x67: {up: light, left: double, right: double},                // ╧
	0x68: {up: double, left: light, right: light},                 // ╨
	0x69: {up: double, left: double, right: double},               // ╩
	0x6A: {up: light, down: light, left: double, right: double},   // ╪
	0x6B: {up: double, down: double, left: light, right: light},   // ╫
	0x6C: {up: double, down: double, left: double, right: double}, // ╬

	// 0x6D..0x73 are the arcs and the diagonals; see drawArc and drawDiagonal.

	0x74: {left: light},               // ╴
	0x75: {up: light},                 // ╵
	0x76: {right: light},              // ╶
	0x77: {down: light},               // ╷
	0x78: {left: heavy},               // ╸
	0x79: {up: heavy},                 // ╹
	0x7A: {right: heavy},              // ╺
	0x7B: {down: heavy},               // ╻
	0x7C: {left: light, right: heavy}, // ╼
	0x7D: {up: light, down: heavy},    // ╽
	0x7E: {left: heavy, right: light}, // ╾
	0x7F: {up: heavy, down: light},    // ╿
}

// drawBox paints r into cell, which is exactly one cell of the grid.
func drawBox(dst *image.Alpha, cell image.Rectangle, r rune, thin int) {
	switch {
	case r >= firstBlockRune:
		drawBlock(dst, cell, r)
	case r >= firstArcRune && r <= lastArcRune:
		drawArc(dst, cell, r, thin)
	case r >= firstDiagRune && r <= lastDiagRune:
		drawDiagonal(dst, cell, r, thin)
	default:
		drawLines(dst, cell, lineGlyphs[r-firstBoxRune], thin)
	}
}

// span is the range a stem of width w takes, centred between lo and hi. Every weight
// shares that centre, so a light stem sits inside the heavy one it meets.
func span(lo, hi, w int) (int, int) {
	a := lo + (hi-lo-w)/2
	return a, a + w
}

func drawLines(dst *image.Alpha, cell image.Rectangle, a arms, thin int) {
	if wider(wider(a.up, a.down), wider(a.left, a.right)) == double {
		drawDoubleLines(dst, cell, a, thin)
		return
	}
	if a.dash != 0 {
		drawDashes(dst, cell, a, thin)
		return
	}

	// Each arm reaches past the perpendicular band, which is what closes a corner. An
	// empty band stops a lone stub at the middle of the cell.
	vlo, vhi := span(cell.Min.X, cell.Max.X, width(wider(a.up, a.down), thin))
	hlo, hhi := span(cell.Min.Y, cell.Max.Y, width(wider(a.left, a.right), thin))

	if a.up != blank {
		lo, hi := span(cell.Min.X, cell.Max.X, width(a.up, thin))
		ink(dst, image.Rect(lo, cell.Min.Y, hi, hhi))
	}
	if a.down != blank {
		lo, hi := span(cell.Min.X, cell.Max.X, width(a.down, thin))
		ink(dst, image.Rect(lo, hlo, hi, cell.Max.Y))
	}
	if a.left != blank {
		lo, hi := span(cell.Min.Y, cell.Max.Y, width(a.left, thin))
		ink(dst, image.Rect(cell.Min.X, lo, vhi, hi))
	}
	if a.right != blank {
		lo, hi := span(cell.Min.Y, cell.Max.Y, width(a.right, thin))
		ink(dst, image.Rect(vlo, lo, cell.Max.X, hi))
	}
}

// drawDashes breaks the line into a.dash dashes, two units of ink to one of gap: 3n-1
// units in all, so a dash lands on each end.
func drawDashes(dst *image.Alpha, cell image.Rectangle, a arms, thin int) {
	// lo..hi is the axis the line runs down, slo..shi the stem across it.
	lo, hi := cell.Min.X, cell.Max.X
	slo, shi := span(cell.Min.Y, cell.Max.Y, width(wider(a.left, a.right), thin))
	vertical := a.up != blank
	if vertical {
		lo, hi = cell.Min.Y, cell.Max.Y
		slo, shi = span(cell.Min.X, cell.Max.X, width(wider(a.up, a.down), thin))
	}

	n := int(a.dash)
	units := 3*n - 1
	for i := range n {
		a0 := lo + (hi-lo)*(3*i)/units
		a1 := max(lo+(hi-lo)*(3*i+2)/units, a0+1)
		if vertical {
			ink(dst, image.Rect(slo, a0, shi, a1))
		} else {
			ink(dst, image.Rect(a0, slo, a1, shi))
		}
	}
}

type stem struct{ lo, hi int }

// axisStems is the ranges an axis's arms occupy: two and a gap for a double, otherwise
// one named twice — which is what lets a double meeting a single share the arithmetic.
func axisStems(lo, hi int, w weight, thin int) (a, b stem, dbl bool) {
	if w == double {
		s, _ := span(lo, hi, 3*thin)
		return stem{s, s + thin}, stem{s + 2*thin, s + 3*thin}, true
	}
	s0, s1 := span(lo, hi, width(w, thin))
	return stem{s0, s1}, stem{s0, s1}, false
}

// drawDoubleLines draws U+2550..256C. The side an arm is missing from is the outside of
// the frame, its nearest stem the outer one, and a line terminates on the stem it turns
// into. Where two doubles cross each is cut between the other's stems: that gap is the
// inside of the frame, and it is what makes a double read as one line.
func drawDoubleLines(dst *image.Alpha, cell image.Rectangle, a arms, thin int) {
	var v, h [2]stem
	var vd, hd bool
	v[0], v[1], vd = axisStems(cell.Min.X, cell.Max.X, wider(a.up, a.down), thin)
	h[0], h[1], hd = axisStems(cell.Min.Y, cell.Max.Y, wider(a.left, a.right), thin)
	up, right, down, left := a.up != blank, a.right != blank, a.down != blank, a.left != blank
	gap := vd && hd

	// The outer stem of each axis, by index.
	oi, oj := 0, 0
	if left {
		oi = 1
	}
	if up {
		oj = 1
	}
	// The stem a row terminates on, and the row a stem does. With both of the other
	// axis's arms present there is no outer line and both take the inner one.
	pairV := func(j int) stem {
		if up && down {
			return v[1-oi]
		}
		if j == oj {
			return v[oi]
		}
		return v[1-oi]
	}
	pairH := func(i int) stem {
		if left && right {
			return h[1-oj]
		}
		if i == oi {
			return h[oj]
		}
		return h[1-oj]
	}

	if left || right {
		for j, row := range h {
			x0, x1 := cell.Min.X, cell.Max.X
			if !left {
				x0 = pairV(j).lo
			}
			if !right {
				x1 = pairV(j).hi
			}
			if gap && ((j == 0 && up) || (j == 1 && down)) {
				ink(dst, image.Rect(x0, row.lo, v[0].hi, row.hi))
				ink(dst, image.Rect(v[1].lo, row.lo, x1, row.hi))
			} else {
				ink(dst, image.Rect(x0, row.lo, x1, row.hi))
			}
			if !hd {
				break // one row, named twice
			}
		}
	}
	if up || down {
		for i, col := range v {
			y0, y1 := cell.Min.Y, cell.Max.Y
			if !up {
				y0 = pairH(i).lo
			}
			if !down {
				y1 = pairH(i).hi
			}
			if gap && ((i == 0 && left) || (i == 1 && right)) {
				ink(dst, image.Rect(col.lo, y0, col.hi, h[0].hi))
				ink(dst, image.Rect(col.lo, h[1].lo, col.hi, y1))
			} else {
				ink(dst, image.Rect(col.lo, y0, col.hi, y1))
			}
			if !vd {
				break
			}
		}
	}
}

// arcSamples supersamples the only glyphs whose edges are not axis-aligned.
const arcSamples = 4

// drawArc draws the light arcs U+256D..2570: as wide a quarter turn as the cell allows,
// tangent to both arms, plus the straight run from there to the edge.
func drawArc(dst *image.Alpha, cell image.Rectangle, r rune, thin int) {
	// Which way the arms point; the centre of curvature is a radius away along both.
	sx, sy := 1.0, 1.0 // U+256D ╭, down and right
	switch r {
	case 0x256E: // ╮ down and left
		sx = -1
	case 0x256F: // ╯ up and left
		sx, sy = -1, -1
	case 0x2570: // ╰ up and right
		sy = -1
	}

	vlo, vhi := span(cell.Min.X, cell.Max.X, thin)
	hlo, hhi := span(cell.Min.Y, cell.Max.Y, thin)
	cx, cy := float64(vlo+vhi)/2, float64(hlo+hhi)/2

	radX, radY := float64(cell.Max.X)-cx, float64(cell.Max.Y)-cy
	if sx < 0 {
		radX = cx - float64(cell.Min.X)
	}
	if sy < 0 {
		radY = cy - float64(cell.Min.Y)
	}
	rad := math.Min(radX, radY)
	ox, oy := cx+sx*rad, cy+sy*rad

	// The arms run on from the tangent points, (cx, oy) and (ox, cy).
	tx, ty := int(math.Round(ox)), int(math.Round(oy))
	if sy > 0 {
		ink(dst, image.Rect(vlo, ty, vhi, cell.Max.Y))
	} else {
		ink(dst, image.Rect(vlo, cell.Min.Y, vhi, ty))
	}
	if sx > 0 {
		ink(dst, image.Rect(tx, hlo, cell.Max.X, hhi))
	} else {
		ink(dst, image.Rect(cell.Min.X, hlo, tx, hhi))
	}

	half := float64(thin) / 2
	sample(dst, cell, func(px, py float64) bool {
		if (px-ox)*sx > 0 || (py-oy)*sy > 0 { // the quarter facing the corner
			return false
		}
		return math.Abs(math.Hypot(px-ox, py-oy)-rad) <= half
	})
}

// drawDiagonal draws U+2571..2573, corner to corner, and for ╳ both ways.
func drawDiagonal(dst *image.Alpha, cell image.Rectangle, r rune, thin int) {
	up := r != 0x2572   // ╱ and ╳ run from the bottom left to the top right
	down := r != 0x2571 // ╲ and ╳ run from the top left to the bottom right
	half := float64(thin) / 2
	w, h := float64(cell.Dx()), float64(cell.Dy())
	norm := math.Hypot(w, h)

	sample(dst, cell, func(px, py float64) bool {
		x := px - float64(cell.Min.X)
		y := py - float64(cell.Min.Y)
		// Distance to the infinite line; the cell does the clipping.
		if up && math.Abs(h*x+w*y-w*h)/norm <= half {
			return true
		}
		return down && math.Abs(h*x-w*y)/norm <= half
	})
}

// sample inks each pixel with the share of its supersamples inside accepts — the one
// path here that antialiases.
func sample(dst *image.Alpha, cell image.Rectangle, inside func(x, y float64) bool) {
	const n = arcSamples
	for y := cell.Min.Y; y < cell.Max.Y; y++ {
		for x := cell.Min.X; x < cell.Max.X; x++ {
			hits := 0
			for j := range n {
				for i := range n {
					px := float64(x) + (float64(i)+0.5)/n
					py := float64(y) + (float64(j)+0.5)/n
					if inside(px, py) {
						hits++
					}
				}
			}
			if hits > 0 {
				inkAt(dst, x, y, uint8(hits*0xFF/(n*n)))
			}
		}
	}
}

// quadrants is U+2596..259F as a mask over the cell's corners.
var quadrants = [10]uint8{
	0x2596 - 0x2596: quadLL,                   // ▖
	0x2597 - 0x2596: quadLR,                   // ▗
	0x2598 - 0x2596: quadUL,                   // ▘
	0x2599 - 0x2596: quadUL | quadLL | quadLR, // ▙
	0x259A - 0x2596: quadUL | quadLR,          // ▚
	0x259B - 0x2596: quadUL | quadUR | quadLL, // ▛
	0x259C - 0x2596: quadUL | quadUR | quadLR, // ▜
	0x259D - 0x2596: quadUR,                   // ▝
	0x259E - 0x2596: quadUR | quadLL,          // ▞
	0x259F - 0x2596: quadUR | quadLL | quadLR, // ▟
}

const (
	quadUL uint8 = 1 << iota
	quadUR
	quadLL
	quadLR
)

// shades is the alpha U+2591..2593 take. Flat rather than dithered: the pattern would
// beat against the pixel grid, and ink mixed with paper is what an alpha already is.
var shades = [3]uint8{0x40, 0x80, 0xBF}

// drawBlock draws the block elements U+2580..259F.
func drawBlock(dst *image.Alpha, cell image.Rectangle, r rune) {
	w, h := cell.Dx(), cell.Dy()
	// Eighths of size: eight of them are the whole cell, and one never rounds away.
	part := func(size, n int) int { return max((size*n+4)/8, 1) }

	switch {
	case r == 0x2580: // ▀ upper half
		ink(dst, image.Rect(cell.Min.X, cell.Min.Y, cell.Max.X, cell.Min.Y+part(h, 4)))
	case r <= 0x2588: // ▁▂▃▄▅▆▇█ the lower n eighths, up to the full block
		n := int(r - 0x2580)
		ink(dst, image.Rect(cell.Min.X, cell.Max.Y-part(h, n), cell.Max.X, cell.Max.Y))
	case r <= 0x258F: // ▉▊▋▌▍▎▏ the left n eighths, seven down to one
		n := 8 - int(r-0x2588)
		ink(dst, image.Rect(cell.Min.X, cell.Min.Y, cell.Min.X+part(w, n), cell.Max.Y))
	case r == 0x2590: // ▐ right half
		ink(dst, image.Rect(cell.Max.X-part(w, 4), cell.Min.Y, cell.Max.X, cell.Max.Y))
	case r <= 0x2593: // ░▒▓
		shade(dst, cell, shades[r-0x2591])
	case r == 0x2594: // ▔ upper one eighth
		ink(dst, image.Rect(cell.Min.X, cell.Min.Y, cell.Max.X, cell.Min.Y+part(h, 1)))
	case r == 0x2595: // ▕ right one eighth
		ink(dst, image.Rect(cell.Max.X-part(w, 1), cell.Min.Y, cell.Max.X, cell.Max.Y))
	default: // ▖▗▘▙▚▛▜▝▞▟
		mask := quadrants[r-0x2596]
		mx, my := cell.Min.X+w/2, cell.Min.Y+h/2
		for _, q := range []struct {
			bit uint8
			box image.Rectangle
		}{
			{quadUL, image.Rect(cell.Min.X, cell.Min.Y, mx, my)},
			{quadUR, image.Rect(mx, cell.Min.Y, cell.Max.X, my)},
			{quadLL, image.Rect(cell.Min.X, my, mx, cell.Max.Y)},
			{quadLR, image.Rect(mx, my, cell.Max.X, cell.Max.Y)},
		} {
			if mask&q.bit != 0 {
				ink(dst, q.box)
			}
		}
	}
}

func ink(dst *image.Alpha, r image.Rectangle) { shade(dst, r, 0xFF) }

func shade(dst *image.Alpha, r image.Rectangle, cov uint8) {
	r = r.Intersect(dst.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := dst.Pix[dst.PixOffset(r.Min.X, y):]
		for x := range r.Dx() {
			row[x] = cov
		}
	}
}

// inkAt keeps the heavier coverage: an arc overlaps the arm it runs into, and the arm is
// what must not thin at the joint.
func inkAt(dst *image.Alpha, x, y int, cov uint8) {
	if !(image.Point{x, y}).In(dst.Bounds()) {
		return
	}
	if i := dst.PixOffset(x, y); dst.Pix[i] < cov {
		dst.Pix[i] = cov
	}
}
