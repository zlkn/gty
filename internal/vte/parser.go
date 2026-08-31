package vte

import "unicode/utf8"

// The DEC ANSI state machine, following Paul Williams' diagram (vt100.net), as one switch per
// state rather than a transition table.
//
// Every state is here even where its dispatch does nothing: a shell emits CSI and OSC from its
// first prompt, and an incomplete automaton prints the rest as garbage.

const (
	maxParams        = 16
	maxIntermediates = 2

	// maxStringLen caps OSC and DCS payloads. A stream that never sends the terminator
	// would otherwise grow the buffer without bound.
	maxStringLen = 4096
)

// CSI is one parsed control sequence. sep records how each parameter was introduced, the only
// thing separating SGR's 38;2;r;g;b from its 38:2::r:g:b.
type CSI struct {
	Final   byte
	Private byte // the ? > = < of a private sequence, or zero
	Params  []int
	Inter   []byte

	// sep[i] is the byte before param i; zero for the first. Unexported because Sub is
	// the only question worth asking of it.
	sep []byte
}

// Arg is parameter i, or dflt when it is missing or zero. Every sequence that counts
// something reads an omitted parameter that way.
func (c CSI) Arg(i, dflt int) int {
	if i < len(c.Params) && c.Params[i] != 0 {
		return c.Params[i]
	}
	return dflt
}

// Raw is parameter i, or zero — for the sequences where zero is a real mode, not an
// omission.
func (c CSI) Raw(i int) int {
	if i < len(c.Params) {
		return c.Params[i]
	}
	return 0
}

// Sub reports whether parameter i was introduced by a colon, making it part of the
// parameter before it rather than one of its own.
func (c CSI) Sub(i int) bool { return i < len(c.sep) && c.sep[i] == ':' }

// sink is what a parsed byte stream drives. Terminal implements it, on unexported methods
// so that the parser's callbacks stay out of the model's API.
type sink interface {
	putRune(r rune)
	execute(b byte)
	csi(c CSI)
	esc(final byte, intermediates []byte)
	osc(data []byte)
}

type parserState uint8

const (
	stGround parserState = iota
	stEscape
	stEscapeIntermediate
	stCSIEntry
	stCSIParam
	stCSIIntermediate
	stCSIIgnore
	stOSCString
	stDCSEntry
	stDCSParam
	stDCSIntermediate
	stDCSPassthrough
	stDCSIgnore
	stSosPmApc
)

type parser struct {
	state parserState

	params []int
	sep    []byte
	inter  []byte
	priv   byte
	str    []byte

	// utf8 holds the bytes of a multi-byte rune that has not arrived in full. A read
	// from the PTY can split one anywhere, so it has to survive between calls.
	utf8 []byte
}

// parse feeds b through the automaton. It is safe to call with a chunk that cuts a
// rune or an escape sequence in half; the state carries over.
func (p *parser) parse(b []byte, out sink) {
	for _, c := range b {
		p.step(c, out)
	}
}

