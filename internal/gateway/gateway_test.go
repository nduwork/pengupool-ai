package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/nduwork/pengupool-ai/internal/client"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// frameClient is the client side of the wire protocol, satisfied by *client.Conn over the unix
// socket and by the test's WebSocket client.
type frameClient interface {
	Hello() (*protocol.Message, error)
	Send(*protocol.Message) error
	WriteFrame(protocol.Frame) error
	Next(context.Context) (protocol.Frame, error)
	Call(context.Context, *protocol.Message) (*protocol.Message, error)
	Close() error
}

// fakeSessions is a worker manager answered by the test.
type fakeSessions struct {
	mu       sync.Mutex
	dialFn   func(ctx context.Context, sessionID string) (net.Conn, error)
	createFn func(ctx context.Context, req CreateRequest) (protocol.SessionView, error)
	killFn   func(ctx context.Context, sessionID string) error

	creates []CreateRequest
	kills   []string
}

func (f *fakeSessions) Dial(ctx context.Context, sessionID string) (net.Conn, error) {
	f.mu.Lock()
	fn := f.dialFn
	f.mu.Unlock()
	if fn == nil {
		return nil, protocol.Errorf(protocol.CodeNotFound, "no such session %s", sessionID)
	}
	return fn(ctx, sessionID)
}

func (f *fakeSessions) Create(ctx context.Context, req CreateRequest) (protocol.SessionView, error) {
	f.mu.Lock()
	fn := f.createFn
	f.creates = append(f.creates, req)
	f.mu.Unlock()
	if fn == nil {
		return protocol.SessionView{}, protocol.Errorf(protocol.CodeUnavailable, "no worker manager")
	}
	return fn(ctx, req)
}

func (f *fakeSessions) Kill(ctx context.Context, sessionID string) error {
	f.mu.Lock()
	fn := f.killFn
	f.kills = append(f.kills, sessionID)
	f.mu.Unlock()
	if fn == nil {
		return protocol.Errorf(protocol.CodeNotFound, "no such session %s", sessionID)
	}
	return fn(ctx, sessionID)
}

// shortDir makes a temp directory whose path is short enough for a unix socket: sun_path is 104
// bytes on macOS, and the default test temp directory is often longer than that.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pp2")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newTestServer(t *testing.T, sessions Sessions) (*Server, *store.Store) {
	t.Helper()
	return newTestServerOpts(t, Options{Sessions: sessions})
}

func newTestServerOpts(t *testing.T, opts Options) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(shortDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	opts.Store = st
	if opts.HTTPAddr == "" {
		opts.HTTPAddr = "127.0.0.1:0"
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.pollInterval = 20 * time.Millisecond
	srv.dialTimeout = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})

	waitFor(t, "unix socket", func() bool {
		_, err := os.Stat(srv.SocketPath())
		return err == nil
	})
	waitFor(t, "http address", func() bool {
		addr := srv.HTTPAddr()
		return addr != "" && addr != opts.HTTPAddr
	})
	return srv, st
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// dialHello opens a unix client and completes the handshake.
// dialHelloToken is dialHello with the host's token in the hello, as the owner's terminal CLI sends it.
func dialHelloToken(t *testing.T, srv *Server, token string) frameClient {
	t.Helper()
	c, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindCLI, "test-cli")
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	m := c.HelloMessage()
	m.Client.Token = token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ok, err := c.Call(ctx, m); err != nil || ok.Type != protocol.TypeHelloOK {
		t.Fatalf("hello: %+v %v", ok, err)
	}
	return c
}

func dialHello(t *testing.T, srv *Server) frameClient {
	t.Helper()
	c, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindCLI, "test-cli")
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	ok, err := c.Hello()
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if ok.Type != protocol.TypeHelloOK {
		t.Fatalf("hello.ok = %+v", ok)
	}
	return c
}

func TestHelloOKCarriesCapabilities(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	dialHello(t, srv)

	caps := map[string]bool{}
	for _, cap := range protocol.Capabilities {
		caps[cap] = true
	}
	for _, want := range []string{"state.subscribe", "session.attach", "session.create", "session.list", "session.kill"} {
		if !caps[want] {
			t.Errorf("capability %q is not advertised in %v", want, protocol.Capabilities)
		}
	}
}

func TestHelloFirstEnforced(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})

	t.Run("control before hello", func(t *testing.T) {
		c, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindCLI, "rude")
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req := &protocol.Message{Type: protocol.TypeStateSubscribe}
		resp, err := c.Call(ctx, req)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp.Type != protocol.TypeError {
			t.Fatalf("response = %+v, want an error", resp)
		}
		if resp.ID != req.ID {
			t.Errorf("error id = %q, want the request id %q", resp.ID, req.ID)
		}
		if resp.Code != protocol.CodeUnsupported {
			t.Errorf("error code = %q, want %q", resp.Code, protocol.CodeUnsupported)
		}
	})

	t.Run("data before hello", func(t *testing.T) {
		conn, err := net.Dial("unix", srv.SocketPath())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		if err := protocol.WriteFrame(conn, protocol.Frame{
			Header:  protocol.Header{Type: protocol.FrameOutput, Channel: 1, Seq: 0, Length: 2},
			Payload: []byte("hi"),
		}); err != nil {
			t.Fatalf("write: %v", err)
		}
		f, err := protocol.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if msg.Type != protocol.TypeError || msg.Code != protocol.CodeUnsupported {
			t.Fatalf("reply = %+v, want an unsupported error", msg)
		}
	})

	t.Run("wrong version", func(t *testing.T) {
		c, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindCLI, "old")
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := c.Call(ctx, &protocol.Message{
			Type:   protocol.TypeHello,
			V:      protocol.Version + 7,
			Client: &protocol.ClientInfo{Kind: protocol.KindCLI},
		})
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp.Type != protocol.TypeError || resp.Code != protocol.CodeIncompatibleVersion {
			t.Fatalf("reply = %+v, want incompatible_version", resp)
		}
	})
}

