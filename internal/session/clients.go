package session

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// inQueue is how many frames a connection may have read but not yet handled.
const inQueue = 16

// exitMessage builds the session.exit a client sees when the agent is gone.
func exitMessage(sessionID string, code int, at time.Time) *protocol.Message {
	c := code
	return &protocol.Message{
		ID:        protocol.NewID(),
		Type:      protocol.TypeSessionExit,
		SessionID: sessionID,
		ExitCode:  &c,
		At:        at.UTC().Format(time.RFC3339Nano),
	}
}

// outFrame is one frame queued for a connection's socket writer.
type outFrame struct {
	typ     protocol.FrameType
	channel uint32
	seq     uint64
	payload []byte
}

// client is one connection to the worker.
//
// The connection's own fields (greeted, attached, channel, gen, sent, acked, paused, viewOnly) are
// only ever touched by its serve goroutine; anything shared with the worker — the clients/attached
// sets, the owner pointer, the ring, the screen, the generation, pendingExit — lives on worker and
// is read under worker.mu. Socket writes happen on the writer goroutine, fed through a
// condition-variable queue so that a slow or paused client never blocks the PTY read loop.
type client struct {
	w    *worker
	conn net.Conn

	id   string
	kind protocol.ClientKind
	name string

	in     chan protocol.Frame
	wake   chan struct{}
	closed chan struct{}

	closeOnce  sync.Once
	outMu      sync.Mutex
	outCond    *sync.Cond
	out        []outFrame
	queued     int    // payload bytes queued or in flight on the socket, guarded by outMu
	written    uint64 // next offset the writer has actually put on the socket, guarded by outMu
	draining   bool
	writerDone chan struct{}

	greeted  bool
	attached bool
	viewOnly bool // the attach said kind "ios": no lease, no input (docs/protocol/v1.md section 4)
	resets   bool // the attach said resets: every snapshot carries the history (snapshotFor)
	// historySent: this client has had the history once. Guarded by worker.mu.
	historySent bool
	channel     uint32
	gen         uint64 // session generation this client's screen is valid for
	sent        uint64 // next byte offset this client has been sent
	acked       uint64 // next byte offset this client has acknowledged
	paused      bool
	// pendingExit is the session.exit this client is owed, queued by pump once the client has
	// caught up with every byte it is owed. Guarded by worker.mu.
	pendingExit *outFrame
}

// leaseNotice is one client's copy of a lease.changed the worker has prepared to send.
type leaseNotice struct {
	c *client
	m *protocol.Message
}

// validSize reports whether a client-supplied geometry is one the worker will apply.
func validSize(cols, rows int) bool {
	return cols >= 1 && cols <= maxResize && rows >= 1 && rows <= maxResize
}

// serve runs one connection until it detaches, disconnects, or the worker shuts down. Every exit
// flushes what is queued for the client and then closes the connection.
func (w *worker) serve(conn net.Conn) {
	c := &client{
		w:          w,
		conn:       conn,
		id:         protocol.NewID(),
		in:         make(chan protocol.Frame, inQueue),
		wake:       make(chan struct{}, 1),
		closed:     make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	c.outCond = sync.NewCond(&c.outMu)

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		conn.Close()
		return
	}
	w.connWG.Add(1)
	w.clients[c] = struct{}{}
	w.mu.Unlock()

	defer w.connWG.Done()
	defer w.removeClient(c)
	defer c.finish()

	go c.writeLoop()
	go c.readLoop()
	w.logf("session %s: connection %s accepted", w.spec.SessionID, c.id)

	for {
		select {
		case <-w.done:
			return
		case <-c.closed:
			return
		case <-c.wake:
			c.pump()
		case f, ok := <-c.in:
			if !ok {
				return
			}
			if !c.handle(f) {
				return
			}
		}
	}
}

// removeClient drops a connection that is done. It runs on that connection's serve goroutine.
func (w *worker) removeClient(c *client) {
	w.mu.Lock()
	if c.attached {
		c.attached = false
		delete(w.attached, c)
	}
	c.pendingExit = nil
	var notices []leaseNotice
	if w.owner == c {
		w.owner = nil // the last owner's size is kept; the lease is not transferred
		notices = w.leaseChangedNotices()
	}
	delete(w.clients, c)
	w.mu.Unlock()

	sendLeaseChanged(notices)
	w.logf("session %s: connection %s closed", w.spec.SessionID, c.id)
	w.checkDone()
}