func (p *parser) step(b byte, out sink) {
	if p.anywhere(b, out) {
		return
	}
	switch p.state {
	case stGround:
		p.ground(b, out)

	case stEscape:
		switch {
		case isC0(b):
			out.execute(b)
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
			p.state = stEscapeIntermediate
		case b == 0x50: // P
			p.clear()
			p.state = stDCSEntry
		case b == 0x58, b == 0x5E, b == 0x5F: // X, ^, _
			p.state = stSosPmApc
		case b == 0x5B: // [
			p.clear()
			p.state = stCSIEntry
		case b == 0x5D: // ]
			p.str = p.str[:0]
			p.state = stOSCString
		case b == 0x7F: // DEL is ignored throughout
		default:
			out.esc(b, p.inter)
			p.state = stGround
		}

	case stEscapeIntermediate:
		switch {
		case isC0(b):
			out.execute(b)
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
		case b == 0x7F:
		default:
			out.esc(b, p.inter)
			p.state = stGround
		}

	case stCSIEntry:
		switch {
		case isC0(b):
			out.execute(b)
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
			p.state = stCSIIntermediate
		case b >= 0x30 && b <= 0x39, b == 0x3B:
			p.param(b)
			p.state = stCSIParam
		case b >= 0x3C && b <= 0x3F: // private marker: ? > = <
			p.priv = b
			p.state = stCSIParam
		case b == 0x3A: // a sub-parameter with nothing before it
			p.param(b)
			p.state = stCSIParam
		case b == 0x7F:
		default:
			out.csi(CSI{Final: b, Private: p.priv, Params: p.params, Inter: p.inter, sep: p.sep})
			p.state = stGround
		}

	case stCSIParam:
		switch {
		case isC0(b):
			out.execute(b)
		case b >= 0x30 && b <= 0x39, b == 0x3B, b == 0x3A:
			p.param(b)
		case b >= 0x3C && b <= 0x3F:
			p.state = stCSIIgnore
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
			p.state = stCSIIntermediate
		case b == 0x7F:
		default:
			out.csi(CSI{Final: b, Private: p.priv, Params: p.params, Inter: p.inter, sep: p.sep})
			p.state = stGround
		}

	case stCSIIntermediate:
		switch {
		case isC0(b):
			out.execute(b)
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
		case b >= 0x30 && b <= 0x3F:
			p.state = stCSIIgnore
		case b == 0x7F:
		default:
			out.csi(CSI{Final: b, Private: p.priv, Params: p.params, Inter: p.inter, sep: p.sep})
			p.state = stGround
		}

	case stCSIIgnore:
		switch {
		case isC0(b):
			out.execute(b)
		case b >= 0x40 && b <= 0x7E:
			p.state = stGround
		}

	case stOSCString:
		switch {
		case b == 0x07: // BEL, xterm's terminator and the one everything actually sends
			out.osc(p.str)
			p.state = stGround
		case b >= 0x20 || b >= 0x80:
			if len(p.str) < maxStringLen {
				p.str = append(p.str, b)
			}
		}

	case stDCSEntry, stDCSParam, stDCSIntermediate, stDCSPassthrough, stDCSIgnore, stSosPmApc:
		// Swallowed until the terminator. Nothing gty does yet answers a DCS, and the
		// ESC of an ST is caught by anywhere.

	}
}

func (p *parser) ground(b byte, out sink) {
	switch {
	case isC0(b):
		out.execute(b)
	case b == 0x7F: // DEL prints nothing
	case b < 0x80:
		p.utf8 = p.utf8[:0]
		out.putRune(rune(b))
	default:
		// A read can cut a rune in half, so bytes accumulate. FullRune is true for an
		// invalid lead byte too, so junk becomes one replacement rather than a stall.
		p.utf8 = append(p.utf8, b)
		if utf8.FullRune(p.utf8) {
			r, _ := utf8.DecodeRune(p.utf8)
			out.putRune(r)
			p.utf8 = p.utf8[:0]
		} else if len(p.utf8) >= utf8.UTFMax {
			out.putRune(utf8.RuneError)
			p.utf8 = p.utf8[:0]
		}
	}
}

// anywhere handles the transitions taken from every state: cancel, substitute, and the escape
// that abandons whatever was in flight.
func (p *parser) anywhere(b byte, out sink) bool {
	switch b {
	case 0x18, 0x1A: // CAN, SUB
		p.endString(out)
		out.execute(b)
		p.state = stGround
		return true
	case 0x1B: // ESC
		p.endString(out)
		p.clear()
		p.state = stEscape
		return true
	}
	return false
}

// endString closes an OSC that something else has interrupted. A well-formed OSC ends
// with ST, which is ESC \ — so this is also the ordinary path.
func (p *parser) endString(out sink) {
	if p.state == stOSCString {
		out.osc(p.str)
	}
}

func (p *parser) clear() {
	p.params, p.sep, p.inter, p.priv = p.params[:0], p.sep[:0], p.inter[:0], 0
}

func (p *parser) collect(b byte) {
	if len(p.inter) < maxIntermediates {
		p.inter = append(p.inter, b)
	}
}

// param folds a digit into the current parameter, or opens the next on a separator and
// remembers which. An omitted parameter reads as zero, which handlers take as the default.
func (p *parser) param(b byte) {
	if len(p.params) == 0 {
		p.params, p.sep = append(p.params, 0), append(p.sep, 0)
	}
	if b == ';' || b == ':' {
		if len(p.params) < maxParams {
			p.params, p.sep = append(p.params, 0), append(p.sep, b)
		}
		return
	}
	i := len(p.params) - 1
	if v := p.params[i]*10 + int(b-'0'); v <= 65535 {
		p.params[i] = v
	}
}

// isC0 reports whether b is a C0 control the automaton executes rather than collects.
// CAN, SUB and ESC are excluded: anywhere has already taken them.
func isC0(b byte) bool { return b <= 0x17 || b == 0x19 || (b >= 0x1C && b <= 0x1F) }