func TestPingPong(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypePing})
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if resp.Type != protocol.TypePong {
		t.Fatalf("reply = %+v, want pong", resp)
	}
}

func TestStateSubscribeSnapshotAndDelta(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snap, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeStateSubscribe})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if snap.Type != protocol.TypeStateSnapshot {
		t.Fatalf("response = %+v, want state.snapshot", snap)
	}
	if snap.Epoch != srv.Epoch() {
		t.Errorf("snapshot epoch = %q, want %q", snap.Epoch, srv.Epoch())
	}
	if snap.Rev != st.State().Rev {
		t.Errorf("snapshot rev = %d, want %d", snap.Rev, st.State().Rev)
	}
	if len(snap.Sessions) != 0 {
		t.Errorf("snapshot sessions = %v, want none", snap.Sessions)
	}

	// A store change shows up as a delta with an increasing revision.
	id := protocol.NewID()
	if err := st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: id, Name: "one", NodeID: s.NodeID, State: "running", Cols: 80, Rows: 24})
		return nil
	}); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	delta := nextControl(t, c, ctx, protocol.TypeStateDelta)
	if delta.Epoch != srv.Epoch() {
		t.Errorf("delta epoch = %q, want %q", delta.Epoch, srv.Epoch())
	}
	if delta.Rev <= snap.Rev {
		t.Errorf("delta rev = %d, want > %d", delta.Rev, snap.Rev)
	}
	if len(delta.Upserted) != 1 || delta.Upserted[0].ID != id {
		t.Fatalf("delta upserted = %+v, want session %s", delta.Upserted, id)
	}
	if len(delta.Removed) != 0 {
		t.Errorf("delta removed = %v, want none", delta.Removed)
	}

	// Removing it shows up as a removal.
	if err := st.Update(func(s *store.State) error { s.Sessions = nil; return nil }); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	delta = nextControl(t, c, ctx, protocol.TypeStateDelta)
	if len(delta.Removed) != 1 || delta.Removed[0] != id {
		t.Fatalf("delta removed = %v, want [%s]", delta.Removed, id)
	}

	t.Run("stale epoch forces a snapshot", func(t *testing.T) {
		resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeStateSubscribe, Epoch: "stale-epoch", SinceRev: st.State().Rev})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if resp.Type != protocol.TypeStateSnapshot {
			t.Fatalf("response = %+v, want a fresh snapshot", resp)
		}
	})

	t.Run("older revision forces a snapshot", func(t *testing.T) {
		resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeStateSubscribe, Epoch: srv.Epoch(), SinceRev: 1})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if resp.Type != protocol.TypeStateSnapshot {
			t.Fatalf("response = %+v, want a fresh snapshot", resp)
		}
	})

	t.Run("matching epoch and revision resumes with a delta", func(t *testing.T) {
		resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeStateSubscribe, Epoch: srv.Epoch(), SinceRev: st.State().Rev})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if resp.Type != protocol.TypeStateDelta || resp.Epoch != srv.Epoch() {
			t.Fatalf("response = %+v, want a delta on the current epoch", resp)
		}
		if len(resp.Upserted) != 0 || len(resp.Removed) != 0 {
			t.Fatalf("resume delta carries changes: %+v", resp)
		}
	})
}

func TestSessionList(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionList})
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	if resp.Type != protocol.TypeSessionList || len(resp.Sessions) != 0 {
		t.Fatalf("session.list = %+v, want no sessions", resp)
	}

	id := protocol.NewID()
	if err := st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: id, Name: "listed", State: "running", Cols: 120, Rows: 40})
		return nil
	}); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	resp, err = c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionList})
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].ID != id || resp.Sessions[0].Name != "listed" || resp.Sessions[0].Cols != 120 {
		t.Fatalf("session.list = %+v", resp.Sessions)
	}
}

