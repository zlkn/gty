package main

import (
	"image"
	"maps"
	"slices"
	"strings"

	"gty/internal/vte"
)

// dir is the axis a split divides: vertical puts its panes side by side.
type dir uint8

const (
	vertical dir = iota
	horizontal
)

// cursorShapeNames is how the config file spells the shapes.
var cursorShapeNames = map[string]vte.CursorShape{
	"block":     vte.CursorBlock,
	"bar":       vte.CursorBar,
	"underline": vte.CursorUnderline,
}

func cursorShapeList() string {
	return strings.Join(slices.Sorted(maps.Keys(cursorShapeNames)), " ")
}

// pane is a leaf of the layout tree: a terminal, where it is drawn, and the last frame read.
type pane struct {
	id         int             // stable label, never reused after a close
	rect       image.Rectangle // framebuffer px, divider excluded
	cols, rows int             // grid that fits rect after padding

	term    *vte.Terminal
	noShell bool // the shell failed to start; do not keep retrying

	// frame is the snapshot every part of a draw reads, so the glyphs, the paint and the
	// cursor cannot disagree. view is its backing store, refilled in place each frame.
	frame vte.Frame
	view  []vte.Row
	cache rowCache

	scroll  int    // lines back from the newest; 0 = pinned to the tail
	retired uint64 // what the terminal had shed when scroll was last adjusted; see follow

	// shown folds DECTCEM, the blink phase and the focus into one bit. text.Layout and the
	// rect quads must read the same value, or a cell goes blank.
	shown bool

	first, count uint32 // the pane's slice of the shared instance buffer
}

func newPane(id int) *pane {
	// No grid yet: the layout pass gives it one, and only then can a shell be told about it.
	return &pane{id: id, term: vte.New(0, 0)}
}

// snap takes the frame this draw reads.
func (p *pane) snap() {
	p.follow()
	p.frame = p.term.Frame(p.view, p.scroll)
	p.view, p.scroll = p.frame.Lines, p.frame.Scroll
	p.cache.fit(p.frame.HistLen + len(p.frame.Lines))
}

// follow keeps a scrolled-back view on the same text as lines retire under it. A view pinned
// to the tail is the only one that wants to move with them.
func (p *pane) follow() {
	was := p.retired
	p.retired = p.term.Retired()
	if p.scroll > 0 {
		p.scroll += int(p.retired - was)
	}
}

// scrollBy moves the view delta lines back through history (negative goes forward) and
// reports whether it moved.
func (p *pane) scrollBy(delta int) bool {
	was := p.scroll
	p.scroll = min(max(p.scroll+delta, 0), p.term.MaxScroll())
	return p.scroll != was
}

// cursorRow is the cursor's row within the window, and its column. ok is false when it is
// hidden, scrolled out of view, or past the grid.
func (p *pane) cursorRow() (row, col int, ok bool) {
	c := p.frame.Cursor
	if !p.shown || c.Col < 0 || c.Col >= p.cols {
		return 0, 0, false
	}
	// The window ends on the screen's last line, so scrolling back pushes the cursor down it.
	row = c.Row + p.frame.Scroll
	if row < 0 || row >= len(p.frame.Lines) {
		return 0, 0, false
	}
	return row, c.Col, true
}

// cursorCell is the cursor's cell box in framebuffer px, on the origin text.Layout lays glyphs
// from. It is inside p.rect by construction: padding + cols*cellW never exceeds Dx - padding.
func (p *pane) cursorCell(cellW, cellH int) (image.Rectangle, bool) {
	row, col, ok := p.cursorRow()
	if !ok {
		return image.Rectangle{}, false
	}
	pad := px(padding)
	x := p.rect.Min.X + pad + col*cellW
	y := p.rect.Min.Y + pad + row*cellH
	return image.Rect(x, y, x+cellW, y+cellH), true
}

// cursorQuads is the filled shape a focused pane draws in its cursor cell.
func cursorQuads(cell image.Rectangle, s vte.CursorShape) []image.Rectangle {
	switch s {
	case vte.CursorBar:
		w := min(px(cursorBarWidth), cell.Dx())
		return []image.Rectangle{image.Rect(cell.Min.X, cell.Min.Y, cell.Min.X+w, cell.Max.Y)}
	case vte.CursorUnderline:
		h := min(px(cursorUnderlineHeight), cell.Dy())
		return []image.Rectangle{image.Rect(cell.Min.X, cell.Max.Y-h, cell.Max.X, cell.Max.Y)}
	default:
		return []image.Rectangle{cell}
	}
}

