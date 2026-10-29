// Package gateway is the daemon's front door. It accepts frame connections on a unix socket and
// over HTTP/WebSocket, answers the control traffic it owns (hello, state.subscribe, ping, the
// session lifecycle requests) and relays session.attach to a worker byte for byte.
//
// The gateway is a frame router, not a terminal: it never parses VT output, never assembles
// snapshots and never decides between replay and snapshot. The worker owns all of that; the
// gateway only rewrites the frame's `channel` field when a client picked a channel of its own.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// TokenFile is the per-daemon bootstrap token inside the store's run directory.
const TokenFile = "hub.token"

// TokenQuery is the /ws query parameter that must carry that token.
const TokenQuery = "token"

// CreateRequest is a session.create request as the gateway maps it off the wire.
type CreateRequest struct {
	Name    string
	Cwd     string
	Harness string
	Command []string
	Cols    int
	Rows    int
}

// Sessions is the worker manager the gateway routes session lifecycle and attach requests to.
// internal/hub behaves this way, but the gateway never imports it: cmd/pengupool supplies the
// adapter, so the two packages can evolve independently.
type Sessions interface {
	Dial(ctx context.Context, sessionID string) (net.Conn, error)
	Create(ctx context.Context, req CreateRequest) (protocol.SessionView, error)
	Kill(ctx context.Context, sessionID string) error
}

// Options configures a Server.
type Options struct {
	// SocketPath is the unix socket clients connect to. Empty derives one inside the store's run
	// directory.
	SocketPath string
	// HTTPAddr is the HTTP/WebSocket listen address. Empty means 127.0.0.1 on an ephemeral port. An
	// empty host (":7792") is rewritten to loopback: only an explicit wildcard address binds more
	// than loopback.
	HTTPAddr string
	// Store is the daemon's state; state.subscribe and session.list read from it.
	Store *store.Store
	// Sessions is the worker manager. It may be nil, in which case attach and the lifecycle
	// requests answer unavailable.
	Sessions Sessions
	// Static is the built web app served at /. Missing files answer 404.
	Static fs.FS
	// Logf receives diagnostics. It may be nil.
	Logf func(string, ...any)
	// RPC answers every pool request type the gateway does not handle itself. It may be nil.
	RPC func(ctx context.Context, c Caller, typ string, params json.RawMessage) (any, error)
	// Filter, when set, narrows the state a connection sees (iOS group access). It may be nil.
	Filter func(c Caller, st store.State) store.State
	// OnClose is told when a connection ends, so per-connection unlocks can be dropped.
	OnClose func(c Caller)
	// Authorize, when set, may refuse an attach (group access for phones).
	Authorize func(c Caller, sessionID string) error
	// OwnerInput, when set, is told that the owner submitted a prompt in a session: the keystrokes of
	// the app's own connection (/ws with the host's token). Not the terminal CLI's: on one account an
	// agent can reparent a process away from its session (( cmd & )) and run the CLI from outside every
	// session's tree, so ancestry says "is an agent" but never "is the owner". Nothing else is the owner
	// typing: an agent reaches the unix socket and a session's own socket without the token.
	OwnerInput func(sessionID, text string)
	// InSession, when set, says whether a local process runs inside an agent session's process tree.
	// A connection from such a process is not the owner's terminal, whatever token it sends: an agent
	// can read the token file and unset the variable the CLI looks at.
	InSession func(pid int) bool
	// PhoneHosts are extra Host names accepted on the HTTP listener (the Tailscale Serve name).
	// A browser that reaches the app through one of them, or from a non-loopback address, without
	// the owner's bootstrap token is a phone: view-only and filtered by group access.
	PhoneHosts func() []string
}

// Caller identifies the connection behind a pool request.
type Caller struct {
	ConnectionID string
	Kind         protocol.ClientKind
	Remote       string
	// Owner: the connection presented this host's token on /ws (the owner's app). A unix-socket
	// connection, where agents' CLI calls arrive, never is, whatever kind it claims.
	Owner bool
	// Phone: the owner's phone (reached the app without the token, through the phone path).
	Phone bool
	// Device: the paired Muse device name, when Phone came with a valid
	// device token on /api/muse/v1. Empty otherwise.
	Device string
	// Terminal: the owner's CLI on the unix socket (it sent the daemon's token in hello).
	Terminal bool
	// PeerPID is the process on the other end of a unix-socket connection (0 if unknown or not a
	// unix socket). An agent's CLI call is a descendant of its session's worker.
	PeerPID int
	// Unix: the connection came in on the unix socket, where agents' CLI calls arrive. A unix caller
	// whose peer process cannot be read is not taken as an agent.
	Unix bool
}