func TestSessionCreateAndKill(t *testing.T) {
	fs := &fakeSessions{}
	fs.createFn = func(_ context.Context, req CreateRequest) (protocol.SessionView, error) {
		if req.Name != "agent" || req.Cwd != "/tmp/work" || req.Harness != "claude" || req.Cols != 100 || req.Rows != 30 {
			t.Errorf("create request = %+v", req)
		}
		if len(req.Command) != 2 || req.Command[0] != "sh" {
			t.Errorf("create command = %v", req.Command)
		}
		return protocol.SessionView{ID: "new-1", Name: req.Name, Cwd: req.Cwd, Harness: req.Harness, State: "running", Cols: req.Cols, Rows: req.Rows}, nil
	}
	srv, _ := newTestServer(t, fs)
	// Inside an agent session the CLI sends no token: it cannot start or stop sessions.
	t.Setenv("PENGUPOOL_SESSION_ID", "agent-1")
	agent := dialHello(t, srv)
	if resp, err := agent.Call(context.Background(), &protocol.Message{Type: protocol.TypeSessionKill, SessionID: "new-1"}); err != nil || resp.Code != protocol.CodeForbidden {
		t.Fatalf("an agent's kill = %+v, %v", resp, err)
	}
	// The owner's terminal sends the daemon's token.
	t.Setenv("PENGUPOOL_SESSION_ID", "")
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.Call(ctx, &protocol.Message{
		Type:    protocol.TypeSessionCreate,
		Name:    "agent",
		Cwd:     "/tmp/work",
		Harness: "claude",
		Command: []string{"sh", "-c"},
		Cols:    100,
		Rows:    30,
	})
	if err != nil {
		t.Fatalf("session.create: %v", err)
	}
	if resp.Type != protocol.TypeSessionCreate {
		t.Fatalf("session.create = %+v", resp)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].ID != "new-1" || resp.Sessions[0].Name != "agent" {
		t.Fatalf("session.create sessions = %+v", resp.Sessions)
	}

	fs.mu.Lock()
	creates := len(fs.creates)
	fs.mu.Unlock()
	if creates != 1 {
		t.Errorf("manager saw %d creates, want 1", creates)
	}

	// kill maps a manager error onto the error frame's code.
	resp, err = c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionKill, SessionID: "ghost"})
	if err != nil {
		t.Fatalf("session.kill: %v", err)
	}
	if resp.Type != protocol.TypeError || resp.Code != protocol.CodeNotFound {
		t.Fatalf("session.kill = %+v, want not_found", resp)
	}

	fs.mu.Lock()
	fs.killFn = func(context.Context, string) error { return nil }
	fs.mu.Unlock()
	resp, err = c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionKill, SessionID: "new-1"})
	if err != nil {
		t.Fatalf("session.kill: %v", err)
	}
	if resp.Type != protocol.TypeSessionKill {
		t.Fatalf("session.kill = %+v, want success", resp)
	}
}

// workerPeer stands in for a worker behind the hub. It answers the first session.attach the
// gateway forwards, emits one output frame, then relays everything else it is sent.
type workerPeer struct {
	workerChan uint32
	greeting   []byte

	attach chan *protocol.Message
	frames chan protocol.Frame
	errs   chan error
}

func newWorkerPeer(workerChan uint32, greeting []byte) (*workerPeer, net.Conn) {
	gw, peer := net.Pipe()
	p := &workerPeer{
		workerChan: workerChan,
		greeting:   greeting,
		attach:     make(chan *protocol.Message, 1),
		frames:     make(chan protocol.Frame, 32),
		errs:       make(chan error, 1),
	}
	go func() {
		f, err := protocol.ReadFrame(peer)
		if err != nil {
			p.errs <- err
			return
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			p.errs <- err
			return
		}
		p.attach <- msg
		if err := protocol.WriteControl(peer, &protocol.Message{
			ID: msg.ID, Type: protocol.TypeSessionAttached, SessionID: msg.SessionID,
			Channel: workerChan, Generation: 1, Cols: 80, Rows: 24, Mode: protocol.ModeSnapshot,
		}); err != nil {
			p.errs <- err
			return
		}
		if err := protocol.WriteFrame(peer, protocol.Frame{
			Header:  protocol.Header{Type: protocol.FrameOutput, Channel: workerChan, Seq: 0, Length: uint32(len(greeting))},
			Payload: greeting,
		}); err != nil {
			p.errs <- err
			return
		}
		for {
			f, err := protocol.ReadFrame(peer)
			if err != nil {
				p.errs <- err
				return
			}
			p.frames <- f
		}
	}()
	return p, gw
}

func (p *workerPeer) expectFrame(t *testing.T) protocol.Frame {
	t.Helper()
	select {
	case f := <-p.frames:
		return f
	case err := <-p.errs:
		t.Fatalf("worker peer closed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a frame for the worker")
	}
	return protocol.Frame{}
}

