//go:build !windows

package pty

// What this file is for: the contract a Session owes its caller, tested without a pane,
// a parser or a grid in the way. The end-to-end test — bytes from a real shell landing
// as text on the screen — lives in the root package and covers all three at once, so it
// cannot say which of them broke. These can, and being inside the package they can also
// reach s.pend, s.err and s.cmd, which is where the answers to "was the child reaped"
// and "what does a departed shell leave behind" actually live.

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// session starts cmd on a pty and hands back the session together with the channel its
// wake callback feeds. Buffered and non-blocking, like the real one: the reader goroutine
// must never stall on a listener that has stopped listening.
func session(t *testing.T, cmd *exec.Cmd, cols, rows int) (*Session, <-chan struct{}) {
	t.Helper()

	woken := make(chan struct{}, 64)
	s, err := StartCommand(cmd, cols, rows, func() {
		select {
		case woken <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(s.Close)
	return s, woken
}

// drainUntil reads the way the event loop does — block for a wake, take whatever is
// there — until want turns up, and returns everything read.
func drainUntil(t *testing.T, s *Session, woken <-chan struct{}, want string) string {
	t.Helper()

	var buf, all []byte
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-woken:
		case <-deadline:
			t.Fatalf("timed out waiting for %q; the shell wrote %q", want, all)
		}

		var err error
		buf, err = s.Take(buf)
		all = append(all, buf...)
		if strings.Contains(string(all), want) {
			return string(all)
		}
		if err != nil {
			t.Fatalf("the shell exited without writing %q; it wrote %q (%v)", want, all, err)
		}
	}
}

// TestTakeEmptiesTheBuffer: Take is called on every frame, and bytes it served once must
// not come back on the next one — they would be parsed and printed twice.
//
// No process here on purpose. pend is reachable from inside the package, so the buffer
// handover is testable on its own, with no timing in it to go flaky.
func TestTakeEmptiesTheBuffer(t *testing.T) {
	s := &Session{pend: []byte("first")}

	got, err := s.Take(nil)
	if string(got) != "first" || err != nil {
		t.Fatalf("Take gave %q, %v; want %q and no error", got, err, "first")
	}

	// Passing the same slice back is how the caller avoids allocating per frame; it must
	// come back empty rather than still holding the previous chunk.
	got, err = s.Take(got)
	if len(got) != 0 || err != nil {
		t.Errorf("the second Take gave %q, %v; want nothing and no error", got, err)
	}
}

// TestTakeReportsTheErrorAfterTheLastBytes: the error and the final bytes arrive from the
// same call, and the caller is expected to feed the bytes before acting on the error.
// Handing the error over early would drop whatever the shell printed on its way out.
func TestTakeReportsTheErrorAfterTheLastBytes(t *testing.T) {
	s := &Session{pend: []byte("goodbye"), err: io.EOF}

	got, err := s.Take(nil)
	if string(got) != "goodbye" {
		t.Errorf("Take gave %q, want the last bytes %q alongside the error", got, "goodbye")
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("Take reported %v, want %v", err, io.EOF)
	}
}

// TestTheShellsDepartureWakesTheLoop covers the two halves of a shell that dies without
// printing anything.
//
// The wake, because the event loop parks in WaitEvents: a pane whose shell exited in
// silence would sit there looking alive until some unrelated event arrived. Reaching the
// assertion below at all is that half — drainUntil only ever runs after a wake.
//
// And what lands in err, because the caller closes the pane on any error at all and so
// never inspects it. Reading a master whose child is gone is EIO on Linux and EOF on the
// BSDs; pinning both is what stops a third answer from going unnoticed.
func TestTheShellsDepartureWakesTheLoop(t *testing.T) {
	s, woken := session(t, exec.Command("/bin/sh", "-c", "exit 0"), 80, 24)

	var buf []byte
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-woken:
		case <-deadline:
			t.Fatal("the shell exited without waking the loop")
		}

		var err error
		if buf, err = s.Take(buf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) {
				t.Errorf("a departed shell surfaced as %v; want %v or %v", err, io.EOF, syscall.EIO)
			}
			return
		}
	}
}

// TestResizeReachesTheChild is the gap the end-to-end test leaves: it only ever checks the
// size StartWithSize was given, so nothing exercises TIOCSWINSZ after the start. Without
// it readline goes on believing the terminal is whatever it was when the shell spawned and
// wraps the line being typed in the wrong place.
//
// The sleep is the synchronisation: stty reads the size a second in, by which time the
// resize below has long landed. Started at 20x5, so the expected answer cannot be the one
// the child would have reported anyway.
func TestResizeReachesTheChild(t *testing.T) {
	s, woken := session(t, exec.Command("/bin/sh", "-c", "sleep 1; stty size"), 20, 5)

	s.Resize(97, 31)
	drainUntil(t, s, woken, "31 97") // rows first, which is how stty prints it
}

// TestCloseReapsTheChild: closing the master hangs up on the child, but hanging up is not
// reaping. A session of splits and closes would otherwise leave a zombie behind each one.
//
// ProcessState is set by Wait and by nothing else, so it is the honest question to ask.
// The cleanup registered by session then closes a second time, which is also the point:
// the pane's release path can reach this twice and must not panic.
func TestCloseReapsTheChild(t *testing.T) {
	s, _ := session(t, exec.Command("/bin/sh", "-c", "sleep 30"), 80, 24)

	s.Close()
	if s.cmd.ProcessState == nil {
		t.Fatal("Close returned before the child was reaped")
	}
}

// TestTakeLosesNothing runs enough output through to cross several reads, so the append
// into pend and the reset in Take genuinely interleave. The assertion is that no line went
// missing; under -race the same test is what covers the lock between the two goroutines.
func TestTakeLosesNothing(t *testing.T) {
	const lines = 2000
	script := fmt.Sprintf("i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); done", lines)
	s, woken := session(t, exec.Command("/bin/sh", "-c", script), 80, 24)

	got := drainUntil(t, s, woken, fmt.Sprintf("line%d\r\n", lines-1))
	for i := range lines {
		// With the terminator, or "line1" would be found inside "line19".
		if !strings.Contains(got, fmt.Sprintf("line%d\r\n", i)) {
			t.Fatalf("line%d never arrived; %d bytes read", i, len(got))
		}
	}
}

// TestStartGivesTheShellItsEnvironment: Start is where the terminal introduces itself, and
// the real TERM is deliberate — it makes the shell send everything it would normally send,
// which is what holds the parser to swallowing all of it.
func TestStartGivesTheShellItsEnvironment(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")

	woken := make(chan struct{}, 64)
	s, err := Start(80, 24, func() {
		select {
		case woken <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(s.Close)

	// The brackets keep the answer apart from the tty's echo of the line that asked.
	s.Write([]byte("echo \"[$TERM][$COLORTERM]\"\n"))
	drainUntil(t, s, woken, "[xterm-256color][truecolor]")
}