// leaseChangedNotices prepares every attached client's copy of lease.changed: session_id and the
// holder's connection_id, with owner set only in the holder's own copy, plus the current size and
// generation. It must be called with w.mu held, right after the owner changed or an owner detached.
func (w *worker) leaseChangedNotices() []leaseNotice {
	holder := ""
	if w.owner != nil {
		holder = w.owner.id
	}
	notices := make([]leaseNotice, 0, len(w.attached))
	for c := range w.attached {
		notices = append(notices, leaseNotice{c: c, m: &protocol.Message{
			ID:           protocol.NewID(),
			Type:         protocol.TypeLeaseChanged,
			SessionID:    w.spec.SessionID,
			ConnectionID: holder,
			Owner:        w.owner == c,
			Cols:         w.cols,
			Rows:         w.rows,
			Generation:   w.gen,
		}})
	}
	return notices
}

// sendLeaseChanged enqueues lease.changed copies prepared under the worker lock.
func sendLeaseChanged(notices []leaseNotice) {
	for _, n := range notices {
		n.c.enqueueControl(n.m)
	}
}

// readLoop decodes frames off the socket. It is the only reader of the connection. A transport
// fault (truncated frame, over-limit length, bad channel) is reported as a protocol error message
// before the connection is dropped, which is all the peer can be given once the stream desynced.
func (c *client) readLoop() {
	defer close(c.in)
	for {
		f, err := protocol.ReadFrame(c.conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || c.isClosed() {
				return
			}
			c.errorReply("", err)
			return
		}
		select {
		case c.in <- f:
		case <-c.closed:
			return
		}
	}
}

// writeLoop drains the queue to the socket. Blocking here is exactly what pauses a slow client.
// It is also the only place that knows which bytes really left the worker, so it advances the
// client's `written` offset and releases the queue's byte budget.
func (c *client) writeLoop() {
	defer close(c.writerDone)
	for {
		f, ok := c.next()
		if !ok {
			return
		}
		frame := protocol.Frame{
			Header:  protocol.Header{Type: f.typ, Channel: f.channel, Seq: f.seq},
			Payload: f.payload,
		}
		if err := protocol.WriteFrame(c.conn, frame); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				c.w.logf("session %s: connection %s write: %v", c.w.spec.SessionID, c.id, err)
			}
			c.close()
			return
		}
		c.outMu.Lock()
		c.queued -= len(f.payload)
		if f.typ == protocol.FrameOutput {
			if end := f.seq + uint64(len(f.payload)); end > c.written {
				c.written = end
			}
		}
		c.outMu.Unlock()
	}
}

func (c *client) next() (outFrame, bool) {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	for len(c.out) == 0 {
		if c.draining || c.isClosed() {
			return outFrame{}, false
		}
		c.outCond.Wait()
	}
	f := c.out[0]
	c.out = c.out[1:]
	return f, true
}

// finish stops accepting new frames, waits briefly for the queued ones to reach the client, and
// closes the connection. The wait is what lets a session.exit still arrive on shutdown.
func (c *client) finish() {
	c.outMu.Lock()
	c.draining = true
	c.outCond.Broadcast()
	c.outMu.Unlock()

	select {
	case <-c.writerDone:
	case <-time.After(flushTimeout):
	}
	c.close()
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.outMu.Lock()
		c.outCond.Broadcast()
		c.outMu.Unlock()
		c.conn.Close()
	})
}

func (c *client) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// enqueue queues a frame for the socket writer. It never blocks the caller, so a client that is not
// draining its socket cannot stall the PTY read loop or another client. A client that is already
// past the queue cap is closed instead: one viewer must never grow the worker without bound.
func (c *client) enqueue(f outFrame) {
	if c.isClosed() {
		return
	}
	c.outMu.Lock()
	if c.queued+len(f.payload) > maxQueuedBytes {
		queued := c.queued
		c.outMu.Unlock()
		c.w.logf("session %s: connection %s is %d bytes behind its queue cap of %d, closing it",
			c.w.spec.SessionID, c.id, queued+len(f.payload), maxQueuedBytes)
		c.close()
		return
	}
	c.queued += len(f.payload)
	c.out = append(c.out, f)
	c.outCond.Signal()
	c.outMu.Unlock()
}