// exerciseAttach drives a full attach over c against p: the attach handshake, the greeting the
// worker sends, input crossing inwards and resize/detach control crossing inwards.
func exerciseAttach(t *testing.T, c frameClient, p *workerPeer, clientChan uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "s1", Channel: clientChan})
	if err != nil {
		t.Fatalf("session.attach: %v", err)
	}
	if resp.Type != protocol.TypeSessionAttached {
		t.Fatalf("attach response = %+v", resp)
	}

	select {
	case seen := <-p.attach:
		if seen.SessionID != "s1" {
			t.Errorf("attach forwarded session_id = %q", seen.SessionID)
		}
		if seen.Channel != clientChan {
			t.Errorf("attach forwarded channel = %d, want the client's %d", seen.Channel, clientChan)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never saw session.attach")
	}

	// The client is spoken to on the channel it chose, however the worker numbered it.
	if resp.Channel != clientChan {
		t.Errorf("attached channel = %d, want %d", resp.Channel, clientChan)
	}

	f, err := c.Next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if f.Header.Type != protocol.FrameOutput || f.Header.Channel != clientChan || !bytes.Equal(f.Payload, p.greeting) {
		t.Fatalf("output frame = %+v %q, want channel %d and %q", f.Header, f.Payload, clientChan, p.greeting)
	}

	if err := c.WriteFrame(protocol.Frame{
		Header:  protocol.Header{Type: protocol.FrameInput, Channel: clientChan, Seq: 0, Length: 2},
		Payload: []byte("go"),
	}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	in := p.expectFrame(t)
	if in.Header.Type != protocol.FrameInput || in.Header.Channel != p.workerChan || string(in.Payload) != "go" {
		t.Fatalf("input forwarded = %+v %q, want channel %d", in.Header, in.Payload, p.workerChan)
	}

	if err := c.Send(&protocol.Message{Type: protocol.TypeSessionResize, SessionID: "s1", Cols: 100, Rows: 40}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	rz := p.expectFrame(t)
	rzMsg, err := protocol.DecodeControl(rz)
	if err != nil {
		t.Fatalf("decode resize: %v", err)
	}
	if rzMsg.Type != protocol.TypeSessionResize || rzMsg.Cols != 100 || rzMsg.Rows != 40 {
		t.Fatalf("resize forwarded = %+v", rzMsg)
	}

	if err := c.Send(&protocol.Message{Type: protocol.TypeSessionDetach}); err != nil {
		t.Fatalf("detach: %v", err)
	}
	dt := p.expectFrame(t)
	dtMsg, err := protocol.DecodeControl(dt)
	if err != nil {
		t.Fatalf("decode detach: %v", err)
	}
	if dtMsg.Type != protocol.TypeSessionDetach {
		t.Fatalf("detach forwarded = %+v", dtMsg)
	}
	select {
	case <-p.errs:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker connection was not closed after detach")
	}
}

func TestAttachOverUnixSocket(t *testing.T) {
	p, gw := newWorkerPeer(9, []byte("hello from the agent"))
	fs := &fakeSessions{dialFn: func(_ context.Context, sessionID string) (net.Conn, error) {
		if sessionID != "s1" {
			return nil, protocol.Errorf(protocol.CodeNotFound, "no such session %s", sessionID)
		}
		return gw, nil
	}}
	srv, _ := newTestServer(t, fs)
	c := dialHello(t, srv)
	// The client picks channel 4; the worker answers on 9 and the gateway maps between them.
	exerciseAttach(t, c, p, 4)
}

func TestAttachUnknownSession(t *testing.T) {
	fs := &fakeSessions{dialFn: func(_ context.Context, sessionID string) (net.Conn, error) {
		return nil, protocol.Errorf(protocol.CodeNotFound, "no such session %s", sessionID)
	}}
	srv, _ := newTestServer(t, fs)
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "ghost", Channel: 1})
	if err != nil {
		t.Fatalf("session.attach: %v", err)
	}
	if resp.Type != protocol.TypeError || resp.Code != protocol.CodeNotFound {
		t.Fatalf("attach response = %+v, want not_found", resp)
	}
}

// A detach with nothing attached (the pane of a session that was just closed) is done, not an error:
// an error without a session would reach every other attached pane.
func TestDetachWhenNothingIsAttached(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionDetach, SessionID: "gone"}); err == nil {
		t.Fatalf("detach with nothing attached answered %+v", resp)
	}
}

// Input or a resize from a pane whose session is gone is dropped without an error for every other pane.
func TestInputWhenNothingIsAttached(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	c := dialHello(t, srv)
	_ = c.WriteFrame(protocol.Frame{Header: protocol.Header{Type: protocol.FrameInput, Channel: 3, Length: 1}, Payload: []byte("x")})
	_ = c.Send(&protocol.Message{ID: "r", Type: protocol.TypeSessionResize, SessionID: "gone", Cols: 80, Rows: 24})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypePing}); err != nil || resp.Type != protocol.TypePong {
		t.Fatalf("ping: %+v %v", resp, err)
	}
	quiet, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	if f, err := c.Next(quiet); err == nil {
		t.Fatalf("input with nothing attached answered %+v", f)
	}
}

func TestWebSocketRoundTrip(t *testing.T) {
	p, gw := newWorkerPeer(7, []byte("ws greeting"))
	fs := &fakeSessions{dialFn: func(_ context.Context, sessionID string) (net.Conn, error) {
		return gw, nil
	}}
	srv, st := newTestServer(t, fs)
	c := mustDialWS(t, srv, st)
	ok, err := c.Hello()
	if err != nil {
		t.Fatalf("ws hello: %v", err)
	}
	if ok.Type != protocol.TypeHelloOK {
		t.Fatalf("ws hello.ok = %+v", ok)
	}
	exerciseAttach(t, c, p, 3)
}

