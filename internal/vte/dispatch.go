package vte

import (
	"fmt"
	"strings"
)

// Terminal is the parser's sink. Every method here mutates the active screen with the write
// lock already held; see feed.

func (t *Terminal) putRune(r rune) { t.scr.put(r) }

func (t *Terminal) execute(b byte) {
	switch b {
	case '\n', 0x0B, 0x0C: // LF, VT and FF all feed a line
		t.scr.lineFeed()
	case '\r':
		t.scr.carriageReturn()
	case '\b':
		t.scr.backspace()
	case '\t':
		t.scr.tab()
	}
	// BEL and the rest are dropped. A visual bell is not this milestone's problem.
}

// csi acts on a control sequence. Queries cannot wait: fish probes at every startup and draws
// nothing until answered, and DA1 is the barrier it hangs on.
func (t *Terminal) csi(c CSI) {
	// Of the sequences carrying an intermediate, DECSCUSR (SP q) is the one gty acts on:
	// it is how vim and neovim mark their modes. The rest are dropped on purpose.
	if len(c.Inter) > 0 {
		if c.Private == 0 && c.Final == 'q' && len(c.Inter) == 1 && c.Inter[0] == ' ' {
			t.setCursorStyle(c.Raw(0))
		}
		return
	}
	s := t.scr

	switch c.Private {
	case '?':
		switch c.Final {
		case 'h':
			t.setModes(c.Params, true)
		case 'l':
			t.setModes(c.Params, false)
		}
		return
	case '>':
		if c.Final == 'c' {
			// DA2: terminal type, firmware version, cartridge ROM. Nothing here has a
			// meaningful version yet.
			t.reply("\x1b[>0;0;0c")
		}
		return
	case 0:
	default:
		return
	}

	switch c.Final {
	case '@':
		s.insertChars(c.Arg(0, 1))
	case 'A':
		s.moveBy(-c.Arg(0, 1), 0)
	case 'B', 'e':
		s.moveBy(c.Arg(0, 1), 0)
	case 'C', 'a':
		s.moveBy(0, c.Arg(0, 1))
	case 'D':
		s.moveBy(0, -c.Arg(0, 1))
	case 'E':
		s.moveTo(s.curRow+c.Arg(0, 1), 0)
	case 'F':
		s.moveTo(s.curRow-c.Arg(0, 1), 0)
	case 'G', '`':
		s.moveTo(s.curRow, c.Arg(0, 1)-1)
	case 'H', 'f':
		s.moveTo(c.Arg(0, 1)-1, c.Arg(1, 1)-1)
	case 'J':
		s.eraseInDisplay(c.Raw(0))
	case 'K':
		s.eraseInLine(c.Raw(0))
	case 'L':
		s.insertLines(c.Arg(0, 1))
	case 'M':
		s.deleteLines(c.Arg(0, 1))
	case 'P':
		s.deleteChars(c.Arg(0, 1))
	case 'S':
		for range c.Arg(0, 1) {
			s.scrollUp()
		}
	case 'T':
		for range c.Arg(0, 1) {
			s.scrollDownAt(s.top)
		}
	case 'X':
		s.eraseChars(c.Arg(0, 1))
	case 'd':
		s.moveTo(c.Arg(0, 1)-1, s.curCol)
	case 'c':
		// DA1. VT220 (62) speaking ANSI colour (22), which is what TERM already promises.
		t.reply("\x1b[?62;22c")
	case 'm':
		t.sgr(c)
	case 'n':
		// DSR. fish asks where the cursor is on every redraw; a terminal that does not
		// answer leaves it guessing.
		switch c.Raw(0) {
		case 5:
			t.reply("\x1b[0n")
		case 6:
			t.reply(fmt.Sprintf("\x1b[%d;%dR", s.curRow+1, s.curCol+1))
		}
	case 'r':
		s.setRegion(c.Raw(0), c.Raw(1))
	case 's':
		s.save()
	case 'u':
		s.restore()
	}
}

