// Package vt adapts github.com/charmbracelet/x/vt into the terminal emulator the PenguPool worker
// uses to follow a session's output.
//
// Every PTY byte is fed to a Screen exactly once. When a client cannot be replayed to from the
// ring (its offset fell out of the window, or the generation changed), the worker sends
// Snapshot instead: a self-contained reconstruction of the visible screen, the cursor and the
// input-affecting modes, preceded by the scrollback (SnapshotHistory) for a client that resets
// before applying it. The host keeps the history, so every such viewer shows the same one after a
// reconnect, a resize or a switch.
//
// Known limit: a program that repaints its live area in place (Claude Code does, inside DEC 2026
// synchronized output) adds no scrollback rows for what it overwrites, here or in any terminal.
//
// A Screen is safe for concurrent use: Write, Resize, Cols, Rows, Cursor, Modes and Snapshot may
// be called from different goroutines. Responses delivers the emulator's query replies on a
// channel; a single consumer must read it (the worker's PTY writer does).
package vt

import (
	"fmt"
	"sync"
	"sync/atomic"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
)

// Modes is the terminal state a client must mirror to talk to the agent exactly like the PTY
// does. Almost everything here changes how the terminal encodes input or paints output, so a
// snapshot carries all of it.
type Modes struct {
	AltScreen         bool   // ?1047 / ?1049
	BracketedPaste    bool   // ?2004
	FocusEvents       bool   // ?1004
	CursorVisible     bool   // ?25
	MouseX10          bool   // ?1000
	MouseButton       bool   // ?1002
	MouseAny          bool   // ?1003
	MouseSGR          bool   // ?1006
	MouseUTF8         bool   // ?1005
	ApplicationCursor bool   // DECCKM ?1
	SyncOutput        bool   // ?2026
	KeyboardFlags     uint16 // kitty keyboard protocol (CSI = flags u sets, CSI > flags u pushes)
	ModifyOtherKeys   int    // XTMODKEYS modifyOtherKeys level (CSI > 1 ; Pv m), 0 when off
	ScrollTop         int    // DECSTBM top row, 1-based; 0 together with ScrollBottom means the
	ScrollBottom      int    // whole screen: DECSTBM bottom row, 1-based
}

// Screen is a terminal emulator the worker drives with raw PTY output.
type Screen interface {
	// Write feeds terminal output into the emulator.
	Write(p []byte) (int, error)
	// Resize changes the screen to cols x rows cells.
	Resize(cols, rows int)
	// Cols and Rows report the current screen size in cells.
	Cols() int
	Rows() int
	// Cursor reports the cursor position, zero-based.
	Cursor() (col, row int)
	// Modes reports the input-affecting terminal modes currently set.
	Modes() Modes
	// Snapshot returns bytes that reconstruct the visible screen, the cursor and the tracked modes
	// on a cleared, same-size, xterm-compatible client. The normal screen is never cleared wholesale
	// (no \x1b[2J).
	Snapshot() []byte
	// SnapshotHistory is Snapshot preceded by the scrollback (newest lines, bounded), for a client
	// that resets before applying it: one that does not would show the history twice.
	SnapshotHistory() []byte
	// Responses returns the channel on which this emulator delivers the replies it produces in
	// answer to a program query (DA1, DA2, DECRQM, DSR, focus, in-band resize). One consumer must
	// read it: the emulator answers into an unbuffered pipe, so an unconsumed reply blocks Write.
	// The worker's PTY writer selects on this channel and forwards the bytes to the PTY master
	// only while no client is attached; see docs/protocol/v1.md section 4. Every value is owned by
	// the receiver. The channel is closed when the emulator stops.
	Responses() <-chan []byte
	// Text returns the visible screen as plain text, one line per row, trailing spaces trimmed.
	Text() string
	// Close stops the emulator; Responses is closed after it.
	Close() error
}

// New returns a Screen of cols x rows cells that keeps HistoryLines of scrollback. Sizes below one
// cell are clamped to one.
func New(cols, rows int) Screen { return newScreen(cols, rows, HistoryLines) }