// cursorOutline is the rim an unfocused pane draws instead of a fill. Leaving the glyph alone
// keeps the inverted cell, and so the ligature split, a focused-pane problem.
func cursorOutline(cell image.Rectangle) []image.Rectangle {
	w := min(px(cursorOutlineWidth), cell.Dx())
	h := min(px(cursorOutlineWidth), cell.Dy())
	return []image.Rectangle{
		image.Rect(cell.Min.X, cell.Min.Y, cell.Max.X, cell.Min.Y+h),
		image.Rect(cell.Min.X, cell.Max.Y-h, cell.Max.X, cell.Max.Y),
		image.Rect(cell.Min.X, cell.Min.Y, cell.Min.X+w, cell.Max.Y),
		image.Rect(cell.Max.X-w, cell.Min.Y, cell.Max.X, cell.Max.Y),
	}
}

// paintRects is a pane's painted background and its underlines, appended to dst so the caller
// keeps one buffer for the frame.
//
// Cells are coalesced into runs of one colour: a full screen would otherwise be twenty thousand
// quads, most of them identical.
func paintRects(dst []quad, p *pane, cellW, cellH int) []quad {
	pad, ul := px(padding), px(underlineHeight)
	x0, y0 := p.rect.Min.X+pad, p.rect.Min.Y+pad

	for i := range p.frame.Lines {
		cells := p.frame.Lines[i].Cells
		if len(cells) > p.cols {
			cells = cells[:p.cols]
		}
		y := y0 + i*cellH

		for c := 0; c < len(cells); {
			if !cells[c].Painted() {
				c++
				continue
			}
			_, bg := cellColors(cells[c])
			end := c + 1
			for end < len(cells) && cells[end].Painted() {
				if _, b := cellColors(cells[end]); b != bg {
					break
				}
				end++
			}
			dst = append(dst, quad{
				rect:  image.Rect(x0+c*cellW, y, x0+end*cellW, y+cellH),
				color: bg,
			})
			c = end
		}

		for c := range cells {
			if cells[c].Attrs&vte.AttrUnderline == 0 {
				continue
			}
			fg, _ := cellColors(cells[c])
			dst = append(dst, quad{
				rect:  image.Rect(x0+c*cellW, y+cellH-ul, x0+(c+1)*cellW, y+cellH),
				color: fg,
			})
		}
	}
	return dst
}

// cursorRects splits the cursors into the focused pane's fill and the rims the others draw,
// two groups because they take different colours.
func cursorRects(panes []*pane, focused *pane, cellW, cellH int) (fills, rims []image.Rectangle) {
	for _, p := range panes {
		cell, ok := p.cursorCell(cellW, cellH)
		if !ok {
			continue
		}
		if p == focused {
			fills = append(fills, cursorQuads(cell, p.frame.Cursor.Shape)...)
		} else {
			rims = append(rims, cursorOutline(cell)...)
		}
	}
	return fills, rims
}

// setGrid takes the grid from a layout pass and refits the terminal to it.
//
// Only on a real change: a layout pass runs on every damaged frame, and a resize costs a
// SIGWINCH, a reshape of every cached row, and the loss of any DECSTBM region.
func (p *pane) setGrid(cols, rows int) {
	if cols == p.cols && rows == p.rows {
		return
	}
	p.cols, p.rows = cols, rows
	p.cache.reset()
	p.term.Resize(cols, rows)
	p.scroll = min(p.scroll, p.term.MaxScroll())
}

// release ends the pane's shell. Safe to call twice.
func (p *pane) release() { p.term.Close() }

// node is either a leaf (pane != nil) or a split of exactly two children.
//
// split and close rewrite a node in place, so the tree needs no parent pointers
// and a *pane survives every layout change — which is what lets the focus be a
// pointer instead of an index that goes stale.
type node struct {
	pane  *pane
	dir   dir
	ratio float32
	kids  [2]*node
}

