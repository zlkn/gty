package main

import (
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
)

// recorder is a sink that remembers everything, so a test can assert what the
// automaton recognised as well as what it let through to the screen.
type recorder struct {
	printed strings.Builder
	exec    []byte
	csi     []string
	esc     []string
	osc     []string
	lastSub []bool
}

func (r *recorder) print(c rune)   { r.printed.WriteRune(c) }
func (r *recorder) execute(b byte) { r.exec = append(r.exec, b) }

func (r *recorder) csiDispatch(c csi) {
	r.csi = append(r.csi, fmt.Sprintf("%s%v%s%c", privStr(c.private), c.params, c.inter, c.final))
	r.lastSub = r.lastSub[:0]
	for i := range c.params {
		r.lastSub = append(r.lastSub, c.sub(i))
	}
}

func (r *recorder) escDispatch(final byte, inter []byte) {
	r.esc = append(r.esc, fmt.Sprintf("%s%c", inter, final))
}

func (r *recorder) oscDispatch(data []byte) { r.osc = append(r.osc, string(data)) }

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
	if len(r.csi)+len(r.esc)+len(r.osc) != 0 {
		t.Errorf("plain text produced sequences: csi=%v esc=%v osc=%v", r.csi, r.esc, r.osc)
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
	if want := []string{"[31]m", "[0]m"}; !slices.Equal(r.csi, want) {
		t.Errorf("recognised %v, want %v", r.csi, want)
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
			if !slices.Equal(r.csi, tc.want) {
				t.Errorf("%q recognised as %v, want %v", tc.in, r.csi, tc.want)
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
	if want := []string{"[38 2 0 255 0 0]m"}; !slices.Equal(r.csi, want) {
		t.Errorf("recognised %v, want %v", r.csi, want)
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
			if want := []string{"0;a title"}; !slices.Equal(r.osc, want) {
				t.Errorf("osc %v, want %v", r.osc, want)
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
	if want := []string{"[31]m"}; !slices.Equal(r.csi, want) {
		t.Errorf("recognised %v, want %v", r.csi, want)
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
	if len(r.csi) != 0 {
		t.Errorf("recognised %v, want the cancelled sequence dropped", r.csi)
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
	if want := []string{"0;user@host: ~"}; !slices.Equal(r.osc, want) {
		t.Errorf("osc %v, want %v", r.osc, want)
	}
	if len(r.csi) != 4 {
		t.Errorf("recognised %v, want four colour sequences", r.csi)
	}
}

// TestPaneParsesOntoTheScreen closes the loop: bytes in, text on the grid, no escape
// residue.
func TestPaneParsesOntoTheScreen(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 300), 40, 3)
	p.feed([]byte("\x1b]0;title\x07\x1b[32mgreen\x1b[0m text\r\nsecond line"))

	if got, want := screenText(p.scr), []string{"green text", "second line", ""}; !slices.Equal(got, want) {
		t.Errorf("screen reads %q, want %q", got, want)
	}
	if p.scr.curRow != 1 || p.scr.curCol != len("second line") {
		t.Errorf("cursor at row %d col %d, want row 1 col %d", p.scr.curRow, p.scr.curCol, len("second line"))
	}
}

// fishStartup is what fish actually sends the moment it starts: capability queries and
// nothing else. It draws no prompt until they are answered, which is why a terminal
// that only drops sequences looks to the user like a shell that never started.
const fishStartup = "\x1b[?u" + // kitty keyboard flags
	"\x1b[>0q" + // XTVERSION
	"\x1b]11;?\x1b\\" + // what is your background
	"\x1b[?1049h" + "\x1bP+q696e646e\x1b\\" + "\x1bP+q71756572792d6f732d6e616d65\x1b\\" + "\x1b[?1049l" +
	"\x1b[0c" // DA1, last: the barrier the rest hang on

func TestPaneAnswersFishStartup(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)

	got := string(p.feed([]byte(fishStartup)))
	if !strings.Contains(got, "\x1b[?62;22c") {
		t.Errorf("answered %q, want a DA1 report in it — fish blocks on this one", got)
	}
	if !strings.Contains(got, "\x1b]11;rgb:") {
		t.Errorf("answered %q, want the background colour it asked for", got)
	}
	if screen := strings.Join(trimTrailing(screenText(p.scr)), ""); screen != "" {
		t.Errorf("the query batch put %q on screen, want nothing", screen)
	}
}

func TestPaneAnswersDeviceAttributes(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"DA1 with no parameter", "\x1b[c", "\x1b[?62;22c"},
		{"DA1 with an explicit zero", "\x1b[0c", "\x1b[?62;22c"},
		{"DA2", "\x1b[>c", "\x1b[>0;0;0c"},
		{"DA3 is not answered", "\x1b[=c", ""},
		{"an unrelated final is not answered", "\x1b[0n", ""},
		{"kitty keyboard is left unanswered on purpose", "\x1b[?u", ""},
		{"XTVERSION is left unanswered on purpose", "\x1b[>0q", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
			if got := string(p.feed([]byte(tc.in))); got != tc.want {
				t.Errorf("%q answered %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPaneAnswersColorQueries(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)

	// backgroundRGBA is 0.09, 0.10, 0.12.
	if got, want := string(p.feed([]byte("\x1b]11;?\x1b\\"))), "\x1b]11;rgb:170a/1999/1eb8\x1b\\"; got != want {
		t.Errorf("background query answered %q, want %q", got, want)
	}
	if got := string(p.feed([]byte("\x1b]10;?\x1b\\"))); !strings.HasPrefix(got, "\x1b]10;rgb:") {
		t.Errorf("foreground query answered %q, want an OSC 10 report", got)
	}
	// Setting a colour is not a query and must not be answered.
	if got := string(p.feed([]byte("\x1b]11;#000000\x1b\\"))); got != "" {
		t.Errorf("a colour assignment was answered with %q, want nothing", got)
	}
}

func trimTrailing(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
