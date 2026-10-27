// Package client is a thin, context-aware frame connection over the PenguPool v1 wire protocol.
//
// It is the shared building block under the CLI, the desktop attach path and the tests of every
// other slice: dial a unix socket, do the hello handshake, then exchange protocol frames. It owns
// no policy — no reconnect, no snapshot assembly, no lease handling — because every caller of the
// daemon (CLI, browser, iOS) needs those to behave differently.
package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// Version is the client version reported in the hello exchange.
const Version = "0.1.0"

// Conn is one framed connection. A background goroutine reads frames off the socket so Next can
// wait on a context, and Close unblocks a reader from any goroutine.
type Conn struct {
	conn  net.Conn
	kind  protocol.ClientKind
	name  string
	token string // the owner's terminal: the daemon's token, read from its run directory

	frames chan protocol.Frame
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex
	err     error
	pending []protocol.Frame

	wmu sync.Mutex
}

// Dial opens a unix-socket connection to a daemon (or worker) at socketPath. It does not send
// hello; callers that want the handshake use Hello or Call.
func Dial(ctx context.Context, socketPath string, kind protocol.ClientKind, name string) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("pengupool client: dial %s: %w", socketPath, err)
	}
	c := New(nc, kind, name)
	// The owner's terminal proves itself with the daemon's token (run/hub.token beside the socket),
	// which lets it start and stop sessions. Inside an agent session the CLI never sends it.
	if os.Getenv("PENGUPOOL_SESSION_ID") == "" {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(socketPath), "hub.token")); err == nil {
			c.token = strings.TrimSpace(string(b))
		}
	}
	return c, nil
}

// New wraps an already-open stream (a unix socket, or an SSH bridge's stdio) as a frame connection.
func New(nc net.Conn, kind protocol.ClientKind, name string) *Conn {
	c := &Conn{
		conn:   nc,
		kind:   kind,
		name:   name,
		frames: make(chan protocol.Frame, 32),
		done:   make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Kind reports the client kind this connection announces in hello.
func (c *Conn) Kind() protocol.ClientKind { return c.kind }

// Hello performs the mandatory handshake and returns the server's hello.ok. A protocol refusal
// still comes back as a *protocol.Message of type error.
func (c *Conn) Hello() (*protocol.Message, error) {
	return c.Call(context.Background(), c.HelloMessage())
}

// HelloMessage builds the hello message this connection would send.
func (c *Conn) HelloMessage() *protocol.Message {
	return &protocol.Message{
		ID:     protocol.NewID(),
		Type:   protocol.TypeHello,
		V:      protocol.Version,
		Client: &protocol.ClientInfo{Kind: c.kind, Name: c.name, Version: Version, Token: c.token},
	}
}

// Send writes one control message on channel 0.
func (c *Conn) Send(m *protocol.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return protocol.WriteControl(c.conn, m)
}

// WriteFrame writes one raw frame, header and payload untouched.
func (c *Conn) WriteFrame(f protocol.Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return protocol.WriteFrame(c.conn, f)
}

// Next returns the next frame: frames read ahead while a Call waited for its response first, then
// frames off the wire. It returns ctx.Err() when the context is done and the connection's terminal
// error once the peer closes.
func (c *Conn) Next(ctx context.Context) (protocol.Frame, error) {
	if f, ok := c.popPending(); ok {
		return f, nil
	}
	return c.nextWire(ctx)
}

// nextWire reads the next frame straight off the wire, ignoring the read-ahead queue. Call uses it
// so a frame it pushes for Next can never be handed back to it — that would spin forever.
func (c *Conn) nextWire(ctx context.Context) (protocol.Frame, error) {
	select {
	case f := <-c.frames:
		return f, nil
	case <-c.done:
		// The reader has stopped: hand over anything it queued before reporting the error.
		select {
		case f := <-c.frames:
			return f, nil
		default:
			return protocol.Frame{}, c.terminalErr()
		}
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

// Call sends a control message and returns the control response that echoes its id. Frames that
// arrive in between (data frames, unrelated responses) are queued for Next.
func (c *Conn) Call(ctx context.Context, m *protocol.Message) (*protocol.Message, error) {
	if m.ID == "" {
		m.ID = protocol.NewID()
	}
	if err := c.Send(m); err != nil {
		return nil, err
	}
	for {
		f, err := c.nextWire(ctx)
		if err != nil {
			return nil, err
		}
		if f.Header.Type != protocol.FrameControl {
			c.pushPending(f)
			continue
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			return nil, err
		}
		if msg.ID == m.ID {
			return msg, nil
		}
		c.pushPending(f)
	}
}

// Close shuts the connection down and unblocks any reader waiting in Next.
func (c *Conn) Close() error {
	c.shutdown(net.ErrClosed)
	return nil
}

func (c *Conn) readLoop() {
	br := bufio.NewReader(c.conn)
	for {
		f, err := protocol.ReadFrame(br)
		if err != nil {
			c.shutdown(err)
			return
		}
		select {
		case c.frames <- f:
		case <-c.done:
			return
		}
	}
}

// shutdown records the first terminal error and closes the socket exactly once.
func (c *Conn) shutdown(err error) {
	c.mu.Lock()
	if c.err == nil && err != nil {
		c.err = err
	}
	c.mu.Unlock()
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

func (c *Conn) terminalErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return io.EOF
}

func (c *Conn) pushPending(f protocol.Frame) {
	c.mu.Lock()
	c.pending = append(c.pending, f)
	c.mu.Unlock()
}

func (c *Conn) popPending() (protocol.Frame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return protocol.Frame{}, false
	}
	f := c.pending[0]
	c.pending = c.pending[1:]
	return f, true
}
