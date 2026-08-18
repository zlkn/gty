package main

import "image"

// dir is the axis a split divides: vertical puts its panes side by side.
type dir uint8

const (
	vertical dir = iota
	horizontal
)

// pane is a leaf of the layout tree. lines belong to the pane, not to the app —
// this is where a per-pane PTY screen will land.
type pane struct {
	id         int             // stable label, never reused after a close
	rect       image.Rectangle // framebuffer px, divider excluded
	cols, rows int             // grid that fits rect after padding
	lines      []line

	first, count uint32 // the pane's slice of the shared instance buffer
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
// sibling, and returns the pane that should take the focus. A root leaf has no
// parent to collapse, so it returns nil and the caller closes the window instead.
func (n *node) close(target *pane) *pane {
	if n.pane != nil {
		return nil
	}
	for i, kid := range n.kids {
		if kid.pane != nil && kid.pane == target {
			*n = *n.kids[1-i]
			return n.leaf()
		}
		if p := kid.close(target); p != nil {
			return p
		}
	}
	return nil
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
	var walk func(n *node, r image.Rectangle)
	walk = func(n *node, r image.Rectangle) {
		if n.pane != nil {
			p := n.pane
			p.rect = r
			p.cols = max(0, (r.Dx()-2*padding)/cellW)
			p.rows = max(0, (r.Dy()-2*padding)/cellH)
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
	if d == vertical {
		avail := max(0, r.Dx()-dividerWidth)
		cut := r.Min.X + min(max(int(float32(avail)*ratio), 0), avail)
		end := min(cut+dividerWidth, r.Max.X)
		return image.Rect(r.Min.X, r.Min.Y, cut, r.Max.Y),
			image.Rect(cut, r.Min.Y, end, r.Max.Y),
			image.Rect(end, r.Min.Y, r.Max.X, r.Max.Y)
	}
	avail := max(0, r.Dy()-dividerWidth)
	cut := r.Min.Y + min(max(int(float32(avail)*ratio), 0), avail)
	end := min(cut+dividerWidth, r.Max.Y)
	return image.Rect(r.Min.X, r.Min.Y, r.Max.X, cut),
		image.Rect(r.Min.X, cut, r.Max.X, end),
		image.Rect(r.Min.X, end, r.Max.X, r.Max.Y)
}
