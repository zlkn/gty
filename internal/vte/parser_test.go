package vte

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// recorder is a sink that remembers everything, so a test can assert what the
// automaton recognised as well as what it let through to the screen.
type recorder struct {
	printed strings.Builder
	exec    []byte
	csis    []string
	escs    []string
	oscs    []string
	lastSub []bool
}

func (r *recorder) putRune(c rune) { r.printed.WriteRune(c) }
func (r *recorder) execute(b byte) { r.exec = append(r.exec, b) }

func (r *recorder) csi(c CSI) {
	r.csis = append(r.csis, fmt.Sprintf("%s%v%s%c", privStr(c.Private), c.Params, c.Inter, c.Final))
	r.lastSub = r.lastSub[:0]
	for i := range c.Params {
		r.lastSub = append(r.lastSub, c.Sub(i))
	}
}

func (r *recorder) esc(final byte, inter []byte) {
	r.escs = append(r.escs, fmt.Sprintf("%s%c", inter, final))
}

func (r *recorder) osc(data []byte) { r.oscs = append(r.oscs, string(data)) }

func privStr(p byte) string {
	if p == 0 {
		return ""
	}
	return string(p)
}

func run(input string) *recorder {
	r := &recorder{}
	var p parser
	p.parse([]byte(input), r)
	return r
}

func TestParserPrintsPlainText(t *testing.T) {
	r := run("hello, world")
	if got := r.printed.String(); got != "hello, world" {
		t.Errorf("printed %q, want %q", got, "hello, world")
	}
	if len(r.csis)+len(r.escs)+len(r.oscs) != 0 {
		t.Errorf("plain text produced sequences: csi=%v esc=%v osc=%v", r.csis, r.escs, r.oscs)
	}
}

// TestParserSwallowsSGR is the whole point of building the automaton before the
// handlers: the sequence is recognised and dropped, and only the text reaches the
// screen. Without it a coloured prompt arrives as punctuation.
func TestParserSwallowsSGR(t *testing.T) {
	r := run("\x1b[31mred\x1b[0m")

	if got := r.printed.String(); got != "red" {
		t.Errorf("printed %q, want %q — escape bytes leaked", got, "red")
	}
	if want := []string{"[31]m", "[0]m"}; !slices.Equal(r.csis, want) {
		t.Errorf("recognised %v, want %v", r.csis, want)
	}
}

func TestParserCSIForms(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"no params", "\x1b[H", []string{"[]H"}},
		{"one param", "\x1b[5A", []string{"[5]A"}},
		{"several", "\x1b[1;22;333m", []string{"[1 22 333]m"}},
		{"empty param reads as zero", "\x1b[;5m", []string{"[0 5]m"}},
		{"private marker", "\x1b[?25l", []string{"?[25]l"}},
		{"private with several", "\x1b[?1049;25h", []string{"?[1049 25]h"}},
		{"intermediate", "\x1b[ q", []string{"[] q"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(tc.in)
			if !slices.Equal(r.csis, tc.want) {
				t.Errorf("%q recognised as %v, want %v", tc.in, r.csis, tc.want)
			}
			if got := r.printed.String(); got != "" {
				t.Errorf("%q printed %q, want nothing", tc.in, got)
			}
		})
	}
}

// TestParserKeepsSubParameters: colons separate a parameter's parts, and only the
// separator tells 38;2;r;g;b from the colon form — so the parser has to keep it.
func TestParserKeepsSubParameters(t *testing.T) {
	r := &recorder{}
	var p parser
	p.parse([]byte("\x1b[38:2::255:0:0mX"), r)

	if got := r.printed.String(); got != "X" {
		t.Errorf("printed %q, want %q", got, "X")
	}
	if want := []string{"[38 2 0 255 0 0]m"}; !slices.Equal(r.csis, want) {
		t.Errorf("recognised %v, want %v", r.csis, want)
	}
	if want := []bool{false, true, true, true, true, true}; !slices.Equal(r.lastSub, want) {
		t.Errorf("separators recorded as %v, want %v", r.lastSub, want)
	}

	// The semicolon form is the same numbers with different separators, which is the
	// whole reason the parser has to keep them.
	r = &recorder{}
	p = parser{}
	p.parse([]byte("\x1b[38;2;255;0;0m"), r)
	if want := []bool{false, false, false, false, false}; !slices.Equal(r.lastSub, want) {
		t.Errorf("semicolon form recorded as %v, want %v", r.lastSub, want)
	}
}

