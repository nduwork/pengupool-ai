package vt

import (
	"testing"
	"time"
)

// Close ends the response pump (Responses closes), also while a program's queries are being
// answered, without racing x/vt's reader; a second Close is a no-op.
func TestCloseEndsThePump(t *testing.T) {
	s := New(80, 24)
	done := make(chan struct{})
	go func() {
		for range s.Responses() {
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		s.Write([]byte("\x1b[c\x1b[6nhello"))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the pump did not end")
	}
	if s.Text() == "" {
		t.Fatal("the screen is gone after Close")
	}
}
