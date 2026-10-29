package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/vt"
)

// Flow control, chunking and shutdown constants from docs/protocol/v1.md (sections 1, 4 and 5)
// and from the M1 data-flow rules.
const (
	// highWaterBytes is the number of unacknowledged output bytes that pauses a client.
	highWaterBytes = 256 << 10
	// lowWaterBytes is the unacknowledged level at which a paused client resumes.
	lowWaterBytes = 32 << 10
	// maxOutputPayload is the largest output frame payload.
	maxOutputPayload = int(protocol.MaxDataPayload)
	// maxSnapshotPayload is the largest snapshot chunk payload.
	maxSnapshotPayload = int(protocol.MaxSnapshotPayload)
	// maxResize is the largest accepted PTY size. It keeps cols/rows inside the uint16 the window
	// size ioctl takes and refuses absurd geometry from a hostile client.
	maxResize = 1000
	// readChunk is the buffer size of one PTY master read.
	readChunk = 32 << 10
	// flushTimeout bounds the wait for a connection's queued frames while the worker shuts down.
	flushTimeout = 250 * time.Millisecond
	// shutdownGrace bounds the wait for connection goroutines to unwind at shutdown.
	shutdownGrace = 2 * time.Second
	// termGrace is how long a signalled worker waits for its child to be reaped before it is
	// killed outright.
	termGrace = 2 * time.Second
	// outputDrainGrace: after the child exits, session.exit waits until the PTY read loop has
	// appended every byte the child left behind (EOF), or has waited this long on an empty PTY (a
	// grandchild holds the slave open), so session.exit is the last thing a client sees.
	outputDrainGrace = 250 * time.Millisecond
	// outputDrainMax bounds that wait while a grandchild keeps writing.
	outputDrainMax = 5 * time.Second
	// ptyInQueue is how many client input frames may wait for the PTY writer goroutine.
	ptyInQueue = 32
	// maxQueuedBytes is how many bytes one connection may have queued or in flight on its socket
	// before the worker gives up on it. The pause rule stops a non-acknowledging client long
	// before this; the cap is what keeps one viewer from growing the worker without bound when the
	// pause path cannot apply (a snapshot bigger than the pause watermark, a client lying about
	// what it has accepted).
	maxQueuedBytes = 2 << 20
)

// WorkerConfig configures RunWorker.
type WorkerConfig struct {
	Spec   LaunchSpec // from paths.go; used when SpecFD < 3
	SpecFD int        // read the LaunchSpec JSON from this fd (3) and close it
	Logf   func(format string, args ...any)
}

// RunWorker owns one PTY session: it starts Spec.Command inside a Spec.Cols x Spec.Rows pty in
// Spec.Dir, listens on Spec.SocketPath, publishes the handshake, and serves protocol clients until
// the child has exited and the last attached client has detached. It then removes the socket and
// the handshake and returns nil (or the cancellation/signal cause when it was asked to stop).
//
// RunWorker is the worker process' main loop: the hub re-executes the daemon binary with
// "worker --session-id <id>" and hands the launch spec over file descriptor 3.
func RunWorker(ctx context.Context, cfg WorkerConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	spec, err := loadSpec(cfg)
	if err != nil {
		return err
	}

	w := &worker{
		spec:            spec,
		logf:            logf,
		workerStartedAt: time.Now().UTC(),
		clients:         make(map[*client]struct{}),
		attached:        make(map[*client]struct{}),
		done:            make(chan struct{}),
		reapedCh:        make(chan struct{}),
		ptyDone:         make(chan struct{}),
	}
	if err := w.start(); err != nil {
		return err
	}
	logf("session %s: worker pid %d listening on %s (%dx%d)", spec.SessionID, os.Getpid(), spec.SocketPath, spec.Cols, spec.Rows)

	go w.acceptLoop()
	go w.readPTY()
	w.ptyWG.Add(1)
	go w.ptyWriter()
	go w.waitChild()
	go w.watchSignals()
	go func() {
		select {
		case <-ctx.Done():
			w.terminate(ctx.Err())
		case <-w.done:
		}
	}()

	<-w.done
	return w.finish()
}

