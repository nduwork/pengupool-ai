package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

const testSessionID = "01900000-0000-7000-8000-000000000001"

// shortTempDir returns a temp directory whose path fits a unix socket: sun_path is 104 bytes on
// darwin, and t.TempDir() embeds the test name in the path.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pp2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// fakeDaemon is the daemon side of an attach test: it completes the hello handshake, answers
// session.list, answers session.attach, and then records every input frame and ack the client sends
// while a test script drives the frames that follow.
type fakeDaemon struct {
	t   *testing.T
	ln  net.Listener
	srv net.Conn

	attach      chan *protocol.Message
	afterAttach func(d *fakeDaemon)

	lastAck atomic.Uint64
	resizes atomic.Int64
	inMu    sync.Mutex
	in      []byte

	wmu sync.Mutex
}

func newFakeDaemon(t *testing.T, dir string) *fakeDaemon {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "run", "hub.sock"))
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{t: t, ln: ln, attach: make(chan *protocol.Message, 4)}
	go d.serve()
	t.Cleanup(func() {
		ln.Close()
		d.close()
	})
	return d
}

func (d *fakeDaemon) close() {
	d.wmu.Lock()
	if d.srv != nil {
		d.srv.Close()
		d.srv = nil
	}
	d.wmu.Unlock()
}

func (d *fakeDaemon) send(m *protocol.Message) {
	if m.ID == "" {
		m.ID = protocol.NewID()
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if d.srv == nil {
		return
	}
	if err := protocol.WriteControl(d.srv, m); err != nil {
		d.t.Logf("fake daemon: control write: %v", err)
	}
}

func (d *fakeDaemon) sendFrame(f protocol.Frame) {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if d.srv == nil {
		return
	}
	if err := protocol.WriteFrame(d.srv, f); err != nil {
		d.t.Logf("fake daemon: frame write: %v", err)
	}
}

func (d *fakeDaemon) serve() {
	conn, err := d.ln.Accept()
	if err != nil {
		return
	}
	d.wmu.Lock()
	d.srv = conn
	d.wmu.Unlock()
	for {
		f, err := protocol.ReadFrame(conn)
		if err != nil {
			return
		}
		switch f.Header.Type {
		case protocol.FrameControl:
			m, err := protocol.DecodeControl(f)
			if err != nil {
				return
			}
			switch m.Type {
			case protocol.TypeHello:
				d.send(&protocol.Message{ID: m.ID, Type: protocol.TypeHelloOK, V: protocol.Version, ConnectionID: "fake", Capabilities: protocol.Capabilities})
			case protocol.TypeSessionList:
				d.send(&protocol.Message{ID: m.ID, Type: protocol.TypeSessionList,
					Sessions: []protocol.SessionView{{ID: testSessionID, Name: "smoke", State: "running", Cols: 80, Rows: 24}}})
			case protocol.TypeSessionAttach:
				d.send(&protocol.Message{ID: m.ID, Type: protocol.TypeSessionAttached, SessionID: testSessionID,
					Channel: m.Channel, Generation: 1, Cols: 80, Rows: 24, Mode: protocol.ModeReplay})
				d.attach <- m
				if d.afterAttach != nil {
					go d.afterAttach(d)
				}
			case protocol.TypeSessionResize:
				d.resizes.Add(1)
			}
		case protocol.FrameAck:
			d.lastAck.Store(f.Header.Seq)
		case protocol.FrameInput:
			d.inMu.Lock()
			d.in = append(d.in, f.Payload...)
			d.inMu.Unlock()
		}
	}
}

// waitAck blocks until the client has acknowledged at least seq. It reports a failure rather than
// blocking forever, so a client that never acks fails the test instead of hanging it.
func (d *fakeDaemon) waitAck(seq uint64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.lastAck.Load() >= seq {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	d.t.Errorf("the attach client never acknowledged byte %d (last ack %d)", seq, d.lastAck.Load())
	return false
}

func (d *fakeDaemon) inputSeen(sub string) bool {
	d.inMu.Lock()
	defer d.inMu.Unlock()
	return bytes.Contains(d.in, []byte(sub))
}

// attachClient is a cmdAttach running against a real pty with a fake daemon on the store socket.
type attachClient struct {
	t    *testing.T
	d    *fakeDaemon
	ptmx *os.File
	out  *syncBuffer
	err  error
	fin  chan struct{}
}

func startAttach(t *testing.T, d *fakeDaemon, dir string, args ...string) *attachClient {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = tty, tty, devnull

	out := &syncBuffer{}
	go readAll(ptmx, out)

	h := &attachClient{t: t, d: d, ptmx: ptmx, out: out, fin: make(chan struct{})}
	go func() {
		h.err = cmdAttach(append([]string{"--store", dir}, args...))
		close(h.fin)
	}()

	t.Cleanup(func() {
		d.close() // unblock a client still waiting on its daemon
		select {
		case <-h.fin:
		case <-time.After(2 * time.Second):
		}
		// Close the client's terminal before the process-wide stdio is put back, so a goroutine
		// the client left blocked in a read fails there instead of picking up the restored global.
		ptmx.Close()
		tty.Close()
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
		devnull.Close()
	})
	return h
}

// detached waits for cmdAttach to return and fails the test if it does not.
func (h *attachClient) detached(timeout time.Duration) error {
	h.t.Helper()
	select {
	case <-h.fin:
		return h.err
	case <-time.After(timeout):
		h.t.Fatalf("the attach client did not exit within %s", timeout)
		return nil
	}
}

// syncBuffer is a bytes.Buffer safe for a reader goroutine and an asserting test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b.Bytes()...)
}

func readAll(f *os.File, w *syncBuffer) {
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// sigwinch delivers SIGWINCH to this process, the way a window resize would.
func sigwinch() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		panic(err)
	}
}

