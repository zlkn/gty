package main

import (
	"fmt"
	"image"

	"gty/internal/vte"
)

// helloWorld is demo output with ligature material in it, so a rendered frame shows the
// shaping working.
var helloWorld = []string{
	`hello, world`,
	`!= == === -> <- => <=> |> ?? ::`,
	`func f[T any](x T) T { return x }`,
}

// cellsOf is a row of cells holding s in the default colours.
func cellsOf(s string) []vte.Cell {
	out := make([]vte.Cell, 0, len(s))
	for _, r := range s {
		out = append(out, vte.Cell{Rune: r})
	}
	return out
}

// viewText is what the pane's last frame shows, oldest row first.
func viewText(p *pane) []string {
	out := make([]string, 0, len(p.frame.Lines))
	for i := range p.frame.Lines {
		out = append(out, p.frame.Lines[i].String())
	}
	return out
}

// feedText drives a pane the way a shell would: CRLF between lines and none after the last,
// so the cursor is left where a prompt would leave it.
func feedText(p *pane, lines ...string) {
	for i, l := range lines {
		if i > 0 {
			p.term.Feed([]byte("\r\n"))
		}
		p.term.Feed([]byte(l))
	}
	p.snap()
}

// numbered is lines "from" through "to"-1, for tests that need to name a scroll position.
func numbered(from, to int) []string {
	out := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

// gridPane is a pane with the grid and screen contents a test wants, skipping the layout
// pass, and with a frame already read: everything a draw touches reads that frame.
func gridPane(id int, rect image.Rectangle, cols, rows int, lines ...string) *pane {
	p := newPane(id)
	p.rect = rect
	p.setGrid(cols, rows)
	feedText(p, lines...)
	p.shown = true
	p.snap()
	return p
}

// fillDemo runs a pane's history to capacity. The pane must already have its grid.
func fillDemo(p *pane) {
	for i := range vte.MaxScrollback {
		if i > 0 {
			p.term.Feed([]byte("\r\n"))
		}
		p.term.Feed([]byte(fmt.Sprintf("pane %d  %05d | %s", p.id, i, helloWorld[i%len(helloWorld)])))
	}
	p.snap()
}

// placeCursor drives the cursor to row, col the way a program does, then refreshes the
// frame: nothing in a draw reads the terminal directly.
func placeCursor(p *pane, row, col int) {
	p.term.Feed([]byte(fmt.Sprintf("\x1b[%d;%dH", row+1, col+1)))
	p.snap()
}

// layout reads every pane's frame and lays it out, in the order relayout does: a draw reads
// one frame, and it has to be the current one.
func layout(txt *text, panes []*pane, focused *pane, runs []chrome) {
	for _, p := range panes {
		p.snap()
	}
	txt.Layout(panes, focused, runs)
}

// setTitle gives a pane the title a shell would, through the escape a shell would use.
func setTitle(p *pane, s string) { p.term.Feed([]byte("\x1b]2;" + s + "\a")) }
