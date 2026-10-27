package vt

import (
	"bytes"
	"testing"
	"time"
)

// TestResponsesDeviceAttributes is the contract's case: a program asks the terminal what it is,
// and the reply arrives on the channel the PTY writer selects on.
func TestResponsesDeviceAttributes(t *testing.T) {
	s := New(80, 24)
	if got := respIfAny(s); len(got) != 0 {
		t.Fatalf("a fresh screen reported %q, want nothing", got)
	}

	s.Write([]byte("\x1b[c")) // DA1
	resp := respWithin(t, s)
	if !bytes.HasPrefix(resp, []byte("\x1b[?")) || !bytes.HasSuffix(resp, []byte("c")) {
		t.Fatalf("got %q, want a primary device attributes reply", resp)
	}
	if got := respIfAny(s); len(got) != 0 {
		t.Fatalf("a second read returned %q, want nothing", got)
	}
}

// TestResponsesModeReport covers DECRQM, whose reply carries the mode's value.
func TestResponsesModeReport(t *testing.T) {
	s := New(80, 24)

	s.Write([]byte("\x1b[?1002h\x1b[?1002$p"))
	if got := respWithin(t, s); string(got) != "\x1b[?1002;1$y" {
		t.Fatalf("got %q, want \"\\x1b[?1002;1$y\" (mode set)", got)
	}

	s.Write([]byte("\x1b[?1002l\x1b[?1002$p"))
	if got := respWithin(t, s); string(got) != "\x1b[?1002;2$y" {
		t.Fatalf("got %q, want \"\\x1b[?1002;2$y\" (mode reset)", got)
	}
}

// TestWriteNeverBlocksOnQueryReply pins the reason the response pump exists: x/vt answers a query
// by writing into an unbuffered pipe, so without a consumer that write would block forever inside
// Write, and the worker's PTY read loop with it.
func TestWriteNeverBlocksOnQueryReply(t *testing.T) {
	s := New(80, 24)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Write([]byte("\x1b[c"))
		s.Write([]byte("\x1b[?1000$p"))
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Write blocked on the emulator's reply pipe")
	}
	_ = respWithin(t, s)
}

// TestResizeInBandReport checks that a resize under DECSET 2048 reports itself through the same
// pipe, and that the report reaches the consumer instead of Resize blocking on the write.
func TestResizeInBandReport(t *testing.T) {
	s := New(80, 24)
	s.Write([]byte("\x1b[?2048h"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Resize(100, 30)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Resize blocked on the emulator's reply pipe")
	}

	if got := respContains(t, s, []byte("100")); !bytes.Contains(got, []byte("100")) {
		t.Fatalf("got %q, want an in-band resize report for the new size", got)
	}
	if s.Cols() != 100 || s.Rows() != 30 {
		t.Fatalf("size = %dx%d, want 100x30", s.Cols(), s.Rows())
	}
}

// respWithin waits for one reply. The emulator answers into its pipe from inside Write, and the
// pump hands that reply over on the channel, so reading the channel is what makes the reply arrive
// deterministically: no polling, no sleep.
func respWithin(t *testing.T, s Screen) []byte {
	t.Helper()
	select {
	case out, ok := <-s.Responses():
		if !ok {
			t.Fatal("the responses channel was closed")
		}
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("no response arrived within 10s")
		return nil
	}
}

// respIfAny reports a reply only when one is already waiting.
func respIfAny(s Screen) []byte {
	select {
	case out := <-s.Responses():
		return out
	default:
		return nil
	}
}

// respContains reads replies until one carries want. A single sequence can produce more than one
// reply (mode 2048 reports the size both when it is set and when it changes).
func respContains(t *testing.T, s Screen, want []byte) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := respWithin(t, s)
		if bytes.Contains(got, want) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reply containing %q arrived within 10s (last %q)", want, got)
		}
	}
}
