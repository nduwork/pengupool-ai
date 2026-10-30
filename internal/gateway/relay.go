package gateway

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"

	"github.com/coder/websocket"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// frameConn is the transport under a client connection: a unix socket or a WebSocket. Both speak
// the same 17-byte-headed frames; only the byte stream differs.
type frameConn interface {
	ReadFrame() (protocol.Frame, error)
	WriteFrame(f protocol.Frame) error
	Close() error
}

// netFrameConn is a frameConn over a net.Conn (the unix socket).
func newNetFrameConn(c net.Conn) *netFrameConn {
	return &netFrameConn{conn: c}
}

type netFrameConn struct {
	conn net.Conn
}

func (c *netFrameConn) ReadFrame() (protocol.Frame, error) {
	return protocol.ReadFrame(c.conn)
}

func (c *netFrameConn) WriteFrame(f protocol.Frame) error {
	return protocol.WriteFrame(c.conn, f)
}

func (c *netFrameConn) Close() error { return c.conn.Close() }

// wsFrameConn is a frameConn over a WebSocket. Each frame goes out as one binary message; on the
// way in we accept either one frame per message or several frames streamed across messages, so a
// browser client is free to batch.
type wsFrameConn struct {
	ws  *websocket.Conn
	ctx context.Context

	wmu sync.Mutex
	buf []byte
}

func (c *wsFrameConn) ReadFrame() (protocol.Frame, error) {
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
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return protocol.Frame{}, err
		}
		if typ != websocket.MessageBinary {
			return protocol.Frame{}, protocol.Errorf(protocol.CodeUnsupported,
				"expected a binary message, got %v", typ)
		}
		c.buf = append(c.buf, data...)
	}
}

func (c *wsFrameConn) WriteFrame(f protocol.Frame) error {
	var buf bytes.Buffer
	buf.Grow(protocol.HeaderSize + len(f.Payload))
	if err := protocol.WriteFrame(&buf, f); err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.Write(c.ctx, websocket.MessageBinary, buf.Bytes())
}

func (c *wsFrameConn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// dialCode maps a manager error onto a stable protocol code: a coded protocol error keeps its
// code (the hub returns not_found for an unknown session), anything else is a worker we could not
// reach.
func dialCode(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return protocol.CodeUnavailable
}

// attachment is one client's relay to a worker. The worker owns replay, snapshots, the size lease
// and flow control; the gateway only moves frames and, when the client asked for a channel of its
// own, rewrites the channel field on the way through.
type attachment struct {
	sessionID string
	// attachID is the session.attach request id. The worker echoes it on its reply, so an error
	// carrying it means the worker refused the attach and the attachment must not survive it.
	attachID string

	mu         sync.Mutex // guards the channel mapping and worker
	clientChan uint32
	workerChan uint32
	worker     net.Conn

	wmu sync.Mutex // serializes writes to the worker
}

// send writes a frame to the worker, rewriting the channel if the client chose one.
func (a *attachment) send(f protocol.Frame) error {
	w := a.workerConn()
	if w == nil {
		return net.ErrClosed
	}
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return protocol.WriteFrame(w, a.toWorker(f))
}

// workerConn returns the worker connection, or nil once the relay has been dropped.
func (a *attachment) workerConn() net.Conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.worker
}

func (a *attachment) toWorker(f protocol.Frame) protocol.Frame {
	if f.Header.Type == protocol.FrameControl {
		return f
	}
	a.mu.Lock()
	client, worker := a.clientChan, a.workerChan
	a.mu.Unlock()
	if worker != 0 && client != 0 && client != worker && f.Header.Channel == client {
		f.Header.Channel = worker
	}
	return f
}

func (a *attachment) toClient(f protocol.Frame) protocol.Frame {
	if f.Header.Type == protocol.FrameControl {
		return f
	}
	a.mu.Lock()
	client, worker := a.clientChan, a.workerChan
	a.mu.Unlock()
	if worker != 0 && client != 0 && client != worker && f.Header.Channel == worker {
		f.Header.Channel = client
	}
	return f
}

// noteWorkerChannel records the channel the worker attached on; every data frame it sends uses it.
func (a *attachment) noteWorkerChannel(ch uint32) {
	if ch == 0 {
		return
	}
	a.mu.Lock()
	a.workerChan = ch
	a.mu.Unlock()
}

// clientChannel reports the channel the client is spoken to on. session.attach must name a nonzero
// channel, so this is always known.
func (a *attachment) clientChannel() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clientChan
}

