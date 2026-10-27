package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

const (
	// testSessionID is the session id every helper spec uses.
	testSessionID = "sess-test"
	// testTimeout bounds a single wait for a frame the worker is expected to send.
	testTimeout = 15 * time.Second
	// idleWindow is how long a connection has to stay quiet before a test calls it paused.
	idleWindow = 300 * time.Millisecond
)

// bigOutput is the shell program the flow-control test drives: it prints ~615 KiB and then idles so
// the worker keeps running while the client deliberately does not acknowledge.
const bigOutput = `i=0; while [ $i -lt 15000 ]; do printf '0123456789012345678901234567890123456789\n'; i=$((i+1)); done; sleep 30`

// testWorker is an in-process worker under test.
type testWorker struct {
	t        *testing.T
	dir      string
	sock     string
	hs       Handshake
	cancel   context.CancelFunc
	errCh    chan error
	returned bool
}

// shortTempDir is t.TempDir with a short random name: a Unix socket path must fit in sun_path (104
// bytes on darwin), and t.TempDir embeds the test name.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pp2worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// startWorker runs a worker for command in a temp dir and waits for its handshake, which is the
// point at which the PTY, the socket and the handshake file are all live.
func startWorker(t *testing.T, command []string) *testWorker {
	t.Helper()
	return startWorkerRing(t, command, 0)
}

// startWorkerRing is startWorker with an explicit ring size in bytes (0 selects the default).
func startWorkerRing(t *testing.T, command []string, ringBytes int) *testWorker {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "run", testSessionID+".sock")
	spec := LaunchSpec{
		SessionID:  testSessionID,
		Name:       "test session",
		Dir:        dir,
		Command:    command,
		Cols:       80,
		Rows:       24,
		RingBytes:  ringBytes,
		SocketPath: sock,
	}
	ctx, cancel := context.WithCancel(context.Background())
	tw := &testWorker{t: t, dir: dir, sock: sock, cancel: cancel, errCh: make(chan error, 1)}
	go func() { tw.errCh <- RunWorker(ctx, WorkerConfig{Spec: spec, Logf: t.Logf}) }()

	deadline := time.Now().Add(testTimeout)
	for {
		tw.checkAlive()
		hs, err := ReadHandshake(HandshakePath(dir, testSessionID))
		if err == nil {
			tw.hs = hs
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not publish a handshake: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(tw.stop)
	return tw
}

// checkAlive fails the test when the worker has already returned, so a start-up error is never
// mistaken for a slow start.
func (tw *testWorker) checkAlive() {
	select {
	case err := <-tw.errCh:
		tw.returned = true
		tw.t.Fatalf("worker returned before its handshake: %v", err)
	default:
	}
}

// stop cancels the worker and waits for it to return. It is the cleanup path: the worker's own
// SIGTERM handling kills the agent, so a test does not have to.
func (tw *testWorker) stop() {
	if tw.returned {
		return
	}
	tw.cancel()
	select {
	case <-tw.errCh:
		tw.returned = true
	case <-time.After(testTimeout):
		tw.t.Errorf("worker did not return after cancellation")
	}
}

// waitReturn waits for RunWorker to return and reports its error.
func (tw *testWorker) waitReturn() error {
	tw.t.Helper()
	select {
	case err := <-tw.errCh:
		tw.returned = true
		return err
	case <-time.After(testTimeout):
		tw.t.Fatal("worker did not return")
		return nil
	}
}

// testConn is a raw protocol connection to a worker socket.
type testConn struct {
	t         *testing.T
	conn      net.Conn
	sessionID string
	// next is the next output byte offset this connection expects, and deadlineHit says the last
	// read returned because the deadline expired (the frame codec wraps the error as text, so it
	// cannot be recognised by value).
	next        uint64
	deadlineHit bool
}

func dialWorker(t *testing.T, sock string) *testConn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	t.Cleanup(func() { conn.Close() })
	return &testConn{t: t, conn: conn, sessionID: testSessionID}
}

func (tc *testConn) close() {
	tc.conn.Close()
}

func (tc *testConn) send(m *protocol.Message) {
	tc.t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		tc.t.Fatalf("marshal %s: %v", m.Type, err)
	}
	if err := protocol.WriteFrame(tc.conn, protocol.Frame{Header: protocol.Header{Type: protocol.FrameControl, Length: uint32(len(b))}, Payload: b}); err != nil {
		tc.t.Fatalf("send %s: %v", m.Type, err)
	}
}

func (tc *testConn) sendFrame(typ protocol.FrameType, channel uint32, seq uint64, payload []byte) {
	tc.t.Helper()
	if err := protocol.WriteFrame(tc.conn, protocol.Frame{Header: protocol.Header{Type: typ, Channel: channel, Seq: seq, Length: uint32(len(payload))}, Payload: payload}); err != nil {
		tc.t.Fatalf("send %s frame: %v", typ, err)
	}
}

func (tc *testConn) read(timeout time.Duration) (protocol.Frame, error) {
	deadline := time.Now().Add(timeout)
	if err := tc.conn.SetReadDeadline(deadline); err != nil {
		tc.t.Fatalf("set read deadline: %v", err)
	}
	f, err := protocol.ReadFrame(tc.conn)
	tc.deadlineHit = err != nil && !errors.Is(err, io.EOF) && !time.Now().Before(deadline)
	return f, err
}

// collect reads frames until done says stop, and returns everything it read including that frame.
func (tc *testConn) collect(timeout time.Duration, done func(protocol.Frame) bool) []protocol.Frame {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	var frames []protocol.Frame
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			tc.t.Fatalf("timed out after %d frames", len(frames))
		}
		f, err := tc.read(remaining)
		if err != nil {
			tc.t.Fatalf("read after %d frames: %v", len(frames), err)
		}
		frames = append(frames, f)
		if done(f) {
			return frames
		}
	}
}

// drainIdle reads frames until the connection has been idle for idle, and returns what it got.
func (tc *testConn) drainIdle(idle time.Duration) []protocol.Frame {
	tc.t.Helper()
	var frames []protocol.Frame
	for {
		f, err := tc.read(idle)
		if err != nil {
			if tc.deadlineHit {
				return frames
			}
			tc.t.Fatalf("read after %d frames: %v", len(frames), err)
		}
		frames = append(frames, f)
	}
}

func (tc *testConn) hello() *protocol.Message {
	tc.t.Helper()
	req := &protocol.Message{ID: protocol.NewID(), Type: protocol.TypeHello, V: protocol.Version, Client: &protocol.ClientInfo{Kind: protocol.KindCLI, Name: "worker-test", Version: "0"}}
	tc.send(req)
	frames := tc.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && (m.Type == protocol.TypeHelloOK || m.Type == protocol.TypeError)
	})
	return lastControl(tc.t, frames)
}

// attach sends session.attach and returns the session.attached reply or fails on an error response.
func (tc *testConn) attach(channel uint32, claim bool, retained *protocol.RetainedState) *protocol.Message {
	tc.t.Helper()
	return tc.attachRequest(&protocol.Message{
		ID:         protocol.NewID(),
		Type:       protocol.TypeSessionAttach,
		SessionID:  tc.sessionID,
		Channel:    channel,
		ClaimLease: claim,
		Retained:   retained,
	})
}

