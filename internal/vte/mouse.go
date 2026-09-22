package vte

import "fmt"

// Mouse tracking: which events a program asked for, how a report is spelled, and the bytes
// that carry one. The events themselves come from the host — this package never sees a
// window and knows nothing about the toolkit that reports them.

// MouseProto is the set of events a program wants, named after the DECSET parameter it
// asked with. The order is deliberate: each protocol carries everything the one before it
// does, so a comparison answers "does this reach far enough".
type MouseProto uint8

const (
	MouseOff    MouseProto = iota
	MouseX10               // 9: presses, and nothing else
	MouseNormal            // 1000: presses and releases
	MouseButton            // 1002: and motion while a button is down
	MouseAny               // 1003: and motion with no button at all
)

// mouseEnc is how a report is spelled. The original encoding cannot carry a coordinate past
// 223, which is why SGR exists and why anything running in a wide window asks for it.
type mouseEnc uint8

const (
	encX10   mouseEnc = iota
	encSGR            // 1006
	encURXVT          // 1015
)

// Mods are the modifiers a report carries. The host translates its own toolkit's mask into
// this one at the callback, which is what keeps glfw out of this package.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModAlt
	ModCtrl
)

// Button codes as a report spells them. The wheel is a button that is only ever pressed.
const (
	MouseLeft      = 0
	MouseMiddle    = 1
	MouseRight     = 2
	MouseWheelUp   = 64
	MouseWheelDown = 65

	// MouseNone is what a motion report names when no button is down, which only
	// any-event tracking ever sends.
	MouseNone = 3
)

// mouseRelease is the code a release carries in the two older spellings, which have no room
// to say which button was let go. SGR keeps the button and changes the final byte instead,
// and that is the whole reason it exists.
const mouseRelease = 3

// MouseReport is the sequence for one event, or nil when there is nothing to send: nobody
// is tracking, or this protocol does not carry this kind of event. row and col are
// one-based, as a report counts them.
func (t *Terminal) MouseReport(btn int, press, motion bool, mods Mods, row, col int) []byte {
	t.mu.RLock()
	proto, enc := t.mouseProto, t.mouseEnc
	t.mu.RUnlock()

	switch {
	case proto == MouseOff:
		return nil
	case motion && proto < MouseButton:
		return nil
	case !press && proto == MouseX10:
		return nil
	}

	code := btn
	if motion {
		code += 32
	}
	if mods&ModShift != 0 {
		code += 4
	}
	if mods&ModAlt != 0 {
		code += 8
	}
	if mods&ModCtrl != 0 {
		code += 16
	}

	if enc == encSGR {
		final := byte('M')
		if !press {
			final = 'm'
		}
		return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", code, col, row, final)
	}
	if !press {
		code = code&^mouseRelease | mouseRelease
	}
	if enc == encURXVT {
		// The same code as X10, printed as a decimal rather than stuffed into a byte.
		return fmt.Appendf(nil, "\x1b[%d;%d;%dM", code+32, col, row)
	}
	return append([]byte("\x1b[M"), x10Byte(code), x10Byte(col), x10Byte(row))
}

// x10Byte is one value in the original encoding: offset by 32 so that it prints, and
// clamped because a single byte cannot say more than 223.
func x10Byte(v int) byte { return byte(min(max(v, 0), 223) + 32) }

// FocusReport is CSI I or CSI O, or nil when nobody asked for them.
func (t *Terminal) FocusReport(in bool) []byte {
	if !t.FocusEvents() {
		return nil
	}
	if in {
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}

// setMouseProto turns one of the four tracking modes on or off. Turning off a mode that is
// not the current one changes nothing, so a program disabling them all in a row stops
// tracking exactly once.
func (t *Terminal) setMouseProto(p MouseProto, on bool) {
	switch {
	case on:
		t.mouseProto = p
	case t.mouseProto == p:
		t.mouseProto = MouseOff
	}
}

func (t *Terminal) setMouseEnc(e mouseEnc, on bool) {
	switch {
	case on:
		t.mouseEnc = e
	case t.mouseEnc == e:
		t.mouseEnc = encX10
	}
}

// resetMouse is what RIS and DECSTR leave behind: nobody tracking, the original spelling,
// and the wheel free to drive the alternate screen again.
func (t *Terminal) resetMouse() {
	t.mouseProto, t.mouseEnc = MouseOff, encX10
	t.focusEvents, t.altScroll = false, true
}

func (t *Terminal) MouseProto() MouseProto {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.mouseProto
}

func (t *Terminal) FocusEvents() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.focusEvents
}

// AltScroll is DECSET 1007: on the alternate screen the wheel drives the program's cursor
// keys, since there is no history there to scroll.
func (t *Terminal) AltScroll() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.altScroll
}

// AltScreen reports whether a full-screen program owns the grid.
func (t *Terminal) AltScreen() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.scr == t.alt
}