// Server is the daemon's gateway.
type Server struct {
	opts       Options
	logf       func(string, ...any)
	socketPath string
	nodeID     string
	epoch      string
	token      string

	// pollInterval is how often a subscription samples the store. Tests shorten it.
	pollInterval time.Duration
	// dialTimeout bounds one worker dial.
	dialTimeout time.Duration

	httpMu   sync.Mutex
	httpAddr string

	serveMu  sync.Mutex
	serving  bool
	serveCtx context.Context

	connMu sync.Mutex
	liveMu sync.Mutex
	live   map[*conn]struct{}
	conns  map[io.Closer]struct{}
}

// New validates the options, mints this daemon's epoch and bootstrap token, and writes the token
// to <store>/run/hub.token with mode 0600.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("gateway: Options.Store is required")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	socketPath := opts.SocketPath
	if socketPath == "" {
		socketPath = opts.Store.Path(store.RunDir, "gateway.sock")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:         opts,
		logf:         logf,
		socketPath:   socketPath,
		nodeID:       opts.Store.NodeID(),
		epoch:        protocol.NewID(),
		token:        token,
		pollInterval: time.Second,
		dialTimeout:  10 * time.Second,
		serveCtx:     context.Background(),
	}
	if err := s.writeToken(); err != nil {
		return nil, err
	}
	return s, nil
}

// SocketPath is where the unix listener listens (or listened).
func (s *Server) SocketPath() string { return s.socketPath }

// HTTPAddr is the bound HTTP address, and only that: it is empty until Serve has a live listener,
// and stays empty when the bind fails. A caller polling it for "am I listening yet?" can trust it.
func (s *Server) HTTPAddr() string {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	return s.httpAddr
}

// Epoch is this daemon's start epoch, carried on every state snapshot and delta.
func (s *Server) Epoch() string { return s.epoch }

// Token is the bootstrap token /ws requires. It is also on disk at <store>/run/hub.token.
func (s *Server) Token() string { return s.token }

// Serve listens on the unix socket and the HTTP address and runs until ctx is cancelled or a
// listener fails. It is safe to call once.
func (s *Server) Serve(ctx context.Context) error {
	s.serveMu.Lock()
	if s.serving {
		s.serveMu.Unlock()
		return errors.New("gateway: Serve called twice")
	}
	s.serving = true
	ctx, cancel := context.WithCancel(ctx)
	s.serveCtx = ctx
	s.serveMu.Unlock()
	defer func() {
		s.serveMu.Lock()
		s.serving = false
		s.serveMu.Unlock()
	}()
	defer cancel()

	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return fmt.Errorf("gateway: socket directory: %w", err)
	}
	if err := takeOverSocket(s.socketPath); err != nil {
		return err
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", s.socketPath, err)
	}
	defer ln.Close()
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return fmt.Errorf("gateway: chmod %s: %w", s.socketPath, err)
	}
	defer os.Remove(s.socketPath)

	addr := s.opts.HTTPAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	addr = normalizeHTTPAddr(addr)
	httpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", addr, err)
	}
	defer httpLn.Close()
	s.setHTTPAddr(httpLn.Addr().String())

	hsrv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs <- s.acceptLoop(ctx, ln) }()
	go func() {
		defer wg.Done()
		if err := hsrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errs:
	}

	ln.Close()
	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	_ = hsrv.Shutdown(shutdownCtx)
	sc()
	s.closeConns()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	return serveErr
}

func (s *Server) setHTTPAddr(addr string) {
	s.httpMu.Lock()
	s.httpAddr = addr
	s.httpMu.Unlock()
}

