package main

import (
	"fmt"
	"image"
	"maps"
	"slices"
	"strings"

	"gty/internal/pty"
	"gty/internal/vt"
)

// dir is the axis a split divides: vertical puts its panes side by side.
type dir uint8

const (
	vertical dir = iota
	horizontal
)

// cursorShape is how the cursor marks its cell. DECSCUSR picks between these per pane;
// a pane that never gets one keeps cursorShapeDefault.
type cursorShape uint8

const (
	cursorBlock cursorShape = iota
	cursorBar
	cursorUnderline
)

// cursorShapeNames is how the config file spells the shapes.
var cursorShapeNames = map[string]cursorShape{
	"block":     cursorBlock,
	"bar":       cursorBar,
	"underline": cursorUnderline,
}

func cursorShapeList() string {
	return strings.Join(slices.Sorted(maps.Keys(cursorShapeNames)), " ")
}

// cursor is how the cursor is drawn. Where it is lives on the screen, which is the
// thing the escape codes actually move.
type cursor struct {
	shape cursorShape
	on    bool // DECTCEM

	// shown folds DECTCEM, the blink phase and the focus into one bit. relayout
	// resolves it; text.Layout and the rect quads must read the same value, or the
	// block hides while the glyph stays inverted and the cell goes blank.
	shown bool
}

// pane is a leaf of the layout tree. The screen and the history behind it belong to
// the pane, not to the app — this is where a per-pane PTY writes.
type pane struct {
	id         int             // stable label, never reused after a close
	rect       image.Rectangle // framebuffer px, divider excluded
	cols, rows int             // grid that fits rect after padding
	pri, alt   *screen         // the primary grid and the one a full-screen program gets
	scr        *screen         // whichever of the two is live
	buf        *scrollback     // the lines that have scrolled off the primary
	par        vt.Parser
	pty        *pty.Session
	noShell    bool   // the shell failed to start; do not keep retrying
	answers    []byte // replies owed to the shell; see feed
	scroll     int    // lines back from the newest; 0 = pinned to the tail
	cursor     cursor

	first, count uint32 // the pane's slice of the shared instance buffer
}

func newPane(id int) *pane {
	buf := newScrollback()
	pri := newScreen(0, 0, buf)
	return &pane{
		id:  id,
		pri: pri,
		// The alternate screen keeps no history: a full-screen program owns the grid,
		// and its repaints are not something to scroll back through.
		alt:    newScreen(0, 0, nil),
		scr:    pri,
		buf:    buf,
		cursor: cursor{shape: cursorShapeDefault, on: true},
	}
}

// histLen is how many history rows the view shows. None while the alternate screen is
// up: scrolling back into what the primary left behind would be nonsense there.
func (p *pane) histLen() int {
	if p.scr == p.alt {
		return 0
	}
	return p.buf.Len()
}

// total is the number of rows in the pane's view: the history, then the live screen.
func (p *pane) total() int { return p.histLen() + p.scr.height() }

// maxScroll is as far back as the view can go before the oldest line is at the top.
func (p *pane) maxScroll() int { return max(0, p.total()-p.rows) }

// rowAt is the i-th row of the view and the generation its cached glyphs must match.
// Rows past the end of the history come from the live screen.
func (p *pane) rowAt(i int) (*shapedRow, uint32) {
	if h := p.histLen(); i >= h {
		return p.scr.row(i - h), p.scr.gen
	}
	return p.buf.Row(i), p.buf.Gen()
}

// feed runs bytes from the PTY through the parser onto the screen and returns the
// answers the shell is owed.
//
// The view keeps its place while this happens: every line the screen retires into the
// history shifts the whole view down by one, whether the ring grew or evicted to take
// it.
//
// The returned slice is reused by the next call.
func (p *pane) feed(b []byte) []byte {
	before := p.buf.pushed
	p.answers = p.answers[:0]
	p.par.Parse(b, p)
	if p.scroll > 0 {
		p.scroll = min(p.scroll+int(p.buf.pushed-before), p.maxScroll())
	}
	return p.answers
}

// pane is the parser's vt.Sink.

func (p *pane) Print(r rune) { p.scr.put(r) }