// NewVisible returns a Screen that keeps no scrollback, for a follower that only reads Text.
func NewVisible(cols, rows int) Screen { return newScreen(cols, rows, 1) }

// HistoryLines is the scrollback a session's screen keeps: tmux's default history-limit, and the
// memory cap. A line is stored trimmed of trailing blanks at 112 bytes a cell (uv.Cell), so the worst
// case grows with the width: 2000 full lines are about 27 MB per session at 120 columns and 56 MB at
// 250 (plus each cell's text), typical agent output far less.
// ponytail: x/vt drops the oldest line with a slice delete, a memmove of 2000 line headers per
// scrolled line once full; a ring buffer if scrolling ever shows in a profile.
const HistoryLines = 2000

// screen is the x/vt-backed Screen.
//
// emu and modes share mu: the mode callbacks registered on the emulator run inside emu.Write, so
// they update the fields directly while the caller holds mu, and Modes reads them under the same
// lock. The response pump deliberately uses its own lock and never takes mu, so a query that the
// emulator answers cannot deadlock against a writer.
type screen struct {
	mu   sync.Mutex
	emu  *xvt.Emulator
	mode Modes
	// closing is set by Close; the pump ends at the reply to closeQuery.
	closing atomic.Bool
	// kbd is the kitty keyboard protocol's flag stack: CSI > flags u pushes, CSI < n u pops.
	kbd []uint16

	// resp carries the emulator's query replies to the PTY writer. It is buffered so the read loop
	// that feeds Write is not held up by a reply in flight; a reply larger than the buffer waits
	// for the writer instead of being dropped.
	resp chan []byte
}

var _ Screen = (*screen)(nil)

// respQueue is how many query replies may wait for the PTY writer before the emulator blocks.
// Back-pressure here is bounded and self-limiting: the emulator only produces a reply for input the
// read loop feeds it, so once the buffer is full the read loop stops feeding it queries.
const respQueue = 256

func newScreen(cols, rows, history int) *screen {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	emu := xvt.NewEmulator(cols, rows)
	emu.SetScrollbackSize(history) // x/vt keeps 10k by default; at least 1 (0 means the default)

	s := &screen{emu: emu, resp: make(chan []byte, respQueue)}
	emu.SetCallbacks(xvt.Callbacks{
		EnableMode:  func(m ansi.Mode) { s.trackMode(m, true) },
		DisableMode: func(m ansi.Mode) { s.trackMode(m, false) },
	})
	// The sequences below reach no handler in x/vt: either the package claims none of them, or
	// (DECSTBM) it must still apply them itself. Tracking them here is what lets a snapshot carry
	// the same keyboard and scrolling semantics as the PTY.
	s.registerTrackingHandlers()
	go s.pumpResponses()
	return s
}