// attachRequest sends a prepared session.attach and reads its session.attached reply.
func (tc *testConn) attachRequest(req *protocol.Message) *protocol.Message {
	tc.t.Helper()
	tc.send(req)
	frames := tc.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && (m.Type == protocol.TypeSessionAttached || m.Type == protocol.TypeError)
	})
	reply := lastControl(tc.t, frames)
	if reply.Type == protocol.TypeError {
		tc.t.Fatalf("session.attach failed: %s: %s", reply.Code, reply.Message)
	}
	if reply.ID != req.ID {
		tc.t.Fatalf("session.attached id = %q, want the request id %q", reply.ID, req.ID)
	}
	tc.next = reply.Offset
	return reply
}

// expectControl reads until a control message of the given type arrives and returns it, failing on
// an error response.
func (tc *testConn) expectControl(typ string) *protocol.Message {
	tc.t.Helper()
	frames := tc.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && (m.Type == typ || m.Type == protocol.TypeError)
	})
	m := lastControl(tc.t, frames)
	if m.Type == protocol.TypeError {
		tc.t.Fatalf("got error %s: %s, want %s", m.Code, m.Message, typ)
	}
	if m.Type != typ {
		tc.t.Fatalf("got %s, want %s", m.Type, typ)
	}
	return m
}

// expectError reads until an error response arrives and returns it.
func (tc *testConn) expectError() *protocol.Message {
	tc.t.Helper()
	frames := tc.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && m.Type == protocol.TypeError
	})
	m := lastControl(tc.t, frames)
	if m.Type != protocol.TypeError {
		tc.t.Fatalf("expected an error response, got %s", m.Type)
	}
	return m
}

// readText reads live output until want appears, verifying that every output frame carries the next
// contiguous byte offset for this connection. It is used where no resize can intervene, so a
// snapshot frame in the middle would be a worker bug rather than a legal resync.
func (tc *testConn) readText(want string) []byte {
	tc.t.Helper()
	var text []byte
	tc.collect(testTimeout, func(f protocol.Frame) bool {
		switch f.Header.Type {
		case protocol.FrameOutput:
			if f.Header.Seq != tc.next {
				tc.t.Fatalf("output seq = %d, want %d", f.Header.Seq, tc.next)
			}
			tc.next += uint64(len(f.Payload))
			text = append(text, f.Payload...)
		case protocol.FrameSnapshot:
			tc.t.Fatalf("snapshot frame during readText: consume snapshots with readSnapshot first")
		case protocol.FrameControl:
			m := decodeControl(tc.t, f)
			switch m.Type {
			case protocol.TypeError:
				tc.t.Fatalf("worker error: %s: %s", m.Code, m.Message)
			case protocol.TypeSnapshotBegin:
				tc.t.Fatalf("snapshot.begin during readText: consume snapshots with readSnapshot first")
			}
		}
		return strings.Contains(string(text), want)
	})
	return text
}

// waitText returns the snapshot bytes when they already contain want, and otherwise the live output
// that carries it: a fresh screen may legitimately arrive empty and be filled in live.
func (tc *testConn) waitText(snapshot []byte, want string) []byte {
	tc.t.Helper()
	if bytes.Contains(snapshot, []byte(want)) {
		return snapshot
	}
	return append(snapshot, tc.readText(want)...)
}

// snapshotData is one complete snapshot sequence.
type snapshotData struct {
	begin  *protocol.Message
	end    *protocol.Message
	chunks []protocol.Frame
	data   []byte
}

// readSnapshot reads and validates a full snapshot sequence: snapshot.begin first, zero-based
// snapshot chunks in order, nothing else in between, then snapshot.end with the matching part count.
func (tc *testConn) readSnapshot() snapshotData {
	tc.t.Helper()
	var s snapshotData
	sawEnd := false
	frames := tc.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m := decodeControl(tc.t, f)
		if m.Type == protocol.TypeSnapshotEnd {
			sawEnd = true
		}
		return sawEnd
	})
	for _, f := range frames {
		switch f.Header.Type {
		case protocol.FrameControl:
			m := decodeControl(tc.t, f)
			switch m.Type {
			case protocol.TypeError:
				tc.t.Fatalf("worker error during snapshot: %s: %s", m.Code, m.Message)
			case protocol.TypeSnapshotBegin:
				if s.begin != nil {
					tc.t.Fatalf("second snapshot.begin in one sequence")
				}
				s.begin = m
			case protocol.TypeSnapshotEnd:
				s.end = m
			}
		case protocol.FrameSnapshot:
			if s.begin == nil {
				tc.t.Fatalf("snapshot chunk before snapshot.begin")
			}
			if s.end != nil {
				tc.t.Fatalf("snapshot chunk after snapshot.end")
			}
			if want := uint64(len(s.chunks)); f.Header.Seq != want {
				tc.t.Fatalf("snapshot chunk seq = %d, want the zero-based part index %d", f.Header.Seq, want)
			}
			s.chunks = append(s.chunks, f)
			s.data = append(s.data, f.Payload...)
		default:
			tc.t.Fatalf("%s frame inside a snapshot sequence", f.Header.Type)
		}
	}
	if s.begin == nil || s.end == nil {
		tc.t.Fatalf("incomplete snapshot sequence")
	}
	// Live output for this connection resumes at the snapshot's capture offset.
	tc.next = s.begin.Offset
	if s.end.SnapshotID != s.begin.SnapshotID {
		tc.t.Fatalf("snapshot.end id = %q, begin id = %q", s.end.SnapshotID, s.begin.SnapshotID)
	}
	if s.end.Parts != len(s.chunks) {
		tc.t.Fatalf("snapshot.end parts = %d, got %d chunks", s.end.Parts, len(s.chunks))
	}
	return s
}

func decodeControl(t *testing.T, f protocol.Frame) *protocol.Message {
	t.Helper()
	m, err := protocol.DecodeControl(f)
	if err != nil {
		t.Fatalf("decode control: %v", err)
	}
	return m
}

func lastControl(t *testing.T, frames []protocol.Frame) *protocol.Message {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Header.Type == protocol.FrameControl {
			return decodeControl(t, frames[i])
		}
	}
	t.Fatalf("no control frame in %d frames", len(frames))
	return nil
}