// loadSpec reads the launch spec from file descriptor SpecFD when the hub passed one, and
// normalizes whichever spec it ends up with.
func loadSpec(cfg WorkerConfig) (LaunchSpec, error) {
	spec := cfg.Spec
	if cfg.SpecFD >= 3 {
		f := os.NewFile(uintptr(cfg.SpecFD), "launch-spec")
		if f == nil {
			return spec, fmt.Errorf("invalid launch spec file descriptor %d", cfg.SpecFD)
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return spec, fmt.Errorf("read launch spec from fd %d: %w", cfg.SpecFD, err)
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			return spec, fmt.Errorf("decode launch spec from fd %d: %w", cfg.SpecFD, err)
		}
	}
	spec, err := spec.Normalize()
	if err != nil {
		return spec, fmt.Errorf("invalid launch spec: %w", err)
	}
	return spec, nil
}

// worker is one PTY session. Everything it shares with its connection goroutines lives under mu.
type worker struct {
	spec LaunchSpec
	logf func(string, ...any)

	cmd             *exec.Cmd
	ptmx            *os.File
	startedAt       time.Time // when the PTY child started
	workerStartedAt time.Time // when this worker process started

	// ptyIn carries client input and responses carries the emulator's query replies to ptyWriter,
	// the one goroutine that writes to the PTY master.
	ptyIn     chan []byte
	responses <-chan []byte

	ln net.Listener

	connWG sync.WaitGroup // connection goroutines, waited for on shutdown
	ptyWG  sync.WaitGroup // the PTY writer goroutine

	done     chan struct{} // closed once the worker starts shutting down
	reapedCh chan struct{} // closed once the child has been waited for
	ptyDone  chan struct{} // closed once the PTY read loop has stopped
	// readWait is when the PTY read loop began waiting in Read (unix nanoseconds), 0 while it is
	// appending what it read: the PTY is drained once it has waited a while.
	readWait atomic.Int64

	mu       sync.Mutex
	ring     *Ring
	screen   vt.Screen
	gen      uint64 // session generation: bumped at PTY start and on every applied resize
	cols     int
	rows     int
	clients  map[*client]struct{} // every live connection
	attached map[*client]struct{} // connections with an active session.attach
	owner    *client              // holder of the size-lease, or nil
	exited   bool
	reaped   bool
	exitCode int
	exitedAt time.Time
	closed   bool
	cause    error
}

// start opens the PTY, starts the child, publishes the socket (0600) and the handshake.
func (w *worker) start() error {
	if w.spec.Cols > 0xffff || w.spec.Rows > 0xffff {
		return fmt.Errorf("session %s: bad pty size %dx%d", w.spec.SessionID, w.spec.Cols, w.spec.Rows)
	}
	if w.spec.Dir != "" {
		st, err := os.Stat(w.spec.Dir)
		if err != nil {
			return fmt.Errorf("session %s: working directory: %w", w.spec.SessionID, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("session %s: working directory %s is not a directory", w.spec.SessionID, w.spec.Dir)
		}
	}

	cmd := exec.Command(w.spec.Command[0], w.spec.Command[1:]...)
	cmd.Dir = w.spec.Dir
	cmd.Env = workerEnv(w.spec.Env)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(w.spec.Cols), Rows: uint16(w.spec.Rows)})
	if err != nil {
		return fmt.Errorf("session %s: start pty: %w", w.spec.SessionID, err)
	}
	w.cmd = cmd
	w.ptmx = ptmx
	w.startedAt = time.Now().UTC()
	w.ring = NewRing(w.spec.RingBytes)
	w.screen = vt.New(w.spec.Cols, w.spec.Rows)
	w.ptyIn = make(chan []byte, ptyInQueue)
	w.responses = w.screen.Responses()
	w.cols, w.rows = w.spec.Cols, w.spec.Rows

	if err := w.listen(); err != nil {
		w.stopChild()
		ptmx.Close()
		return err
	}
	if err := w.writeHandshake(); err != nil {
		w.ln.Close()
		os.Remove(w.spec.SocketPath)
		w.stopChild()
		ptmx.Close()
		return err
	}
	return nil
}

