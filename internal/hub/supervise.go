package hub

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/vt"
)

// superviseAsync brings a running session under supervision, once.
func (h *Hub) superviseAsync(id string) {
	h.mu.Lock()
	if h.closed || h.supervised[id] {
		h.mu.Unlock()
		return
	}
	h.supervised[id] = true
	h.wg.Add(1)
	h.mu.Unlock()

	go func() {
		defer h.wg.Done()
		h.supervise(h.ctx, id)
		h.mu.Lock()
		delete(h.supervised, id)
		h.mu.Unlock()
	}()
}

// supervise keeps one connection to a worker and keeps its session record current, reconnecting with
// backoff for as long as the record says running. A worker can drop a connection — its own shutdown,
// a restarted writer — without the session being over, so losing the link is not losing the worker.
func (h *Hub) supervise(ctx context.Context, id string) {
	wait := superviseRetry
	for {
		err := h.superviseOnce(ctx, id)
		if ctx.Err() != nil {
			return
		}
		rec, ok := h.lookup(id)
		if !ok || rec.State != stateRunning {
			if err != nil {
				h.logf("hub: session %s: supervision ended: %v", id, err)
			}
			return
		}
		if err != nil {
			h.logf("hub: session %s: supervision: %v; reconnecting in %s", id, err, wait)
		}
		if h.markLostIfGone(ctx, id, err) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
		if wait > superviseRetryMax {
			wait = superviseRetryMax
		}
	}
}

// superviseOnce holds one protocol connection to a worker until it ends: the connection is lost, the
// worker reports its child's exit, the record stops saying running, or ctx is done.
//
// The hub attaches as a bridge client that never claims the size lease. It asks for no retained
// state, so the worker answers with a snapshot: the cheapest mode for a client that only wants the
// control traffic. Every output frame is acknowledged immediately, which keeps the worker's writer
// draining instead of pausing it after 256 KiB. session.exit is what the hub is really waiting for;
// closing the connection afterwards lets the worker finish its shutdown.
func (h *Hub) superviseOnce(ctx context.Context, id string) error {
	c, err := h.dialSupervision(ctx, id)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.Close()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-stop:
		}
	}()
	go h.sizeLoop(ctx, id, stop)
	var scr vt.Screen
	defer func() { h.followScreen(id, &scr, 0, 0) }()

	if err := c.writeControl(&protocol.Message{
		Type:       protocol.TypeSessionAttach,
		SessionID:  id,
		Channel:    supervisorChannel,
		ClaimLease: false,
	}); err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	for {
		f, err := c.readFrame()
		if err != nil {
			return err
		}
		switch f.Header.Type {
		case protocol.FrameControl:
			m, err := protocol.DecodeControl(f)
			if err != nil {
				h.logf("hub: session %s: bad control frame: %v", id, err)
				continue
			}
			switch m.Type {
			case protocol.TypeSessionAttached:
				if err := h.updateSize(id, m.Cols, m.Rows); err != nil {
					h.logf("hub: session %s: record size: %v", id, err)
				}
			case protocol.TypeSnapshotBegin:
				// A snapshot rebuilds the screen on a cleared one: at the attach, and after a resize.
				h.followScreen(id, &scr, m.Cols, m.Rows)
			case protocol.TypeSessionExit:
				h.logf("hub: session %s: worker reported child exit (code %s)", id, exitCodeText(m.ExitCode))
				if err := h.settle(id, stateExited, m.ExitCode, exitTime(m.At)); err != nil {
					h.logf("hub: session %s: record exit: %v", id, err)
				}
				return nil
			case protocol.TypeError:
				h.logf("hub: session %s: worker answered %s: %s", id, m.Code, m.Message)
			}
		case protocol.FrameOutput:
			// Follow the screen, and tell the worker the bytes are accepted, so flow control never
			// pauses the supervision connection and session.exit cannot end up behind a stalled writer.
			if scr != nil {
				scr.Write(f.Payload)
			}
			next := f.Header.Seq + uint64(len(f.Payload))
			if err := c.writeFrame(protocol.Frame{Header: protocol.Header{
				Type:    protocol.FrameAck,
				Channel: f.Header.Channel,
				Seq:     next,
			}}); err != nil {
				return err
			}
		case protocol.FrameSnapshot:
			// A snapshot is bounded by the screen size, so it never trips the pause threshold, and
			// an ack carrying a part index would corrupt the byte-offset accounting.
			if scr != nil {
				scr.Write(f.Payload)
			}
		case protocol.FrameAck, protocol.FrameInput:
		}
	}
}

