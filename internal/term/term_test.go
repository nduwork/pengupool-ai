package term

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestRawSizeResetAndRestore exercises the whole local-terminal contract on a real pty: the size is
// readable, raw mode can be entered, and Restore both clears the agent's modes on the terminal and
// puts the termios state back — including when it is called twice.
func TestRawSizeResetAndRestore(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()

	if !IsTerminal(tty) {
		t.Fatal("the pty slave must report as a terminal")
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 140, Rows: 45}); err != nil {
		t.Fatal(err)
	}
	cols, rows, err := Size(tty)
	if err != nil {
		t.Fatal(err)
	}
	if cols != 140 || rows != 45 {
		t.Fatalf("size is %dx%d, want 140x45", cols, rows)
	}

	session, err := MakeRaw(tty)
	if err != nil {
		t.Fatal(err)
	}

	// Restore must write the reset sequence to the terminal before it puts termios back.
	type read struct {
		data []byte
		err  error
	}
	got := make(chan read, 1)
	go func() {
		buf := make([]byte, len(ResetModes))
		_, err := io.ReadFull(ptmx, buf)
		got <- read{data: buf, err: err}
	}()
	if err := session.Restore(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !bytes.Equal(r.data, ResetModes) {
			t.Fatalf("reset sequence is %q, want %q", r.data, ResetModes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Restore wrote nothing to the terminal")
	}

	if err := session.Restore(); err != nil {
		t.Fatalf("Restore must be idempotent: %v", err)
	}
	// Every mode an agent can turn on must be cleared, including the ones a detach used to leave
	// behind: UTF-8 mouse reporting, X10 mouse reporting, modifyOtherKeys and the application
	// keypad (review R15).
	for _, want := range []string{
		"\x1b[?1049l", // alternate screen
		"\x1b[?2004l", // bracketed paste
		"\x1b[?1005l", // UTF-8 mouse reporting
		"\x1b[?9l",    // X10 mouse reporting
		"\x1b[>4m",    // modifyOtherKeys
		"\x1b>",       // application keypad
	} {
		if !bytes.Contains(ResetModes, []byte(want)) {
			t.Fatalf("the reset sequence must contain %q, got %q", want, ResetModes)
		}
	}
}
