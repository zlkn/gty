package main

import (
	"fmt"
	"image"
	"slices"

	"gty/internal/font"
)

// tab is one layout tree and the focus inside it. Every tab is laid out against the
// same rect, on display or not, so coming forward costs no Setsize.
type tab struct {
	root     *node
	focused  *pane
	panes    []*pane
	dividers []image.Rectangle
}

func (a *app) cur() *tab { return a.tabs[a.active] }

// syncActive and stashActive are the only writers of the app's active-tab aliases,
// which is what keeps every reader of them unaware there is more than one tab.
func (a *app) syncActive() {
	t := a.cur()
	a.root, a.focused, a.panes, a.dividers = t.root, t.focused, t.panes, t.dividers
}

func (a *app) stashActive() {
	t := a.cur()
	t.root, t.focused, t.panes, t.dividers = a.root, a.focused, a.panes, a.dividers
}

func (a *app) newTab() {
	a.stashActive()
	p := a.newPane()
	a.tabs = append(a.tabs, &tab{root: &node{pane: p}, focused: p})
	a.active = len(a.tabs) - 1
	a.syncActive()
	a.Damage()
}

// gotoTab ignores an index out of range: goto_tab_9 is bound whether or not nine tabs
// are open.
func (a *app) gotoTab(i int) {
	if i < 0 || i >= len(a.tabs) || i == a.active {
		return
	}
	a.stashActive()
	a.active = i
	a.syncActive()
	a.Damage()
}

func (a *app) cycleTab(delta int) {
	if n := len(a.tabs); n > 1 {
		a.gotoTab(((a.active+delta)%n + n) % n)
	}
}

// closeTabAt ends every shell in tab i. The last tab takes the window with it.
func (a *app) closeTabAt(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	// Even when i is the tab on display: the aliases may hold a focus the tab does not
	// have yet, and dropping a tab must not take that with it.
	a.stashActive()
	for _, p := range a.tabs[i].root.leaves(nil) {
		p.release()
	}
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if len(a.tabs) == 0 {
		a.window.SetShouldClose(true)
		return
	}
	a.active = activeAfterClose(i, a.active, len(a.tabs))
	a.syncActive()
	a.Damage()
}

// activeAfterClose is where the display lands once tab i is gone and n remain.
func activeAfterClose(i, active, n int) int {
	switch {
	case i < active:
		return active - 1
	case i == active:
		return min(active, n-1)
	}
	return active
}

func (a *app) tabOf(p *pane) int {
	for i, t := range a.tabs {
		if t.root.has(p) {
			return i
		}
	}
	return -1
}

// label is whatever the tab's focused pane last set with OSC 0 or 2, or its number.
func (t *tab) label(i int) string {
	if t.focused != nil && t.focused.title != "" {
		return t.focused.title
	}
	return fmt.Sprintf("%d: shell", i+1)
}

// setWindowTitle only on a change: relayout runs on every damaged frame and this is a
// round trip to the window manager.
func (a *app) setWindowTitle() {
	want := a.cur().label(a.active)
	if len(a.tabs) > 1 {
		want = fmt.Sprintf("%s — %d/%d", want, a.active+1, len(a.tabs))
	}
	if want != a.windowTitle {
		a.windowTitle = want
		a.window.SetTitle(want)
	}
}

const (
	// tabPadding is the air either side of a label and barInset the room the row of
	// tabs leaves at both ends of the bar. Both in cells, so they track the font rather
	// than needing px().
	tabPadding = 2
	barInset   = 2

	// tabUnderline marks the active tab. Drawn over the divider rather than above it,
	// so the two read as one line.
	tabUnderline = 3

	// tabAccent indexes the palette for that underline. The theme's red rather than
	// cursorColor, which defaults to the foreground and so would not be a colour at
	// all; colors.ansi moves it.
	tabAccent = 1

	// dividerFade is how far the second row of the divider is pulled towards the
	// background: the darker line on top, the lighter one under it, so the edge reads
	// as dissipating rather than as a rule two pixels thick.
	dividerFade = 0.55

	// dividerInset keeps the divider off both window edges and dividerPad holds the
	// terminal off its underside. Logical pixels, not cells: they are gaps, not room
	// for anything.
	dividerInset = 6
	dividerPad   = 5
)

