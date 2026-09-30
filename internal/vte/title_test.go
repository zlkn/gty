package vte

import (
	"os"
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

// TestOSCCwd: OSC 7 names the shell's directory, but only one on this machine.
func TestOSCCwd(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"no host, ends with BEL", "\x1b]7;file:///tmp\a", "/tmp"},
		{"localhost, ends with ST", "\x1b]7;file://localhost/usr/lib\x1b\\", "/usr/lib"},
		{"percent-encoded", "\x1b]7;file:///tmp/a%20b\a", "/tmp/a b"},
		{"another host names nothing here", "\x1b]7;file://elsewhere.invalid/srv\a", ""},
		{"not a file url", "\x1b]7;http:///tmp\a", ""},
		{"not absolute", "\x1b]7;tmp\a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := vtTerm(40, 4, tc.in)
			if got := term.Cwd(); got != tc.want {
				t.Errorf("%q left the directory %q, want %q", tc.in, got, tc.want)
			}
			if got := term.Title(); got != "" {
				t.Errorf("%q set the title to %q", tc.in, got)
			}
		})
	}
}

// TestOSCCwdOwnHost: the host name a local shell puts in the URL is this machine's.
func TestOSCCwdOwnHost(t *testing.T) {
	h, err := os.Hostname()
	if err != nil {
		t.Skipf("no hostname: %v", err)
	}
	if got := vtTerm(40, 4, "\x1b]7;file://"+h+"/tmp\a").Cwd(); got != "/tmp" {
		t.Errorf("a URL from %s left the directory %q, want /tmp", h, got)
	}
}
