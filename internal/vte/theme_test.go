package vte

import "testing"

func TestThemeQuery(t *testing.T) {
	tm := New(80, 24)
	if got := answer(tm, "\x1b[?996n"); got != "" {
		t.Errorf("a host with no Dark answered %q", got)
	}
	dark := true
	tm.dark = func() bool { return dark }
	if got, want := answer(tm, "\x1b[?996n"), "\x1b[?997;1n"; got != want {
		t.Errorf("dark answered %q, want %q", got, want)
	}
	dark = false
	if got, want := answer(tm, "\x1b[?996n"), "\x1b[?997;2n"; got != want {
		t.Errorf("light answered %q, want %q", got, want)
	}
}

func TestThemeReport(t *testing.T) {
	tm := New(80, 24)
	if got := tm.ThemeReport(true); got != nil {
		t.Errorf("reported %q without DECSET 2031", got)
	}
	tm.Feed([]byte("\x1b[?2031h"))
	if got, want := string(tm.ThemeReport(true)), "\x1b[?997;1n"; got != want {
		t.Errorf("dark reported %q, want %q", got, want)
	}
	if got, want := string(tm.ThemeReport(false)), "\x1b[?997;2n"; got != want {
		t.Errorf("light reported %q, want %q", got, want)
	}
	tm.Feed([]byte("\x1b[?2031l"))
	if got := tm.ThemeReport(true); got != nil {
		t.Errorf("reported %q after DECRST 2031", got)
	}
	tm.Feed([]byte("\x1b[?2031h\x1bc"))
	if got := tm.ThemeReport(true); got != nil {
		t.Errorf("reported %q after RIS", got)
	}
}

func TestThemeModeQuery(t *testing.T) {
	tm := New(80, 24)
	if got, want := answer(tm, "\x1b[?2031$p"), "\x1b[?2031;2$y"; got != want {
		t.Errorf("DECRQM 2031 reset answered %q, want %q", got, want)
	}
	if got, want := answer(tm, "\x1b[?2031h\x1b[?2031$p"), "\x1b[?2031;1$y"; got != want {
		t.Errorf("DECRQM 2031 set answered %q, want %q", got, want)
	}
	if got := answer(tm, "\x1b[?2026$p"); got != "" {
		t.Errorf("DECRQM 2026 answered %q", got)
	}
}
