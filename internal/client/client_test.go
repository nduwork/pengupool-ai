package client_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/client"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

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

// startServer listens on a unix socket and hands back the server side of the accepted connection.
func startServer(t *testing.T) (path string, accept func() net.Conn) {
	t.Helper()
	path = filepath.Join(shortDir(t), "client.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()
	return path, func() net.Conn {
		select {
		case c := <-accepted:
			t.Cleanup(func() { c.Close() })
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("server never accepted a connection")
			return nil
		}
	}
}

func TestHelloHandshake(t *testing.T) {
	path, accept := startServer(t)
	c, err := client.Dial(context.Background(), path, protocol.KindCLI, "test-client")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	srv := accept()

	helloCh := make(chan *protocol.Message, 1)
	go func() {
		f, err := protocol.ReadFrame(srv)
		if err != nil {
			return
		}
		hello, err := protocol.DecodeControl(f)
		if err != nil {
			return
		}
		helloCh <- hello
		protocol.WriteControl(srv, &protocol.Message{
			ID:           hello.ID,
			Type:         protocol.TypeHelloOK,
			V:            protocol.Version,
			NodeID:       "node-1",
			ConnectionID: "conn-1",
			Capabilities: protocol.Capabilities,
		})
	}()

	ok, err := c.Hello()
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if ok.Type != protocol.TypeHelloOK || ok.NodeID != "node-1" || ok.ConnectionID != "conn-1" {
		t.Fatalf("hello.ok = %+v", ok)
	}
	if len(ok.Capabilities) != len(protocol.Capabilities) {
		t.Errorf("capabilities = %v", ok.Capabilities)
	}

	select {
	case hello := <-helloCh:
		if hello.Type != protocol.TypeHello {
			t.Fatalf("first frame type = %q, want %q", hello.Type, protocol.TypeHello)
		}
		if hello.V != protocol.Version {
			t.Errorf("hello v = %d, want %d", hello.V, protocol.Version)
		}
		if hello.Client == nil || hello.Client.Kind != protocol.KindCLI || hello.Client.Name != "test-client" {
			t.Errorf("hello client = %+v", hello.Client)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the hello")
	}
}

func TestCallReturnsErrorResponse(t *testing.T) {
	path, accept := startServer(t)
	c, err := client.Dial(context.Background(), path, protocol.KindCLI, "test-client")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	srv := accept()

	go func() {
		f, err := protocol.ReadFrame(srv)
		if err != nil {
			return
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			return
		}
		protocol.WriteControl(srv, protocol.ErrorMessage(msg.ID, protocol.Errorf(protocol.CodeNotFound, "session not attached")))
	}()

	req := &protocol.Message{Type: protocol.TypeStateSubscribe}
	resp, err := c.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Type != protocol.TypeError {
		t.Fatalf("response type = %q, want error", resp.Type)
	}
	if resp.ID != req.ID {
		t.Errorf("response id = %q, want %q", resp.ID, req.ID)
	}
	if resp.Code != protocol.CodeNotFound {
		t.Errorf("response code = %q, want %q", resp.Code, protocol.CodeNotFound)
	}
}

// TestCallQueuesInterleavedFrames: data frames that arrive while a Call waits for its response
// must not be lost; the next Next returns them.
func TestCallQueuesInterleavedFrames(t *testing.T) {
	path, accept := startServer(t)
	c, err := client.Dial(context.Background(), path, protocol.KindCLI, "test-client")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	srv := accept()

	go func() {
		f, err := protocol.ReadFrame(srv)
		if err != nil {
			return
		}
		msg, err := protocol.DecodeControl(f)
		if err != nil {
			return
		}
		// An output frame races ahead of the response.
		protocol.WriteFrame(srv, protocol.Frame{
			Header:  protocol.Header{Type: protocol.FrameOutput, Channel: 1, Seq: 0, Length: 5},
			Payload: []byte("early"),
		})
		protocol.WriteControl(srv, &protocol.Message{ID: msg.ID, Type: protocol.TypeStateSnapshot, Rev: 3})
	}()

	resp, err := c.Call(context.Background(), &protocol.Message{Type: protocol.TypeStateSubscribe})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Type != protocol.TypeStateSnapshot {
		t.Fatalf("response type = %q, want state.snapshot", resp.Type)
	}
	f, err := c.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if f.Header.Type != protocol.FrameOutput || string(f.Payload) != "early" || f.Header.Channel != 1 {
		t.Fatalf("queued frame = %+v payload %q", f.Header, f.Payload)
	}
}

func TestWriteFrameAndSend(t *testing.T) {
	path, accept := startServer(t)
	c, err := client.Dial(context.Background(), path, protocol.KindIOS, "phone")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	srv := accept()

	if err := c.Send(&protocol.Message{Type: protocol.TypePing}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := c.WriteFrame(protocol.Frame{
		Header:  protocol.Header{Type: protocol.FrameInput, Channel: 2, Seq: 0, Length: 3},
		Payload: []byte("abc"),
	}); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	ping, err := protocol.DecodeControl(mustRead(t, srv))
	if err != nil {
		t.Fatalf("decode ping: %v", err)
	}
	if ping.Type != protocol.TypePing || ping.ID == "" {
		t.Fatalf("ping = %+v", ping)
	}
	in := mustRead(t, srv)
	if in.Header.Type != protocol.FrameInput || in.Header.Channel != 2 || string(in.Payload) != "abc" {
		t.Fatalf("input frame = %+v payload %q", in.Header, in.Payload)
	}
}

func TestNextHonoursContext(t *testing.T) {
	path, accept := startServer(t)
	c, err := client.Dial(context.Background(), path, protocol.KindCLI, "test-client")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	accept() // the server says nothing

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, &protocol.Message{Type: protocol.TypePing}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call error = %v, want context.DeadlineExceeded", err)
	}
}

func TestCloseUnblocksNext(t *testing.T) {
	path, accept := startServer(t)
	c, err := client.Dial(context.Background(), path, protocol.KindCLI, "test-client")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	srv := accept()
	// The server closes its end right away.
	srv.Close()

	if _, err := c.Next(context.Background()); err == nil {
		t.Fatal("Next after peer close returned no error")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Next(context.Background()); err == nil {
		t.Fatal("Next after Close returned no error")
	}
}

func mustRead(t *testing.T, c net.Conn) protocol.Frame {
	t.Helper()
	f, err := protocol.ReadFrame(c)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

// TestDialStaleSocketFailsFast: a daemon that died left a socket file behind; a client must get an
// error, never block.
func TestDialStaleSocketFailsFast(t *testing.T) {
	path := filepath.Join(shortDir(t), "dead.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	done := make(chan error, 1)
	go func() {
		c, err := client.Dial(context.Background(), path, protocol.KindCLI, "cli")
		if c != nil {
			c.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dialing a stale socket succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dialing a stale socket blocked")
	}
}