func (p *pane) Execute(b byte) {
	switch b {
	case '\n', 0x0B, 0x0C: // LF, VT and FF all feed a line
		p.scr.lineFeed()
	case '\r':
		p.scr.carriageReturn()
	case '\b':
		p.scr.backspace()
	case '\t':
		p.scr.tab()
	}
	// BEL and the rest are dropped. A visual bell is not this milestone's problem.
}

// Most sequences are recognised by the automaton and deliberately dropped: SGR, CUP,
// ED/EL, DECSTBM and DECSET are the next milestone, and dropping them is what keeps a
// shell prompt from arriving on screen as punctuation.
//
// Queries are the exception, and they cannot wait. A shell that probes the terminal —
// fish does, at every startup — writes nothing at all until its questions are
// answered. DA1 is the barrier the probe hangs on: a prober sends its queries and then
// a DA1, so the DA1 reply is what tells it the rest are never coming. A terminal that
// answers nothing simply looks dead.
func (p *pane) CSIDispatch(c vt.CSI) {
	// Of the sequences carrying an intermediate, DECSCUSR (SP q) is the one gty acts on:
	// it is how vim and neovim mark their modes. The rest are dropped on purpose; DA1 is
	// the barrier a prober actually waits on.
	if len(c.Inter) > 0 {
		if c.Private == 0 && c.Final == 'q' && len(c.Inter) == 1 && c.Inter[0] == ' ' {
			p.setCursorStyle(c.Raw(0))
		}
		return
	}
	s := p.scr

	switch c.Private {
	case '?':
		switch c.Final {
		case 'h':
			p.setModes(c.Params, true)
		case 'l':
			p.setModes(c.Params, false)
		}
		return
	case '>':
		if c.Final == 'c' {
			// DA2: terminal type, firmware version, cartridge ROM. Nothing here has a
			// meaningful version yet.
			p.reply("\x1b[>0;0;0c")
		}
		return
	case 0:
	default:
		return
	}

	switch c.Final {
	case '@':
		s.insertChars(c.Arg(0, 1))
	case 'A':
		s.moveBy(-c.Arg(0, 1), 0)
	case 'B', 'e':
		s.moveBy(c.Arg(0, 1), 0)
	case 'C', 'a':
		s.moveBy(0, c.Arg(0, 1))
	case 'D':
		s.moveBy(0, -c.Arg(0, 1))
	case 'E':
		s.moveTo(s.curRow+c.Arg(0, 1), 0)
	case 'F':
		s.moveTo(s.curRow-c.Arg(0, 1), 0)
	case 'G', '`':
		s.moveTo(s.curRow, c.Arg(0, 1)-1)
	case 'H', 'f':
		s.moveTo(c.Arg(0, 1)-1, c.Arg(1, 1)-1)
	case 'J':
		s.eraseInDisplay(c.Raw(0))
	case 'K':
		s.eraseInLine(c.Raw(0))
	case 'L':
		s.insertLines(c.Arg(0, 1))
	case 'M':
		s.deleteLines(c.Arg(0, 1))
	case 'P':
		s.deleteChars(c.Arg(0, 1))
	case 'S':
		for range c.Arg(0, 1) {
			s.scrollUp()
		}
	case 'T':
		for range c.Arg(0, 1) {
			s.scrollDownAt(s.top)
		}
	case 'X':
		s.eraseChars(c.Arg(0, 1))
	case 'd':
		s.moveTo(c.Arg(0, 1)-1, s.curCol)
	case 'c':
		// DA1. VT220 (62) speaking ANSI colour (22), which is what TERM already
		// promises and what gty now actually does.
		p.reply("\x1b[?62;22c")
	case 'm':
		p.sgr(c)
	case 'n':
		// DSR. fish asks where the cursor is on every redraw; a terminal that does not
		// answer leaves it guessing.
		switch c.Raw(0) {
		case 5:
			p.reply("\x1b[0n")
		case 6:
			p.reply(fmt.Sprintf("\x1b[%d;%dR", s.curRow+1, s.curCol+1))
		}
	case 'r':
		s.setRegion(c.Raw(0), c.Raw(1))
	case 's':
		s.save()
	case 'u':
		s.restore()
	}
}