func (c *client) enqueueControl(m *protocol.Message) {
	if m.ID == "" {
		m.ID = protocol.NewID()
	}
	b, err := json.Marshal(m)
	if err != nil {
		c.w.logf("session %s: connection %s: marshal %s: %v", c.w.spec.SessionID, c.id, m.Type, err)
		return
	}
	c.enqueue(outFrame{typ: protocol.FrameControl, payload: b})
}

func (c *client) errorReply(id string, err error) {
	if id == "" {
		id = protocol.NewID()
	}
	c.enqueueControl(protocol.ErrorMessage(id, err))
	c.w.logf("session %s: connection %s: %v", c.w.spec.SessionID, c.id, err)
}

// wakeUp asks the connection to try sending pending output. The channel is buffered, so this is
// safe to call from the PTY read loop while holding the worker lock.
func (c *client) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// handle dispatches one frame. It reports whether the connection should stay open.
func (c *client) handle(f protocol.Frame) bool {
	switch f.Header.Type {
	case protocol.FrameControl:
		m, err := protocol.DecodeControl(f)
		if err != nil {
			c.errorReply("", err)
			return false
		}
		return c.handleControl(m)
	case protocol.FrameInput:
		c.input(f)
		return true
	case protocol.FrameAck:
		c.ack(f)
		return true
	default:
		c.w.logf("session %s: connection %s: ignoring %s frame from a client", c.w.spec.SessionID, c.id, f.Header.Type)
		return true
	}
}

func (c *client) handleControl(m *protocol.Message) bool {
	if !c.greeted {
		if m.Type != protocol.TypeHello {
			c.errorReply(m.ID, protocol.Errorf(protocol.CodeUnauthorized, "hello must be the first frame on a connection"))
			return false
		}
		return c.hello(m)
	}
	switch m.Type {
	case protocol.TypeHello:
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeInvalidArgument, "hello was already received"))
		return true
	case protocol.TypePing:
		c.enqueueControl(&protocol.Message{ID: m.ID, Type: protocol.TypePong})
		return true
	case protocol.TypeSessionAttach:
		return c.attach(m)
	case protocol.TypeSessionDetach:
		c.detach()
		return true
	case protocol.TypeSessionResize:
		c.resize(m)
		return true
	default:
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeUnsupported, "message type %q is not handled by a worker", m.Type))
		return true
	}
}

func (c *client) hello(m *protocol.Message) bool {
	if m.V != protocol.Version {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeIncompatibleVersion, "protocol version %d is not supported, this worker speaks %d", m.V, protocol.Version))
		return false
	}
	if m.Client != nil {
		c.kind, c.name = m.Client.Kind, m.Client.Name
	}
	c.greeted = true
	c.enqueueControl(&protocol.Message{
		ID:           m.ID,
		Type:         protocol.TypeHelloOK,
		V:            protocol.Version,
		ConnectionID: c.id,
		Capabilities: protocol.Capabilities,
	})
	return true
}