func TestParserOSC(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"terminated by BEL", "\x1b]0;a title\x07"},
		{"terminated by ST", "\x1b]0;a title\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(tc.in + "after")
			if want := []string{"0;a title"}; !slices.Equal(r.oscs, want) {
				t.Errorf("osc %v, want %v", r.oscs, want)
			}
			if got := r.printed.String(); got != "after" {
				t.Errorf("printed %q, want %q", got, "after")
			}
		})
	}
}

func TestParserExecutesC0(t *testing.T) {
	r := run("a\r\nb\tc\bd")

	if got := r.printed.String(); got != "abcd" {
		t.Errorf("printed %q, want %q", got, "abcd")
	}
	if want := []byte{'\r', '\n', '\t', '\b'}; !slices.Equal(r.exec, want) {
		t.Errorf("executed %v, want %v", r.exec, want)
	}
}

// TestParserSurvivesSplitInput: a read from the pty cuts wherever it likes, and the
// automaton has to carry its state across the seam.
func TestParserSurvivesSplitInput(t *testing.T) {
	r := &recorder{}
	var p parser
	for _, chunk := range []string{"ab\x1b", "[3", "1m", "cd", "\xd0", "\xbf"} {
		p.parse([]byte(chunk), r)
	}

	if got, want := r.printed.String(), "abcdп"; got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if want := []string{"[31]m"}; !slices.Equal(r.csis, want) {
		t.Errorf("recognised %v, want %v", r.csis, want)
	}
}

func TestParserInvalidUTF8(t *testing.T) {
	r := run("a\xffb")
	if got, want := r.printed.String(), "a�b"; got != want {
		t.Errorf("printed %q, want %q — a bad byte must not stall the stream", got, want)
	}
}

// TestParserCancelAbandonsSequence: CAN and SUB drop whatever was in flight, so the
// bytes after them are ordinary text again.
func TestParserCancelAbandonsSequence(t *testing.T) {
	r := run("\x1b[12\x18m")

	if got := r.printed.String(); got != "m" {
		t.Errorf("printed %q, want %q", got, "m")
	}
	if len(r.csis) != 0 {
		t.Errorf("recognised %v, want the cancelled sequence dropped", r.csis)
	}
}

func TestParserSwallowsDCS(t *testing.T) {
	r := run("\x1bP1$r0m\x1b\\X")
	if got := r.printed.String(); got != "X" {
		t.Errorf("printed %q, want %q", got, "X")
	}
}

// TestParserRealPrompt is the case the milestone exists for: the standard coloured
// bash PS1, which sets the window title and four colours before printing anything.
func TestParserRealPrompt(t *testing.T) {
	const prompt = "\x1b]0;user@host: ~\x07\x1b[01;32muser@host\x1b[00m:" +
		"\x1b[01;34m~\x1b[00m$ "

	r := run(prompt)
	if got, want := r.printed.String(), "user@host:~$ "; got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	// The payload arrives whole, command number included: splitting it is the OSC
	// handler's job, not the automaton's.
	if want := []string{"0;user@host: ~"}; !slices.Equal(r.oscs, want) {
		t.Errorf("osc %v, want %v", r.oscs, want)
	}
	if len(r.csis) != 4 {
		t.Errorf("recognised %v, want four colour sequences", r.csis)
	}
}