// TestAttachAcksLargeOutput drives a >300 KiB session through the client. The fake daemon behaves
// like the real worker's flow control: it refuses to send past its 256 KiB unacknowledged
// high-water mark until the client has acknowledged what it wrote. Without the client's batched
// acks the session would freeze and the test would time out (review K1).
func TestAttachAcksLargeOutput(t *testing.T) {
	dir := shortTempDir(t)
	d := newFakeDaemon(t, dir)

	const total = 448 * 1024 // well past the 256 KiB pause point
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}

	d.afterAttach = func(d *fakeDaemon) {
		const chunk = 64 * 1024
		sent := 0
		for sent < total {
			n := chunk
			if total-sent < n {
				n = total - sent
			}
			d.sendFrame(protocol.Frame{
				Header:  protocol.Header{Type: protocol.FrameOutput, Channel: 1, Seq: uint64(sent), Length: uint32(n)},
				Payload: payload[sent : sent+n],
			})
			sent += n
			if sent == 256*1024 {
				// The real worker pauses here; nothing more is sent until the client acks.
				if !d.waitAck(256*1024, 5*time.Second) {
					return
				}
			}
		}
	}

	h := startAttach(t, d, dir, "smoke")
	select {
	case <-d.attach:
	case <-time.After(5 * time.Second):
		t.Fatal("the client never sent session.attach")
	}

	waitFor(t, 15*time.Second, "the whole session written to the local terminal", func() bool {
		return len(h.out.Bytes()) >= total
	})
	if got := h.out.Bytes()[:total]; !bytes.Equal(got, payload) {
		t.Fatalf("the terminal received %d bytes that do not match the session output", total)
	}
	waitFor(t, 5*time.Second, "the ack for the final byte", func() bool {
		return d.lastAck.Load() >= total
	})

	// While it holds the lease this client is the responder: its terminal's query replies pass
	// through unchanged, like any other key.
	if _, err := h.ptmx.Write([]byte("\x1b[?1;2c")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ptmx.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the lease holder's query reply to be forwarded", func() bool {
		return d.inputSeen("\x1b[?1;2c")
	})
	if !d.inputSeen("hi") {
		t.Fatal("plain input was not forwarded while holding the lease")
	}

	// Ctrl-\ detaches; the session keeps running.
	if _, err := h.ptmx.Write([]byte{0x1c}); err != nil {
		t.Fatal(err)
	}
	if err := h.detached(5 * time.Second); err != nil {
		t.Fatalf("attach returned %v after the detach key, want nil", err)
	}
}