// attach binds this connection to the worker's session, optionally taking the size-lease, and
// answers with replay or snapshot semantics per docs/protocol/v1.md section 3. A claim may carry
// the client's geometry, which is applied before the snapshot; a client whose kind is "ios" is
// view-only and never takes the lease.
func (c *client) attach(m *protocol.Message) bool {
	if c.attached {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeConflict, "connection is already attached"))
		return true
	}
	if m.SessionID != "" && m.SessionID != c.w.spec.SessionID {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeNotFound, "unknown session %s", m.SessionID))
		return true
	}
	if m.Channel == 0 {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeInvalidArgument, "session.attach needs a nonzero channel"))
		return true
	}
	// The attach carries the kind of the client behind it (the gateway relays it); fall back to the
	// kind the hello named.
	kind := m.Kind
	if kind == "" {
		kind = string(c.kind)
	}
	c.viewOnly = kind == string(protocol.KindIOS)
	c.resets = m.Resets
	claim := m.ClaimLease && !c.viewOnly
	if claim && (m.Cols != 0 || m.Rows != 0) && !validSize(m.Cols, m.Rows) {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeInvalidArgument, "resize %dx%d is out of range", m.Cols, m.Rows))
		return true
	}

	w := c.w
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	// The claim's geometry is applied to the PTY before the snapshot is captured, so the client's
	// first screen is the size it asked for (R6).
	resized := false
	if claim && m.Cols > 0 && m.Rows > 0 && (m.Cols != w.cols || m.Rows != w.rows) {
		if err := pty.Setsize(w.ptmx, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)}); err != nil {
			w.mu.Unlock()
			c.errorReply(m.ID, protocol.Errorf(protocol.CodeUnavailable, "resize pty: %v", err))
			return true
		}
		w.cols, w.rows = m.Cols, m.Rows
		w.gen++
		w.screen.Resize(m.Cols, m.Rows)
		resized = true
	}
	c.channel = m.Channel
	c.attached = true
	w.attached[c] = struct{}{}
	var notices []leaseNotice
	if claim && w.owner != c {
		if w.owner != nil {
			w.logf("session %s: size lease moves from %s to %s", w.spec.SessionID, w.owner.id, c.id)
		}
		w.owner = c
		// The lease change is published to everyone attached, this client included.
		notices = w.leaseChangedNotices()
	}
	gen, cols, rows := w.gen, w.cols, w.rows
	start := w.ring.End()
	end := start
	mode := protocol.ModeSnapshot
	var snap []byte
	if r := m.Retained; r != nil && r.Generation == gen && r.Offset >= w.ring.Start() && r.Offset <= start {
		start = r.Offset
		mode = protocol.ModeReplay
	} else {
		// Capture the screen and the offset it belongs to under the same lock the PTY read loop
		// writes under, so live output starts exactly where the snapshot ends.
		snap = c.snapshotFor()
	}
	exited, exitCode, exitedAt := w.exited, w.exitCode, w.exitedAt
	w.mu.Unlock()

	c.gen = gen
	c.sent = start
	c.acked = start
	c.enqueueControl(&protocol.Message{
		ID:         m.ID,
		Type:       protocol.TypeSessionAttached,
		SessionID:  w.spec.SessionID,
		Channel:    c.channel,
		Generation: gen,
		Cols:       cols,
		Rows:       rows,
		Mode:       mode,
		Offset:     start,
		End:        end,
	})
	if mode == protocol.ModeSnapshot {
		c.sendSnapshot(gen, cols, rows, snap, start)
	}
	c.pump()
	// The lease change is announced after this client's session.attached, so no client sees a lease
	// change for an attachment it does not know about yet.
	sendLeaseChanged(notices)
	if resized {
		if err := w.writeHandshake(); err != nil {
			w.logf("session %s: refresh handshake: %v", w.spec.SessionID, err)
		}
	}
	if exited {
		// The session is already over: the exit follows everything still owed to this client.
		w.mu.Lock()
		c.queueExitLocked(w.spec.SessionID, exitCode, exitedAt)
		w.mu.Unlock()
	}
	return true
}

// detach drops this connection's attachment. The size the last owner set is kept.
func (c *client) detach() {
	if !c.attached {
		return
	}
	w := c.w
	c.attached = false
	c.paused = false

	w.mu.Lock()
	delete(w.attached, c)
	c.pendingExit = nil
	var notices []leaseNotice
	if w.owner == c {
		w.owner = nil
		notices = w.leaseChangedNotices()
	}
	exited := w.exited
	w.mu.Unlock()

	sendLeaseChanged(notices)
	w.logf("session %s: connection %s detached", w.spec.SessionID, c.id)
	if exited {
		w.checkDone()
	}
}

