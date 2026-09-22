package vte

import "testing"

// TestMouseProtoSwitching: a program turns one mode on and another off, and only the mode
// it is actually in can turn tracking off.
func TestMouseProtoSwitching(t *testing.T) {
	tm := New(80, 24)
	if tm.MouseProto() != MouseOff {
		t.Error("a fresh terminal is already tracking the mouse")
	}

	tm.Feed([]byte("\x1b[?1002h"))
	if got := tm.MouseProto(); got != MouseButton {
		t.Errorf("DECSET 1002 left the protocol at %d, want %d", got, MouseButton)
	}

	// Disabling a mode it is not in changes nothing; this is what an application's
	// teardown sends, one parameter at a time.
	tm.Feed([]byte("\x1b[?1000l"))
	if got := tm.MouseProto(); got != MouseButton {
		t.Errorf("DECRST 1000 turned off 1002, leaving %d", got)
	}
	tm.Feed([]byte("\x1b[?1002l"))
	if got := tm.MouseProto(); got != MouseOff {
		t.Errorf("DECRST 1002 left the protocol at %d, want off", got)
	}

	tm.Feed([]byte("\x1b[?1003h\x1b[?1006h\x1b[?1004h"))
	if !tm.FocusEvents() {
		t.Error("DECSET 1004 did not turn focus events on")
	}
	tm.Feed([]byte("\x1bc")) // RIS
	if tm.MouseProto() != MouseOff || tm.FocusEvents() {
		t.Error("RIS left mouse tracking behind")
	}
}

func TestMouseReportEncodings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup string
		btn   int
		press bool
		mods  Mods
		want  string
	}{
		{"x10 press", "\x1b[?1000h", MouseLeft, true, 0, "\x1b[M\x20\x22\x21"},
		{"x10 release loses the button", "\x1b[?1000h", MouseRight, false, 0, "\x1b[M#\x22\x21"},
		{"x10 with control", "\x1b[?1000h", MouseLeft, true, ModCtrl, "\x1b[M0\x22\x21"},
		{"sgr press", "\x1b[?1000h\x1b[?1006h", MouseMiddle, true, 0, "\x1b[<1;2;1M"},
		{"sgr keeps the button on release", "\x1b[?1000h\x1b[?1006h", MouseRight, false, 0, "\x1b[<2;2;1m"},
		{"urxvt prints the same code", "\x1b[?1000h\x1b[?1015h", MouseLeft, true, 0, "\x1b[32;2;1M"},
		{"wheel is a button", "\x1b[?1000h\x1b[?1006h", MouseWheelUp, true, 0, "\x1b[<64;2;1M"},
	} {
		tm := New(80, 24)
		tm.Feed([]byte(tc.setup))
		got := string(tm.MouseReport(tc.btn, tc.press, false, tc.mods, 1, 2))
		if got != tc.want {
			t.Errorf("%s reported %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMouseReportSilence: every case where a report is not owed.
func TestMouseReportSilence(t *testing.T) {
	tm := New(80, 24)
	if got := tm.MouseReport(MouseLeft, true, false, 0, 1, 1); got != nil {
		t.Errorf("an untracked press reported %q", got)
	}

	tm.Feed([]byte("\x1b[?9h")) // X10: presses only
	if got := tm.MouseReport(MouseLeft, false, false, 0, 1, 1); got != nil {
		t.Errorf("X10 reported a release: %q", got)
	}

	tm.Feed([]byte("\x1b[?9l\x1b[?1000h")) // normal tracking carries no motion
	if got := tm.MouseReport(MouseLeft, true, true, 0, 1, 1); got != nil {
		t.Errorf("normal tracking reported motion: %q", got)
	}
	tm.Feed([]byte("\x1b[?1002h"))
	if got := string(tm.MouseReport(MouseLeft, true, true, 0, 1, 1)); got == "" {
		t.Error("button-event tracking dropped a motion report")
	}
}

// TestMouseCoordinatesClamp: the original encoding has one byte per ordinate, so a window
// wider than 223 columns pins the report rather than wrapping it.
func TestMouseCoordinatesClamp(t *testing.T) {
	tm := New(300, 24)
	tm.Feed([]byte("\x1b[?1000h"))

	got := tm.MouseReport(MouseLeft, true, false, 0, 1, 400)
	if want := byte(223 + 32); got[4] != want {
		t.Errorf("column 400 encoded as %d, want %d", got[4], want)
	}

	tm.Feed([]byte("\x1b[?1006h"))
	if got, want := string(tm.MouseReport(MouseLeft, true, false, 0, 1, 400)), "\x1b[<0;400;1M"; got != want {
		t.Errorf("SGR reported %q, want %q — it has no such limit", got, want)
	}
}

func TestFocusReport(t *testing.T) {
	tm := New(80, 24)
	if got := tm.FocusReport(true); got != nil {
		t.Errorf("focus was reported without DECSET 1004: %q", got)
	}
	tm.Feed([]byte("\x1b[?1004h"))
	if got, want := string(tm.FocusReport(true)), "\x1b[I"; got != want {
		t.Errorf("focus in reported %q, want %q", got, want)
	}
	if got, want := string(tm.FocusReport(false)), "\x1b[O"; got != want {
		t.Errorf("focus out reported %q, want %q", got, want)
	}
}