func TestWorkerHelloFirst(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf ready; sleep 30"})

	t.Run("non-hello first frame", func(t *testing.T) {
		c := dialWorker(t, tw.sock)
		c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypePing})
		m := c.expectError()
		if m.Code != protocol.CodeUnauthorized {
			t.Fatalf("code = %q, want %q", m.Code, protocol.CodeUnauthorized)
		}
		if _, err := c.read(testTimeout); !errors.Is(err, io.EOF) {
			t.Fatalf("connection still open after a refused first frame: %v", err)
		}
	})

	t.Run("attach before hello", func(t *testing.T) {
		c := dialWorker(t, tw.sock)
		c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionAttach, SessionID: testSessionID, Channel: 1})
		if m := c.expectError(); m.Code != protocol.CodeUnauthorized {
			t.Fatalf("code = %q, want %q", m.Code, protocol.CodeUnauthorized)
		}
	})

	t.Run("foreign protocol version", func(t *testing.T) {
		c := dialWorker(t, tw.sock)
		c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeHello, V: protocol.Version + 1, Client: &protocol.ClientInfo{Kind: protocol.KindCLI}})
		if m := c.expectError(); m.Code != protocol.CodeIncompatibleVersion {
			t.Fatalf("code = %q, want %q", m.Code, protocol.CodeIncompatibleVersion)
		}
	})

	t.Run("over-limit frame", func(t *testing.T) {
		c := dialWorker(t, tw.sock)
		c.hello()
		hdr := protocol.Header{Type: protocol.FrameOutput, Channel: 1, Length: protocol.MaxDataPayload + 1}
		if _, err := c.conn.Write(hdr.MarshalBinary()); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if m := c.expectError(); m.Code != protocol.CodeFrameTooLarge {
			t.Fatalf("code = %q, want %q", m.Code, protocol.CodeFrameTooLarge)
		}
		if _, err := c.read(testTimeout); !errors.Is(err, io.EOF) {
			t.Fatalf("connection still open after a transport fault: %v", err)
		}
	})

	t.Run("hello then ping", func(t *testing.T) {
		c := dialWorker(t, tw.sock)
		ok := c.hello()
		if ok.Type != protocol.TypeHelloOK {
			t.Fatalf("hello answer = %s", ok.Type)
		}
		if ok.V != protocol.Version {
			t.Fatalf("hello.ok v = %d", ok.V)
		}
		if ok.ConnectionID == "" {
			t.Fatal("hello.ok has no connection id")
		}
		if strings.Join(ok.Capabilities, ",") != strings.Join(protocol.Capabilities, ",") {
			t.Fatalf("capabilities = %v, want %v", ok.Capabilities, protocol.Capabilities)
		}
		id := protocol.NewID()
		c.send(&protocol.Message{ID: id, Type: protocol.TypePing})
		frames := c.collect(testTimeout, func(f protocol.Frame) bool {
			if f.Header.Type != protocol.FrameControl {
				return false
			}
			m, err := protocol.DecodeControl(f)
			return err == nil && m.Type == protocol.TypePong
		})
		if pong := lastControl(t, frames); pong.ID != id {
			t.Fatalf("pong id = %q, want %q", pong.ID, id)
		}
	})
}

func TestWorkerAttachReplayAndSnapshot(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf hello; sleep 30"})

	// The socket is the worker's own, owner-only.
	st, err := os.Stat(tw.sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %o, want 600", perm)
	}
	hs := tw.hs
	if hs.Protocol != protocol.Version {
		t.Fatalf("handshake protocol = %d", hs.Protocol)
	}
	if hs.Pid != os.Getpid() {
		t.Fatalf("handshake pid = %d, want the worker pid %d", hs.Pid, os.Getpid())
	}
	if hs.SessionID != testSessionID || hs.Socket != tw.sock || hs.Cols != 80 || hs.Rows != 24 {
		t.Fatalf("handshake = %+v", hs)
	}

	// A fresh attach has no retained state, so it is answered with a snapshot.
	a := dialWorker(t, tw.sock)
	a.hello()
	attached := a.attach(7, true, nil)
	if attached.Mode != protocol.ModeSnapshot {
		t.Fatalf("fresh attach mode = %q, want %q", attached.Mode, protocol.ModeSnapshot)
	}
	if attached.Generation != 0 || attached.Cols != 80 || attached.Rows != 24 || attached.Channel != 7 {
		t.Fatalf("session.attached = %+v", attached)
	}
	snap := a.readSnapshot()
	if snap.begin.Generation != 0 || snap.begin.Offset != attached.Offset || snap.begin.Cols != 80 || snap.begin.Rows != 24 {
		t.Fatalf("snapshot.begin = %+v, attached = %+v", snap.begin, attached)
	}
	// Live output resumes exactly at the captured offset, with no gap and no duplicate. The screen
	// may be empty at attach time, in which case "hello" arrives live instead.
	if text := a.waitText(snap.data, "hello"); !strings.Contains(string(text), "hello") {
		t.Fatalf("neither the snapshot nor the live output contains hello: %q", text)
	}

	// Input reaches the PTY master: the line discipline echoes it back as output.
	a.sendFrame(protocol.FrameInput, 7, 0, []byte("abc"))
	if text := a.readText("abc"); !strings.Contains(string(text), "abc") {
		t.Fatalf("input was not echoed by the pty: %q", text)
	}

	// A retained offset inside the ring and a matching generation is replayed, not snapshotted.
	b := dialWorker(t, tw.sock)
	b.hello()
	replay := b.attach(9, false, &protocol.RetainedState{Generation: attached.Generation, Offset: 0})
	if replay.Mode != protocol.ModeReplay {
		t.Fatalf("retained attach mode = %q, want %q", replay.Mode, protocol.ModeReplay)
	}
	if replay.Offset != 0 {
		t.Fatalf("replay offset = %d, want 0", replay.Offset)
	}
	frames := b.collect(testTimeout, func(f protocol.Frame) bool {
		return f.Header.Type == protocol.FrameOutput && f.Header.Seq == 0
	})
	first := frames[len(frames)-1]
	if !strings.HasPrefix(string(first.Payload), "hello") {
		t.Fatalf("replayed bytes = %q, want them to start with %q", first.Payload, "hello")
	}

	// A matching offset with a stale generation must not be replayed: the screen is not proof.
	c := dialWorker(t, tw.sock)
	c.hello()
	stale := c.attach(11, false, &protocol.RetainedState{Generation: attached.Generation + 1, Offset: 0})
	if stale.Mode != protocol.ModeSnapshot {
		t.Fatalf("stale-generation attach mode = %q, want %q", stale.Mode, protocol.ModeSnapshot)
	}
	snap2 := c.readSnapshot()
	if snap2.begin.Generation != attached.Generation {
		t.Fatalf("snapshot generation = %d, want %d", snap2.begin.Generation, attached.Generation)
	}
	if len(snap2.chunks) == 0 {
		t.Fatal("snapshot of a screen with contents has no chunks")
	}
	if !strings.Contains(string(snap2.data), "hello") {
		t.Fatalf("snapshot does not contain the screen contents: %q", snap2.data)
	}

	// One attachment per connection, and a session id must match this worker.
	if m := func() *protocol.Message {
		c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionAttach, SessionID: testSessionID, Channel: 12})
		return c.expectError()
	}(); m.Code != protocol.CodeConflict {
		t.Fatalf("second attach code = %q, want %q", m.Code, protocol.CodeConflict)
	}
	d := dialWorker(t, tw.sock)
	d.hello()
	d.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionAttach, SessionID: "other", Channel: 1})
	if m := d.expectError(); m.Code != protocol.CodeNotFound {
		t.Fatalf("foreign session code = %q, want %q", m.Code, protocol.CodeNotFound)
	}
}

