package session

// ring.go implements the worker's output ring: a fixed-capacity byte buffer that keeps only the
// most recent bytes of a session's output stream. Offsets are absolute and monotonic for the
// lifetime of the ring, so a client can resume from the last byte it accepted for as long as that
// offset has not been pushed out of the window (`Start() <= offset <= End()`).
//
// The ring is safe for concurrent use: every method takes the ring's own lock and Read returns a
// copy. Callers must still capture the ring's end offset and the matching VT snapshot under one
// outer lock (see the data flow rules in docs/design/m1-contract.md) so that live output resumes
// exactly where the snapshot ends.

import "sync"

// Ring is a fixed-capacity circular byte buffer addressed by absolute offsets.
//
// start is the offset of the oldest byte still buffered, end the offset one past the newest byte
// ever written. Both only grow. When the ring is full, Write drops the oldest bytes and advances
// start, so the readable window is always [start, end].
type Ring struct {
	mu    sync.RWMutex
	buf   []byte
	head  int // index in buf of the byte at offset start
	n     int // number of readable bytes, at most len(buf)
	start uint64
	end   uint64
}

// NewRing creates a ring that keeps at most bytes bytes of stream history. A non-positive size
// selects DefaultRingBytes.
func NewRing(bytes int) *Ring {
	if bytes <= 0 {
		bytes = DefaultRingBytes
	}
	return &Ring{buf: make([]byte, bytes)}
}

// Write appends p to the ring, discarding the oldest bytes when the ring is full, and returns the
// new end offset. Write never blocks and never fails: a slow client can never hold the agent back.
func (r *Ring) Write(p []byte) (end uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(p) >= len(r.buf) {
		// Only the tail fits; copy it straight over the buffer rather than looping.
		copy(r.buf, p[len(p)-len(r.buf):])
		r.head = 0
		r.n = len(r.buf)
		r.end += uint64(len(p))
		r.start = r.end - uint64(r.n)
		return r.end
	}

	for written := 0; written < len(p); {
		// The next byte goes just past the readable window; when the ring is full that position
		// holds the oldest byte, which this write evicts.
		idx := (r.head + r.n) % len(r.buf)
		space := min(len(r.buf)-idx, len(p)-written)
		if r.n < len(r.buf) {
			space = min(space, len(r.buf)-r.n) // don't run past the oldest byte
		}
		copy(r.buf[idx:idx+space], p[written:written+space])
		if r.n == len(r.buf) {
			r.head = (idx + space) % len(r.buf)
			r.start += uint64(space)
		} else {
			r.n += space
		}
		written += space
	}

	r.end += uint64(len(p))
	return r.end
}

// Read returns a copy of up to max bytes starting at the absolute offset. It reports false when
// offset is outside [Start, End]: before Start those bytes have been overwritten (a gap the caller
// must resynchronize with a snapshot), after End the offset is in the future. Reading at End
// returns an empty slice and true. The returned slice never aliases the ring's buffer.
func (r *Ring) Read(offset uint64, max int) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if offset < r.start || offset > r.end {
		return nil, false
	}
	n := r.end - offset
	if max <= 0 || n == 0 {
		return nil, true
	}
	n = min(n, uint64(max))

	idx := (r.head + int(offset-r.start)) % len(r.buf)
	out := make([]byte, n)
	if first := copy(out, r.buf[idx:]); first < len(out) {
		copy(out[first:], r.buf)
	}
	return out, true
}

// Start returns the absolute offset of the oldest byte still buffered. A client whose next
// expected offset is below Start can no longer be replayed to and needs a snapshot.
func (r *Ring) Start() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.start
}

// End returns the absolute offset one past the newest byte written.
func (r *Ring) End() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.end
}

// Buffered reports how many bytes of history are readable right now, i.e. End()-Start(). A worker
// uses it to tell a client how much of its missing output can still be replayed.
func (r *Ring) Buffered() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.n
}

// Cap reports the ring's fixed capacity in bytes.
func (r *Ring) Cap() int { return len(r.buf) }