// setCursorStyle handles DECSCUSR. Only the shape is read — the odd parameters also ask
// for a blink, but that belongs to the window and runs on one clock for every pane. Out
// of range leaves the shape alone, as xterm does.
func (p *pane) setCursorStyle(param int) {
	switch param {
	case 0:
		p.cursor.shape = cursorShapeDefault
	case 1, 2:
		p.cursor.shape = cursorBlock
	case 3, 4:
		p.cursor.shape = cursorUnderline
	case 5, 6:
		p.cursor.shape = cursorBar
	}
}

// setModes handles DECSET and DECRST. Anything not listed is recognised and ignored:
// mouse reporting, modifyOtherKeys, the kitty keyboard protocol.
func (p *pane) setModes(params []int, on bool) {
	for _, m := range params {
		switch m {
		case 7: // DECAWM
			p.pri.autowrap, p.alt.autowrap = on, on
		case 25: // DECTCEM
			p.cursor.on = on
		case 47, 1047, 1049:
			p.useAlt(on, m == 1049)
		}
	}
}

// useAlt switches between the two grids. 1049 parks the cursor on the way in and puts
// it back on the way out, which is what lets vim leave the shell's prompt exactly where
// it found it.
func (p *pane) useAlt(on, withCursor bool) {
	if on == (p.scr == p.alt) {
		return
	}
	if on {
		if withCursor {
			p.pri.save()
		}
		p.alt.reset()
		p.scr = p.alt
	} else {
		p.scr = p.pri
		if withCursor {
			p.pri.restore()
		}
	}
	p.scroll = 0
}

func (p *pane) ESCDispatch(final byte, inter []byte) {
	if len(inter) > 0 {
		return // charset designation and friends
	}
	switch final {
	case '7':
		p.scr.save()
	case '8':
		p.scr.restore()
	case 'D': // IND
		p.scr.lineFeed()
	case 'E': // NEL
		p.scr.carriageReturn()
		p.scr.lineFeed()
	case 'M': // RI
		p.scr.reverseIndex()
	case 'c': // RIS
		p.useAlt(false, false)
		p.pri.reset()
		p.cursor.shape = cursorShapeDefault
	}
}

func (p *pane) OSCDispatch(data []byte) {
	// The colour queries are worth answering because the answer is true: apps ask so
	// they can pick a light or a dark theme.
	switch string(data) {
	case "10;?":
		p.reply(oscColor(10, foreground))
	case "11;?":
		p.reply(oscColor(11, backgroundRGBA))
	}
}

// oscColor formats a colour the way an OSC 10/11 answer wants it: sixteen bits a
// channel, which is what xterm has always sent.
func oscColor(code int, c [4]float32) string {
	q := func(v float32) uint16 { return uint16(min(max(v, 0), 1) * 0xFFFF) }
	return fmt.Sprintf("\x1b]%d;rgb:%04x/%04x/%04x\x1b\\", code, q(c[0]), q(c[1]), q(c[2]))
}

// reply queues an answer to a terminal query. It is collected rather than written
// straight out so one read from the shell produces at most one write back.
func (p *pane) reply(s string) { p.answers = append(p.answers, s...) }

// scrollBy moves the view delta lines back through history (negative goes forward)
// and reports whether it moved.
func (p *pane) scrollBy(delta int) bool {
	was := p.scroll
	p.scroll = min(max(p.scroll+delta, 0), p.maxScroll())
	return p.scroll != was
}

// visible is the range of the view on screen, oldest first. At scroll 0 it is exactly
// the live screen; scrolling back walks into the history in front of it.
func (p *pane) visible() (from, to int) {
	to = p.total() - p.scroll
	return max(0, to-p.rows), to
}

// cursorAt is the cursor's line, as an index into the history, and its column. ok is
// false when the cursor is hidden, scrolled out of view, or past the grid.
func (p *pane) cursorAt() (line, col int, ok bool) {
	col = p.scr.curCol
	if !p.cursor.shown || col < 0 || col >= p.cols {
		return 0, 0, false
	}
	from, to := p.visible()
	i := p.histLen() + p.scr.curRow
	if i < from || i >= to {
		return 0, 0, false
	}
	return i, col, true
}