// TestAttachDropsQueryRepliesWithoutLease covers K10 and the CLI half of R1: a client that does not
// hold the lease neither resizes nor forwards its own terminal's query replies, and lease.changed
// hands the responder role back.
func TestAttachDropsQueryRepliesWithoutLease(t *testing.T) {
	dir := shortTempDir(t)
	d := newFakeDaemon(t, dir)

	h := startAttach(t, d, dir, "--no-claim", "smoke")
	select {
	case m := <-d.attach:
		if m.ClaimLease {
			t.Fatal("--no-claim still claimed the lease")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client never sent session.attach")
	}

	// The worker names the holder: another client owns the lease.
	d.send(&protocol.Message{Type: protocol.TypeLeaseChanged, SessionID: testSessionID,
		ConnectionID: "someone-else", Owner: false, Cols: 80, Rows: 24, Generation: 1})

	replies := []string{
		"\x1b[?1;2c",     // DA1
		"\x1b[>0;95;0c",  // DA2
		"\x1b[?25;1$y",   // DECRQM reply
		"\x1b[10;20R",    // cursor position report
		"\x1b[I",         // focus in
		"\x1b[O",         // focus out
		"\x1b[48;24;80t", // in-band resize report
	}
	for _, r := range replies {
		if _, err := h.ptmx.Write([]byte(r)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.ptmx.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "ordinary input to be forwarded", func() bool { return d.inputSeen("hi") })
	for _, r := range replies {
		if d.inputSeen(r) {
			t.Fatalf("a non-owner forwarded its terminal's reply %q into the session", r)
		}
	}

	// A non-owner does not resize the PTY, so its SIGWINCH must not even produce a request.
	sigwinch()
	time.Sleep(150 * time.Millisecond)
	if n := d.resizes.Load(); n != 0 {
		t.Fatalf("a non-owner sent %d resize request(s)", n)
	}

	// An error reply — a resize refused with `forbidden`, say — must not end the attach.
	d.send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeError,
		Code: protocol.CodeForbidden, Message: "connection does not hold the size lease"})
	time.Sleep(100 * time.Millisecond)
	select {
	case <-h.fin:
		t.Fatal("a forbidden reply ended the attach")
	default:
	}

	// lease.changed makes this client the holder again: from then on it is the responder, its
	// replies pass through, and its resize reaches the PTY.
	d.send(&protocol.Message{Type: protocol.TypeLeaseChanged, SessionID: testSessionID,
		ConnectionID: "me", Owner: true, Cols: 80, Rows: 24, Generation: 1})
	waitFor(t, 5*time.Second, "the lease to move back to this client", func() bool {
		if _, err := h.ptmx.Write([]byte("\x1b[?1;2c")); err != nil {
			t.Fatal(err)
		}
		return d.inputSeen("\x1b[?1;2c")
	})
	waitFor(t, 5*time.Second, "the new lease holder's resize", func() bool {
		sigwinch()
		return d.resizes.Load() > 0
	})

	if _, err := h.ptmx.Write([]byte{0x1c}); err != nil {
		t.Fatal(err)
	}
	if err := h.detached(5 * time.Second); err != nil {
		t.Fatalf("attach returned %v after the detach key, want nil", err)
	}
}

// TestAttachExitsOnSessionExit covers K9: any session.exit ends the attach and restores the local
// terminal, including a signal death reported as 128+signal.
func TestAttachExitsOnSessionExit(t *testing.T) {
	for _, code := range []int{0, 1, 143} {
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			dir := shortTempDir(t)
			d := newFakeDaemon(t, dir)
			exit := code
			d.afterAttach = func(d *fakeDaemon) {
				d.send(&protocol.Message{Type: protocol.TypeSessionExit, SessionID: testSessionID,
					ExitCode: &exit, At: time.Now().UTC().Format(time.RFC3339Nano)})
			}

			h := startAttach(t, d, dir, "smoke")
			select {
			case <-d.attach:
			case <-time.After(5 * time.Second):
				t.Fatal("the client never sent session.attach")
			}
			if err := h.detached(5 * time.Second); err != nil {
				t.Fatalf("attach returned %v on session.exit(%d), want nil", err, code)
			}
			assertCooked(t, h)
		})
	}
}

// TestAttachTrapsSignalsAndRestoresTerminal covers N4: SIGINT and SIGQUIT end the attach like
// SIGTERM and SIGHUP, and none of them can leave the user's shell in raw mode.
func TestAttachTrapsSignalsAndRestoresTerminal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := shortTempDir(t)
			d := newFakeDaemon(t, dir)

			h := startAttach(t, d, dir, "smoke")
			select {
			case <-d.attach:
			case <-time.After(5 * time.Second):
				t.Fatal("the client never sent session.attach")
			}
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			if err := h.detached(10 * time.Second); err != nil {
				t.Fatalf("attach returned %v after %s, want nil", err, sig)
			}
			assertCooked(t, h)
		})
	}
}

// assertCooked shows the local terminal is back in cooked mode: a cooked tty echoes input, a tty
// left in raw mode does not.
func assertCooked(t *testing.T, h *attachClient) {
	t.Helper()
	if _, err := h.ptmx.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the local terminal to be restored (echo back)", func() bool {
		return bytes.Contains(h.out.Bytes(), []byte{'x'})
	})
}