// workerEnv builds the child environment: the spec's when it carries one, otherwise the worker's,
// with TERM defined so terminal-aware agents behave.
func workerEnv(specEnv []string) []string {
	env := make([]string, 0, len(specEnv)+2)
	if len(specEnv) == 0 {
		env = append(env, os.Environ()...)
	} else {
		env = append(env, specEnv...)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}

// listen unlinks a stale socket and listens on it, owner-only.
func (w *worker) listen() error {
	sock := w.spec.SocketPath
	if dir := filepath.Dir(sock); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("session %s: create run dir %s: %w", w.spec.SessionID, dir, err)
		}
	}
	os.Remove(sock) // a worker that died without cleanup leaves a dead socket behind
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("session %s: listen: %w", w.spec.SessionID, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("session %s: chmod socket: %w", w.spec.SessionID, err)
	}
	w.ln = ln
	return nil
}

// handshakePath is the session's adoption file, next to its socket.
func (w *worker) handshakePath() string {
	return filepath.Join(filepath.Dir(w.spec.SocketPath), w.spec.SessionID+".json")
}

// writeHandshake writes the adoption file. It is written once the PTY and socket are live,
// refreshed on every applied resize, and refreshed again with the child's exit code when the child
// is gone, so a daemon can pick up the current size and record an exit that happened while it was
// down.
func (w *worker) writeHandshake() error {
	if err := WriteHandshake(w.handshakePath(), w.handshake()); err != nil {
		return fmt.Errorf("session %s: write handshake: %w", w.spec.SessionID, err)
	}
	return nil
}

// handshake builds the adoption file's contents from the worker's current state.
func (w *worker) handshake() Handshake {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := Handshake{
		SessionID:       w.spec.SessionID,
		Pid:             os.Getpid(),
		StartedAt:       w.startedAt,
		WorkerStartedAt: w.workerStartedAt,
		Protocol:        protocol.Version,
		Cols:            w.cols,
		Rows:            w.rows,
		Socket:          w.spec.SocketPath,
		Cwd:             w.spec.Dir,
		Command:         w.spec.Command,
		Name:            w.spec.Name,
	}
	if w.exited {
		code := w.exitCode
		h.ExitCode = &code
		h.EndedAt = w.exitedAt
	}
	return h
}

func (w *worker) acceptLoop() {
	for {
		conn, err := w.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			w.logf("session %s: accept: %v", w.spec.SessionID, err)
			return
		}
		go w.serve(conn)
	}
}

// readPTY is the only reader of the PTY master. It appends to the ring and the screen and never
// writes to the PTY, so neither a slow client nor a paste the agent is not reading can wedge it.
func (w *worker) readPTY() {
	defer close(w.ptyDone)
	buf := make([]byte, readChunk)
	for {
		w.readWait.Store(time.Now().UnixNano())
		n, err := w.ptmx.Read(buf)
		w.readWait.Store(0)
		if ptyReadDelay > 0 {
			time.Sleep(ptyReadDelay)
		}
		if n > 0 {
			w.onOutput(buf[:n])
		}
		if err != nil {
			if !w.isDone() {
				w.logf("session %s: pty read: %v", w.spec.SessionID, err)
			}
			return
		}
	}
}

// onOutput appends PTY output to the ring and the screen under one lock — the same lock a snapshot
// and its offset are captured under — and wakes every connection pump.
func (w *worker) onOutput(p []byte) {
	w.mu.Lock()
	w.ring.Write(p)
	w.screen.Write(p)
	for c := range w.clients {
		c.wakeUp()
	}
	w.mu.Unlock()
}

