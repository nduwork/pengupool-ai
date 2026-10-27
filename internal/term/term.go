// Package term handles the local terminal of an attach client: entering raw mode, restoring it on
// detach, and clearing the modes an agent may have enabled.
//
// PenguPool itself never enables mouse reporting or the alternate screen. It does have to undo
// them: an agent that died or detached mid-alternate-screen otherwise leaves the user's terminal
// unusable.
package term

import (
	"os"

	"golang.org/x/term"
)

// ResetModes undoes the modes an agent commonly turns on: mouse reporting (9/1000/1002/1003/1005/
// 1006/1015), focus events (1004), bracketed paste (2004), synchronized output (2026), the alternate
// screen (1049), hidden cursor (25), the kitty keyboard protocol (CSI > 0 u), modifyOtherKeys
// (CSI > 4 m), application cursor keys (1), application keypad mode (ESC >), colours, and auto-wrap
// (7). Written on detach and on error.
var ResetModes = []byte("\x1b[?9l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l\x1b[?2026l\x1b[?1049l\x1b[?25h\x1b[>0u\x1b[>4m\x1b[?1l\x1b[0m\x1b[?7h\x1b>")

// Session is a local terminal put into raw mode, remembering how to put it back.
type Session struct {
	f      *os.File
	fd     int
	state  *term.State
	closed bool
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// Size reports the terminal's size in columns and rows.
func Size(f *os.File) (cols, rows int, err error) {
	return term.GetSize(int(f.Fd()))
}

// MakeRaw puts f into raw mode and returns a Session that can restore it.
func MakeRaw(f *os.File) (*Session, error) {
	fd := int(f.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return &Session{f: f, fd: fd, state: state}, nil
}

// Restore clears the agent's terminal modes, writes them to the local terminal, and restores the
// saved termios state. It is safe to call more than once.
func (s *Session) Restore() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.f != nil {
		s.f.Write(ResetModes)
	}
	return term.Restore(s.fd, s.state)
}
