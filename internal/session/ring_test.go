package session

import (
	"bytes"
	"sync"
	"testing"
)

func TestRingWriteRead(t *testing.T) {
	r := NewRing(8)
	if got := r.Start(); got != 0 {
		t.Fatalf("Start() = %d, want 0", got)
	}
	if got := r.End(); got != 0 {
		t.Fatalf("End() = %d, want 0", got)
	}
	if got := r.Buffered(); got != 0 {
		t.Fatalf("Buffered() = %d, want 0", got)
	}

	if end := r.Write([]byte("hello")); end != 5 {
		t.Fatalf("Write() = %d, want 5", end)
	}
	if r.Start() != 0 || r.End() != 5 || r.Buffered() != 5 {
		t.Fatalf("window = [%d,%d) buffered=%d, want [0,5) buffered=5", r.Start(), r.End(), r.Buffered())
	}
	if got, ok := r.Read(0, 100); !ok || string(got) != "hello" {
		t.Fatalf("Read(0, 100) = %q, %v, want \"hello\", true", got, ok)
	}
	if got, ok := r.Read(1, 3); !ok || string(got) != "ell" {
		t.Fatalf("Read(1, 3) = %q, %v, want \"ell\", true", got, ok)
	}
	// Reading at End is legal and yields nothing; reading beyond it is a future offset.
	if got, ok := r.Read(5, 10); !ok || len(got) != 0 {
		t.Fatalf("Read(5, 10) = %q, %v, want empty, true", got, ok)
	}
	if got, ok := r.Read(6, 10); ok || got != nil {
		t.Fatalf("Read(6, 10) = %q, %v, want nil, false", got, ok)
	}
	if got, ok := r.Read(0, 0); !ok || len(got) != 0 {
		t.Fatalf("Read(0, 0) = %q, %v, want empty, true", got, ok)
	}

	// A zero-length write moves nothing but still reports the offset.
	if end := r.Write(nil); end != 5 {
		t.Fatalf("Write(nil) = %d, want 5", end)
	}
}

func TestRingWrapAround(t *testing.T) {
	r := NewRing(10)
	r.Write([]byte("0123456789")) // fills the ring exactly

	if end := r.Write([]byte("ab")); end != 12 {
		t.Fatalf("Write() = %d, want 12", end)
	}
	if got := r.Start(); got != 2 {
		t.Fatalf("Start() = %d, want 2: the two oldest bytes were evicted", got)
	}
	if got := r.End(); got != 12 {
		t.Fatalf("End() = %d, want 12", got)
	}
	if got := r.Buffered(); got != 10 {
		t.Fatalf("Buffered() = %d, want 10", got)
	}

	// The window now starts in the middle of the backing array and wraps: offsets 2..11 read
	// "23456789ab".
	if got, ok := r.Read(2, 100); !ok || string(got) != "23456789ab" {
		t.Fatalf("Read(2, 100) = %q, %v, want \"23456789ab\", true", got, ok)
	}
	// A read that spans the wrap boundary.
	if got, ok := r.Read(8, 100); !ok || string(got) != "89ab" {
		t.Fatalf("Read(8, 100) = %q, %v, want \"89ab\", true", got, ok)
	}
	if got, ok := r.Read(9, 2); !ok || string(got) != "9a" {
		t.Fatalf("Read(9, 2) = %q, %v, want \"9a\", true", got, ok)
	}
	if got, ok := r.Read(7, 3); !ok || string(got) != "789" {
		t.Fatalf("Read(7, 3) = %q, %v, want \"789\", true", got, ok)
	}
	// Everything before Start fell out of the window.
	if got, ok := r.Read(1, 1); ok || got != nil {
		t.Fatalf("Read(1, 1) = %q, %v, want nil, false (evicted)", got, ok)
	}
	if _, ok := r.Read(0, 1); ok {
		t.Fatal("Read(0, 1) reported success, want a gap")
	}
}

func TestRingWritesAcrossWrapInSteps(t *testing.T) {
	r := NewRing(10)
	// Write in small steps so the head lands in the middle of the array, then keep writing until
	// the copy has to wrap several times.
	var want []byte
	total := 0
	for i := range 40 {
		chunk := []byte{byte('A' + i%26), byte('a' + i%26), byte('0' + i%10)}
		total += len(chunk)
		want = append(want, chunk...)
		if got := r.Write(chunk); got != uint64(total) {
			t.Fatalf("Write #%d = %d, want %d", i, got, total)
		}
		want = want[max(0, len(want)-10):]
		if got, ok := r.Read(r.Start(), 100); !ok || !bytes.Equal(got, want) {
			t.Fatalf("after write #%d: Read(Start) = %q, %v, want %q", i, got, ok, want)
		}
		if got := r.Buffered(); got != len(want) {
			t.Fatalf("after write #%d: Buffered() = %d, want %d", i, got, len(want))
		}
	}
}

func TestRingOversizedWrite(t *testing.T) {
	r := NewRing(8)
	// Longer than the whole ring: only the tail can survive.
	if end := r.Write([]byte("0123456789abcdefghij")); end != 20 {
		t.Fatalf("Write() = %d, want 20", end)
	}
	if r.Start() != 12 || r.End() != 20 || r.Buffered() != 8 {
		t.Fatalf("window = [%d,%d) buffered=%d, want [12,20) buffered=8", r.Start(), r.End(), r.Buffered())
	}
	if got, ok := r.Read(12, 100); !ok || string(got) != "cdefghij" {
		t.Fatalf("Read(12, 100) = %q, %v, want \"cdefghij\", true", got, ok)
	}
	if _, ok := r.Read(11, 1); ok {
		t.Fatal("Read(11, 1) reported success, want a gap")
	}

	// Exactly the capacity long.
	r = NewRing(8)
	if end := r.Write([]byte("abcdefgh")); end != 8 {
		t.Fatalf("Write() = %d, want 8", end)
	}
	if got, ok := r.Read(0, 100); !ok || string(got) != "abcdefgh" {
		t.Fatalf("Read(0, 100) = %q, %v, want \"abcdefgh\", true", got, ok)
	}
	if got, ok := r.Read(8, 100); !ok || len(got) != 0 {
		t.Fatalf("Read(8, 100) = %q, %v, want empty, true", got, ok)
	}
}

func TestRingReadDoesNotAliasBuffer(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abcdefgh"))
	out, ok := r.Read(0, 8)
	if !ok {
		t.Fatal("Read(0, 8) failed")
	}
	// Overwrite the whole ring, then check the earlier read is untouched.
	r.Write([]byte("XXXXXXXX"))
	if string(out) != "abcdefgh" {
		t.Fatalf("Read result changed after a later write: %q", out)
	}
}

func TestRingDefaultCapacity(t *testing.T) {
	r := NewRing(0)
	if r.Cap() != DefaultRingBytes {
		t.Fatalf("Cap() = %d, want %d", r.Cap(), DefaultRingBytes)
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing(1024)
	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 500 {
			r.Write([]byte{byte(i), byte(i >> 8), byte(i >> 16)})
		}
	}()

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lastEnd uint64
			for range 500 {
				end := r.End()
				if end < lastEnd {
					t.Errorf("End went backwards: %d → %d", lastEnd, end)
					return
				}
				lastEnd = end
				if b, ok := r.Read(r.Start(), 4); ok && len(b) > 4 {
					t.Errorf("Read returned %d bytes for max=4", len(b))
					return
				}
				_ = r.Buffered()
			}
		}()
	}
}
