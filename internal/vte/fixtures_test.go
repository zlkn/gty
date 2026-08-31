package vte

import (
	"fmt"
)

// cellsOf is a row of cells holding s in the default colours.
func cellsOf(s string) []Cell {
	out := make([]Cell, 0, len(s))
	for _, r := range s {
		out = append(out, Cell{Rune: r})
	}
	return out
}

// rowOf is a line holding s, for the ring's own tests: the screen is the only real caller
// of append, and these have no screen.
func rowOf(s string) *Row { return &Row{Cells: cellsOf(s)} }

// screenText is the whole grid read back, top row first.
func screenText(s *screen) []string {
	out := make([]string, 0, s.height())
	for i := range s.lines {
		out = append(out, s.lines[i].String())
	}
	return out
}

// frameText is what a view scrolled back lines shows, oldest row first.
func frameText(t *Terminal, back int) []string {
	f := t.Frame(nil, back)
	out := make([]string, len(f.Lines))
	for i, r := range f.Lines {
		out[i] = r.String()
	}
	return out
}

// feedText drives a terminal the way a shell would: CRLF between lines and none after the
// last, so the cursor is left where a prompt would leave it.
func feedText(t *Terminal, lines ...string) {
	for i, l := range lines {
		if i > 0 {
			t.Feed([]byte("\r\n"))
		}
		t.Feed([]byte(l))
	}
}

// numbered is lines "from" through "to"-1, for tests that need to name a scroll position.
func numbered(from, to int) []string {
	out := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

// gridTerm is a terminal with the grid and the screen contents a test wants, and no shell
// behind it.
func gridTerm(cols, rows int, lines ...string) *Terminal {
	t := New(cols, rows)
	feedText(t, lines...)
	return t
}

// answer is what the terminal owes the shell after in. With no pty to flush them to, the
// replies are simply left where feed collected them.
func answer(t *Terminal, in string) string {
	t.Feed([]byte(in))
	return string(t.answers)
}

// fill runs a terminal's history to capacity.
func fill(t *Terminal) {
	for i := range MaxScrollback {
		if i > 0 {
			t.Feed([]byte("\r\n"))
		}
		t.Feed([]byte(fmt.Sprintf("line %05d", i)))
	}
}

func trimTrailing(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