// cursorCell is the cursor's cell box in framebuffer px, on the same origin
// text.Layout lays glyphs from before it backs off by the atlas padding. The cell is
// inside p.rect by construction: padding + cols*cellW never exceeds Dx - padding.
func (p *pane) cursorCell(cellW, cellH int) (image.Rectangle, bool) {
	i, col, ok := p.cursorAt()
	if !ok {
		return image.Rectangle{}, false
	}
	from, _ := p.visible()
	pad := px(padding)
	x := p.rect.Min.X + pad + col*cellW
	y := p.rect.Min.Y + pad + (i-from)*cellH
	return image.Rect(x, y, x+cellW, y+cellH), true
}

// cursorQuads is the filled shape a focused pane draws in its cursor cell.
func cursorQuads(cell image.Rectangle, s cursorShape) []image.Rectangle {
	switch s {
	case cursorBar:
		w := min(px(cursorBarWidth), cell.Dx())
		return []image.Rectangle{image.Rect(cell.Min.X, cell.Min.Y, cell.Min.X+w, cell.Max.Y)}
	case cursorUnderline:
		h := min(px(cursorUnderlineHeight), cell.Dy())
		return []image.Rectangle{image.Rect(cell.Min.X, cell.Max.Y-h, cell.Max.X, cell.Max.Y)}
	default:
		return []image.Rectangle{cell}
	}
}

// cursorOutline is the rim an unfocused pane draws instead of a fill. It leaves the
// glyph alone, which is what keeps the inverted cell — and so the ligature split —
// a focused-pane-only problem.
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

// paintRects is a pane's painted background: the cell backgrounds a program has set,
// and the underlines. Appended to dst so the caller can keep one buffer for the frame.
//
// Cells are coalesced into runs of one colour. A full screen of them would otherwise be
// twenty thousand quads, most of them adjacent and identical.
func paintRects(dst []quad, p *pane, cellW, cellH int) []quad {
	from, to := p.visible()
	pad, ul := px(padding), px(underlineHeight)
	x0, y0 := p.rect.Min.X+pad, p.rect.Min.Y+pad

	for i := from; i < to; i++ {
		row, _ := p.rowAt(i)
		cells := row.cells
		if len(cells) > p.cols {
			cells = cells[:p.cols]
		}
		y := y0 + (i-from)*cellH

		for c := 0; c < len(cells); {
			if !cells[c].painted() {
				c++
				continue
			}
			_, bg := cells[c].colors()
			end := c + 1
			for end < len(cells) && cells[end].painted() {
				if _, b := cells[end].colors(); b != bg {
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
			if cells[c].Attrs&attrUnderline == 0 {
				continue
			}
			fg, _ := cells[c].colors()
			dst = append(dst, quad{
				rect:  image.Rect(x0+c*cellW, y+cellH-ul, x0+(c+1)*cellW, y+cellH),
				color: fg,
			})
		}
	}
	return dst
}

// cursorRects splits the panes' cursors into the focused pane's filled shape and the
// rims the others draw. Two groups because they take different colours.
func cursorRects(panes []*pane, focused *pane, cellW, cellH int) (fills, rims []image.Rectangle) {
	for _, p := range panes {
		cell, ok := p.cursorCell(cellW, cellH)
		if !ok {
			continue
		}
		if p == focused {
			fills = append(fills, cursorQuads(cell, p.cursor.shape)...)
		} else {
			rims = append(rims, cursorOutline(cell)...)
		}
	}
	return fills, rims
}

// setGrid takes the grid from a layout pass and refits the screen to it. The screen
// resize can push lines into the history, so the scroll clamp comes after it.
func (p *pane) setGrid(cols, rows int) {
	changed := cols != p.cols || rows != p.rows
	p.cols, p.rows = cols, rows
	p.buf.setCols(cols)
	// Both grids, so switching back to a screen that was not on display finds it the
	// right shape.
	p.pri.resize(cols, rows)
	p.alt.resize(cols, rows)
	p.scroll = min(p.scroll, p.maxScroll())

	// Only on a real change: relayout runs on every damaged frame, and every Setsize
	// is a SIGWINCH to the shell.
	if changed && p.pty != nil {
		p.pty.Resize(cols, rows)
	}
}

// release ends the pane's shell. Safe to call twice.
func (p *pane) release() {
	if p.pty != nil {
		p.pty.Close()
		p.pty = nil
	}
}

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