func (s *Server) writeToken() error {
	path := s.opts.Store.Path(store.RunDir, TokenFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("gateway: token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(s.token+"\n"), 0o600); err != nil {
		return fmt.Errorf("gateway: write %s: %w", path, err)
	}
	return nil
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("gateway: bootstrap token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// normalizeHTTPAddr rewrites a listen address with an empty host (":7792") to loopback. An
// address that asked for no host must not bind every interface: a caller that wants the LAN passes
// an explicit wildcard address, which is exactly what --lan does.
func normalizeHTTPAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "" {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// takeOverSocket prepares path for a unix listener. A leftover socket that refuses a connection is
// stale and removed; a socket a live listener answers is never unlinked, because removing it would
// cut the running daemon off from clients that already know the path. Anything that is not a
// socket is refused.
func takeOverSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("gateway: %s exists and is not a socket", path)
	}
	// Dial first: only a socket that refuses the connection may be replaced.
	c, derr := net.DialTimeout("unix", path, time.Second)
	if derr == nil {
		c.Close()
		return fmt.Errorf("gateway: %s is in use by a live listener", path)
	}
	if !errors.Is(derr, syscall.ECONNREFUSED) && !errors.Is(derr, syscall.ENOENT) {
		return fmt.Errorf("gateway: %s: cannot prove the socket is stale: %w", path, derr)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("gateway: accept: %w", err)
		}
		go s.handleNetConn(ctx, c)
	}
}

func (s *Server) handleNetConn(ctx context.Context, c net.Conn) {
	if err := checkPeerUID(c); err != nil {
		s.logf("gateway: refusing unix peer %s: %v", c.RemoteAddr(), err)
		c.Close()
		return
	}
	untrack := s.track(c)
	defer untrack()
	defer c.Close()
	s.handleConn(ctx, newNetFrameConn(c), c.RemoteAddr().String(), false, false, peerPID(c), isUnixConn(c))
}

// handle runs one client connection to completion.
func (s *Server) handle(ctx context.Context, fc frameConn, remote string) {
	s.handlePhone(ctx, fc, remote, false, false)
}

// phoneRequest reports a tokenless request a phone may make: on a PhoneHosts name (the Tailscale
// Serve name, or this machine's own tailnet name; Serve's proxy arrives from loopback with that
// Host), or from another host on a LAN listener. Either way the connection is view-only.
func (s *Server) phoneRequest(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if s.opts.PhoneHosts != nil {
		for _, h := range s.opts.PhoneHosts() {
			if h != "" && strings.EqualFold(h, host) {
				return true
			}
		}
	}
	ra, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(ra)
	return err == nil && ip != nil && !ip.IsLoopback()
}

func (s *Server) handlePhone(ctx context.Context, fc frameConn, remote string, phone, owner bool) {
	s.handleConn(ctx, fc, remote, phone, owner, 0, false)
}

func isUnixConn(c net.Conn) bool { _, ok := c.(*net.UnixConn); return ok }

func (s *Server) handleConn(ctx context.Context, fc frameConn, remote string, phone, owner bool, peerPID int, unix bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := newConn(s, fc, remote)
	c.phone, c.owner, c.peerPID, c.unix = phone, owner && !phone, peerPID, unix
	defer c.teardown()
	// Unblock a read parked inside the frame connection when the connection's context ends.
	go func() {
		select {
		case <-ctx.Done():
			fc.Close()
		case <-c.done:
		}
	}()
	c.serve(ctx)
}

// newConn is the only way to build a conn, so done is never nil and teardown can never panic.
func newConn(s *Server, fc frameConn, remote string) *conn {
	c := &conn{
		srv:    s,
		fc:     fc,
		remote: remote,
		id:     protocol.NewID(),
		done:   make(chan struct{}),
	}
	s.liveMu.Lock()
	if s.live == nil {
		s.live = map[*conn]struct{}{}
	}
	s.live[c] = struct{}{}
	s.liveMu.Unlock()
	return c
}

// Revoke detaches every live attachment the match selects (access withdrawn).
func (s *Server) Revoke(match func(c Caller, sessionID string) bool) {
	s.liveMu.Lock()
	conns := make([]*conn, 0, len(s.live))
	for c := range s.live {
		conns = append(conns, c)
	}
	s.liveMu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		att := c.att
		c.mu.Unlock()
		if att != nil && match(c.caller(), att.sessionID) {
			c.mu.Lock()
			if c.att == att {
				c.att = nil
			}
			c.mu.Unlock()
			att.close()
			c.sendError("", protocol.Errorf(protocol.CodeForbidden, "access to session %s was withdrawn", att.sessionID))
		}
	}
}

