// Package vte is the terminal emulator: the grid a shell writes to, the history behind it,
// and the parser that drives both.
//
// This is the model, holding nothing a renderer needs — no fonts, glyphs or pixels — so a
// session can run with nothing drawing it.
//
// One goroutine per terminal reads the pty and only buffers bytes; parsing happens wherever the
// host calls Pump. The lock guards what those two share.
package vte

import (
	"os/exec"
	"sync"
)

// CursorShape is how a cursor marks its cell. DECSCUSR picks between these; drawing one is
// the view's business.
type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorBar
	CursorUnderline
)

// Cursor is where the cursor is and what it looks like. Whether it is painted folds in a
// blink phase and a focus, which are the view's.
type Cursor struct {
	Row, Col int // on the live screen, not in the view
	Shape    CursorShape
	Visible  bool // DECTCEM
}

// altSeqBase keeps the alternate screen's line numbers clear of the primary's: the two grids
// have their own rows and Gens, and a cache keyed by Seq must not confuse them.
const altSeqBase = 1 << 63

// Options configures the shell behind a Terminal. The grid is not among them: Attach uses the
// terminal's own, which is the only size that can be right.
type Options struct {
	// Cmd is the child to start. nil runs $SHELL.
	Cmd *exec.Cmd

	// CursorShape is what the terminal starts with, and what DECSCUSR 0 and RIS return to.
	CursorShape CursorShape

	// Wake is called from the reader goroutine once the shell has written. A host with an
	// event loop to poke wants this, one that can block on a channel wants UpdateChan.
	Wake func()

	// ReportColor answers OSC 10 and 11 at sixteen bits a channel, which is how an app tells
	// a light theme from a dark one. nil leaves them unanswered.
	ReportColor func(code int) (r, g, b uint16, ok bool)
}

// Frame is everything a view needs to draw one frame, read under a single lock so that no
// part of it can disagree with another.
type Frame struct {
	// Lines is the visible window, oldest first, copied out of the terminal.
	Lines []Row

	Cursor  Cursor
	Cols    int
	Height  int    // rows on the live screen
	HistLen int    // lines of history behind it, and so also the furthest Scroll can go
	Title   string // the last OSC 0 or 2; "" until the shell sets one

	// Scroll is how far back the window really is once clamped. A view that asked for more
	// than exists stores this back.
	Scroll int
}

type Terminal struct {
	// mu guards everything below. Parsing takes it for writing, a view for reading.
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

// New is a terminal with no shell behind it: what a test drives with Feed, and what a saved
// session is replayed into.
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

// Start is a terminal with a shell behind it, already reading.
func Start(cols, rows int, o Options) (*Terminal, error) {
	t := New(cols, rows)
	if err := t.Attach(o); err != nil {
		return nil, err
	}
	return t, nil
}

// Attach starts a shell at the grid the terminal already has, and is a no-op once one is
// running: a shell told it has no terminal writes its first prompt into nothing.
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

// Pump parses whatever the shell has written since the last call and answers its queries.
// changed is a view's cue to draw; an error means the shell is gone.
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

// Feed parses bytes as though the shell had written them.
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

// Write sends bytes to the shell — a key press, or a paste.
func (t *Terminal) Write(b []byte) {
	if t.pty != nil {
		t.pty.write(b)
	}
}

// Resize refits both grids and tells the shell. The primary can shed lines into the history
// on the way, which keeps the prompt in view when a window is dragged shorter.
//
// There is no reflow: a line is clipped or padded on the right.
func (t *Terminal) Resize(cols, rows int) {
	t.mu.Lock()
	t.pri.resize(cols, rows)
	t.alt.resize(cols, rows)
	t.mu.Unlock()

	if t.pty != nil {
		t.pty.resize(cols, rows)
	}
}

// Close ends the shell. Safe to call twice.
func (t *Terminal) Close() {
	if t.pty != nil {
		t.pty.close()
		t.pty = nil
	}
}

// Title is the last title the shell set with OSC 0 or 2. Separate from Frame because a host
// labels sessions it is not drawing.
func (t *Terminal) Title() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.title
}

// Size is the grid.
func (t *Terminal) Size() (cols, rows int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.scr.cols, t.scr.height()
}

// Retired is how many lines have ever left the screen, evicted ones included. The history's
// length cannot stand in for it, because the ring stops growing.
func (t *Terminal) Retired() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hist.retired
}

// AppCursor is DECCKM: the cursor keys and Home and End go out as SS3 rather than CSI. A host
// reads it when a key is pressed, not per frame, because a program sets it before it draws.
func (t *Terminal) AppCursor() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.appCursor
}

// AppKeypad is DECKPAM: the keypad sends SS3 rather than the digits printed on it. Read at
// key time for the same reason as AppCursor.
func (t *Terminal) AppKeypad() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.appKeypad
}

// MaxScroll is as far back as a view can go, which is exactly the history's length: the
// window is the height of the screen.
func (t *Terminal) MaxScroll() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.histLen()
}

// Frame is the view's per-frame read: the live screen scrolled back lines further, with all
// of it taken under one lock so no part can disagree with another.
//
// dst is reused and its rows keep their cell allocations, so a steady stream of frames
// allocates nothing. The rows come back as copies, safe to hold.
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

// GetViewport is lines [start, end) of the view, oldest first, numbered from the oldest line
// still held. It allocates; Frame is the path for a view drawing every frame.
func (t *Terminal) GetViewport(start, end int) []Row {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.appendRows(nil, start, end)
}

// appendRows refills dst with rows [start, end) of the view, clamped to what exists, with the
// read lock held. Refilled and not rebuilt: the rows in dst's array keep their cell slices.
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
		// To the grid's width, not this line's: history is stored trimmed and the screen is
		// not, so a window straddling both would reallocate every frame as it scrolled.
		if n := len(src.Cells); cap(dst[i].Cells) < n {
			dst[i].Cells = make([]Cell, 0, max(n, cols))
		}
		dst[i].Cells = append(dst[i].Cells[:0], src.Cells...)
		dst[i].Seq, dst[i].Gen = base+uint64(start+i), src.Gen
	}
	return dst
}

// viewRow is the i-th row of the view, oldest first. Rows past the end of the history come
// from the live screen.
func (t *Terminal) viewRow(i int) *Row {
	if h := t.histLen(); i >= h {
		return &t.scr.lines[i-h]
	}
	return t.hist.row(i)
}

// histLen is how many history lines a view can reach. None on the alternate screen: scrolling
// back into what the primary left behind would be nonsense.
func (t *Terminal) histLen() int {
	if t.scr == t.alt {
		return 0
	}
	return t.hist.len()
}
