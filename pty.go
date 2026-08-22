package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// ptySession is a shell on the far end of a pseudo-terminal.
//
// Only pump touches the file. It parses nothing: bytes land in pend and the event loop
// is woken to drain them, so the parser and the screen stay on the main thread. That
// is what keeps the whole terminal state lock-free rather than merely mutexed.
type ptySession struct {
	cmd *exec.Cmd
	f   *os.File

	mu   sync.Mutex
	pend []byte
	err  error // EOF or a read failure; the pane closes on it
}

// startPTY spawns the user's shell on a new pseudo-terminal sized to cols x rows and
// starts pumping its output. wake is called from the reader's own goroutine whenever
// there is something new to draw.
func startPTY(cols, rows int, wake func()) (*ptySession, error) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	cmd := exec.Command(sh)
	// The real TERM on purpose: let the shell send everything it would normally send,
	// so the parser is held to swallowing all of it rather than only what it handles.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	return startCommand(cmd, cols, rows, wake)
}

// startCommand puts cmd on a new pseudo-terminal and starts pumping its output.
func startCommand(cmd *exec.Cmd, cols, rows int, wake func()) (*ptySession, error) {
	f, err := pty.StartWithSize(cmd, winsize(cols, rows))
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	s := &ptySession{cmd: cmd, f: f}
	go s.pump(wake)
	return s, nil
}

func winsize(cols, rows int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(max(cols, 1)), Rows: uint16(max(rows, 1))}
}

func (s *ptySession) pump(wake func()) {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.f.Read(buf)

		s.mu.Lock()
		s.pend = append(s.pend, buf[:n]...)
		if err != nil {
			s.err = err
		}
		s.mu.Unlock()

		wake()
		if err != nil {
			return
		}
	}
}

// take moves everything read since the last call into dst, and reports whether the
// shell is gone. dst is reused between calls so a busy pane does not allocate a buffer
// per frame.
func (s *ptySession) take(dst []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst = append(dst[:0], s.pend...)
	s.pend = s.pend[:0]
	return dst, s.err
}

// resize tells the shell its new geometry. Without it readline keeps believing the
// terminal is 80x24 and wraps the line being typed in the wrong place.
func (s *ptySession) resize(cols, rows int) { _ = pty.Setsize(s.f, winsize(cols, rows)) }

func (s *ptySession) write(b []byte) {
	if len(b) > 0 {
		_, _ = s.f.Write(b)
	}
}

// close ends the session. Closing the master hangs up on the child, and the Wait
// reaps it so a long session of splits does not leave zombies.
func (s *ptySession) close() {
	s.f.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}
