package vt

import "unicode/utf8"

// The DEC ANSI state machine, following Paul Williams' diagram (vt100.net). Written as
// one switch per state rather than a transition table: same automaton, and in Go the
// switch is the readable form.
//
// The automaton has to be complete before anything it recognises can be acted on. A
// shell running under TERM=xterm-256color emits CSI and OSC from its first prompt, and
// a parser that only understands what it implements prints the rest as garbage. So the
// states below are all here, while most of the dispatch handlers are still empty: the
// sequence is recognised and dropped, not leaked onto the screen.

const (
	maxParams        = 16
	maxIntermediates = 2

	// maxStringLen caps OSC and DCS payloads. A stream that never sends the terminator
	// would otherwise grow the buffer without bound.
	maxStringLen = 4096
)

// CSI is one parsed control sequence.
//
// sep records how each parameter was introduced, because that is the only thing that
// separates SGR's two extended-colour forms: 38;2;r;g;b and 38:2::r:g:b mean the same
// thing but do not have the same parameters.
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

// Sink is what a parsed byte stream drives.
//
// Every method is exported because it has to be: a Sink is implemented from outside this
// package, and an interface carrying an unexported method cannot be.
type Sink interface {
	Print(r rune)
	Execute(b byte)
	CSIDispatch(c CSI)
	ESCDispatch(final byte, intermediates []byte)
	OSCDispatch(data []byte)
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

type Parser struct {
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
func (p *Parser) Parse(b []byte, out Sink) {
	for _, c := range b {
		p.step(c, out)
	}
}

func (p *Parser) step(b byte, out Sink) {
	if p.anywhere(b, out) {
		return
	}
	switch p.state {
	case stGround:
		p.ground(b, out)

	case stEscape:
		switch {
		case isC0(b):
			out.Execute(b)
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
			out.ESCDispatch(b, p.inter)
			p.state = stGround
		}

	case stEscapeIntermediate:
		switch {
		case isC0(b):
			out.Execute(b)
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
		case b == 0x7F:
		default:
			out.ESCDispatch(b, p.inter)
			p.state = stGround
		}

	case stCSIEntry:
		switch {
		case isC0(b):
			out.Execute(b)
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
			out.CSIDispatch(CSI{Final: b, Private: p.priv, Params: p.params, Inter: p.inter, sep: p.sep})
			p.state = stGround
		}

	case stCSIParam:
		switch {
		case isC0(b):
			out.Execute(b)
		case b >= 0x30 && b <= 0x39, b == 0x3B, b == 0x3A:
			p.param(b)
		case b >= 0x3C && b <= 0x3F:
			p.state = stCSIIgnore
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
			p.state = stCSIIntermediate
		case b == 0x7F:
		default:
			out.CSIDispatch(CSI{Final: b, Private: p.priv, Params: p.params, Inter: p.inter, sep: p.sep})
			p.state = stGround
		}

	case stCSIIntermediate:
		switch {
		case isC0(b):
			out.Execute(b)
		case b >= 0x20 && b <= 0x2F:
			p.collect(b)
		case b >= 0x30 && b <= 0x3F:
			p.state = stCSIIgnore
		case b == 0x7F:
		default:
			out.CSIDispatch(CSI{Final: b, Private: p.priv, Params: p.params, Inter: p.inter, sep: p.sep})
			p.state = stGround
		}

	case stCSIIgnore:
		switch {
		case isC0(b):
			out.Execute(b)
		case b >= 0x40 && b <= 0x7E:
			p.state = stGround
		}

	case stOSCString:
		switch {
		case b == 0x07: // BEL, xterm's terminator and the one everything actually sends
			out.OSCDispatch(p.str)
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

func (p *Parser) ground(b byte, out Sink) {
	switch {
	case isC0(b):
		out.Execute(b)
	case b == 0x7F: // DEL prints nothing
	case b < 0x80:
		p.utf8 = p.utf8[:0]
		out.Print(rune(b))
	default:
		// A read can cut a multi-byte rune in half, so bytes accumulate until the
		// rune is whole. FullRune is true for an invalid lead byte too, which turns
		// junk into one replacement character instead of stalling the stream.
		p.utf8 = append(p.utf8, b)
		if utf8.FullRune(p.utf8) {
			r, _ := utf8.DecodeRune(p.utf8)
			out.Print(r)
			p.utf8 = p.utf8[:0]
		} else if len(p.utf8) >= utf8.UTFMax {
			out.Print(utf8.RuneError)
			p.utf8 = p.utf8[:0]
		}
	}
}

// anywhere handles the transitions the automaton takes from every state: cancel,
// substitute, and the escape that abandons whatever was in flight and starts a fresh
// sequence.
func (p *Parser) anywhere(b byte, out Sink) bool {
	switch b {
	case 0x18, 0x1A: // CAN, SUB
		p.endString(out)
		out.Execute(b)
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
func (p *Parser) endString(out Sink) {
	if p.state == stOSCString {
		out.OSCDispatch(p.str)
	}
}

func (p *Parser) clear() {
	p.params, p.sep, p.inter, p.priv = p.params[:0], p.sep[:0], p.inter[:0], 0
}

func (p *Parser) collect(b byte) {
	if len(p.inter) < maxIntermediates {
		p.inter = append(p.inter, b)
	}
}

// param folds a digit into the current parameter, or opens the next one on a separator
// and remembers which separator it was. An omitted parameter reads as zero, which every
// handler treats as "use the default".
func (p *Parser) param(b byte) {
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
