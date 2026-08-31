//go:build !windows

package vte

// The integration tests: a real shell on a real pty, its bytes crossing from the reader
// goroutine into the parser and onto the grid. Three layers at once, which is the point —
// and also why a failure here does not say which of them broke. The pty's own contract is
// tested apart, in pty_test.go.

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startUntil runs cmd under a real terminal and pumps it — waiting on the update channel,
// exactly as a headless host would — until want shows up on the screen.
func startUntil(t *testing.T, cols, rows int, cmd *exec.Cmd, want string) *Terminal {
	t.Helper()

	tm, err := Start(cols, rows, Options{
		Cmd:         cmd,
		ReportColor: func(int) (r, g, b uint16, ok bool) { return 0, 0, 0, true },
	})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(tm.Close)

	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-tm.UpdateChan():
		case <-deadline:
			t.Fatalf("timed out waiting for %q; screen is %q", want, screenText(tm.scr))
		}

		_, err := tm.Pump()
		if strings.Contains(strings.Join(screenText(tm.scr), "\n"), want) {
			return tm
		}
		if err != nil {
			t.Fatalf("the shell exited without %q on screen: %q", want, screenText(tm.scr))
		}
	}
}

// TestIntegrationPTYFeedsTheTerminal is the milestone end to end.
func TestIntegrationPTYFeedsTheTerminal(t *testing.T) {
	startUntil(t, 80, 24, exec.Command("/bin/sh", "-c", "echo hi"), "hi")
}

// TestIntegrationPTYReportsTheGrid: without TIOCSWINSZ the shell keeps believing it has
// 80x24 and wraps the line being typed in the wrong place. Only the size the pty was
// started at; that it can be changed afterwards is the pty's own test's business.
func TestIntegrationPTYReportsTheGrid(t *testing.T) {
	startUntil(t, 97, 31, exec.Command("/bin/sh", "-c", "stty size"), "31 97")
}

// TestIntegrationPTYAnswersAProbingShell is the regression for a terminal that looked dead:
// fish asks what the terminal can do and draws nothing until it is told. Dropping every
// sequence, which is otherwise the right thing this early, leaves it waiting forever.
func TestIntegrationPTYAnswersAProbingShell(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skipf("fish is not installed: %v", err)
	}
	// fish's stock prompt ends in '>'. Anything on screen at all is the point: before the
	// answers it drew nothing.
	startUntil(t, 98, 26, exec.Command(fish, "--no-config", "-i"), ">")
}

// TestStartWakesTheHost: a host with its own event loop is poked rather than polling, which
// is what lets an idle terminal cost nothing.
func TestStartWakesTheHost(t *testing.T) {
	woken := make(chan struct{}, 8)
	tm, err := Start(80, 24, Options{
		Cmd:  exec.Command("/bin/sh", "-c", "echo poke"),
		Wake: func() { woken <- struct{}{} },
	})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(tm.Close)

	select {
	case <-woken:
	case <-time.After(15 * time.Second):
		t.Fatal("the shell wrote and the host was never woken")
	}
}