// resize applies a new PTY size for the lease holder. A resize to the size the PTY already has only
// moves the lease: the generation is not bumped and no client is disturbed. A real change resizes
// the existing screen — its contents stay valid and are never thrown away — and bumps the
// generation, which lets a client whose screen is stale resynchronise on its next pump.
func (c *client) resize(m *protocol.Message) {
	if !c.attached {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeNotFound, "session not attached"))
		return
	}
	if !validSize(m.Cols, m.Rows) {
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeInvalidArgument, "resize %dx%d is out of range", m.Cols, m.Rows))
		return
	}

	w := c.w
	w.mu.Lock()
	var notices []leaseNotice
	if m.ClaimLease && !c.viewOnly && w.owner != c {
		// The browser never re-attaches to take the size: it claims on explicit focus or first
		// input and resizes in the same request (docs/protocol/v1.md section 4).
		w.owner = c
		notices = w.leaseChangedNotices()
	}
	if w.owner != c {
		w.mu.Unlock()
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeForbidden, "connection %s does not hold the size lease", c.id))
		return
	}
	if w.closed {
		w.mu.Unlock()
		return
	}
	if m.Cols == w.cols && m.Rows == w.rows {
		// The PTY already has this size: taking the lease is the whole request. Bumping the
		// generation here would blank every attached mirror for nothing (K2).
		w.mu.Unlock()
		sendLeaseChanged(notices)
		return
	}
	if err := pty.Setsize(w.ptmx, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)}); err != nil {
		w.mu.Unlock()
		sendLeaseChanged(notices)
		c.errorReply(m.ID, protocol.Errorf(protocol.CodeUnavailable, "resize pty: %v", err))
		return
	}
	w.cols, w.rows = m.Cols, m.Rows
	w.gen++
	w.screen.Resize(m.Cols, m.Rows)
	gen := w.gen
	w.mu.Unlock()

	sendLeaseChanged(notices)
	w.logf("session %s: resized to %dx%d, generation %d", w.spec.SessionID, m.Cols, m.Rows, gen)
	if err := w.writeHandshake(); err != nil {
		w.logf("session %s: refresh handshake: %v", w.spec.SessionID, err)
	}
}

// ack records how far a client has accepted output and resumes it once it is drained enough. The
// offset is clamped to what the writer has actually put on the socket: a client cannot have
// accepted bytes it was never sent, and trusting such an ack would stop flow control from ever
// pausing it.
func (c *client) ack(f protocol.Frame) {
	if !c.attached || f.Header.Channel != c.channel {
		return
	}
	c.outMu.Lock()
	written := c.written
	c.outMu.Unlock()
	off := f.Header.Seq
	if off > written {
		off = written
	}
	if off > c.sent {
		off = c.sent
	}
	if off > c.acked {
		c.acked = off
	}
	if c.paused && c.sent-c.acked < lowWaterBytes {
		c.paused = false
	}
	c.wakeUp()
}

// input forwards raw client input to the PTY master through the worker's single PTY writer. Input
// is best-effort: it is never replayed and never acknowledged (docs/protocol/v1.md section 5). A
// view-only client's input is refused.
func (c *client) input(f protocol.Frame) {
	if !c.attached || f.Header.Channel != c.channel {
		c.w.logf("session %s: connection %s: dropping %d input bytes on unattached channel %d", c.w.spec.SessionID, c.id, len(f.Payload), f.Header.Channel)
		return
	}
	if c.viewOnly {
		c.errorReply("", protocol.Errorf(protocol.CodeForbidden, "client kind %q is view-only: input is refused", string(protocol.KindIOS)))
		return
	}
	if len(f.Payload) == 0 {
		return
	}
	c.w.forwardInput(f.Payload)
}

// queueExitLocked hands this client the session.exit it is owed. The exit is not enqueued here:
// pump enqueues it once the client has been sent every byte it is owed, so it can never overtake
// output (R5). It runs with worker.mu held.
func (c *client) queueExitLocked(sessionID string, code int, at time.Time) {
	b, err := json.Marshal(exitMessage(sessionID, code, at))
	if err != nil {
		c.w.logf("session %s: connection %s: marshal session.exit: %v", c.w.spec.SessionID, c.id, err)
		return
	}
	c.pendingExit = &outFrame{typ: protocol.FrameControl, payload: b}
	c.wakeUp()
}

