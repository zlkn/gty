package vte

import "github.com/mattn/go-runewidth"

// widths answers how many cells a rune takes. A condition of its own rather than the
// package's default, which reads the locale at init: under a CJK locale it doubles the
// ambiguous runes — box drawing among them — and every program inside the terminal would
// disagree with the grid. No lookup table either: it is half a megabyte, and the ASCII
// path below never reaches the search it would replace.
var widths = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = false
	return c
}()

// runeWidth is 0, 1 or 2 cells. Control characters never get here, so a 0 is a combining
// mark, a joiner or a variation selector.
func runeWidth(r rune) int {
	if r < 0x7F {
		return 1
	}
	return widths.RuneWidth(r)
}
