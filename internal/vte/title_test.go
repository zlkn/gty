package vte

import (
	"strings"
	"testing"
)

// TestOSCTitle: OSC 0 or 2 names the session, terminated either way.
func TestOSCTitle(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"osc 2 ends with ST", "\x1b]2;editing layout.go\x1b\\", "editing layout.go"},
		{"osc 0 ends with BEL", "\x1b]0;~/Personal/gty\a", "~/Personal/gty"},
		{"a DEL is not part of a title", "\x1b]2;one\x7ftwo\x1b\\", "onetwo"},
		{"a colour query is not a title", "\x1b]11;?\x1b\\", ""},
		{"an empty title clears it", "\x1b]2;\x1b\\", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := vtTerm(40, 4, tc.in).Title(); got != tc.want {
				t.Errorf("%q left the title %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCleanTitleCaps: a shell cannot hang an unbounded string on a host's tab.
func TestCleanTitleCaps(t *testing.T) {
	if got := len([]rune(cleanTitle(strings.Repeat("x", MaxTitle+50)))); got != MaxTitle {
		t.Errorf("a title of %d runes came back as %d, want it capped at %d", MaxTitle+50, got, MaxTitle)
	}
}