// barHeight is a row and a half for the label and its divider, then the gap that holds
// the terminal off.
func barHeight(cellH int) int { return cellH*3/2 + px(dividerPad) }

// splitBar gives the bar to a lone tab as well, so the window opens looking the way it
// will keep looking. Only a window too short to spare the pixels goes without.
func splitBar(surface image.Rectangle, ntabs, cellH int) (bar, content image.Rectangle) {
	h := barHeight(cellH)
	if ntabs < 1 || h >= surface.Dy() {
		return image.Rectangle{}, surface
	}
	cut := surface.Min.Y + h
	return image.Rect(surface.Min.X, surface.Min.Y, surface.Max.X, cut),
		image.Rect(surface.Min.X, cut, surface.Max.X, surface.Max.Y)
}

// layoutBar lays the tabs left to right, each as wide as its own label. The active one
// is bold and underlined; the underline is appended after the divider so that it paints
// over it rather than sitting above it.
func layoutBar(tabs []*tab, active int, bar image.Rectangle, cellW, cellH int) (fills []quad, labels []chrome) {
	if bar.Empty() || len(tabs) == 0 || cellW <= 0 {
		return nil, nil
	}
	pad, line, mark := tabPadding*cellW, px(dividerWidth), px(tabUnderline)

	// The divider stops short of the bar's bottom, leaving dividerPad of air before the
	// terminal, and a lighter second row under it fades the edge out into that air.
	gap := min(px(dividerInset), bar.Dx()/2)
	base := bar.Max.Y - px(dividerPad)
	edge := image.Rect(bar.Min.X+gap, base-line, bar.Max.X-gap, base)
	fills = append(fills,
		quad{rect: edge, color: selectionColor},
		quad{rect: edge.Add(image.Pt(0, line)), color: mix(selectionColor, backgroundRGBA, dividerFade)},
	)
	x0, x1 := bar.Min.X+barInset*cellW, bar.Max.X-barInset*cellW
	if x1 <= x0 {
		return fills, nil
	}
	row := image.Rect(x0, bar.Min.Y, x1, base-line)

	budget := labelBudget(tabs, row.Dx(), cellW, pad)
	labelY := row.Min.Y + (row.Dy()-cellH)/2
	x := row.Min.X
	for i, t := range tabs {
		if x >= row.Max.X {
			break // out of bar
		}
		// Every label at full strength: bold and the underline are the whole cue.
		cells := labelCells(t.label(i), budget)
		if i == active {
			for j := range cells {
				cells[j].Style = font.Bold
			}
		}

		slice := image.Rect(x, row.Min.Y, min(x+len(cells)*cellW+2*pad, row.Max.X), row.Max.Y)
		x = slice.Max.X
		if i == active {
			fills = append(fills, quad{
				rect:  image.Rect(slice.Min.X, base-mark, slice.Max.X, base),
				color: palette[tabAccent],
			})
		}
		labels = append(labels, chrome{
			rect:  slice,
			cells: cells,
			x:     float32(slice.Min.X + pad),
			y:     float32(labelY),
			fg:    foreground,
		})
	}
	return fills, labels
}

// labelBudget is how many cells of label a tab may take: all of its own while the tabs
// fit the bar, and an equal share of it once they do not.
func labelBudget(tabs []*tab, width, cellW, pad int) int {
	need := 0
	for i, t := range tabs {
		need += len([]rune(t.label(i)))*cellW + 2*pad
	}
	if need <= width {
		return maxTitle // no label is longer, so nothing is clipped
	}
	return max((width/len(tabs)-2*pad)/cellW, 0)
}

// labelCells is s clipped to cols, ending in an ellipsis when it did not fit.
func labelCells(s string, cols int) []cell {
	if cols <= 0 {
		return nil
	}
	rs := []rune(s)
	if len(rs) > cols {
		rs = append(rs[:cols-1:cols-1], '…')
	}
	cells := make([]cell, len(rs))
	for i, r := range rs {
		cells[i] = cell{Rune: r}
	}
	return cells
}