func TestWorkerResizeOwnership(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf hello; sleep 30"})

	owner := dialWorker(t, tw.sock)
	owner.hello()
	ownerAttached := owner.attach(1, true, nil)
	ownerSnapshot := owner.readSnapshot()
	if ownerAttached.Generation != 0 {
		t.Fatalf("initial generation = %d", ownerAttached.Generation)
	}
	if text := owner.waitText(ownerSnapshot.data, "hello"); !strings.Contains(string(text), "hello") {
		t.Fatalf("the session never showed hello: %q", text)
	}

	guest := dialWorker(t, tw.sock)
	guest.hello()
	guestAttached := guest.attach(2, false, nil)
	guest.readSnapshot()
	if guestAttached.Generation != 0 {
		t.Fatalf("guest generation = %d", guestAttached.Generation)
	}

	// A non-owner resize is refused and must not touch the PTY.
	guest.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 100, Rows: 30})
	if m := guest.expectError(); m.Code != protocol.CodeForbidden {
		t.Fatalf("non-owner resize code = %q, want %q", m.Code, protocol.CodeForbidden)
	}

	// The owner's resize applies and bumps the generation, but it does not force a snapshot on the
	// clients that are already attached: their mirrors stay put until they need a resync.
	owner.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 100, Rows: 30})
	for name, tc := range map[string]*testConn{"owner": owner, "guest": guest} {
		for _, f := range tc.drainIdle(idleWindow) {
			if f.Header.Type != protocol.FrameControl {
				continue
			}
			if m := decodeControl(t, f); m.Type == protocol.TypeSnapshotBegin {
				t.Fatalf("%s: a real resize pushed a snapshot: %+v", name, m)
			}
		}
	}

	// The generation did move: an attach that presents the old generation gets a snapshot at the new
	// size, and the resize kept the screen contents.
	fresh := dialWorker(t, tw.sock)
	fresh.hello()
	stale := fresh.attach(3, false, &protocol.RetainedState{Generation: ownerAttached.Generation, Offset: 0})
	if stale.Mode != protocol.ModeSnapshot {
		t.Fatalf("stale-generation attach mode = %q, want %q", stale.Mode, protocol.ModeSnapshot)
	}
	resized := fresh.readSnapshot()
	if resized.begin.Generation != 1 || resized.begin.Cols != 100 || resized.begin.Rows != 30 {
		t.Fatalf("snapshot after the resize = %+v", resized.begin)
	}
	if text := fresh.waitText(resized.data, "hello"); !strings.Contains(string(text), "hello") {
		t.Fatalf("the resize threw the screen contents away: %q", text)
	}
	fresh.close()

	// A resize refreshes the handshake so a daemon can pick up the new size.
	hs, err := ReadHandshake(HandshakePath(tw.dir, testSessionID))
	if err != nil {
		t.Fatalf("read refreshed handshake: %v", err)
	}
	if hs.Cols != 100 || hs.Rows != 30 {
		t.Fatalf("handshake size = %dx%d, want 100x30", hs.Cols, hs.Rows)
	}

	// Every applied resize bumps the generation again.
	owner.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 60, Rows: 20})
	second := dialWorker(t, tw.sock)
	second.hello()
	secondAttached := second.attach(4, false, &protocol.RetainedState{Generation: resized.begin.Generation, Offset: 0})
	if secondAttached.Mode != protocol.ModeSnapshot {
		t.Fatalf("second resize attach mode = %q", secondAttached.Mode)
	}
	if snap := second.readSnapshot(); snap.begin.Generation != 2 || snap.begin.Cols != 60 || snap.begin.Rows != 20 {
		t.Fatalf("second resize snapshot = %+v", snap.begin)
	}
	second.close()

	// The lease is not transferred: after the owner detaches, nobody may resize.
	owner.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionDetach})
	owner.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 70, Rows: 21})
	if m := owner.expectError(); m.Code != protocol.CodeNotFound {
		t.Fatalf("resize after detach code = %q, want %q", m.Code, protocol.CodeNotFound)
	}
	guest.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 70, Rows: 21})
	if m := guest.expectError(); m.Code != protocol.CodeForbidden {
		t.Fatalf("resize after the owner detached code = %q, want %q", m.Code, protocol.CodeForbidden)
	}
}

// TestWorkerSameSizeResizeMovesTheLeaseOnly is K2: a resize to the size the PTY already has must
// take the lease and touch nothing else — no generation bump, no screen replacement, no snapshot.
func TestWorkerSameSizeResizeMovesTheLeaseOnly(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf hello; sleep 30"})

	owner := dialWorker(t, tw.sock)
	owner.hello()
	ownerAttached := owner.attach(1, true, nil)
	ownerSnap := owner.readSnapshot()
	_ = owner.waitText(ownerSnap.data, "hello")

	guest := dialWorker(t, tw.sock)
	guest.hello()
	guestAttached := guest.attach(2, false, nil)
	guest.readSnapshot()

	// Clear what the attaches left queued (the owner's own claim published a lease.changed too), so
	// what follows is only what the same-size resize produces.
	owner.drainIdle(idleWindow)
	guest.drainIdle(idleWindow)

	// The guest claims the lease with a resize to the size the PTY already has, the way the browser
	// claims on focus.
	guest.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 80, Rows: 24, ClaimLease: true})

	// Every attached client learns the lease moved, and nothing else arrives.
	for name, tc := range map[string]*testConn{"owner": owner, "guest": guest} {
		frames := tc.drainIdle(idleWindow)
		leases := 0
		sawOwner := false
		for _, f := range frames {
			if f.Header.Type != protocol.FrameControl {
				t.Fatalf("%s: same-size resize sent a %s frame", name, f.Header.Type)
			}
			m := decodeControl(t, f)
			switch m.Type {
			case protocol.TypeLeaseChanged:
				leases++
				sawOwner = m.Owner
				if m.ConnectionID == "" || m.Cols != 80 || m.Rows != 24 || m.Generation != guestAttached.Generation {
					t.Fatalf("%s: lease.changed = %+v", name, m)
				}
			default:
				t.Fatalf("%s: same-size resize sent %s", name, m.Type)
			}
		}
		if leases != 1 {
			t.Fatalf("%s: saw %d lease.changed after the claim, want 1", name, leases)
		}
		if want := name == "guest"; sawOwner != want {
			t.Fatalf("%s: lease.changed owner = %v, want %v", name, sawOwner, want)
		}
	}
	if ownerAttached.Generation != guestAttached.Generation {
		t.Fatalf("generations differ before the resize")
	}

	// The generation did not move and the screen was not replaced: a fresh attach at the same size
	// is still at the original generation.
	check := dialWorker(t, tw.sock)
	check.hello()
	if got := check.attach(3, false, nil); got.Generation != ownerAttached.Generation || got.Cols != 80 || got.Rows != 24 {
		t.Fatalf("generation moved on a same-size resize: %+v", got)
	}
	check.close()

	// The lease really did move: the guest may now resize, the former owner may not.
	owner.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 90, Rows: 25})
	if m := owner.expectError(); m.Code != protocol.CodeForbidden {
		t.Fatalf("former owner resize code = %q, want %q", m.Code, protocol.CodeForbidden)
	}
	guest.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 90, Rows: 25})
	// The guest is now the owner: its resize applies and bumps the generation. New output makes the
	// stale screen resynchronise (a real resize does not force a snapshot by itself).
	guest.sendFrame(protocol.FrameInput, 2, 0, []byte("z"))
	if snap := guest.readSnapshot(); snap.begin.Generation != ownerAttached.Generation+1 || snap.begin.Cols != 90 {
		t.Fatalf("guest resize snapshot = %+v", snap.begin)
	}
}

// TestWorkerResizeKeepsContent is the other half of K2: a real size change resizes the screen that
// is already there instead of building a blank one, so the contents survive.
func TestWorkerResizeKeepsContent(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf 'keep me'; read line; sleep 30"})

	c := dialWorker(t, tw.sock)
	c.hello()
	attached := c.attach(1, true, nil)
	snap := c.readSnapshot()
	if text := c.waitText(snap.data, "keep me"); !strings.Contains(string(text), "keep me") {
		t.Fatalf("the session never showed its output: %q", text)
	}

	c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 100, Rows: 30})
	// Nothing is pushed for the resize itself; new output makes the client resynchronise.
	c.sendFrame(protocol.FrameInput, 1, 0, []byte("\n"))
	resized := c.readSnapshot()
	if resized.begin.Generation != attached.Generation+1 || resized.begin.Cols != 100 || resized.begin.Rows != 30 {
		t.Fatalf("snapshot after the resize = %+v", resized.begin)
	}
	if text := c.waitText(resized.data, "keep me"); !strings.Contains(string(text), "keep me") {
		t.Fatalf("the resize blanked the screen: %q", text)
	}
}

