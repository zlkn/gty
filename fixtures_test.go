package main

import (
	"fmt"
	"image"
	"strings"
)

// helloWorld is demo output with ligature material in it, so a rendered frame shows
// the shaping working.
var helloWorld = []string{
	`hello, world`,
	`!= == === -> <- => <=> |> ?? ::`,
	`func f[T any](x T) T { return x }`,
}

// cellsOf is a row of cells holding s in the default colours.
func cellsOf(s string) []cell {
	out := make([]cell, 0, len(s))
	for _, r := range s {
		out = append(out, cell{Rune: r})
	}
	return out
}

// rowText reads a row of cells back as a string, trailing blanks dropped.
func rowText(cells []cell) string {
	var b strings.Builder
	for _, c := range trimBlanks(cells) {
		r := c.Rune
		if r == 0 {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

// screenText is the whole grid read back, top row first.
func screenText(s *screen) []string {
	out := make([]string, 0, s.height())
	for i := range s.lines {
		out = append(out, rowText(s.lines[i].cells))
	}
	return out
}

// viewText is what the pane shows, oldest row first — history and screen together.
func viewText(p *pane) []string {
	from, to := p.visible()
	out := make([]string, 0, max(0, to-from))
	for i := from; i < to; i++ {
		r, _ := p.rowAt(i)
		out = append(out, rowText(r.cells))
	}
	return out
}

// feedText drives a pane the way a shell would: CRLF between lines and none after the
// last, so the cursor is left where a prompt would leave it.
func feedText(p *pane, lines ...string) {
	for i, l := range lines {
		if i > 0 {
			p.feed([]byte("\r\n"))
		}
		p.feed([]byte(l))
	}
}

// numbered is lines "from" through "to"-1, for tests that need to name a scroll
// position.
func numbered(from, to int) []string {
	out := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

// gridPane is a pane with the grid and screen contents a test wants, skipping the
// layout pass.
func gridPane(id int, rect image.Rectangle, cols, rows int, lines ...string) *pane {
	p := newPane(id)
	p.rect = rect
	p.setGrid(cols, rows)
	feedText(p, lines...)
	return p
}

// fillDemo runs a pane's history to capacity. It used to live in main.go, feeding
// panes until a PTY could; now it is only a fixture for the tests and benchmarks that
// need a full scrollback. The pane must already have its grid.
func fillDemo(p *pane) {
	for i := range maxScrollback {
		if i > 0 {
			p.feed([]byte("\r\n"))
		}
		p.feed([]byte(fmt.Sprintf("pane %d  %05d | %s", p.id, i, helloWorld[i%len(helloWorld)])))
	}
}