// conn is one client connection's state.
type conn struct {
	srv    *Server
	fc     frameConn
	remote string
	id     string
	kind   protocol.ClientKind

	done      chan struct{}
	closeOnce sync.Once

	wmu sync.Mutex

	phone bool // reached the app without the owner's token: always view-only
	owner bool // presented the owner's token on /ws
	// terminal: the owner's CLI on the unix socket (sent the token in hello): it may start and
	// stop sessions, nothing more.
	terminal bool
	peerPID  int
	unix     bool
	typed    typedText // what the owner typed since the last Enter (owner connections only)

	mu        sync.Mutex
	att       *attachment
	subCancel context.CancelFunc
	subGen    uint64
}

// connCloser wraps a close function so it can be a comparable registry key: a bare func type is
// unhashable and panics when used in a map.
type connCloser struct {
	close func() error
}

func (c *connCloser) Close() error { return c.close() }

func (s *Server) track(c io.Closer) func() {
	s.connMu.Lock()
	if s.conns == nil {
		s.conns = make(map[io.Closer]struct{})
	}
	s.conns[c] = struct{}{}
	s.connMu.Unlock()
	return func() {
		s.connMu.Lock()
		delete(s.conns, c)
		s.connMu.Unlock()
	}
}

func (s *Server) closeConns() {
	s.connMu.Lock()
	conns := s.conns
	s.conns = nil
	s.connMu.Unlock()
	for c := range conns {
		c.Close()
	}
}

func (c *conn) serve(ctx context.Context) {
	f, err := c.fc.ReadFrame()
	if err != nil {
		return
	}
	if !c.checkHello(f) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		default:
		}
		f, err := c.fc.ReadFrame()
		if err != nil {
			return
		}
		c.dispatch(ctx, f)
	}
}

func (c *conn) teardown() {
	// A nil done can only come from a construction path that bypassed newConn, and closing a nil
	// channel would take the whole daemon down; never let a dropped client do that.
	if c.done != nil {
		c.closeOnce.Do(func() { close(c.done) })
	}
	c.mu.Lock()
	att := c.att
	c.att = nil
	cancel := c.subCancel
	c.subCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if att != nil {
		att.close()
	}
	if c.srv.opts.OnClose != nil {
		c.srv.opts.OnClose(c.caller())
	}
	c.srv.liveMu.Lock()
	delete(c.srv.live, c)
	c.srv.liveMu.Unlock()
}

// checkHello enforces "hello must be the first frame" and answers with hello.ok.
func (c *conn) checkHello(f protocol.Frame) bool {
	if f.Header.Type != protocol.FrameControl {
		c.sendError(protocol.NewID(), protocol.Errorf(protocol.CodeUnsupported,
			"hello must be the first frame, got %s on channel %d", f.Header.Type, f.Header.Channel))
		return false
	}
	msg, err := protocol.DecodeControl(f)
	if err != nil {
		c.sendError(protocol.NewID(), err)
		return false
	}
	if msg.Type != protocol.TypeHello {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnsupported,
			"hello must be the first message, got %q", msg.Type))
		return false
	}
	if msg.V != protocol.Version {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeIncompatibleVersion,
			"protocol version %d is not supported, want %d", msg.V, protocol.Version))
		return false
	}
	// The kind is the connection's, not the client's own claim on a later request: the worker
	// relies on it to keep an ios client view-only.
	if msg.Client != nil {
		c.kind = msg.Client.Kind
		c.terminal = msg.Client.Token != "" && subtle.ConstantTimeCompare([]byte(msg.Client.Token), []byte(c.srv.token)) == 1
		if c.terminal && c.unix && c.peerPID > 0 && c.srv.opts.InSession != nil && c.srv.opts.InSession(c.peerPID) {
			c.terminal = false // the token, but from inside an agent: not the owner's
		}
	}
	if c.phone {
		c.kind = protocol.KindIOS
	}
	ok := &protocol.Message{
		ID:           msg.ID,
		Type:         protocol.TypeHelloOK,
		V:            protocol.Version,
		NodeID:       c.srv.nodeID,
		ConnectionID: c.id,
		Capabilities: protocol.Capabilities,
	}
	return c.writeControl(ok) == nil
}

