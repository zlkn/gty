package vte

// Character sets, of which gty needs exactly two. A program that draws a frame with
// ncurses still designates the DEC line-drawing set and sends "lqqqk"; without the
// translation those five letters are what lands on the screen.

type charset uint8

const (
	charsetASCII charset = iota
	charsetGraphics
)

// decGraphics is the DEC Special Character and Line Drawing Set, from ` to ~ — the only
// part of it that differs from ASCII. The glyphs are the ones every terminal has settled
// on, and internal/font/boxdraw.go already draws the box-drawing half of them.
var decGraphics = [...]rune{
	'◆', '▒', '␉', '␌', '␍', '␊', '°', '±', // ` a b c d e f g
	'␤', '␋', '┘', '┐', '┌', '└', '┼', '⎺', // h i j k l m n o
	'⎻', '─', '⎼', '⎽', '├', '┤', '┴', '┬', // p q r s t u v w
	'│', '≤', '≥', 'π', '≠', '£', '·', //       x y z { | } ~
}

// translate maps a rune through whichever set is invoked as GL. Everything outside the
// designated range, and every rune at all while the set is ASCII, passes through.
func (s *screen) translate(r rune) rune {
	if s.g[s.gl] != charsetGraphics {
		return r
	}
	if r >= '`' && r <= '~' {
		return decGraphics[r-'`']
	}
	return r
}