// setCursorStyle handles DECSCUSR. Only the shape: the odd parameters also ask for a blink,
// which belongs to whatever is drawing. Out of range leaves it alone, as xterm does.
func (t *Terminal) setCursorStyle(param int) {
	switch param {
	case 0:
		t.shape = t.shapeDefault
	case 1, 2:
		t.shape = CursorBlock
	case 3, 4:
		t.shape = CursorUnderline
	case 5, 6:
		t.shape = CursorBar
	}
}

// setModes handles DECSET and DECRST. Anything not listed is recognised and ignored:
// mouse reporting, modifyOtherKeys, the kitty keyboard protocol.
func (t *Terminal) setModes(params []int, on bool) {
	for _, m := range params {
		switch m {
		case 7: // DECAWM
			t.pri.autowrap, t.alt.autowrap = on, on
		case 25: // DECTCEM
			t.visible = on
		case 47, 1047, 1049:
			t.useAlt(on, m == 1049)
		}
	}
}

// useAlt switches between the two grids. 1049 parks the cursor on the way in and restores it
// on the way out, which lets vim leave the prompt where it found it.
//
// A scrolled-back view needs no telling: the alternate screen has no history to clamp against.
func (t *Terminal) useAlt(on, withCursor bool) {
	if on == (t.scr == t.alt) {
		return
	}
	if on {
		if withCursor {
			t.pri.save()
		}
		t.alt.reset()
		t.scr = t.alt
	} else {
		t.scr = t.pri
		if withCursor {
			t.pri.restore()
		}
	}
}

func (t *Terminal) esc(final byte, inter []byte) {
	if len(inter) > 0 {
		return // charset designation and friends
	}
	switch final {
	case '7':
		t.scr.save()
	case '8':
		t.scr.restore()
	case 'D': // IND
		t.scr.lineFeed()
	case 'E': // NEL
		t.scr.carriageReturn()
		t.scr.lineFeed()
	case 'M': // RI
		t.scr.reverseIndex()
	case 'c': // RIS
		t.useAlt(false, false)
		t.pri.reset()
		t.shape = t.shapeDefault
	}
}

func (t *Terminal) osc(data []byte) {
	s := string(data)

	// 0 sets the icon name and the title, 2 the title alone; nothing here shows an icon
	// name, so the two mean the same thing.
	if rest, ok := strings.CutPrefix(s, "0;"); ok {
		t.title = cleanTitle(rest)
		return
	}
	if rest, ok := strings.CutPrefix(s, "2;"); ok {
		t.title = cleanTitle(rest)
		return
	}

	// The colour queries are worth answering because the answer is true: apps ask so they
	// can pick a light or a dark theme.
	switch s {
	case "10;?":
		t.replyColor(10)
	case "11;?":
		t.replyColor(11)
	}
}

// replyColor answers OSC 10 or 11 at the sixteen bits a channel xterm has always sent. A host
// with nothing to say leaves the query unanswered.
func (t *Terminal) replyColor(code int) {
	if t.reportColor == nil {
		return
	}
	r, g, b, ok := t.reportColor(code)
	if !ok {
		return
	}
	t.reply(fmt.Sprintf("\x1b]%d;rgb:%04x/%04x/%04x\x1b\\", code, r, g, b))
}

// MaxTitle bounds the title. Exported because a view sizing a label wants a width no title
// can exceed.
const MaxTitle = 256

// cleanTitle drops the controls that would draw as a replacement box. The parser strips C0
// from an OSC payload, but not DEL.
func cleanTitle(s string) string {
	out := make([]rune, 0, min(len(s), MaxTitle))
	for _, r := range s {
		if r < ' ' || r == 0x7F {
			continue
		}
		if len(out) == MaxTitle {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

// reply queues an answer to a query, collected so one read from the shell produces at most
// one write back. feed flushes it.
func (t *Terminal) reply(s string) { t.answers = append(t.answers, s...) }