// TestWorkerResizeDoesNotLeakGoroutines is K3: 200 resizes must not each leave an emulator and its
// response pump behind.
func TestWorkerResizeDoesNotLeakGoroutines(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf hi; sleep 30"})

	c := dialWorker(t, tw.sock)
	c.hello()
	c.attach(1, true, nil)
	c.readSnapshot()

	settle := func() int {
		before := runtime.NumGoroutine()
		for range 20 {
			time.Sleep(10 * time.Millisecond)
			now := runtime.NumGoroutine()
			if now == before {
				return now
			}
			before = now
		}
		return before
	}
	before := settle()

	// Alternate the size so every one of the 200 resizes is a real change.
	for i := range 200 {
		cols, rows := 80, 24
		if i%2 == 1 {
			cols, rows = 81, 25
		}
		c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: cols, Rows: rows})
	}
	// A ping is answered in order behind the 200 resizes, so the pong proves they were all applied.
	ping := protocol.NewID()
	c.send(&protocol.Message{ID: ping, Type: protocol.TypePing})
	c.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && m.Type == protocol.TypePong && m.ID == ping
	})

	after := settle()
	if after > before+20 {
		t.Fatalf("200 resizes grew the worker from %d to %d goroutines", before, after)
	}
}

func TestWorkerResizeAppliesToPTY(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "stty size; read line; stty size; sleep 30"})

	c := dialWorker(t, tw.sock)
	c.hello()
	attached := c.attach(1, true, nil)
	snap := c.readSnapshot()
	if text := c.waitText(snap.data, "24 80"); !strings.Contains(string(text), "24 80") {
		t.Fatalf("pty did not report its start size: %q", text)
	}

	c.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 100, Rows: 30})
	// The attach claim's lease.changed notice may arrive late; what a resize must not push is screen
	// data (a snapshot or output) at a client whose screen is still valid.
	var pushed []protocol.Frame
	for _, f := range c.drainIdle(idleWindow) {
		if m, err := protocol.DecodeControl(f); err == nil && m.Type == protocol.TypeLeaseChanged {
			continue
		}
		pushed = append(pushed, f)
	}
	if len(pushed) != 0 {
		t.Fatalf("a real resize pushed %d frames at the attached client", len(pushed))
	}

	// The size was applied to the PTY itself, not just recorded: stty reports the new geometry.
	c.sendFrame(protocol.FrameInput, 1, 0, []byte("x\n"))
	resized := c.readSnapshot()
	if resized.begin.Generation != attached.Generation+1 || resized.begin.Cols != 100 || resized.begin.Rows != 30 {
		t.Fatalf("resize snapshot = %+v", resized.begin)
	}
	if text := c.readText("30 100"); !strings.Contains(string(text), "30 100") {
		t.Fatalf("pty did not report the resized geometry: %q", text)
	}
}

func TestWorkerFlowControlPauseResume(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", bigOutput})

	c := dialWorker(t, tw.sock)
	c.hello()
	attached := c.attach(1, true, nil)
	c.readSnapshot()

	// Read and never acknowledge: the client is paused once it has 256 KiB in flight.
	next := attached.Offset
	var received uint64
	read := func(frames []protocol.Frame) {
		for _, f := range frames {
			switch f.Header.Type {
			case protocol.FrameOutput:
				if f.Header.Seq != next {
					t.Fatalf("output seq = %d, want %d (no gap, no duplicate)", f.Header.Seq, next)
				}
				next += uint64(len(f.Payload))
				received += uint64(len(f.Payload))
			case protocol.FrameControl:
				m := decodeControl(t, f)
				if m.Type == protocol.TypeError {
					t.Fatalf("worker error: %s: %s", m.Code, m.Message)
				}
			}
		}
	}

	read(c.drainIdle(idleWindow))
	if received < highWaterBytes {
		t.Fatalf("paused after %d bytes, want at least the %d byte high-water mark", received, highWaterBytes)
	}
	// The agent produced far more than this; a non-paused writer would have delivered it all.
	const totalOutput = 600000
	if received >= totalOutput {
		t.Fatalf("client received %d bytes without acknowledging; flow control did not pause it", received)
	}
	pausedAt := received
	t.Logf("paused at %d bytes", pausedAt)

	// Acknowledging the accepted bytes resumes the stream, and it continues without a gap.
	const maxRounds = 10
	for round := range maxRounds {
		c.sendFrame(protocol.FrameAck, 1, next, nil)
		before := received
		read(c.drainIdle(idleWindow))
		if received == before {
			break
		}
		t.Logf("round %d: %d bytes", round, received)
	}
	if received < totalOutput {
		t.Fatalf("received %d bytes after resume rounds, want all %d", received, totalOutput)
	}
	if received == pausedAt {
		t.Fatalf("ack-driven resume never delivered more than the first %d bytes", pausedAt)
	}
}

func TestWorkerPausedClientFallsOutOfRing(t *testing.T) {
	// A 64 KiB ring cannot hold what a paused client still owes itself, so resuming it must send a
	// snapshot rather than a replay with a hole in it (docs/protocol/v1.md section 5).
	// After the burst the program keeps printing, so live output exists to check after the resync
	// however fast the burst drained while the client was paused.
	trickle := strings.Replace(bigOutput, "sleep 30", "while :; do printf '0123456789\\n'; sleep 0.2; done", 1)
	tw := startWorkerRing(t, []string{"/bin/sh", "-c", trickle}, 64<<10)

	c := dialWorker(t, tw.sock)
	c.hello()
	attached := c.attach(1, true, nil)
	c.readSnapshot()

	next := attached.Offset
	var received uint64
	for _, f := range c.drainIdle(idleWindow) {
		if f.Header.Type != protocol.FrameOutput {
			continue
		}
		if f.Header.Seq != next {
			t.Fatalf("output seq = %d, want %d", f.Header.Seq, next)
		}
		next += uint64(len(f.Payload))
		received += uint64(len(f.Payload))
	}
	if received < highWaterBytes || received >= 600000 {
		t.Fatalf("flow control did not pause a %d byte client", received)
	}

	// Acknowledging resumes the client, whose pending offset has long fallen out of the ring.
	c.sendFrame(protocol.FrameAck, 1, next, nil)
	snap := c.readSnapshot()
	if snap.begin.Generation != attached.Generation {
		t.Fatalf("resync generation = %d, want %d", snap.begin.Generation, attached.Generation)
	}
	if snap.begin.Offset <= next {
		t.Fatalf("resync offset = %d, want a fresh offset past the client's %d", snap.begin.Offset, next)
	}
	// Live output continues from the snapshot offset, with no gap and no duplicate.
	if text := c.readText("0123456789"); !strings.Contains(string(text), "0123456789") {
		t.Fatalf("live output after the resync is wrong: %q", text)
	}
}

