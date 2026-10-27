package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/term"
)

// detachCtrlBackslash and detachNone are the accepted --detach-key values.
const (
	detachCtrlBackslash = `ctrl-\`
	detachNone          = "none"
)

// parseDetachKey maps the CLI flag to a byte, or to -1 for "no detach key". Ctrl-\ is the default
// because both dtach and screen use it and because a raw-mode attach otherwise has no way out.
func parseDetachKey(s string) (byte, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case detachCtrlBackslash, "ctrl+\\":
		return 0x1c, nil
	case detachNone, "":
		return 0, nil
	default:
		return 0, fmt.Errorf("unknown detach key %q: use %s or %s", s, detachCtrlBackslash, detachNone)
	}
}

// cmdAttach implements `pengupool attach <session>`: raw passthrough with no status bar and no
// replacement screen. It forwards live PTY output unchanged, forwards input unchanged, owns the
// PTY size while attached, and restores the local terminal on detach, on error and on session exit.
func cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	noClaim := fs.Bool("no-claim", false, "do not take the terminal size lease")
	detachArg := fs.String("detach-key", detachCtrlBackslash, `key that detaches: "ctrl-\" or "none"`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := fs.Arg(0)
	if target == "" {
		return errors.New("usage: pengupool attach <session> [--no-claim] [--detach-key ctrl-\\|none]")
	}
	detachKey, err := parseDetachKey(*detachArg)
	if err != nil {
		return err
	}
	// The local terminal is resolved once: the reader and resize goroutines must not re-read the
	// process-wide os.Stdin on every iteration.
	in := os.Stdin
	if !term.IsTerminal(in) {
		return errors.New("attach needs a terminal on stdin")
	}

	// SIGINT and SIGQUIT are trapped as well as SIGTERM and SIGHUP: a signal death must restore the
	// user's terminal instead of leaving their shell in raw mode (review N4).
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()

	dir := storeDir(*storeFlag)
	c, err := dialDaemon(ctx, dir, protocol.KindCLI, "pengupool attach")
	if err != nil {
		return err
	}
	defer c.Close()

	sess, err := resolveSession(ctx, c, target)
	if err != nil {
		return err
	}

	cols, rows, err := term.Size(in)
	if err != nil || cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}

	attach := &protocol.Message{
		ID:         protocol.NewID(),
		Type:       protocol.TypeSessionAttach,
		SessionID:  sess.ID,
		Channel:    1, // the client picks the stream id and the server echoes it
		ClaimLease: !*noClaim,
		Cols:       cols,
		Rows:       rows,
		Kind:       string(protocol.KindCLI),
	}
	if err := c.Send(attach); err != nil {
		return err
	}

	// The first reply is the attach outcome. Read it before entering raw mode so a failure prints
	// on a normal terminal.
	var attached *protocol.Message
	for attached == nil {
		f, err := c.Next(ctx)
		if err != nil {
			return err
		}
		if f.Header.Type != protocol.FrameControl {
			continue
		}
		m, err := protocol.DecodeControl(f)
		if err != nil {
			return err
		}
		if m.Type == protocol.TypeError {
			return errors.New("attach refused: " + m.Code + ": " + m.Message)
		}
		if m.ID == attach.ID && m.Type == protocol.TypeSessionAttached {
			attached = m
		}
	}

	local, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	// Restore on every path out, including a panic in the reader loop.
	defer func() {
		if err := local.Restore(); err != nil {
			fmt.Fprintf(os.Stderr, "pengupool: restoring the terminal failed: %v\n", err)
		}
	}()

	channel := attached.Channel
	if channel == 0 {
		channel = 1
	}
	fmt.Fprintf(os.Stderr, "pengupool: attached to %s (%s) %dx%d, %s; detach with %s\r\n",
		shortID(sess.ID), sess.Name, attached.Cols, attached.Rows, attached.Mode, detachKeyLabel(*detachArg))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The size lease and the terminal-query responder role go together: only the holder resizes the
	// PTY, and only the holder forwards its own terminal's query replies. This client claims the
	// lease at attach unless --no-claim; lease.changed tells it whenever that changes
	// (docs/protocol/v1.md section 4).
	var leaseHeld atomic.Bool
	leaseHeld.Store(!*noClaim)
	filter := newQueryFilter()

	var outMu sync.Mutex
	writeOut := func(b []byte) error {
		outMu.Lock()
		defer outMu.Unlock()
		_, err := os.Stdout.Write(b)
		return err
	}

	// Local input → session. The only byte the client consumes is the detach key, the documented
	// exception to pass-through. While this client holds the lease, bytes pass through unchanged
	// because it is the responder. While it does not, the lease holder answers the terminal's
	// queries and this client's own terminal's replies (DA1/DA2, DECRQM, cursor position, focus,
	// in-band resize) are dropped rather than injected into agent input a second time (review R1).
	detached := make(chan struct{})
	go func() {
		defer close(detached)
		held := leaseHeld.Load()
		forward := func(p []byte) bool {
			if len(p) == 0 {
				return true
			}
			f := protocol.Frame{
				Header:  protocol.Header{Type: protocol.FrameInput, Channel: channel, Length: uint32(len(p))},
				Payload: p,
			}
			return c.WriteFrame(f) == nil
		}
		send := func(p []byte) bool {
			if leaseHeld.Load() {
				if !held {
					// This client just took the lease: the bytes held back as a non-responder are
					// now ours to send.
					if !forward(filter.flush()) {
						return false
					}
				}
				held = true
				return forward(p)
			}
			held = false
			return forward(filter.push(p))
		}

		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if detachKey != 0 {
					if idx := indexByte(chunk, detachKey); idx >= 0 {
						if !send(chunk[:idx]) {
							return
						}
						forward(filter.flush())
						_ = c.Send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionDetach})
						return
					}
				}
				if !send(chunk) {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// A closed `detached` means stdin ended or the detach key was pressed: the client stops reading
	// input and the frame loop unblocks through the cancelled context.
	go func() {
		<-detached
		cancel()
	}()

	// Resize: only the lease holder owns the PTY size, and a non-owner's resize is refused with
	// `forbidden`. A client that does not hold the lease therefore skips the resize instead of
	// provoking an error, and takes the lease up again only through lease.changed (review K10).
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	defer signal.Stop(resizes)
	go func() {
		for range resizes {
			if !leaseHeld.Load() {
				continue
			}
			cols, rows, err := term.Size(in)
			if err != nil {
				continue
			}
			c.Send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionResize, SessionID: sess.ID, Cols: cols, Rows: rows})
		}
	}()

	// Every output chunk written to the local terminal is acknowledged with the next expected offset
	// (the frame's offset plus its length), batched per chunk, so the worker never pauses this
	// client at its 256 KiB unacknowledged high-water mark — the freeze the CLI used to hit
	// (docs/protocol/v1.md section 7, review K1).
	ackOffset := attached.Offset
	sendAck := func() error {
		f := protocol.Frame{Header: protocol.Header{Type: protocol.FrameAck, Channel: channel, Seq: ackOffset}}
		return c.WriteFrame(f)
	}

	// Snapshot chunks are held until snapshot.end so a partial snapshot is never painted.
	var (
		snapshot  []byte
		inSnap    bool
		snapParts int
		snapSeen  int
		exited    bool
		exitCode  *int
	)
	for {
		select {
		case <-detached:
			fmt.Fprintf(os.Stderr, "\r\npengupool: detached; session %s is still running\r\n", shortID(sess.ID))
			return nil
		default:
		}

		f, err := c.Next(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "\r\npengupool: detached; session %s is still running\r\n", shortID(sess.ID))
				return nil
			}
			return err
		}
		switch f.Header.Type {
		case protocol.FrameOutput:
			if inSnap {
				return fmt.Errorf("protocol: live output arrived during snapshot %d", snapParts)
			}
			if err := writeOut(f.Payload); err != nil {
				return err
			}
			ackOffset = f.Header.Seq + uint64(len(f.Payload))
			if err := sendAck(); err != nil {
				return err
			}
		case protocol.FrameSnapshot:
			if !inSnap {
				return errors.New("protocol: snapshot chunk without snapshot.begin")
			}
			if int(f.Header.Seq) != snapSeen {
				return fmt.Errorf("protocol: snapshot chunk %d out of order, expected %d", f.Header.Seq, snapSeen)
			}
			snapSeen++
			snapshot = append(snapshot, f.Payload...)
		case protocol.FrameControl:
			m, err := protocol.DecodeControl(f)
			if err != nil {
				return err
			}
			switch m.Type {
			case protocol.TypeSnapshotBegin:
				inSnap, snapSeen, snapParts, snapshot = true, 0, m.Parts, nil
				// Live output resumes at the snapshot's offset.
				ackOffset = m.Offset
			case protocol.TypeSnapshotEnd:
				if !inSnap {
					return errors.New("protocol: snapshot.end without snapshot.begin")
				}
				inSnap = false
				if err := writeOut(snapshot); err != nil {
					return err
				}
				snapshot = nil
			case protocol.TypeSessionExit:
				// Any session.exit ends the attach, whatever the code: a child killed by a signal
				// is reported as 128+signal, which the old `exit >= 0` check ignored, leaving the
				// client running and the terminal in raw mode (review K9).
				exited = true
				exitCode = m.ExitCode
			case protocol.TypeLeaseChanged:
				// The worker names the size-lease holder; `owner` is set in the holder's own copy.
				leaseHeld.Store(m.Owner)
			case protocol.TypeError:
				// An error reply (a resize refused with `forbidden` when the lease moved away, say)
				// must never end the attach.
				fmt.Fprintf(os.Stderr, "\r\npengupool: session error: %s: %s\r\n", m.Code, m.Message)
			case protocol.TypePing:
				c.Send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypePong})
			}
		}
		if exited {
			if err := local.Restore(); err != nil {
				return err
			}
			if exitCode != nil {
				fmt.Fprintf(os.Stderr, "pengupool: session %s exited with code %s\n", shortID(sess.ID), describeExit(*exitCode))
			} else {
				fmt.Fprintf(os.Stderr, "pengupool: session %s exited\n", shortID(sess.ID))
			}
			return nil
		}
	}
}

// describeExit renders a worker-reported exit code, naming the signal when the child died from one
// (the worker reports 128+signal).
func describeExit(code int) string {
	if code > 128 {
		if sig := syscall.Signal(code - 128); sig > 0 {
			return fmt.Sprintf("%d (signal %d: %s)", code, code-128, sig)
		}
	}
	return strconv.Itoa(code)
}

func detachKeyLabel(flagValue string) string {
	if strings.EqualFold(flagValue, detachNone) || flagValue == "" {
		return "disabled (--detach-key none)"
	}
	return `ctrl-\`
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