// pump sends every byte the client has not been sent yet, in ≤64 KiB output frames at their byte
// offsets. It stops while the client is paused above the high-water mark, and resynchronises with a
// snapshot whenever a replay would leave a gap or the client's screen is stale.
func (c *client) pump() {
	w := c.w
	for {
		if c.isClosed() {
			return
		}
		w.mu.Lock()
		if !c.attached {
			w.mu.Unlock()
			return
		}
		if w.gen != c.gen {
			// A resize invalidated this client's screen.
			gen, cols, rows := w.gen, w.cols, w.rows
			snap := c.snapshotFor()
			start := w.ring.End()
			w.mu.Unlock()
			c.sendSnapshot(gen, cols, rows, snap, start)
			continue
		}
		if c.paused && c.sent-c.acked >= lowWaterBytes {
			w.mu.Unlock()
			return
		}
		end := w.ring.End()
		if c.sent < w.ring.Start() || c.sent > end {
			// The pending offset fell out of the ring while the client was paused: replay would
			// leave a gap, so send a snapshot from the current offset instead.
			gen, cols, rows := w.gen, w.cols, w.rows
			snap := c.snapshotFor()
			w.mu.Unlock()
			c.sendSnapshot(gen, cols, rows, snap, end)
			continue
		}
		if c.sent == end {
			if c.pendingExit != nil {
				// Everything this client is owed has been enqueued; the exit follows it, so it can
				// never overtake the last output byte (R5).
				f := *c.pendingExit
				c.pendingExit = nil
				w.mu.Unlock()
				c.enqueue(f)
				return
			}
			w.mu.Unlock()
			return
		}
		data, ok := w.ring.Read(c.sent, maxOutputPayload)
		if !ok {
			gen, cols, rows := w.gen, w.cols, w.rows
			snap := c.snapshotFor()
			w.mu.Unlock()
			c.sendSnapshot(gen, cols, rows, snap, end)
			continue
		}
		w.mu.Unlock()

		if len(data) == 0 {
			return
		}
		// The ring hands out its own copy, so the payload can outlive the lock.
		c.enqueue(outFrame{typ: protocol.FrameOutput, channel: c.channel, seq: c.sent, payload: data})
		c.sent += uint64(len(data))
		if c.sent-c.acked >= highWaterBytes {
			c.paused = true
			return
		}
	}
}

// sendSnapshot emits snapshot.begin, the zero-based numbered chunks, and snapshot.end, then hands
// the client back to pump() at the captured offset.
// snapshotFor is the snapshot this client is sent: with the history when the client resets before
// applying one, and once otherwise, so a terminal that keeps what it was sent (pengupool attach) never
// shows the history twice. Called with worker.mu held.
func (c *client) snapshotFor() []byte {
	if c.resets || !c.historySent {
		c.historySent = true
		return c.w.screen.SnapshotHistory()
	}
	return c.w.screen.Snapshot()
}

func (c *client) sendSnapshot(gen uint64, cols, rows int, snap []byte, off uint64) {
	c.gen = gen
	c.sent = off
	c.acked = off
	// A snapshot restarts the byte accounting at the captured offset, but frames the writer has not
	// flushed yet still occupy this client's queue: clearing the pause flag here would let another
	// queue-full pile up behind them, which is how a non-reading client grew to megabytes (R4).
	c.outMu.Lock()
	empty := c.queued == 0
	c.outMu.Unlock()
	if empty {
		c.paused = false
	}

	sid := protocol.NewID()
	c.enqueueControl(&protocol.Message{
		ID:         sid,
		Type:       protocol.TypeSnapshotBegin,
		SessionID:  c.w.spec.SessionID,
		SnapshotID: sid,
		Generation: gen,
		Cols:       cols,
		Rows:       rows,
		Offset:     off,
	})
	parts := 0
	for len(snap) > 0 {
		n := len(snap)
		if n > maxSnapshotPayload {
			n = maxSnapshotPayload
		}
		c.enqueue(outFrame{typ: protocol.FrameSnapshot, channel: c.channel, seq: uint64(parts), payload: snap[:n]})
		snap = snap[n:]
		parts++
	}
	c.enqueueControl(&protocol.Message{
		ID:         sid,
		Type:       protocol.TypeSnapshotEnd,
		SessionID:  c.w.spec.SessionID,
		SnapshotID: sid,
		Parts:      parts,
	})
}