func (c *conn) dispatch(ctx context.Context, f protocol.Frame) {
	if f.Header.Type != protocol.FrameControl {
		if f.Header.Type == protocol.FrameInput && c.owner && c.srv.opts.OwnerInput != nil {
			c.mu.Lock()
			att := c.att
			c.mu.Unlock()
			if att != nil {
				for _, text := range c.typed.feed(f.Payload) {
					c.srv.opts.OwnerInput(att.sessionID, text)
				}
			}
		}
		// With nothing attached the frame is dropped: it was typed into a pane whose session was just
		// closed or went. An error here has no session_id, so every other attached pane would show it.
		c.forward(f)
		return
	}
	msg, err := protocol.DecodeControl(f)
	if err != nil {
		c.sendError(protocol.NewID(), err)
		return
	}
	switch msg.Type {
	case protocol.TypeHello:
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnsupported, "hello was already received"))
	case protocol.TypePing:
		_ = c.writeControl(&protocol.Message{ID: msg.ID, Type: protocol.TypePong})
	case protocol.TypeStateSubscribe:
		c.subscribe(ctx, msg)
	case protocol.TypeSessionAttach:
		c.attach(ctx, msg)
	case protocol.TypeSessionDetach:
		c.detach(msg)
	case protocol.TypeSessionResize:
		c.forward(f) // nothing attached: a closed pane's last resize, dropped like its input above
	case protocol.TypeSessionList:
		c.list(msg)
	case protocol.TypeSessionCreate, protocol.TypeSessionKill:
		// Starting and stopping sessions is the owner's: the app, or the owner's terminal CLI.
		// Agents reach the same socket without the token.
		if !c.owner && !c.terminal {
			c.sendError(msg.ID, protocol.Errorf(protocol.CodeForbidden, "%s is for the owner, in the PenguPool app", msg.Type))
			return
		}
		if msg.Type == protocol.TypeSessionCreate {
			c.create(ctx, msg)
		} else {
			c.kill(ctx, msg)
		}
	default:
		if c.srv.opts.RPC == nil {
			c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnsupported, "unsupported message type %q", msg.Type))
			return
		}
		// Pool requests may wait (a permission approval, a provider call): never on the read loop.
		go c.rpc(ctx, msg)
	}
}

func (c *conn) caller() Caller {
	return Caller{ConnectionID: c.id, Kind: c.kind, Remote: c.remote, Owner: c.owner, Phone: c.phone, Terminal: c.terminal, PeerPID: c.peerPID, Unix: c.unix}
}

func (c *conn) rpc(ctx context.Context, msg *protocol.Message) {
	res, err := c.srv.opts.RPC(ctx, c.caller(), msg.Type, msg.Params)
	if err != nil {
		c.sendError(msg.ID, err)
		return
	}
	b, err := json.Marshal(res)
	if err != nil {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeInvalidArgument, "marshal result: %v", err))
		return
	}
	if err := c.writeControl(&protocol.Message{ID: msg.ID, Type: msg.Type, Result: b}); err != nil {
		c.srv.logf("gateway: %s: reply %s: %v", c.remote, msg.Type, err)
	}
}

func (c *conn) writeControl(msg *protocol.Message) error {
	f, err := controlFrame(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(f)
}

func (c *conn) writeFrame(f protocol.Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.fc.WriteFrame(f)
}

func (c *conn) sendError(id string, err error) {
	if id == "" {
		id = protocol.NewID()
	}
	if werr := c.writeControl(protocol.ErrorMessage(id, err)); werr != nil {
		c.srv.logf("gateway: %s: send error %q: %v", c.remote, protocol.CodeOf(err), werr)
	}
}

func controlFrame(msg *protocol.Message) (protocol.Frame, error) {
	if msg.ID == "" {
		msg.ID = protocol.NewID()
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return protocol.Frame{}, protocol.Errorf(protocol.CodeInvalidArgument, "marshal %q: %v", msg.Type, err)
	}
	if len(b) > protocol.MaxControlPayload {
		return protocol.Frame{}, protocol.Errorf(protocol.CodeFrameTooLarge, "control message %q is %d bytes", msg.Type, len(b))
	}
	return protocol.Frame{Header: protocol.Header{Type: protocol.FrameControl, Length: uint32(len(b))}, Payload: b}, nil
}

// handler builds the HTTP surface: static app at /, WebSocket at /ws, health at /api/healthz.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/healthz", s.handleHealthz)
	mux.HandleFunc("/ws", s.handleWS)
	s.workspaceRoutes(mux)
	s.museRoutes(mux)
	mux.HandleFunc("/", s.handleStatic)
	return s.hostGuard(mux)
}

