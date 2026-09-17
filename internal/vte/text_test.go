package vte

import (
	"strings"
	"testing"
)

func TestBracketedPaste(t *testing.T) {
	tm := New(80, 24)
	if tm.BracketedPaste() {
		t.Fatal("bracketed paste should be off by default")
	}

	tm.Feed([]byte("\x1b[?2004h"))
	if !tm.BracketedPaste() {
		t.Fatal("bracketed paste should be enabled after DECSET 2004")
	}

	tm.Feed([]byte("\x1b[?2004l"))
	if tm.BracketedPaste() {
		t.Fatal("bracketed paste should be disabled after DECRST 2004")
	}
}

func TestTerminalText(t *testing.T) {
	tm := gridTerm(10, 4, "hello", "world", "third line", "fourth")
	frame := tm.Frame(nil, 0)
	if len(frame.Lines) != 4 {
		t.Fatalf("want 4 lines, got %d", len(frame.Lines))
	}

	seq0 := frame.Lines[0].Seq
	seq1 := frame.Lines[1].Seq
	seq2 := frame.Lines[2].Seq

	// 1. Single-row partial columns
	if got, want := tm.Text(Pos{Seq: seq0, Col: 0}, Pos{Seq: seq0, Col: 5}, false), "hello"; got != want {
		t.Errorf("Text(0, 5) = %q, want %q", got, want)
	}
	if got, want := tm.Text(Pos{Seq: seq0, Col: 1}, Pos{Seq: seq0, Col: 4}, false), "ell"; got != want {
		t.Errorf("Text(1, 4) = %q, want %q", got, want)
	}

	// 2. Multi-row extraction
	if got, want := tm.Text(Pos{Seq: seq0, Col: 1}, Pos{Seq: seq1, Col: 3}, false), "ello\nwor"; got != want {
		t.Errorf("Text multi-row = %q, want %q", got, want)
	}

	// 3. Block mode extraction
	if got, want := tm.Text(Pos{Seq: seq0, Col: 1}, Pos{Seq: seq2, Col: 4}, true), "ell\norl\nhir"; got != want {
		t.Errorf("Text block = %q, want %q", got, want)
	}

	// 4. Inverted from/to order
	if got, want := tm.Text(Pos{Seq: seq1, Col: 3}, Pos{Seq: seq0, Col: 1}, false), "ello\nwor"; got != want {
		t.Errorf("Text inverted = %q, want %q", got, want)
	}

	// 5. Evicted Seq returns ""
	if got := tm.Text(Pos{Seq: 999999, Col: 0}, Pos{Seq: 9999999, Col: 5}, false); got != "" {
		t.Errorf("Text for evicted/future seq = %q, want \"\"", got)
	}
}

func TestTerminalTextScrollbackRagged(t *testing.T) {
	// A small terminal where lines retire into scrollback.
	// Scrollback rows are stored TrimBlanks-shortened, so row.Cells is ragged.
	tm := New(20, 2)
	feedText(tm, "short", "also short", "third", "live1", "live2")

	frame := tm.Frame(nil, 3) // look back into history
	if len(frame.Lines) == 0 {
		t.Fatal("expected lines in frame")
	}

	seqFirst := frame.Lines[0].Seq
	seqLast := frame.Lines[len(frame.Lines)-1].Seq

	// Selecting up to column 20 across ragged scrollback lines must not panic.
	text := tm.Text(Pos{Seq: seqFirst, Col: 0}, Pos{Seq: seqLast, Col: 20}, false)
	if !strings.Contains(text, "short") {
		t.Errorf("expected text to contain 'short', got %q", text)
	}
}

func TestTerminalTextWrappedJoin(t *testing.T) {
	// 5-column terminal: writing 10 characters without newline will autowrap onto line 2.
	tm := New(5, 4)
	tm.Feed([]byte("1234567890"))

	frame := tm.Frame(nil, 0)
	if !frame.Lines[0].Wrapped {
		t.Errorf("expected row 0 to be marked Wrapped, got false")
	}

	// Extracting across the wrap should join them without a newline.
	got := tm.Text(Pos{Seq: frame.Lines[0].Seq, Col: 0}, Pos{Seq: frame.Lines[1].Seq, Col: 5}, false)
	want := "1234567890"
	if got != want {
		t.Errorf("Text across soft-wrap = %q, want %q", got, want)
	}

	// In block mode, it should preserve newline between rows regardless of Wrapped.
	gotBlock := tm.Text(Pos{Seq: frame.Lines[0].Seq, Col: 1}, Pos{Seq: frame.Lines[1].Seq, Col: 4}, true)
	wantBlock := "234\n789"
	if gotBlock != wantBlock {
		t.Errorf("Text block across soft-wrap = %q, want %q", gotBlock, wantBlock)
	}
}

func TestRowWrappedClearedOnScroll(t *testing.T) {
	tm := New(5, 2)
	// Write wrapped line on row 0 and 1
	tm.Feed([]byte("1234567890"))
	f1 := tm.Frame(nil, 0)
	if !f1.Lines[0].Wrapped {
		t.Fatal("expected row 0 to be wrapped")
	}

	// Scroll it off by writing new non-wrapped lines
	tm.Feed([]byte("\r\nABC\r\nDEF\r\n"))
	f2 := tm.Frame(nil, 0)
	// The reused screen rows should have Wrapped = false
	for i, r := range f2.Lines {
		if r.Wrapped {
			t.Errorf("expected reused row %d to have Wrapped = false", i)
		}
	}
}