func TestWorkerChildExitAndCleanup(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf hello; read line; exit 7"})

	c := dialWorker(t, tw.sock)
	c.hello()
	c.attach(1, true, nil)
	snap := c.readSnapshot()
	if text := c.waitText(snap.data, "hello"); !strings.Contains(string(text), "hello") {
		t.Fatalf("neither the snapshot nor the live output contains hello: %q", text)
	}

	// The child reads one line from the PTY and exits with code 7.
	c.sendFrame(protocol.FrameInput, 1, 0, []byte("go\n"))
	frames := c.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && m.Type == protocol.TypeSessionExit
	})
	exit := lastControl(t, frames)
	if exit.Type != protocol.TypeSessionExit {
		t.Fatalf("got %s, want session.exit", exit.Type)
	}
	if exit.ExitCode == nil || *exit.ExitCode != 7 {
		t.Fatalf("session.exit = %+v, want exit code 7", exit)
	}
	if exit.SessionID != testSessionID {
		t.Fatalf("session.exit session id = %q, want %q", exit.SessionID, testSessionID)
	}
	if _, err := time.Parse(time.RFC3339, exit.At); err != nil {
		t.Fatalf("session.exit at = %q: %v", exit.At, err)
	}
	if exit.ID == "" {
		t.Fatal("session.exit has no message id")
	}

	// The worker keeps serving after the child is gone: it stops only once the last client detaches.
	select {
	case err := <-tw.errCh:
		t.Fatalf("worker returned before its client detached: %v", err)
	default:
	}

	c.close()
	if err := tw.waitReturn(); err != nil {
		t.Fatalf("RunWorker = %v, want nil", err)
	}
	if _, err := os.Stat(tw.sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket survived the worker: %v", err)
	}
	if conn, err := net.Dial("unix", tw.sock); err == nil {
		conn.Close()
		t.Fatal("socket still accepts connections")
	}
	// The handshake stays behind with the exit code, so a daemon that was not supervising this
	// worker records exited(7) instead of lost; the daemon removes it once it has done so.
	hs, err := ReadHandshake(HandshakePath(tw.dir, testSessionID))
	if err != nil {
		t.Fatalf("the handshake did not survive the worker: %v", err)
	}
	if hs.ExitCode == nil || *hs.ExitCode != 7 {
		t.Fatalf("handshake exit code = %v, want 7", hs.ExitCode)
	}
	if hs.EndedAt.IsZero() {
		t.Fatal("handshake has no end time")
	}
	if hs.WorkerStartedAt.IsZero() {
		t.Fatal("handshake has no worker start time")
	}
}

// TestWorkerSignalExitCode is K9: a child killed by a signal has no exit status, and a client must
// still learn how it ended: 128+signal.
func TestWorkerSignalExitCode(t *testing.T) {
	const want = 128 + int(syscall.SIGTERM)
	// The agent waits for a line, then kills itself: a signal death, not an exit status.
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf up; read line; kill -TERM $$; sleep 30"})

	c := dialWorker(t, tw.sock)
	c.hello()
	c.attach(1, true, nil)
	snap := c.readSnapshot()
	if text := c.waitText(snap.data, "up"); !strings.Contains(string(text), "up") {
		t.Fatalf("the session never started: %q", text)
	}
	c.sendFrame(protocol.FrameInput, 1, 0, []byte("go\n"))

	frames := c.collect(testTimeout, func(f protocol.Frame) bool {
		if f.Header.Type != protocol.FrameControl {
			return false
		}
		m, err := protocol.DecodeControl(f)
		return err == nil && m.Type == protocol.TypeSessionExit
	})
	exit := lastControl(t, frames)
	if exit.ExitCode == nil || *exit.ExitCode != want {
		t.Fatalf("session.exit = %+v, want the signal exit code %d", exit, want)
	}

	// Once the client is gone the worker stops, and the handshake it leaves behind carries the same
	// code for a daemon that was not supervising it.
	c.close()
	if err := tw.waitReturn(); err != nil {
		t.Fatalf("RunWorker = %v, want nil", err)
	}
	hs, err := ReadHandshake(HandshakePath(tw.dir, testSessionID))
	if err != nil {
		t.Fatalf("the handshake did not survive the worker: %v", err)
	}
	if hs.ExitCode == nil || *hs.ExitCode != want {
		t.Fatalf("handshake exit code = %v, want %d", hs.ExitCode, want)
	}
}

// TestWorkerLeaseChangedReachesEveryAttachedClient is the worker half of R1 and K10: every lease
// change — a claim, a move, an owner detaching — is published to every attached client, naming the
// holder, with the holder's own copy flagged.
func TestWorkerLeaseChangedReachesEveryAttachedClient(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "sleep 30"})

	owner := dialWorker(t, tw.sock)
	owner.hello()
	ownerAttached := owner.attach(1, true, nil)
	owner.readSnapshot()
	// The claimer's own copy says owner.
	m := owner.expectControl(protocol.TypeLeaseChanged)
	if !m.Owner || m.ConnectionID == "" || m.SessionID != testSessionID {
		t.Fatalf("the claimer's lease.changed = %+v", m)
	}
	if m.Cols != 80 || m.Rows != 24 || m.Generation != ownerAttached.Generation {
		t.Fatalf("the claimer's lease.changed carries %dx%d generation %d", m.Cols, m.Rows, m.Generation)
	}

	guest := dialWorker(t, tw.sock)
	guest.hello()
	guest.attach(2, false, nil)
	guest.readSnapshot()
	// A non-claiming attach changes nothing, so it publishes nothing.
	if frames := guest.drainIdle(idleWindow); len(frames) != 0 {
		t.Fatalf("a non-claiming attach published %d frames", len(frames))
	}

	// The guest claims: the lease moves, and both attached clients are told, each with its own copy
	// of the owner flag.
	guest.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 80, Rows: 24, ClaimLease: true})
	holder := owner.expectControl(protocol.TypeLeaseChanged)
	if holder.Owner || holder.ConnectionID == "" {
		t.Fatalf("the former holder's lease.changed = %+v", holder)
	}
	mine := guest.expectControl(protocol.TypeLeaseChanged)
	if !mine.Owner || mine.ConnectionID != holder.ConnectionID {
		t.Fatalf("the new holder's lease.changed = %+v, want connection_id %q and owner", mine, holder.ConnectionID)
	}

	// The owner detaches: the remaining attached clients learn the lease is free.
	guest.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionDetach})
	free := owner.expectControl(protocol.TypeLeaseChanged)
	if free.Owner || free.ConnectionID != "" || free.SessionID != testSessionID {
		t.Fatalf("lease.changed after the holder detached = %+v", free)
	}
	if free.Generation != ownerAttached.Generation {
		t.Fatalf("lease.changed generation = %d, want %d (no resize happened)", free.Generation, ownerAttached.Generation)
	}
}

