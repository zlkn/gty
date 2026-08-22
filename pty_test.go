//go:build !windows

package main

import (
	"image"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// pumpUntil runs the real loop — read on one goroutine, parse and answer on this one —
// until want shows up on the pane's screen.
func pumpUntil(t *testing.T, p *pane, cmd *exec.Cmd, want string) {
	t.Helper()

	woken := make(chan struct{}, 64)
	s, err := startCommand(cmd, p.cols, p.rows, func() {
		select {
		case woken <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(s.close)
	p.pty = s

	var buf []byte
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-woken:
		case <-deadline:
			t.Fatalf("timed out waiting for %q; screen is %q", want, screenText(p.scr))
		}

		buf, err = s.take(buf)
		s.write(p.feed(buf))
		if strings.Contains(strings.Join(screenText(p.scr), "\n"), want) {
			return
		}
		if err != nil {
			t.Fatalf("the shell exited without %q on screen: %q", want, screenText(p.scr))
		}
	}
}

// TestPTYFeedsThePane is the milestone end to end: a real shell on a real pty, its
// bytes crossing from the reader goroutine to the main thread, through the parser and
// onto the grid.
func TestPTYFeedsThePane(t *testing.T) {
	p := gridPane(1, image.Rect(0, 0, 800, 600), 80, 24)
	pumpUntil(t, p, exec.Command("/bin/sh", "-c", "echo hi"), "hi")
}

// TestPTYReportsTheGrid: without TIOCSWINSZ the shell keeps believing it has 80x24 and
// wraps the line being typed in the wrong place.
func TestPTYReportsTheGrid(t *testing.T) {
	const cols, rows = 97, 31
	p := gridPane(1, image.Rect(0, 0, 1200, 800), cols, rows)
	pumpUntil(t, p, exec.Command("/bin/sh", "-c", "stty size"), "31 97")
}

// TestPTYAnswersAProbingShell is the regression for a terminal that looked dead: fish
// asks what the terminal can do and draws nothing until it is told. Dropping every
// sequence, which is otherwise the right thing this early, leaves it waiting forever.
func TestPTYAnswersAProbingShell(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skipf("fish is not installed: %v", err)
	}
	p := gridPane(1, image.Rect(0, 0, 900, 600), 98, 26)
	// fish's stock prompt ends in '>'. Anything on screen at all is the point: before
	// the answers it drew nothing.
	pumpUntil(t, p, exec.Command(fish, "--no-config", "-i"), ">")
}
