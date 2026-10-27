package hub

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// writeTimeout bounds a single frame write to a worker, so a wedged worker cannot hang the hub.
const writeTimeout = 10 * time.Second

// conn is the hub's protocol connection to one worker socket. It is deliberately small: whoever
// holds it (Hub.Dial's caller relaying frames, or supervision) speaks the frozen frame protocol
// directly on it.
type conn struct {
	net.Conn
}

// dialWorker connects to a worker's unix socket and performs the worker-hop hello handshake, which
// the protocol requires to be the first frame on every connection. The connection it returns is
// ready for session traffic and has no deadline left on it.
func dialWorker(ctx context.Context, socketPath string) (*conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	nc, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	c := &conn{Conn: nc}
	hello := &protocol.Message{
		ID:     protocol.NewID(),
		Type:   protocol.TypeHello,
		V:      protocol.Version,
		Client: &protocol.ClientInfo{Kind: protocol.KindBridge, Name: "hub"},
	}
	if err := c.writeControl(hello); err != nil {
		nc.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	f, err := c.readFrame()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	m, err := protocol.DecodeControl(f)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	switch {
	case m.Type == protocol.TypeError:
		nc.Close()
		return nil, fmt.Errorf("worker refused hello: %s (%s)", m.Message, m.Code)
	case m.Type != protocol.TypeHelloOK:
		nc.Close()
		return nil, fmt.Errorf("worker answered %q to hello", m.Type)
	case m.V != protocol.Version:
		nc.Close()
		return nil, protocol.Errorf(protocol.CodeIncompatibleVersion,
			"worker speaks protocol %d, this daemon speaks %d", m.V, protocol.Version)
	}
	// Clear the deadline that guarded the handshake: the caller owns this connection now.
	if err := nc.SetWriteDeadline(time.Time{}); err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// writeFrame writes one frame, bounded by writeTimeout.
func (c *conn) writeFrame(f protocol.Frame) error {
	if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return protocol.WriteFrame(c.Conn, f)
}

// writeControl writes one control message on channel 0.
func (c *conn) writeControl(m *protocol.Message) error {
	if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return protocol.WriteControl(c.Conn, m)
}

// readFrame reads one frame. It blocks until the worker sends one or the connection ends.
func (c *conn) readFrame() (protocol.Frame, error) {
	return protocol.ReadFrame(c.Conn)
}