// TestWorkerIOClientIsViewOnly is R16: a client whose attach says kind "ios" never gets the lease
// and its input is refused with forbidden.
func TestWorkerIOClientIsViewOnly(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "printf hello; read line; sleep 30"})

	phone := dialWorker(t, tw.sock)
	phone.hello()
	attached := phone.attachRequest(&protocol.Message{
		ID: protocol.NewID(), Type: protocol.TypeSessionAttach, SessionID: testSessionID,
		Channel: 1, ClaimLease: true, Cols: 120, Rows: 40, Kind: string(protocol.KindIOS),
	})
	if attached.Cols != 80 || attached.Rows != 24 {
		t.Fatalf("an ios claim resized the PTY to %dx%d", attached.Cols, attached.Rows)
	}
	phone.readSnapshot()

	// Its input is refused and never reaches the PTY: the shell would have echoed it.
	phone.sendFrame(protocol.FrameInput, 1, 0, []byte("echo PHONE\n"))
	if m := phone.expectError(); m.Code != protocol.CodeForbidden {
		t.Fatalf("ios input code = %q, want %q", m.Code, protocol.CodeForbidden)
	}
	for _, f := range phone.drainIdle(idleWindow) {
		if f.Header.Type == protocol.FrameOutput && strings.Contains(string(f.Payload), "PHONE") {
			t.Fatalf("refused input reached the PTY: %q", f.Payload)
		}
	}

	// It never got the lease: a resize that claims it is refused.
	phone.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, Cols: 100, Rows: 30, ClaimLease: true})
	if m := phone.expectError(); m.Code != protocol.CodeForbidden {
		t.Fatalf("ios resize code = %q, want %q", m.Code, protocol.CodeForbidden)
	}

	// A real client's claim still works, and the phone learns the lease went elsewhere.
	cli := dialWorker(t, tw.sock)
	cli.hello()
	cli.attach(2, true, nil)
	cli.readSnapshot()
	notice := phone.expectControl(protocol.TypeLeaseChanged)
	if notice.Owner || notice.ConnectionID == "" {
		t.Fatalf("the phone's lease.changed = %+v, want a named holder and no owner flag", notice)
	}
}

// TestWorkerAttachClaimAppliesSize is R6: a claim that carries a different geometry applies it to
// the PTY before the snapshot.
func TestWorkerAttachClaimAppliesSize(t *testing.T) {
	tw := startWorker(t, []string{"/bin/sh", "-c", "stty size; read line; stty size; sleep 30"})

	c := dialWorker(t, tw.sock)
	c.hello()
	attached := c.attachRequest(&protocol.Message{
		ID: protocol.NewID(), Type: protocol.TypeSessionAttach, SessionID: testSessionID,
		Channel: 1, ClaimLease: true, Cols: 132, Rows: 40,
	})
	if attached.Cols != 132 || attached.Rows != 40 {
		t.Fatalf("session.attached reports %dx%d, want 132x40", attached.Cols, attached.Rows)
	}
	c.readSnapshot()

	// The size reached the PTY itself, not just the reply.
	c.sendFrame(protocol.FrameInput, 1, 0, []byte("x\n"))
	if text := c.readText("40 132"); !strings.Contains(string(text), "40 132") {
		t.Fatalf("the PTY did not take the claimed size: %q", text)
	}
}

// TestWorkerExitArrivesAfterTheLastOwedByte is R5: session.exit is queued through the same path as
// output, so nothing a client is owed can arrive after it.
func TestWorkerExitArrivesAfterTheLastOwedByte(t *testing.T) {
	// The agent prints ~615 KiB and then exits, without ever reading stdin.
	tw := startWorker(t, []string{"/bin/sh", "-c", `i=0; while [ $i -lt 15000 ]; do printf '0123456789012345678901234567890123456789\n'; i=$((i+1)); done`})

	c := dialWorker(t, tw.sock)
	c.hello()
	attached := c.attach(1, true, nil)
	c.readSnapshot()

	next := attached.Offset
	var received uint64
	sawExit := false
	outputAfterExit := false
	read := func(frames []protocol.Frame) {
		for _, f := range frames {
			switch f.Header.Type {
			case protocol.FrameOutput:
				if f.Header.Seq != next {
					t.Fatalf("output seq = %d, want %d", f.Header.Seq, next)
				}
				next += uint64(len(f.Payload))
				received += uint64(len(f.Payload))
				if sawExit {
					outputAfterExit = true
				}
			case protocol.FrameControl:
				m := decodeControl(t, f)
				switch m.Type {
				case protocol.TypeSessionExit:
					if m.ExitCode == nil || *m.ExitCode != 0 {
						t.Fatalf("session.exit = %+v, want exit code 0", m)
					}
					sawExit = true
				case protocol.TypeError:
					t.Fatalf("worker error: %s: %s", m.Code, m.Message)
				}
			}
		}
	}

	// First read without acknowledging: flow control pauses the client while the agent is still
	// owed, which is the state in which the exit used to overtake the last bytes.
	read(c.drainIdle(idleWindow))
	if received < highWaterBytes {
		t.Fatalf("paused after %d bytes, want at least the high-water mark", received)
	}
	if received >= 600000 {
		t.Fatalf("received all %d bytes without acknowledging; the client was never paused", received)
	}
	if sawExit {
		t.Fatal("session.exit arrived before the bytes the client was still owed")
	}

	// Acknowledging drains the rest, and the exit arrives with it, last.
	for round := 0; round < 40 && !sawExit; round++ {
		c.sendFrame(protocol.FrameAck, 1, next, nil)
		read(c.drainIdle(idleWindow))
	}
	if !sawExit {
		t.Fatal("session.exit never arrived")
	}
	if outputAfterExit {
		t.Fatal("output arrived after session.exit")
	}
	if received < 600000 {
		t.Fatalf("received %d bytes, want the whole %d byte output before the exit", received, 600000)
	}
}

// TestWorkerReadLoopSurvivesABlockedPaste is R3: a client pastes more than the PTY can absorb while
// the agent does not read stdin, and the read loop must still deliver the agent's later output.
// Before the fix the read loop wrote the mirror's query answer to the same PTY master the paste
// write was blocked on, so it stopped reading and TAILMARK never arrived.
func TestWorkerReadLoopSurvivesABlockedPaste(t *testing.T) {
	// stty -echo: an agent runs its terminal raw, with no echo. A Linux tty in cooked mode echoes the
	// whole paste back and stalls the child's own output behind it, which is the kernel's and not the
	// worker's read loop that this test is about.
	tw := startWorker(t, []string{"/bin/sh", "-c", `stty -echo; printf '\033[c'; sleep 1; printf TAILMARK; sleep 30`})

	c := dialWorker(t, tw.sock)
	c.hello()
	c.attach(1, false, nil) // no lease holder: the mirror is the responder
	c.readSnapshot()

	// Paste 512 KiB from its own goroutine: the worker's socket write back-pressure must not hang
	// the test harness itself.
	paste := bytes.Repeat([]byte("x"), 512<<10)
	go func() {
		for off := 0; off < len(paste); off += maxOutputPayload {
			end := min(off+maxOutputPayload, len(paste))
			hdr := protocol.Header{Type: protocol.FrameInput, Channel: 1, Length: uint32(end - off)}
			if _, err := c.conn.Write(append(hdr.MarshalBinary(), paste[off:end]...)); err != nil {
				return
			}
		}
	}()

	if text := c.readText("TAILMARK"); !strings.Contains(string(text), "TAILMARK") {
		t.Fatalf("the read loop never delivered the agent's output: %q", text)
	}
}