// ptyWriter is the only goroutine that writes to the PTY master. Client input and the emulator's
// query replies both arrive on channels and are serialized here, so exactly one goroutine owns the
// writes and the read loop never shares the descriptor (R3).
func (w *worker) ptyWriter() {
	defer w.ptyWG.Done()
	responses := w.responses
	for {
		select {
		case <-w.done:
			return
		case p := <-w.ptyIn:
			if _, err := w.ptmx.Write(p); err != nil {
				if !w.isDone() {
					w.logf("session %s: pty input: %v", w.spec.SessionID, err)
				}
				return
			}
		case resp, ok := <-responses:
			if !ok {
				responses = nil // the emulator is done producing replies
				continue
			}
			w.forwardResponse(resp)
		}
	}
}

// forwardResponse sends one emulator reply to the PTY. The mirror only answers a program query
// while nobody is attached: with a client attached that client's own terminal is the responder,
// and a second answer would leave the agent in the wrong keyboard mode (docs/protocol/v1.md
// section 4).
func (w *worker) forwardResponse(resp []byte) {
	if len(resp) == 0 {
		return
	}
	w.mu.Lock()
	silent := len(w.attached) > 0
	w.mu.Unlock()
	if silent {
		w.logf("session %s: dropping %d emulator response bytes, a client is attached", w.spec.SessionID, len(resp))
		return
	}
	if _, err := w.ptmx.Write(resp); err != nil && !w.isDone() {
		w.logf("session %s: pty response write: %v", w.spec.SessionID, err)
	}
}

// forwardInput hands client input to the PTY writer goroutine.
func (w *worker) forwardInput(p []byte) {
	if len(p) == 0 {
		return
	}
	select {
	case w.ptyIn <- p:
	case <-w.done:
	}
}

// waitChild reaps the agent and tells every attached client how it ended.
func (w *worker) waitChild() {
	err := w.cmd.Wait()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitCode(exitErr)
		} else {
			code = -1
			w.logf("session %s: child wait: %v", w.spec.SessionID, err)
		}
	}
	at := time.Now().UTC()

	// Let the PTY read loop append whatever the child left behind, so the exit message is the last
	// thing an attached client sees: until it reads EOF, or has waited outputDrainGrace on an empty
	// PTY. A grandchild holding the slave open cannot hold the worker hostage: the wait is bounded.
	w.drainOutput(time.Now().Add(outputDrainMax))

	w.mu.Lock()
	w.exited = true
	w.reaped = true
	w.exitCode = code
	w.exitedAt = at
	w.mu.Unlock()
	w.logf("session %s: child exited with code %d", w.spec.SessionID, code)

	// Leave the exit code where a daemon can read it even if it was not supervising this worker,
	// and do it before the worker can finish: reapedCh releases the shutdown path, and a client that
	// sees session.exit may disconnect at any moment.
	if err := w.writeHandshake(); err != nil {
		w.logf("session %s: handshake with the exit code: %v", w.spec.SessionID, err)
	}
	close(w.reapedCh)

	w.mu.Lock()
	for c := range w.attached {
		// The exit is queued behind everything the client is still owed, and only reaches it once
		// the pump has caught up (see client.queueExitLocked).
		c.queueExitLocked(w.spec.SessionID, code, at)
	}
	w.mu.Unlock()
	w.checkDone()
}

// drainOutput waits for the PTY read loop to finish appending the child's output, or to sit idle
// on an empty PTY for outputDrainGrace, or for the deadline.
func (w *worker) drainOutput(deadline time.Time) {
	tick := time.NewTicker(outputDrainGrace / 5)
	defer tick.Stop()
	for {
		select {
		case <-w.ptyDone:
			return
		case <-tick.C:
		}
		if since := w.readWait.Load(); since != 0 && time.Since(time.Unix(0, since)) >= outputDrainGrace || time.Now().After(deadline) {
			return
		}
	}
}

// ptyReadDelay slows the PTY read loop down by this much per read: tests only, to hold output
// back past the child's exit.
var ptyReadDelay time.Duration

// exitCode reports how a child ended the way a shell does: a child that died from a signal has no
// exit status, so 128+signal is reported instead of the -1 the process state carries.
func exitCode(exitErr *exec.ExitError) int {
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return exitErr.ExitCode()
}

