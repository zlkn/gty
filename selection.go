package main

import (
	"time"
	"unicode"

	"gty/internal/vte"
)

type selMode uint8

const (
	selChar selMode = iota
	selWord
	selLine
)

// selPos is a location within the session: a line by its absolute Seq, and a 0-based column.
type selPos struct {
	seq uint64
	col int
}

// selection records the current text highlight in a pane.
//
// Positions are anchored on Row.Seq rather than view indices so that the selection
// stays attached to its lines as new shell output scrolls the buffer.
type selection struct {
	anchor   selPos
	head     selPos
	mode     selMode
	block    bool
	active   bool
	dragging bool
}

// clear deactivates the selection.
func (s *selection) clear() {
	s.active = false
}

// empty reports whether the selection has nothing selected.
func (s selection) empty() bool {
	if !s.active {
		return true
	}
	lo, hi := s.ordered()
	if s.block {
		return lo.col >= hi.col || lo.seq > hi.seq
	}
	return lo == hi
}

// ordered returns the normalized selection endpoints [lo, hi) where lo <= hi.
// In block mode, lo holds (minSeq, minCol) and hi holds (maxSeq, maxCol).
func (s selection) ordered() (lo, hi selPos) {
	if s.block {
		minCol, maxCol := min(s.anchor.col, s.head.col), max(s.anchor.col, s.head.col)
		minSeq, maxSeq := min(s.anchor.seq, s.head.seq), max(s.anchor.seq, s.head.seq)
		return selPos{seq: minSeq, col: minCol}, selPos{seq: maxSeq, col: maxCol + 1}
	}

	forward := s.head.seq > s.anchor.seq || (s.head.seq == s.anchor.seq && s.head.col >= s.anchor.col)
	if forward {
		lo = s.anchor
		hi = s.head
		if s.mode == selChar {
			hi.col++
		}
	} else {
		lo = s.head
		hi = s.anchor
		if s.mode == selChar {
			hi.col++
		}
	}
	return lo, hi
}

// contains reports whether the cell at (seq, col) is part of the selection.
func (s selection) contains(seq uint64, col int) bool {
	if !s.active {
		return false
	}
	lo, hi := s.ordered()
	if s.block {
		if seq < lo.seq || seq > hi.seq {
			return false
		}
		return col >= lo.col && col < hi.col
	}

	if seq < lo.seq || seq > hi.seq {
		return false
	}
	if lo.seq == hi.seq {
		return col >= lo.col && col < hi.col
	}
	if seq == lo.seq {
		return col >= lo.col
	}
	if seq == hi.seq {
		return col < hi.col
	}
	return true
}

const (
	classWhitespace = iota
	classDelimiter
	classWord
)

// runeClass categorises runes for word expansion on double click.
// Alphanumerics and common path/URL symbols form the word class so that
// double clicking a path or URL takes the entire run.
func runeClass(r rune) int {
	switch {
	case r == 0 || unicode.IsSpace(r):
		return classWhitespace
	case r == '(' || r == ')' || r == '[' || r == ']' || r == '{' || r == '}' ||
		r == '<' || r == '>' || r == '"' || r == '\'' || r == '`' || r == ',' || r == ';':
		return classDelimiter
	case unicode.IsLetter(r) || unicode.IsDigit(r) ||
		r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '@' ||
		r == '?' || r == '&' || r == '=' || r == '%' || r == '#' || r == '+' || r == '~':
		return classWord
	default:
		return classDelimiter
	}
}

// wordAt returns the half-open column range [from, to) for the token under col.
func wordAt(cells []vte.Cell, col, cols int) (from, to int) {
	if cols <= 0 {
		return 0, 0
	}
	col = min(max(col, 0), cols-1)
	cls := runeClass(vte.CellAt(cells, col).Rune)

	from = col
	for from > 0 && runeClass(vte.CellAt(cells, from-1).Rune) == cls {
		from--
	}

	to = col + 1
	for to < cols && runeClass(vte.CellAt(cells, to).Rune) == cls {
		to++
	}
	return from, to
}

// clickTracker tracks mouse clicks to distinguish single, double, and triple clicks.
type clickTracker struct {
	pos    selPos
	paneID int
	time   time.Time
	count  int
}

// click registers a mouse click and returns the resulting click count (1, 2, or 3).
func (c *clickTracker) click(paneID int, pos selPos, now time.Time) int {
	const doubleClickInterval = 400 * time.Millisecond
	if paneID == c.paneID && pos == c.pos && now.Sub(c.time) < doubleClickInterval {
		c.count = (c.count % 3) + 1
	} else {
		c.count = 1
		c.paneID = paneID
		c.pos = pos
	}
	c.time = now
	return c.count
}
