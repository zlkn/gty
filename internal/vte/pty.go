package vte

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	creack "github.com/creack/pty"
)

// pty is the child process and the master side of its terminal. Only pump reads the
// file, buffering raw bytes into pend; parsing them happens wherever the host calls Pump.
type pty struct {
	cmd *exec.Cmd
	f   *os.File

	mu   sync.Mutex
	pend []byte
	err  error // EOF or a read failure; the terminal is finished once it surfaces
}

func startShell(dir string, cols, rows int, wake func()) (*pty, error) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	cmd := exec.Command(sh)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	startIn(cmd, dir)
	return startPTY(cmd, cols, rows, wake)
}

// startIn points cmd at dir, unless dir is gone: a pane whose directory was deleted still
// opens a shell. PWD goes with it, or the shell would show the path with its links resolved.
func startIn(cmd *exec.Cmd, dir string) {
	if fi, err := os.Stat(dir); dir == "" || err != nil || !fi.IsDir() {
		return
	}
	cmd.Dir = dir
	if cmd.Env != nil {
		cmd.Env = append(cmd.Env, "PWD="+dir)
	}
}

func startPTY(cmd *exec.Cmd, cols, rows int, wake func()) (*pty, error) {
	f, err := creack.StartWithSize(cmd, winsize(cols, rows))
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	s := &pty{cmd: cmd, f: f}
	go s.pump(wake)
	return s, nil
}

func winsize(cols, rows int) *creack.Winsize {
	return &creack.Winsize{Cols: uint16(max(cols, 1)), Rows: uint16(max(rows, 1))}
}

func (s *pty) pump(wake func()) {
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

// take moves everything read since the last call into dst and reports whether the shell is
// gone. dst is reused, so a busy terminal does not allocate a buffer per frame.
func (s *pty) take(dst []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst = append(dst[:0], s.pend...)
	s.pend = s.pend[:0]
	return dst, s.err
}

func (s *pty) resize(cols, rows int) { _ = creack.Setsize(s.f, winsize(cols, rows)) }

func (s *pty) write(b []byte) {
	if len(b) > 0 {
		_, _ = s.f.Write(b)
	}
}

// Closing the master hangs up on the child, and the Wait reaps it, so a long session of
// splits does not leave zombies.
func (s *pty) close() {
	s.f.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}