// watchSignals turns SIGTERM/SIGINT into an orderly shutdown: the agent is stopped, attached
// clients still learn the exit code, and the socket is removed while the handshake stays for a
// daemon to read.
func (w *worker) watchSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(ch)
	select {
	case sig := <-ch:
		w.logf("session %s: got %s, stopping", w.spec.SessionID, sig)
		w.terminate(fmt.Errorf("session %s: stopped by %s", w.spec.SessionID, sig))
	case <-w.done:
	}
}

// terminate stops the child's process group, waits for it to be reaped so session.exit still
// reaches attached clients, and then shuts the worker down.
func (w *worker) terminate(cause error) {
	if w.isDone() {
		return
	}
	// Record why before the shutdown paths can race: the child exiting on this signal may report a
	// clean end first, and RunWorker must still return the real cause.
	w.mu.Lock()
	w.firstCause(cause)
	w.mu.Unlock()
	w.signalChild(syscall.SIGTERM)
	select {
	case <-w.reapedCh:
	case <-time.After(termGrace):
		w.signalChild(syscall.SIGKILL)
		select {
		case <-w.reapedCh:
		case <-time.After(termGrace):
		}
	}
	w.shutdown(cause)
}

// firstCause keeps the first real reason the worker stopped. It must be called with w.mu held; a nil
// cause (a clean end) never replaces one.
func (w *worker) firstCause(cause error) {
	if cause != nil && w.cause == nil {
		w.cause = cause
	}
}

// signalChild signals the agent's process group. pty.Start makes the child a session leader, so
// its process group id is its pid.
func (w *worker) signalChild(sig syscall.Signal) {
	if w.cmd == nil || w.cmd.Process == nil {
		return
	}
	pid := w.cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		w.logf("session %s: signal process group %d: %v", w.spec.SessionID, pid, err)
		_ = w.cmd.Process.Signal(sig)
	}
}

// stopChild kills an agent that has not been reaped yet.
func (w *worker) stopChild() {
	w.mu.Lock()
	reaped := w.reaped
	w.mu.Unlock()
	if reaped {
		return
	}
	w.signalChild(syscall.SIGKILL)
}

// checkDone shuts the worker down once the child is gone and no attached client is left.
func (w *worker) checkDone() {
	w.mu.Lock()
	done := w.exited && len(w.attached) == 0
	w.mu.Unlock()
	if done {
		w.shutdown(nil)
	}
}

// shutdown closes the listener and lets RunWorker finish. It is idempotent, and the first real
// cause wins: a clean end (the child gone and no client left) and a cancellation or signal race each
// other, and a clean end must never hide what actually stopped the worker.
func (w *worker) shutdown(cause error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.firstCause(cause)
	w.mu.Unlock()

	w.logf("session %s: shutting down", w.spec.SessionID)
	close(w.done)
	w.ln.Close()
}

func (w *worker) isDone() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// waitTimeout waits for wg, reporting whether it finished within d. A worker must never hang on a
// connection that refuses to die, so the wait is bounded.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// finish flushes what the connections still owe their clients, then removes the worker's socket and
// reports how the worker stopped. The handshake stays behind: it carries the child's exit code for
// a daemon that was not supervising this worker (the daemon removes it once recorded).
func (w *worker) finish() error {
	w.mu.Lock()
	clients := make([]*client, 0, len(w.clients))
	for c := range w.clients {
		clients = append(clients, c)
	}
	cause := w.cause
	w.mu.Unlock()

	for _, c := range clients {
		c.finish()
	}
	w.ptmx.Close()
	w.stopChild()
	if !waitTimeout(&w.connWG, shutdownGrace) {
		w.logf("session %s: connection goroutines did not unwind in %s", w.spec.SessionID, shutdownGrace)
	}
	if !waitTimeout(&w.ptyWG, shutdownGrace) {
		w.logf("session %s: the PTY writer did not unwind in %s", w.spec.SessionID, shutdownGrace)
	}
	if err := os.Remove(w.spec.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		w.logf("session %s: remove socket: %v", w.spec.SessionID, err)
	}
	w.logf("session %s: worker stopped", w.spec.SessionID)
	return cause
}