// split turns the leaf holding target into a split of target and nu, and reports
// whether target was found.
func (n *node) split(target *pane, d dir, nu *pane) bool {
	if n.pane != nil {
		if n.pane != target {
			return false
		}
		*n = node{dir: d, ratio: 0.5, kids: [2]*node{{pane: target}, {pane: nu}}}
		return true
	}
	for _, kid := range n.kids {
		if kid.split(target, d, nu) {
			return true
		}
	}
	return false
}

// close drops the leaf holding target, collapsing its parent into the surviving
// sibling, and returns the pane that should take the focus.
//
// found separates the two ways next can be nil: target was the root leaf, which has no
// parent to collapse and so takes the window with it, or target is not in this tree at
// all. Conflating them means a lookup miss closes the window.
func (n *node) close(target *pane) (next *pane, found bool) {
	if n.pane != nil {
		return nil, n.pane == target
	}
	for i, kid := range n.kids {
		if kid.pane == target {
			*n = *n.kids[1-i]
			return n.leaf(), true
		}
		if p, ok := kid.close(target); ok {
			return p, true
		}
	}
	return nil, false
}

// has asks the tree rather than the pane slice a layout pass produced, so it answers
// for a pane split in but not yet laid out.
func (n *node) has(target *pane) bool {
	if n.pane != nil {
		return n.pane == target
	}
	return n.kids[0].has(target) || n.kids[1].has(target)
}

// leaves appends every pane in the subtree, in layout order. Unlike layoutTree it
// gives no rects, so it answers before a layout pass has run.
func (n *node) leaves(dst []*pane) []*pane {
	if n.pane != nil {
		return append(dst, n.pane)
	}
	return n.kids[1].leaves(n.kids[0].leaves(dst))
}

// leaf is the subtree's first pane in layout order.
func (n *node) leaf() *pane {
	if n.pane != nil {
		return n.pane
	}
	return n.kids[0].leaf()
}

// nextPane is the pane after focused in layout order, wrapping around. It returns
// focused unchanged when there is nowhere else to go.
func nextPane(panes []*pane, focused *pane) *pane {
	for i, p := range panes {
		if p == focused {
			return panes[(i+1)%len(panes)]
		}
	}
	return focused
}

// layoutTree gives every pane its rect and grid, and returns the leaves in layout
// order — the order Ctrl+Tab walks — with the dividers between them.
func layoutTree(root *node, r image.Rectangle, cellW, cellH int) (panes []*pane, dividers []image.Rectangle) {
	pad := px(padding)
	var walk func(n *node, r image.Rectangle)
	walk = func(n *node, r image.Rectangle) {
		if n.pane != nil {
			p := n.pane
			p.rect = r
			p.setGrid(max(0, (r.Dx()-2*pad)/cellW), max(0, (r.Dy()-2*pad)/cellH))
			panes = append(panes, p)
			return
		}
		a, div, b := splitRect(r, n.dir, n.ratio)
		dividers = append(dividers, div)
		walk(n.kids[0], a)
		walk(n.kids[1], b)
	}
	walk(root, r)
	return panes, dividers
}

// splitRect divides r along d at ratio, reserving dividerWidth px for the line
// between the halves. A rect too small to divide yields empty halves rather than
// negative ones.
func splitRect(r image.Rectangle, d dir, ratio float32) (a, div, b image.Rectangle) {
	dw := px(dividerWidth)
	if d == vertical {
		avail := max(0, r.Dx()-dw)
		cut := r.Min.X + min(max(int(float32(avail)*ratio), 0), avail)
		end := min(cut+dw, r.Max.X)
		return image.Rect(r.Min.X, r.Min.Y, cut, r.Max.Y),
			image.Rect(cut, r.Min.Y, end, r.Max.Y),
			image.Rect(end, r.Min.Y, r.Max.X, r.Max.Y)
	}
	avail := max(0, r.Dy()-dw)
	cut := r.Min.Y + min(max(int(float32(avail)*ratio), 0), avail)
	end := min(cut+dw, r.Max.Y)
	return image.Rect(r.Min.X, r.Min.Y, r.Max.X, cut),
		image.Rect(r.Min.X, cut, r.Max.X, end),
		image.Rect(r.Min.X, end, r.Max.X, r.Max.Y)
}