// hostGuard rejects a request whose Host header is not the address we bound: a page on any other
// host (DNS rebinding) must never reach the daemon.
func (s *Server) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r, s.HTTPAddr()) && !s.phoneHost(r) && !tunneled(r) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// CORS for the desktop app's pages only; every API route still checks the token.
		if r.Header.Get("Origin") == DesktopOrigin {
			w.Header().Set("Access-Control-Allow-Origin", DesktopOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "X-PenguPool-Token, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) phoneHost(r *http.Request) bool {
	if s.opts.PhoneHosts == nil {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	for _, h := range s.opts.PhoneHosts() {
		if h != "" && strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// tunneled reports a request that arrived over an SSH port forward from the app on another machine:
// the connection comes from this host's loopback (sshd), and Host names a loopback address on
// whatever port the forward uses there. A DNS name is still refused, so rebinding stays out;
// the token still gates every route.
func tunneled(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(peer).IsLoopback() {
		return false
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostAllowed reports whether the request's Host may reach a listener bound at bound.
//
// The bound address with its port is always accepted. On a wildcard bind (0.0.0.0, ::) the only
// Hosts accepted are an IP literal — the address the user actually reached — and localhost, so a
// DNS name that resolves to this machine (DNS rebinding) is refused. A loopback bind is also
// reachable as localhost. Everything else, including another port, is refused.
func hostAllowed(r *http.Request, bound string) bool {
	if bound == "" {
		return true
	}
	boundHost, boundPort, err := net.SplitHostPort(bound)
	if err != nil {
		return false
	}
	rHost, rPort, err := net.SplitHostPort(r.Host)
	if err != nil || rPort != boundPort {
		return false
	}
	if strings.EqualFold(rHost, boundHost) {
		return true
	}
	ip := net.ParseIP(boundHost)
	if ip == nil {
		// A named bind address resolved by the listener matches only itself.
		return false
	}
	if ip.IsUnspecified() {
		return net.ParseIP(rHost) != nil || strings.EqualFold(rHost, "localhost")
	}
	if ip.IsLoopback() {
		return strings.EqualFold(rHost, "localhost")
	}
	return false
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	st := s.opts.Store.State()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": protocol.Version,
		"epoch":   s.epoch,
		"node_id": st.NodeID,
		"rev":     st.Rev,
		// owner: the caller holds this host's token, so a probe can tell its own store's host
		// from another one on the same port.
		"owner": subtle.ConstantTimeCompare([]byte(r.Header.Get("X-PenguPool-Token")), []byte(s.token)) == 1,
	}); err != nil {
		s.logf("gateway: healthz: %v", err)
	}
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.opts.Static == nil {
		http.NotFound(w, r)
		return
	}
	http.FileServer(http.FS(s.opts.Static)).ServeHTTP(w, r)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	phone := false
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get(TokenQuery)), []byte(s.token)) != 1 {
		if r.URL.Query().Get(TokenQuery) != "" || !s.phoneRequest(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		phone = true
	}
	if !originAllowed(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{r.Host, "pengupool"}})
	if err != nil {
		s.logf("gateway: ws upgrade from %s: %v", r.RemoteAddr, err)
		return
	}
	ws.SetReadLimit(int64(protocol.MaxControlPayload + protocol.HeaderSize))
	untrack := s.track(&connCloser{close: func() error {
		return ws.Close(websocket.StatusGoingAway, "server shutting down")
	}})
	defer untrack()
	defer ws.Close(websocket.StatusNormalClosure, "")

	s.serveMu.Lock()
	parent := s.serveCtx
	s.serveMu.Unlock()
	s.handlePhone(parent, &wsFrameConn{ws: ws, ctx: parent}, r.RemoteAddr, phone, !phone)
}

// DesktopOrigin is the origin of the PenguPool desktop app's own pages. The app loads its UI from
// its bundle, not from this listener; it still needs the owner's token on every request.
const DesktopOrigin = "app://pengupool"

// originAllowed accepts a missing Origin (non-browser clients) and, when present, requires the
// app's own origin: the scheme of the bound listener on the same host, or the desktop app.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == DesktopOrigin {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host == r.Host
}