// registerTrackingHandlers records the sequences x/vt does not track. They all run inside Write,
// with the caller holding mu, and every handler for a sequence x/vt does handle returns false so
// the package's own handler still runs.
func (s *screen) registerTrackingHandlers() {
	emu := s.emu
	// XTMODKEYS: CSI > Pp ; Pv m. Pp 1 (and its shorthand 4) set the modifyOtherKeys level, Pp 0
	// resets it. x/vt has no handler for the "> m" command at all.
	emu.RegisterCsiHandler(ansi.Command('>', 0, 'm'), func(params ansi.Params) bool {
		pp, _, _ := params.Param(0, 0)
		pv, _, _ := params.Param(1, 0)
		switch pp {
		case 0:
			s.mode.ModifyOtherKeys = 0
		case 1, 4:
			s.mode.ModifyOtherKeys = pv
		}
		return true
	})
	// Kitty keyboard protocol flags: CSI = flags ; mode u sets, CSI > flags u pushes, CSI < n u
	// pops. x/vt tracks none of the three.
	emu.RegisterCsiHandler(ansi.Command('=', 0, 'u'), func(params ansi.Params) bool {
		flags, _, _ := params.Param(0, 0)
		mode, _, _ := params.Param(1, 1)
		s.setKeyboardFlags(uint16(flags), mode)
		return true
	})
	emu.RegisterCsiHandler(ansi.Command('>', 0, 'u'), func(params ansi.Params) bool {
		flags, _, _ := params.Param(0, 0)
		s.kbd = append(s.kbd, s.mode.KeyboardFlags)
		s.mode.KeyboardFlags = uint16(flags)
		return true
	})
	emu.RegisterCsiHandler(ansi.Command('<', 0, 'u'), func(params ansi.Params) bool {
		n, _, _ := params.Param(0, 1)
		if n < 1 {
			n = 1
		}
		for ; n > 0 && len(s.kbd) > 0; n-- {
			s.mode.KeyboardFlags = s.kbd[len(s.kbd)-1]
			s.kbd = s.kbd[:len(s.kbd)-1]
		}
		if n > 0 {
			s.mode.KeyboardFlags = 0 // popped past the bottom: the kitty spec resets every flag
		}
		return true
	})
	// RIS (ESC c) resets the terminal, and x/vt's full reset covers its own mode map only: the
	// keyboard and scrolling state tracked above would survive it. Clear it here and decline the
	// sequence so fullReset still runs.
	emu.RegisterEscHandler('c', func() bool {
		s.mode.ModifyOtherKeys = 0
		s.mode.KeyboardFlags = 0
		s.kbd = nil
		s.mode.ScrollTop, s.mode.ScrollBottom = 0, 0
		return false
	})
	// DECSTBM: CSI top ; bottom r. x/vt moves the margins itself (and homes the cursor), so this
	// only records the region and declines: returning true would swallow the sequence.
	emu.RegisterCsiHandler('r', func(params ansi.Params) bool {
		s.recordScrollRegion(params)
		return false
	})
}

// setKeyboardFlags applies the set form of the kitty keyboard protocol: mode 1 replaces the flags,
// 2 ORs them in, 3 clears them.
func (s *screen) setKeyboardFlags(flags uint16, mode int) {
	switch mode {
	case 2:
		s.mode.KeyboardFlags |= flags
	case 3:
		s.mode.KeyboardFlags &^= flags
	default:
		s.mode.KeyboardFlags = flags
	}
}

// recordScrollRegion mirrors the clampings of x/vt's own DECSTBM handler, so the tracked region
// stays exactly what the emulator applied: below one row, the parameters default to the screen,
// and top >= bottom is a request the emulator declines.
//
// A bottom below the screen is clamped to the last row, as xterm does. x/vt v0.1.0 (and its main
// branch) takes it as is, and the next reverse index or insert line indexes a row the buffer does not
// have and panics. An agent sends exactly that when it draws for the size it had before a resize.
// params aliases the parser's own buffer, which x/vt's handler reads next, so the clamp is written
// back there; TestShrinkThenStaleScrollRegion fails if that stops holding.
func (s *screen) recordScrollRegion(params ansi.Params) {
	top, _, _ := params.Param(0, 1)
	if top < 1 {
		top = 1
	}
	height := s.emu.Height()
	bottom, _, _ := params.Param(1, height)
	if bottom < 1 {
		bottom = height
	}
	if bottom > height {
		bottom = height
		params[1] = ansi.Param(height)
	}
	if top >= bottom {
		return
	}
	if top == 1 && bottom == height {
		s.mode.ScrollTop, s.mode.ScrollBottom = 0, 0 // the whole screen is the default
		return
	}
	s.mode.ScrollTop, s.mode.ScrollBottom = top, bottom
}

// Write feeds terminal output into the emulator. The mirror must never take the worker down with
// it: if x/vt panics on output it cannot handle, the rest of p is dropped, the scroll region is
// reset by resizing to the same size (the cursor stays, clamped), CAN returns the parser to ground
// so the next write is not read as the rest of the broken sequence, and the panic comes back as an
// error.
// ponytail: the screen may show a few wrong cells until the agent repaints; a full repaint would
// need the agent's help, and Claude Code and Muse repaint often anyway.
func (s *screen) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			s.emu.Resize(s.emu.Width(), s.emu.Height())
			_, _ = s.emu.Write([]byte{0x18}) // CAN
			s.mode.ScrollTop, s.mode.ScrollBottom = 0, 0
			n, err = len(p), fmt.Errorf("terminal emulator panic, screen reset: %v", r)
		}
	}()
	return s.emu.Write(p)
}