// TestWorkerMirrorAnswersOnlyWithoutClients is the worker half of R1: with no client attached the
// mirror answers a program query so the agent is never left waiting, and the moment a client is
// attached the mirror goes silent so the answer cannot arrive twice.
func TestWorkerMirrorAnswersOnlyWithoutClients(t *testing.T) {
	// The agent waits for the client to be in place, asks the terminal what it is, and records in a
	// file whether an answer came back.
	const ask = `sleep 1; stty -icanon min 1 -echo; printf '\033[c'; head -c 1 >/dev/null && printf answered > answered.txt; sleep 30`
	answered := func(t *testing.T, tw *testWorker) bool {
		t.Helper()
		_, err := os.Stat(filepath.Join(tw.dir, "answered.txt"))
		return err == nil
	}

	t.Run("no client attached", func(t *testing.T) {
		tw := startWorker(t, []string{"/bin/sh", "-c", ask})
		deadline := time.Now().Add(testTimeout)
		for !answered(t, tw) {
			if time.Now().After(deadline) {
				t.Fatal("the mirror never answered while nothing was attached")
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	t.Run("client attached", func(t *testing.T) {
		tw := startWorker(t, []string{"/bin/sh", "-c", ask})
		c := dialWorker(t, tw.sock)
		c.hello()
		c.attach(1, false, nil)
		c.readSnapshot()
		// The agent is about to ask, and a client is attached: the mirror must stay silent, and
		// nothing in this test answers instead.
		time.Sleep(2 * time.Second)
		if answered(t, tw) {
			t.Fatal("the mirror answered while a client was attached")
		}
	})
}

// TestWorkerQueueCapClosesClient is R4: the per-client queue has a byte cap, and a client that
// would grow past it is closed instead of growing the worker. The pause rule keeps an ordinary
// non-acknowledging client far below the cap (TestWorkerFlowControlPauseResume); this covers the
// backstop itself.
func TestWorkerQueueCapClosesClient(t *testing.T) {
	w := &worker{spec: LaunchSpec{SessionID: testSessionID}, logf: t.Logf}
	conn, peer := net.Pipe()
	defer peer.Close()
	c := &client{w: w, conn: conn, id: "capped", closed: make(chan struct{})}
	c.outCond = sync.NewCond(&c.outMu)

	for range maxQueuedBytes/(64<<10) + 4 {
		c.enqueue(outFrame{typ: protocol.FrameOutput, channel: 1, payload: make([]byte, 64<<10)})
		if c.queued > maxQueuedBytes {
			t.Fatalf("the queue grew to %d bytes past the cap of %d", c.queued, maxQueuedBytes)
		}
		if c.isClosed() {
			if c.queued > maxQueuedBytes {
				t.Fatalf("closed with %d bytes queued, past the cap", c.queued)
			}
			return
		}
	}
	t.Fatalf("the client was never closed after %d bytes", c.queued)
}

// TestClientAckIsClampedToWhatWasWritten is the other half of R4: an ack can only name bytes the
// writer has actually put on the socket, so a client cannot claim its way out of flow control.
func TestClientAckIsClampedToWhatWasWritten(t *testing.T) {
	w := &worker{spec: LaunchSpec{SessionID: testSessionID}, logf: t.Logf}
	c := &client{
		w:        w,
		id:       "acker",
		channel:  1,
		attached: true,
		sent:     1000,
		wake:     make(chan struct{}, 1),
		closed:   make(chan struct{}),
	}
	c.outMu.Lock()
	c.written = 400
	c.outMu.Unlock()

	ack := func(seq uint64) {
		c.ack(protocol.Frame{Header: protocol.Header{Type: protocol.FrameAck, Channel: 1, Seq: seq}})
	}
	ack(1 << 40)
	if c.acked != 400 {
		t.Fatalf("acked = %d, want it clamped to the 400 bytes actually written", c.acked)
	}
	// A paused client with that much still owed stays paused: the clamp cannot fake a drain.
	c.sent = 400 + highWaterBytes
	c.paused = true
	ack(1 << 40)
	if !c.paused {
		t.Fatal("a lying ack resumed a paused client")
	}
	// An honest ack below the low-water mark does resume it.
	c.sent = 400 + lowWaterBytes/2
	ack(400)
	if c.paused {
		t.Fatal("an honest ack did not resume the client")
	}
}

// TestClientSnapshotKeepsThePauseWhileFramesAreQueued is the third half of R4: a snapshot restarts
// the byte accounting at its capture offset, but it must not unpause a client whose queue still
// holds unwritten frames — that is how a non-reading client grew to megabytes.
func TestClientSnapshotKeepsThePauseWhileFramesAreQueued(t *testing.T) {
	w := &worker{spec: LaunchSpec{SessionID: testSessionID}, logf: t.Logf}
	conn, peer := net.Pipe()
	defer peer.Close()
	c := &client{w: w, conn: conn, id: "behind", channel: 1, closed: make(chan struct{})}
	c.outCond = sync.NewCond(&c.outMu)
	c.paused = true
	c.enqueue(outFrame{typ: protocol.FrameOutput, channel: 1, payload: []byte("still queued")})

	c.sendSnapshot(2, 80, 24, []byte("screen"), 1000)
	if !c.paused {
		t.Fatal("a snapshot cleared the pause with frames still queued")
	}
	if c.gen != 2 || c.sent != 1000 || c.acked != 1000 {
		t.Fatalf("snapshot state = gen %d sent %d acked %d", c.gen, c.sent, c.acked)
	}

	// With the queue empty it may clear the pause.
	c.outMu.Lock()
	c.out = nil
	c.queued = 0
	c.outMu.Unlock()
	c.sendSnapshot(3, 80, 24, nil, 2000)
	if c.paused {
		t.Fatal("a snapshot did not clear the pause of a client with an empty queue")
	}
}

func TestWorkerSpecFromFD(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "run", "sess-fd.sock")
	spec := LaunchSpec{
		SessionID:  "sess-fd",
		Dir:        dir,
		Command:    []string{"/bin/sh", "-c", "printf fd; sleep 30"},
		Cols:       100,
		Rows:       40,
		SocketPath: sock,
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(raw); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	// RunWorker owns and closes the descriptor it reads, so hand it a duplicate.
	fd, err := syscall.Dup(int(pr.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	pr.Close()
	if fd < 3 {
		t.Fatalf("dup returned fd %d, the worker needs >= 3", fd)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunWorker(ctx, WorkerConfig{Spec: LaunchSpec{}, SpecFD: fd, Logf: t.Logf}) }()
	// Whatever happens below, the worker is stopped and drained before the test ends: a worker that
	// logs after its test finished panics the whole binary.
	returned := false
	t.Cleanup(func() {
		cancel()
		if returned {
			return
		}
		select {
		case <-errCh:
		case <-time.After(testTimeout):
			t.Error("worker did not return after cancellation")
		}
	})

	deadline := time.Now().Add(testTimeout)
	var hs Handshake
	for {
		hs, err = ReadHandshake(HandshakePath(dir, "sess-fd"))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no handshake from the spec on fd %d: %v", fd, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hs.SessionID != "sess-fd" || hs.Cols != 100 || hs.Rows != 40 || hs.Socket != sock || hs.Command[0] != "/bin/sh" {
		t.Fatalf("handshake = %+v", hs)
	}

	c := dialWorker(t, sock)
	c.sessionID = "sess-fd"
	c.hello()
	attached := c.attach(1, true, nil)
	if attached.Cols != 100 || attached.Rows != 40 {
		t.Fatalf("session.attached = %+v", attached)
	}
	snap := c.readSnapshot()
	if text := c.waitText(snap.data, "fd"); !strings.Contains(string(text), "fd") {
		t.Fatalf("pty output = %q", text)
	}
	c.close()
	cancel()
	select {
	case err := <-errCh:
		returned = true
		if err == nil {
			t.Fatal("RunWorker = nil after cancellation, want the cancellation cause")
		}
	case <-time.After(testTimeout):
		t.Fatal("worker did not return after cancellation")
	}
}