// followScreen replaces the session's followed screen with a fresh one of cols x rows, or with
// none when cols is 0 (supervision ended).
func (h *Hub) followScreen(id string, scr *vt.Screen, cols, rows int) {
	old := *scr
	*scr = nil
	if cols > 0 && rows > 0 {
		*scr = vt.NewVisible(cols, rows) // ScreenText reads the visible screen only
		go func(r <-chan []byte) {       // the PTY's own terminal answers its queries, not this copy
			for range r {
			}
		}((*scr).Responses())
	}
	h.mu.Lock()
	if *scr != nil {
		h.screens[id] = *scr
	} else if h.screens[id] == old {
		delete(h.screens, id)
	}
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// ScreenText is the visible text of a supervised session's screen, as the supervision stream left it.
func (h *Hub) ScreenText(id string) (string, bool) {
	h.mu.Lock()
	scr, ok := h.screens[id]
	h.mu.Unlock()
	if !ok {
		return "", false
	}
	return scr.Text(), true
}

// sizeLoop re-reads the handshake file: the worker rewrites it whenever a client resizes the PTY,
// which is the only notification the hub gets about a size it did not ask for.
func (h *Hub) sizeLoop(ctx context.Context, id string, stop <-chan struct{}) {
	t := time.NewTicker(sizeRefresh)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			h.refreshSize(id)
		}
	}
}

// dialSupervision connects to a worker's socket, retrying while the socket is not accepting yet.
// It gives up as soon as the worker process is gone.
func (h *Hub) dialSupervision(ctx context.Context, id string) (*conn, error) {
	deadline := time.Now().Add(dialRetry)
	for {
		rec, ok := h.lookup(id)
		if !ok {
			return nil, fmt.Errorf("session %s is no longer recorded", id)
		}
		if rec.State != stateRunning {
			return nil, fmt.Errorf("session %s is %s", id, rec.State)
		}
		if rec.Pid > 0 && !pidAlive(rec.Pid) {
			return nil, fmt.Errorf("worker %d for session %s is gone", rec.Pid, id)
		}
		c, err := dialWorker(ctx, h.socketPath(rec))
		if err == nil {
			return c, nil
		}
		last := err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial worker socket %s: %w", h.socketPath(rec), last)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval * 5):
		}
	}
}

// markLostIfGone records a session as lost when supervision ended and the worker behind it is gone.
// It returns true when the session is settled, so supervision stops reconnecting; a worker that is
// still there — or one whose identity cannot be read — keeps the session running. It stays quiet when
// the hub itself is shutting down.
//
// A worker that shuts down closes its clients before it exits, so the check retries for a moment:
// that is the difference between losing track of a running worker and watching one finish.
func (h *Hub) markLostIfGone(ctx context.Context, id string, cause error) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline := time.Now().Add(lostGrace)
	for {
		rec, ok := h.lookup(id)
		if !ok || rec.State != stateRunning {
			return true
		}
		ref := h.workerProcess(id, rec)
		if ref.status == procGone || ref.status == procForeign {
			h.logf("hub: session %s: %s (%v); marking lost", id, ref.detail, cause)
			if err := h.loseSession(id, ref); err != nil {
				h.logf("hub: session %s: record lost: %v", id, err)
			}
			return true
		}
		if ref.status == procOurs && ref.pid != rec.Pid {
			// The worker is alive under another pid: fix the record so the reconnect can find it.
			if err := h.syncPid(id, ref.pid); err != nil {
				h.logf("hub: session %s: record pid: %v", id, err)
			}
			return false
		}
		if time.Now().After(deadline) {
			h.logf("hub: session %s: supervision lost its connection (%v) but %s", id, cause, ref.detail)
			return false
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(gonePoll):
		}
	}
}

// exitTime parses the RFC 3339 timestamp of a session.exit message, falling back to now.
func exitTime(at string) time.Time {
	if at != "" {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

// exitCodeText renders a session.exit code for the log.
func exitCodeText(code *int) string {
	if code == nil {
		return "unknown"
	}
	return strconv.Itoa(*code)
}
