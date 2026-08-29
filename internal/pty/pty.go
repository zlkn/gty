package pty

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	creack "github.com/creack/pty"
)

// Only pump reads the file, buffering raw bytes into pend and waking the event loop.
// Confining parsing and screen updates to the main thread keeps terminal state lock-free.
type Session struct {
	cmd *exec.Cmd
	f   *os.File

	mu   sync.Mutex
	pend []byte
	err  error // EOF or a read failure; the pane closes on it
}

func Start(cols, rows int, wake func()) (*Session, error) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	cmd := exec.Command(sh)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	return StartCommand(cmd, cols, rows, wake)
}

func StartCommand(cmd *exec.Cmd, cols, rows int, wake func()) (*Session, error) {
	f, err := creack.StartWithSize(cmd, winsize(cols, rows))
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	s := &Session{cmd: cmd, f: f}
	go s.pump(wake)
	return s, nil
}

func winsize(cols, rows int) *creack.Winsize {
	return &creack.Winsize{Cols: uint16(max(cols, 1)), Rows: uint16(max(rows, 1))}
}

func (s *Session) pump(wake func()) {
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

// Take moves everything read since the last call into dst, and reports whether the
// shell is gone. dst is reused between calls so a busy pane does not allocate a buffer
// per frame.
func (s *Session) Take(dst []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst = append(dst[:0], s.pend...)
	s.pend = s.pend[:0]
	return dst, s.err
}

func (s *Session) Resize(cols, rows int) { _ = creack.Setsize(s.f, winsize(cols, rows)) }

func (s *Session) Write(b []byte) {
	if len(b) > 0 {
		_, _ = s.f.Write(b)
	}
}

// Closing the master hangs up on the child, and the Wait reaps it, so a long session of
// splits does not leave zombies.
func (s *Session) Close() {
	s.f.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}