// Resize changes the screen size. The x/vt emulator clamps the cursor into the new bounds and
// resets the scroll region to the whole screen, so the tracked region is reset with it.
func (s *screen) Resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emu.Resize(cols, rows)
	s.mode.ScrollTop, s.mode.ScrollBottom = 0, 0
}

// Text returns the visible screen as plain text.
func (s *screen) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emu.String()
}

// closeQuery asks for a private mode no program uses (DECRQM); its reply is how Close wakes the pump.
const closeQuery, closeReply = "\x1b[?7777$p", "\x1b[?7777;0$y"

// Close stops the response pump, so the screen can be dropped. x/vt's own Close races with the
// pump's Read (an unguarded flag), so it is never called: the pump is woken with the reply to
// closeQuery instead, and ends there. Nothing may be written after Close.
func (s *screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing.Swap(true) {
		return nil
	}
	_, err := s.emu.Write([]byte(closeQuery))
	return err
}

// Cols reports the screen width in cells.
func (s *screen) Cols() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emu.Width()
}

// Rows reports the screen height in cells.
func (s *screen) Rows() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emu.Height()
}

// Cursor reports the cursor position, zero-based.
func (s *screen) Cursor() (col, row int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := s.emu.CursorPosition()
	return pos.X, pos.Y
}

// Modes reports the input-affecting modes. Alt screen and cursor visibility are read from the
// emulator itself, which owns those bits; the rest are tracked from the mode callbacks because
// x/vt keeps its mode map private.
func (s *screen) Modes() Modes {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.mode
	m.AltScreen = s.emu.IsAltScreen()
	m.CursorVisible = !s.emu.CursorHidden()
	return m
}

// Responses returns the channel the emulator delivers its query replies on. The returned slices
// are owned by the receiver.
func (s *screen) Responses() <-chan []byte { return s.resp }

// pumpResponses hands the emulator's reply pipe to the Responses channel. x/vt answers a program
// query by writing the reply into an unbuffered io.Pipe, and that write blocks until somebody
// reads it: without this pump a program that asks the terminal a question would wedge the
// emulator inside Write. Nothing polls: a reply reaches the PTY writer through the channel, in
// order, exactly once.
func (s *screen) pumpResponses() {
	buf := make([]byte, 512)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 && s.closing.Load() && string(buf[:n]) == closeReply {
			close(s.resp)
			return
		}
		if n > 0 {
			out := make([]byte, n)
			copy(out, buf[:n])
			s.resp <- out
		}
		if err != nil {
			close(s.resp)
			return
		}
	}
}

// trackMode records a DEC private mode as it is set or reset. It runs inside Write, with the
// caller holding mu.
func (s *screen) trackMode(mode ansi.Mode, set bool) {
	dec, ok := mode.(ansi.DECMode)
	if !ok {
		return // ANSI modes do not affect how the terminal answers or encodes input here
	}
	switch int(dec) {
	case 1:
		s.mode.ApplicationCursor = set
	case 1000:
		s.mode.MouseX10 = set
	case 1002:
		s.mode.MouseButton = set
	case 1003:
		s.mode.MouseAny = set
	case 1004:
		s.mode.FocusEvents = set
	case 1005:
		s.mode.MouseUTF8 = set
	case 1006:
		s.mode.MouseSGR = set
	case 2004:
		s.mode.BracketedPaste = set
	case 2026:
		s.mode.SyncOutput = set
	}
}

// cellText returns the printable content of a cell, using a space for a blank cell so that
// styles and backgrounds survive.
func cellText(c *uv.Cell) string {
	if c.Content == "" {
		return " "
	}
	return c.Content
}