func TestHealthz(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	req, err := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/api/healthz", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Epoch  string `json:"epoch"`
		Rev    uint64 `json:"rev"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("healthz body: %v", err)
	}
	if body.Status != "ok" || body.Epoch != srv.Epoch() || body.Rev != st.State().Rev {
		t.Fatalf("healthz body = %+v", body)
	}
}

func TestStaticServing(t *testing.T) {
	app := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>pengupool</html>")}}
	srv, _ := newTestServerOpts(t, Options{Static: fs.FS(app), Sessions: &fakeSessions{}})

	resp, err := http.Get("http://" + srv.HTTPAddr() + "/index.html")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static status = %d", resp.StatusCode)
	}

	// A missing file is a 404, not a crash.
	resp, err = http.Get("http://" + srv.HTTPAddr() + "/nope.js")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file status = %d, want 404", resp.StatusCode)
	}
}

func TestStaticNilIs404(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	resp, err := http.Get("http://" + srv.HTTPAddr() + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWSTokenFile(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	path := st.Path(store.RunDir, TokenFile)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %04o, want 0600", perm)
	}
	if token := readToken(t, st); token != srv.Token() {
		t.Errorf("token on disk = %q, want %q", token, srv.Token())
	}
}

func TestWSRejectsBadToken(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	_, resp, err := websocket.Dial(context.Background(),
		"ws://"+srv.HTTPAddr()+"/ws?token=not-the-token", nil)
	if err == nil {
		t.Fatal("upgrade with a bad token succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response = %+v, want 401", resp)
	}
}

func TestWSRejectsForeignOrigin(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	url := "ws://" + srv.HTTPAddr() + "/ws?token=" + readToken(t, st)
	_, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
	})
	if err == nil {
		t.Fatal("upgrade from a foreign origin succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %+v, want 403", resp)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	req, err := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/ws?token="+readToken(t, st), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestSocketPathDefault(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	want := st.Path(store.RunDir, "gateway.sock")
	if got := srv.SocketPath(); got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
}

// TestClientDisconnectAfterHelloDoesNotPanic guards the daemon's most damaging failure: a client
// that connects and drops without ever attaching used to panic the whole process in teardown.
func TestClientDisconnectAfterHelloDoesNotPanic(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	ctx := context.Background()

	for range 3 {
		// Dropped before hello.
		raw, err := net.Dial("unix", srv.SocketPath())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		raw.Close()

		// Hello, then dropped.
		c, err := client.Dial(ctx, srv.SocketPath(), protocol.KindCLI, "flapper")
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if _, err := c.Hello(); err != nil {
			t.Fatalf("hello: %v", err)
		}
		c.Close()
	}

	// The daemon is still there: a panic in a handler goroutine would have taken the test binary
	// down, but prove the listener still answers.
	c := dialHello(t, srv)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if resp, err := c.Call(callCtx, &protocol.Message{Type: protocol.TypePing}); err != nil || resp.Type != protocol.TypePong {
		t.Fatalf("ping after disconnects: %+v, %v", resp, err)
	}
}

func TestServeRemovesSocketOnReturn(t *testing.T) {
	st, err := store.Open(shortDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv, err := New(Options{Store: st, HTTPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	waitFor(t, "socket", func() bool {
		_, err := os.Stat(srv.SocketPath())
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	if _, err := os.Stat(srv.SocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket %s survived a clean shutdown (stat err = %v)", srv.SocketPath(), err)
	}
}

// TestStaleSocketIsReplaced: a daemon that died without cleaning up leaves its socket file
// behind; the next start must take it over instead of refusing to listen.
func TestStaleSocketIsReplaced(t *testing.T) {
	st, err := store.Open(shortDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv, err := New(Options{Store: st, HTTPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ln, err := net.Listen("unix", srv.SocketPath())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(srv.SocketPath()); err != nil {
		t.Fatalf("expected a stale socket file: %v", err)
	}
	// Dialing the stale socket fails fast rather than hanging.
	if c, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindCLI, "stale"); err == nil {
		c.Close()
		t.Fatal("dialing a stale socket succeeded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	waitFor(t, "socket", func() bool {
		_, err := os.Stat(srv.SocketPath())
		return err == nil
	})
	// The stale file already exists, so retry until the listener behind it answers.
	var c *client.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var derr error
		c, derr = client.Dial(context.Background(), srv.SocketPath(), protocol.KindCLI, "test-cli")
		if derr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway never took over the stale socket: %v", derr)
		}
		time.Sleep(time.Millisecond)
	}
	defer c.Close()
	if _, err := c.Hello(); err != nil {
		t.Fatalf("hello after stale socket: %v", err)
	}
}

// TestNonSocketPathRefused: Serve must never delete a file it did not create.
func TestNonSocketPathRefused(t *testing.T) {
	dir := shortDir(t)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	path := filepath.Join(dir, "gateway.sock")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	srv, err := New(Options{Store: st, SocketPath: path, HTTPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("Serve accepted a non-socket at the socket path")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("Serve clobbered the file: %q, %v", data, err)
	}
}

// refuseAttach stands in for a worker that rejects the attach: it answers the first session.attach
// with an error and then holds the connection open until the gateway drops it.
func refuseAttach(peer net.Conn, code string) {
	go func() {
		f, err := protocol.ReadFrame(peer)
		if err != nil {
			return
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			return
		}
		_ = protocol.WriteControl(peer, protocol.ErrorMessage(msg.ID, protocol.Errorf(code, "attach refused")))
		buf := make([]byte, 64)
		for {
			if _, err := peer.Read(buf); err != nil {
				return
			}
		}
	}()
}

// TestRefusedAttachLeavesNothingBehind: a worker that refuses session.attach must leave no
// attachment on the connection, or every retry is answered conflict forever.
func TestRefusedAttachLeavesNothingBehind(t *testing.T) {
	var mu sync.Mutex
	dials := 0
	fs := &fakeSessions{}
	fs.dialFn = func(_ context.Context, _ string) (net.Conn, error) {
		mu.Lock()
		dials++
		first := dials == 1
		mu.Unlock()
		gw, peer := net.Pipe()
		if first {
			refuseAttach(peer, protocol.CodeForbidden)
			return gw, nil
		}
		_, accepting := newWorkerPeer(1, []byte("retry ok"))
		return accepting, nil
	}
	srv, _ := newTestServer(t, fs)
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "s1", Channel: 4})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if resp.Type != protocol.TypeError || resp.Code != protocol.CodeForbidden {
		t.Fatalf("refused attach reply = %+v, want forbidden", resp)
	}

	// The refused attach must not linger: the retry dials again and attaches.
	resp, err = c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "s1", Channel: 4})
	if err != nil {
		t.Fatalf("retry attach: %v", err)
	}
	if resp.Type != protocol.TypeSessionAttached {
		t.Fatalf("retry reply = %+v, want session.attached", resp)
	}
	mu.Lock()
	n := dials
	mu.Unlock()
	if n != 2 {
		t.Errorf("the gateway dialed %d times, want 2: a refused attach must not be reused", n)
	}
}

// TestAttachChannelZeroRefused: session.attach names a nonzero stream id; the gateway must refuse a
// channel-0 attach before it dials a worker.
func TestAttachChannelZeroRefused(t *testing.T) {
	fs := &fakeSessions{}
	dialed := false
	fs.dialFn = func(context.Context, string) (net.Conn, error) {
		dialed = true
		return nil, protocol.Errorf(protocol.CodeNotFound, "no session")
	}
	srv, _ := newTestServer(t, fs)
	c := dialHello(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "s1"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if resp.Type != protocol.TypeError || resp.Code != protocol.CodeInvalidArgument {
		t.Fatalf("channel-0 attach reply = %+v, want invalid_argument", resp)
	}
	if dialed {
		t.Error("the gateway dialed a worker for a channel-0 attach")
	}
}

// TestRelayedAttachCarriesClientKind: the worker decides who may hold the lease from the attach's
// kind, and that kind must come from the connection's hello, not from the client's own field.
func TestRelayedAttachCarriesClientKind(t *testing.T) {
	p, gw := newWorkerPeer(1, nil)
	fs := &fakeSessions{dialFn: func(context.Context, string) (net.Conn, error) { return gw, nil }}
	srv, _ := newTestServer(t, fs)

	c, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindIOS, "phone")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Hello(); err != nil {
		t.Fatalf("hello: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The request lies about its kind; the gateway must overwrite it with the hello kind.
	resp, err := c.Call(ctx, &protocol.Message{
		Type: protocol.TypeSessionAttach, SessionID: "s1", Channel: 2, Kind: string(protocol.KindCLI),
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if resp.Type != protocol.TypeSessionAttached {
		t.Fatalf("attach reply = %+v", resp)
	}
	select {
	case seen := <-p.attach:
		if seen.Kind != string(protocol.KindIOS) {
			t.Fatalf("relayed attach kind = %q, want %q", seen.Kind, protocol.KindIOS)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never saw session.attach")
	}
}

// TestLocalhostAcceptedOnLoopbackBind: a URL printed for a loopback bind must not be rejected when
// the user follows it as localhost.
func TestLocalhostAcceptedOnLoopbackBind(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	_, port, err := net.SplitHostPort(srv.HTTPAddr())
	if err != nil {
		t.Fatalf("bound address %q: %v", srv.HTTPAddr(), err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+"/api/healthz", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Host = "localhost:" + port
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("localhost Host on a loopback bind = %d, want 200", resp.StatusCode)
	}
}

// TestEmptyHostBindsLoopback: an address with no host (':0') must not bind every interface.
func TestEmptyHostBindsLoopback(t *testing.T) {
	srv, _ := newTestServerOpts(t, Options{HTTPAddr: ":0", Sessions: &fakeSessions{}})
	host, _, err := net.SplitHostPort(srv.HTTPAddr())
	if err != nil {
		t.Fatalf("bound address %q: %v", srv.HTTPAddr(), err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("':0' bound host %q, want a loopback address", host)
	}
}

// TestHTTPAddrEmptyUntilBound: HTTPAddr must report nothing before the bind and the real bound
// address after; a failed bind is never reported as listening.
func TestHTTPAddrEmptyUntilBound(t *testing.T) {
	st, err := store.Open(shortDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv, err := New(Options{Store: st, HTTPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := srv.HTTPAddr(); got != "" {
		t.Fatalf("HTTPAddr before Serve = %q, want empty", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	waitFor(t, "bound http address", func() bool { return srv.HTTPAddr() != "" })
	host, port, err := net.SplitHostPort(srv.HTTPAddr())
	if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
		t.Fatalf("bound HTTPAddr = %q, want 127.0.0.1 with a real port", srv.HTTPAddr())
	}
}

// TestFailedBindIsNotReportedListening: a daemon whose HTTP bind fails must not look like it is
// listening.
func TestFailedBindIsNotReportedListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer ln.Close()

	st, err := store.Open(shortDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv, err := New(Options{Store: st, HTTPAddr: ln.Addr().String()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("Serve reported success on an occupied HTTP port")
	}
	if got := srv.HTTPAddr(); got != "" {
		t.Fatalf("failed bind reported HTTPAddr %q, want empty", got)
	}
}

// TestLiveSocketIsNotUnlinked: a second Serve on a socket path a live daemon holds must refuse, and
// must leave the live daemon reachable.
func TestLiveSocketIsNotUnlinked(t *testing.T) {
	srv1, _ := newTestServer(t, &fakeSessions{})

	st2, err := store.Open(shortDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv2, err := New(Options{Store: st2, SocketPath: srv1.SocketPath(), HTTPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv2.Serve(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the second Serve took over a socket a live daemon holds")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Serve did not refuse a live socket")
	}

	// The live daemon is untouched: the socket path still answers.
	c := dialHello(t, srv1)
	callCtx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	if resp, err := c.Call(callCtx, &protocol.Message{Type: protocol.TypePing}); err != nil || resp.Type != protocol.TypePong {
		t.Fatalf("ping on the live daemon after a second Serve: %+v, %v", resp, err)
	}
}

func readToken(t *testing.T, st *store.Store) string {
	t.Helper()
	data, err := os.ReadFile(st.Path(store.RunDir, TokenFile))
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// nextControl reads frames until the expected control message arrives.
func nextControl(t *testing.T, c frameClient, ctx context.Context, typ string) *protocol.Message {
	t.Helper()
	for {
		f, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if f.Header.Type != protocol.FrameControl {
			t.Fatalf("unexpected frame %+v", f.Header)
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if msg.Type == protocol.TypeError {
			t.Fatalf("server error: %s: %s", msg.Code, msg.Message)
		}
		if msg.Type == typ {
			return msg
		}
	}
}

func mustDialWS(t *testing.T, srv *Server, st *store.Store) *wsClient {
	t.Helper()
	addr := srv.HTTPAddr()
	url := "ws://" + addr + "/ws?token=" + readToken(t, st)
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://" + addr}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	ws.SetReadLimit(2 << 20)
	c := &wsClient{ws: ws, ctx: ctx}
	t.Cleanup(func() { c.Close() })
	return c
}

// wsClient is the frame client over a WebSocket, used to prove /ws carries the same frames.
type wsClient struct {
	ws  *websocket.Conn
	ctx context.Context

	mu      sync.Mutex
	buf     []byte
	pending []protocol.Frame
}

func (c *wsClient) Hello() (*protocol.Message, error) {
	return c.Call(c.ctx, &protocol.Message{
		Type:   protocol.TypeHello,
		V:      protocol.Version,
		Client: &protocol.ClientInfo{Kind: protocol.KindBrowser, Name: "ws-test"},
	})
}

func (c *wsClient) Send(m *protocol.Message) error {
	if m.ID == "" {
		m.ID = protocol.NewID()
	}
	f, err := controlFrame(m)
	if err != nil {
		return err
	}
	return c.WriteFrame(f)
}

func (c *wsClient) WriteFrame(f protocol.Frame) error {
	var buf bytes.Buffer
	if err := protocol.WriteFrame(&buf, f); err != nil {
		return err
	}
	return c.ws.Write(c.ctx, websocket.MessageBinary, buf.Bytes())
}

func (c *wsClient) Next(ctx context.Context) (protocol.Frame, error) {
	c.mu.Lock()
	if len(c.pending) > 0 {
		f := c.pending[0]
		c.pending = c.pending[1:]
		c.mu.Unlock()
		return f, nil
	}
	c.mu.Unlock()
	return c.readFrame(ctx)
}

func (c *wsClient) readFrame(ctx context.Context) (protocol.Frame, error) {
	for {
		if len(c.buf) >= protocol.HeaderSize {
			h, err := protocol.ParseHeader(c.buf[:protocol.HeaderSize])
			if err != nil {
				return protocol.Frame{}, err
			}
			total := protocol.HeaderSize + int(h.Length)
			if len(c.buf) >= total {
				f, err := protocol.ReadFrame(bytes.NewReader(c.buf[:total]))
				if err != nil {
					return protocol.Frame{}, err
				}
				rest := make([]byte, len(c.buf)-total)
				copy(rest, c.buf[total:])
				c.buf = rest
				return f, nil
			}
		}
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return protocol.Frame{}, err
		}
		c.buf = append(c.buf, data...)
	}
}

func (c *wsClient) Call(ctx context.Context, m *protocol.Message) (*protocol.Message, error) {
	if m.ID == "" {
		m.ID = protocol.NewID()
	}
	if err := c.Send(m); err != nil {
		return nil, err
	}
	for {
		// Read straight off the wire: a frame pushed for Next must never come back here.
		f, err := c.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		if f.Header.Type != protocol.FrameControl {
			c.push(f)
			continue
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			return nil, err
		}
		if msg.ID == m.ID {
			return msg, nil
		}
		c.push(f)
	}
}

func (c *wsClient) push(f protocol.Frame) {
	c.mu.Lock()
	c.pending = append(c.pending, f)
	c.mu.Unlock()
}

func (c *wsClient) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// TestOnlyTheAppIsOwner: a request over /ws with the host token is the owner's; one over the unix
// socket (where agents' CLI calls arrive) never is, even when it claims to be the browser.
func TestOnlyTheAppIsOwner(t *testing.T) {
	got := make(chan bool, 2)
	srv, st := newTestServerOpts(t, Options{Sessions: &fakeSessions{}, RPC: func(_ context.Context, c Caller, _ string, _ json.RawMessage) (any, error) {
		got <- c.Owner
		return nil, nil
	}})
	ask := func(c frameClient) bool {
		if _, err := c.Call(context.Background(), &protocol.Message{ID: protocol.NewID(), Type: "log.settings_set", V: protocol.Version}); err != nil {
			t.Fatal(err)
		}
		return <-got
	}
	sock, err := client.Dial(context.Background(), srv.SocketPath(), protocol.KindBrowser, "agent pretending")
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	if _, err := sock.Hello(); err != nil {
		t.Fatal(err)
	}
	if ask(sock) {
		t.Fatal("a unix-socket connection counted as the owner")
	}
	ws := mustDialWS(t, srv, st)
	if _, err := ws.Hello(); err != nil {
		t.Fatal(err)
	}
	if !ask(ws) {
		t.Fatal("the app's /ws connection is not the owner")
	}
}

// OwnerInput hears a prompt only from a connection that presented the host's token: the app. An agent
// reaches the unix socket without it, and what it types there is not the owner submitting a prompt.
func TestOwnerInputIsTheTokenHoldersPromptOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner bool
		input string
		want  string
	}{
		{"the app submits a prompt", true, "go on\r", "go on"},
		{"the app types without Enter", true, "go on", ""},
		{"an agent on the unix socket submits one", false, "go on\r", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, gw := newWorkerPeer(7, []byte("hi"))
			fs := &fakeSessions{dialFn: func(context.Context, string) (net.Conn, error) { return gw, nil }}
			heard := make(chan string, 4)
			srv, st := newTestServerOpts(t, Options{Sessions: fs, OwnerInput: func(id, text string) { heard <- id + ":" + text }})
			var c frameClient
			if tc.owner {
				w := mustDialWS(t, srv, st)
				if _, err := w.Hello(); err != nil {
					t.Fatal(err)
				}
				c = w
			} else {
				c = dialHello(t, srv)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "s1", Channel: 3}); err != nil || resp.Type != protocol.TypeSessionAttached {
				t.Fatalf("attach: %+v %v", resp, err)
			}
			<-p.attach
			if err := c.WriteFrame(protocol.Frame{Header: protocol.Header{Type: protocol.FrameInput, Channel: 3, Length: uint32(len(tc.input))}, Payload: []byte(tc.input)}); err != nil {
				t.Fatal(err)
			}
			p.expectFrame(t) // the input still reaches the session either way
			select {
			case got := <-heard:
				if tc.want == "" || got != "s1:"+tc.want {
					t.Fatalf("OwnerInput(%q), want %q", got, tc.want)
				}
			case <-time.After(300 * time.Millisecond):
				if tc.want != "" {
					t.Fatal("OwnerInput was not called")
				}
			}
		})
	}
}

// The owner's terminal CLI is not a source of prompts: on one account an agent can run it from outside
// every session's tree (a reparented process), so only the app's own connection is the owner typing.
func TestTheTerminalCLIsEnterIsNobodysCredit(t *testing.T) {
	p, gw := newWorkerPeer(7, []byte("hi"))
	fs := &fakeSessions{dialFn: func(context.Context, string) (net.Conn, error) { return gw, nil }}
	heard := make(chan string, 4)
	srv, st := newTestServerOpts(t, Options{Sessions: fs, OwnerInput: func(id, text string) { heard <- text }})
	token, err := os.ReadFile(st.Path(store.RunDir, TokenFile))
	if err != nil {
		t.Skipf("no token file: %v", err)
	}
	c := dialHelloToken(t, srv, strings.TrimSpace(string(token)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if resp, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionAttach, SessionID: "s1", Channel: 3}); err != nil || resp.Type != protocol.TypeSessionAttached {
		t.Fatalf("attach: %+v %v", resp, err)
	}
	<-p.attach
	_ = c.WriteFrame(protocol.Frame{Header: protocol.Header{Type: protocol.FrameInput, Channel: 3, Length: 3}, Payload: []byte("go\r")})
	p.expectFrame(t)
	select {
	case text := <-heard:
		t.Fatalf("the terminal CLI's Enter was the owner's: %q", text)
	case <-time.After(300 * time.Millisecond):
	}
}