func (a *attachment) close() {
	a.mu.Lock()
	w := a.worker
	a.worker = nil
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// forward relays a client frame to the attached worker. With nothing attached the frame is dropped.
func (c *conn) forward(f protocol.Frame) {
	c.mu.Lock()
	att := c.att
	c.mu.Unlock()
	if att == nil {
		return
	}
	if err := att.send(f); err != nil {
		c.srv.logf("gateway: %s: relay to session %s: %v", c.remote, att.sessionID, err)
		c.dropAttachment(att)
	}
}

func (c *conn) attach(ctx context.Context, msg *protocol.Message) {
	if msg.SessionID == "" {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeInvalidArgument, "session.attach needs session_id"))
		return
	}
	// The client picks a nonzero stream id and the server echoes it; channel 0 is control, so an
	// attach with no channel would leave the relay with nothing to map.
	if msg.Channel == 0 {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeInvalidArgument, "session.attach needs a nonzero channel"))
		return
	}
	if c.srv.opts.Sessions == nil {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnavailable, "no worker manager is configured"))
		return
	}
	c.mu.Lock()
	attached := c.att != nil
	c.mu.Unlock()
	if attached {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeConflict, "already attached to a session"))
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, c.srv.dialTimeout)
	worker, err := c.srv.opts.Sessions.Dial(dialCtx, msg.SessionID)
	cancel()
	if err != nil {
		c.sendError(msg.ID, protocol.Errorf(dialCode(err), "session %s: %v", msg.SessionID, err))
		return
	}

	// The worker decides who may hold the lease and who may type, and it does that from the
	// connection's hello kind — never from a field the client could set on this request.
	msg.Kind = string(c.kind)
	f, err := controlFrame(msg)
	if err != nil {
		worker.Close()
		c.sendError(msg.ID, err)
		return
	}

	a := &attachment{sessionID: msg.SessionID, attachID: msg.ID, clientChan: msg.Channel, worker: worker}
	c.mu.Lock()
	c.att = a
	c.mu.Unlock()

	// The hub has already completed the worker-hop hello; this is the client's session.attach,
	// forwarded with the connection's kind stamped on it and the request id intact, so the worker
	// can answer it and enforce who may hold the lease.
	if err := a.send(f); err != nil {
		c.dropAttachment(a)
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnavailable, "session %s: %v", msg.SessionID, err))
		return
	}
	go c.relayFromWorker(a)
}

// relayFromWorker copies the worker's frames to the client until the worker goes away.
func (c *conn) relayFromWorker(a *attachment) {
	worker := a.workerConn()
	if worker == nil {
		return
	}
	for {
		f, err := protocol.ReadFrame(worker)
		if err != nil {
			break
		}
		if f.Header.Type == protocol.FrameControl {
			msg, derr := protocol.DecodeControl(f)
			if derr == nil {
				if msg.Type == protocol.TypeError && msg.ID == a.attachID {
					// The worker refused the attach. Drop the attachment before the refusal
					// reaches the client, so a retry sent in response cannot race into a
					// conflict; do not keep an attachment the worker never accepted.
					c.dropAttachment(a)
					_ = c.writeFrame(a.toClient(f))
					return
				}
				if msg.Type == protocol.TypeSessionAttached {
					a.noteWorkerChannel(msg.Channel)
					// Speak to the client in the channel it chose: the worker's channel never
					// leaves this connection.
					if ch := a.clientChannel(); ch != msg.Channel {
						msg.Channel = ch
						if nf, ferr := controlFrame(msg); ferr == nil {
							f = nf
						}
					}
				}
			}
		}
		if err := c.writeFrame(a.toClient(f)); err != nil {
			break
		}
	}
	c.dropAttachment(a)
}

// dropAttachment releases the worker connection if a is still the live attachment.
func (c *conn) dropAttachment(a *attachment) {
	c.mu.Lock()
	if c.att == a {
		c.att = nil
	}
	c.mu.Unlock()
	a.close()
}

// detach handles session.detach: the worker is told, then the relay is dropped.
func (c *conn) detach(msg *protocol.Message) {
	c.mu.Lock()
	att := c.att
	c.mu.Unlock()
	if att == nil {
		// Already detached: the session was closed, or its worker went, and the relay dropped. The
		// pane's detach is then done, not an error, which every other attached pane would show.
		return
	}
	f, err := controlFrame(msg)
	if err == nil {
		_ = att.send(f)
	}
	c.dropAttachment(att)
}
