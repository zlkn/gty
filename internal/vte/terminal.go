package vte

import (
	"os/exec"
	"sync"
)

type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorBar
	CursorUnderline
)

type Cursor struct {
	Row, Col int // Position on the live screen
	Shape    CursorShape
	Visible  bool // DECTCEM
}

const altSeqBase = 1 << 63

type Options struct {
	// Cmd is the child to start. nil runs $SHELL.
	Cmd *exec.Cmd

	CursorShape CursorShape

	// Wake is called from the reader goroutine once the shell has written. A host with an
	// event loop to poke wants this, one that can block on a channel wants UpdateChan.
	Wake func()

	// ReportColor answers OSC 10 and 11 at sixteen bits a channel, which is how an app tells
	// a light theme from a dark one. nil leaves them unanswered.
	ReportColor func(code int) (r, g, b uint16, ok bool)
}

type Frame struct {
	Lines []Row

	Cursor  Cursor
	Cols    int
	Height  int    // rows on the live screen
	HistLen int    // lines of history behind it, and so also the furthest Scroll can go
	Title   string // the last OSC 0 or 2; "" until the shell sets one

	Scroll int
}

type Terminal struct {
	mu sync.RWMutex

	pri, alt *screen // the primary grid and the one a full-screen program gets
	scr      *screen // whichever of the two is live
	hist     *scrollback

	par parser
	pty *pty

	shape        CursorShape
	shapeDefault CursorShape
	visible      bool // DECTCEM
	appCursor    bool // DECCKM
	appKeypad    bool // DECKPAM
	title        string

	answers []byte // replies owed to the shell; see feed
	inbuf   []byte // bytes taken from the pty, reused between Pumps

	updates     chan struct{}
	wake        func()
	reportColor func(code int) (r, g, b uint16, ok bool)
}

func New(cols, rows int) *Terminal {
	hist := &scrollback{}
	pri := newScreen(cols, rows, hist)
	return &Terminal{
		pri: pri,
		// The alternate screen keeps no history: a full-screen program owns the grid, and
		// its repaints are not something to scroll back through.
		alt:     newScreen(cols, rows, nil),
		scr:     pri,
		hist:    hist,
		visible: true,
		updates: make(chan struct{}, 1),
	}
}

func Start(cols, rows int, o Options) (*Terminal, error) {
	t := New(cols, rows)
	if err := t.Attach(o); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Terminal) Attach(o Options) error {
	if t.pty != nil {
		return nil
	}
	t.shape, t.shapeDefault = o.CursorShape, o.CursorShape
	t.wake, t.reportColor = o.Wake, o.ReportColor

	cols, rows := t.Size()
	var err error
	if o.Cmd != nil {
		t.pty, err = startPTY(o.Cmd, cols, rows, t.notify)
	} else {
		t.pty, err = startShell(cols, rows, t.notify)
	}
	return err
}

// UpdateChan fires when Pump has work. Buffered to one and sent to without blocking, so a
// host slow to drain it sees one signal rather than a queue.
func (t *Terminal) UpdateChan() <-chan struct{} { return t.updates }

// notify runs on the reader goroutine, so it touches nothing the lock guards.
func (t *Terminal) notify() {
	select {
	case t.updates <- struct{}{}:
	default: // a signal is already pending, and one is as good as two
	}
	if t.wake != nil {
		t.wake()
	}
}

func (t *Terminal) Pump() (changed bool, err error) {
	if t.pty == nil {
		return false, nil
	}
	t.inbuf, err = t.pty.take(t.inbuf)
	if len(t.inbuf) == 0 {
		return false, err
	}
	t.feed(t.inbuf)
	return true, err
}

func (t *Terminal) Feed(b []byte) {
	if len(b) > 0 {
		t.feed(b)
	}
}

func (t *Terminal) feed(b []byte) {
	t.mu.Lock()
	t.answers = t.answers[:0]
	t.par.parse(b, t)
	t.mu.Unlock()

	// Outside the lock: a write to a pty whose buffer is full would otherwise stall every
	// reader. One write per read, however many queries were in it.
	if len(t.answers) > 0 && t.pty != nil {
		t.pty.write(t.answers)
	}
}

func (t *Terminal) Write(b []byte) {
	if t.pty != nil {
		t.pty.write(b)
	}
}

func (t *Terminal) Resize(cols, rows int) {
	t.mu.Lock()
	t.pri.resize(cols, rows)
	t.alt.resize(cols, rows)
	t.mu.Unlock()

	if t.pty != nil {
		t.pty.resize(cols, rows)
	}
}

func (t *Terminal) Close() {
	if t.pty != nil {
		t.pty.close()
		t.pty = nil
	}
}

func (t *Terminal) Title() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.title
}

func (t *Terminal) Size() (cols, rows int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.scr.cols, t.scr.height()
}

func (t *Terminal) Retired() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hist.retired
}

func (t *Terminal) AppCursor() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.appCursor
}

func (t *Terminal) AppKeypad() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.appKeypad
}

func (t *Terminal) MaxScroll() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.histLen()
}

func (t *Terminal) Frame(dst []Row, back int) Frame {
	t.mu.RLock()
	defer t.mu.RUnlock()

	h, hist := t.scr.height(), t.histLen()
	back = min(max(back, 0), hist)
	to := hist + h - back

	return Frame{
		Lines:   t.appendRows(dst, max(0, to-h), to),
		Cursor:  Cursor{Row: t.scr.curRow, Col: t.scr.curCol, Shape: t.shape, Visible: t.visible},
		Cols:    t.scr.cols,
		Height:  h,
		HistLen: hist,
		Title:   t.title,
		Scroll:  back,
	}
}

func (t *Terminal) GetViewport(start, end int) []Row {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.appendRows(nil, start, end)
}

func (t *Terminal) appendRows(dst []Row, start, end int) []Row {
	hist := t.histLen()
	start, end = max(start, 0), min(end, hist+t.scr.height())
	n := max(end-start, 0)

	dst = dst[:cap(dst)] // the rows past len are ours, and still hold their allocations
	if len(dst) < n {
		dst = append(dst, make([]Row, n-len(dst))...)
	}
	dst = dst[:n]

	base := t.hist.retired - uint64(hist)
	if t.scr == t.alt {
		base = altSeqBase
	}
	cols := t.scr.cols
	for i := range dst {
		src := t.viewRow(start + i)
		if n := len(src.Cells); cap(dst[i].Cells) < n {
			dst[i].Cells = make([]Cell, 0, max(n, cols))
		}
		dst[i].Cells = append(dst[i].Cells[:0], src.Cells...)
		dst[i].Seq, dst[i].Gen = base+uint64(start+i), src.Gen
	}
	return dst
}

func (t *Terminal) viewRow(i int) *Row {
	if h := t.histLen(); i >= h {
		return &t.scr.lines[i-h]
	}
	return t.hist.row(i)
}

func (t *Terminal) histLen() int {
	if t.scr == t.alt {
		return 0
	}
	return t.hist.len()
}
