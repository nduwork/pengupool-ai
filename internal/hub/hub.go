// Package hub owns the workers of one daemon.
//
// A worker is a separate process (internal/session's RunWorker) that owns one PTY, one agent
// process and one unix socket. The hub is the only component that starts one: it re-executes its
// own binary as `worker --session-id <id>` and hands the launch specification over a pipe on file
// descriptor 3, so the spec never appears in a process listing. Workers outlive the daemon; after
// a restart the hub adopts the ones whose handshake file is still there.
//
// For every worker that is running, the hub keeps one supervision connection: an ordinary
// protocol connection that attaches to the session, discards the screen bytes, acknowledges the
// output it drops and watches for session.exit. That is what keeps state.json's state, pid, cols,
// rows and exit_code current without polling the process table.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
	"github.com/nduwork/pengupool-ai/internal/vt"
)

// Session states recorded in state.json.
const (
	stateRunning = "running"
	stateExited  = "exited"
	stateLost    = "lost"
)

const (
	// handshakeTimeout is how long Spawn waits for a fresh worker's handshake file.
	handshakeTimeout = 5 * time.Second
	// pollInterval is how often the spawn path looks for the handshake file.
	pollInterval = 10 * time.Millisecond
	// killGrace is how long Kill waits after SIGTERM before resorting to SIGKILL.
	killGrace = 5 * time.Second
	// killTimeout bounds the wait after SIGKILL.
	killTimeout = 2 * time.Second
	// dialTimeout bounds one connect attempt to a worker socket.
	dialTimeout = 2 * time.Second
	// dialRetry is how long supervision retries a worker socket that is not accepting yet.
	dialRetry = 10 * time.Second
	// lostGrace is how long supervision waits for a worker that dropped the connection to
	// disappear before it declares the session lost.
	lostGrace = time.Second
	// gonePoll is how often the lost check re-reads the process table during that grace.
	gonePoll = 100 * time.Millisecond
	// superviseRetry is how long supervision waits before reconnecting to a worker that is still
	// recorded as running; it doubles up to superviseRetryMax.
	superviseRetry = 100 * time.Millisecond
	// superviseRetryMax caps that wait.
	superviseRetryMax = 5 * time.Second
	// sizeRefresh is how often supervision re-reads the handshake file, which the worker rewrites
	// when a client resizes the PTY.
	sizeRefresh = time.Second
	// supervisorChannel is the channel the hub's supervision attachment uses. Channels are labels
	// on one connection, so the number cannot collide with a client's choice.
	supervisorChannel uint32 = 0xffffffff
)

// Options configures a hub.
type Options struct {
	// Store is the daemon's store. Required.
	Store *store.Store
	// Executable is the pengupool binary the hub re-executes as `worker --session-id <id>`.
	// Required.
	Executable string
	// Logf receives human-readable progress and failure notes. Optional.
	Logf func(string, ...any)
}

// SpawnSpec is one request to start a worker.
type SpawnSpec struct {
	Name    string
	Cwd     string
	Harness string
	Command []string
	Cols    int
	Rows    int
	// ID, when set, is the session id to use (a harness launch needs it before the spawn).
	ID string
	// Env, when set, is the agent's whole environment.
	Env []string
	// Decorate sets pool fields on the new record before it is stored.
	Decorate func(*store.Session)
}

// Hub spawns, adopts and supervises the workers of one daemon.
type Hub struct {
	store      *store.Store
	executable string
	exePath    string
	nodeID     string
	logf       func(string, ...any)

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	started    bool
	closed     bool
	supervised map[string]bool
	// screens follows each supervised session's visible screen from the supervision stream, so the
	// pool can see what an agent shows (a dialog, a ready prompt) without attaching.
	screens map[string]vt.Screen
	wg      sync.WaitGroup
}

// New returns a hub that records sessions in opts.Store and starts workers from opts.Executable.
func New(opts Options) (*Hub, error) {
	if opts.Store == nil {
		return nil, errors.New("hub: Options.Store is required")
	}
	if strings.TrimSpace(opts.Executable) == "" {
		return nil, errors.New("hub: Options.Executable is required")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	h := &Hub{
		store:      opts.Store,
		executable: opts.Executable,
		exePath:    resolveExecutable(opts.Executable),
		nodeID:     opts.Store.NodeID(),
		logf:       logf,
		supervised: map[string]bool{},
		screens:    map[string]vt.Screen{},
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	return h, nil
}

// Start adopts the workers of a previous daemon and brings every running session under
// supervision. It returns once the store reflects reality; supervision keeps running in the
// background until ctx is done or Close is called.
func (h *Hub) Start(ctx context.Context) error {
	h.mu.Lock()
	switch {
	case h.closed:
		h.mu.Unlock()
		return protocol.Errorf(protocol.CodeUnavailable, "hub is closed")
	case h.started:
		h.mu.Unlock()
		return protocol.Errorf(protocol.CodeConflict, "hub is already started")
	}
	h.started = true
	h.mu.Unlock()

	// Start owns the hub's lifetime: when the daemon's context ends, supervision stops.
	go func() {
		select {
		case <-ctx.Done():
			h.cancel()
		case <-h.ctx.Done():
		}
	}()

	if err := h.reconcile(); err != nil {
		return err
	}
	for _, s := range h.List() {
		if s.State == stateRunning {
			h.superviseAsync(s.ID)
		}
	}
	return nil
}

// Spawn starts one worker and records it. It returns as soon as the worker's handshake file is
// there, i.e. once its PTY is live, or an error if that did not happen within five seconds.
func (h *Hub) Spawn(ctx context.Context, spec SpawnSpec) (store.Session, error) {
	var zero store.Session
	if len(spec.Command) == 0 {
		return zero, protocol.Errorf(protocol.CodeInvalidArgument, "spawn: command is required")
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return zero, protocol.Errorf(protocol.CodeUnavailable, "hub is closed")
	}

	id := spec.ID
	if id == "" {
		id = protocol.NewID()
	}
	launch := session.LaunchSpec{
		Env:        spec.Env,
		SessionID:  id,
		Name:       spec.Name,
		Dir:        spec.Cwd,
		Command:    append([]string(nil), spec.Command...),
		Cols:       spec.Cols,
		Rows:       spec.Rows,
		SocketPath: session.SocketPath(h.store.Dir(), id),
	}
	launch, err := launch.Normalize()
	if err != nil {
		return zero, protocol.Errorf(protocol.CodeInvalidArgument, "spawn: %v", err)
	}
	payload, err := json.Marshal(launch)
	if err != nil {
		return zero, fmt.Errorf("hub: marshal launch spec: %w", err)
	}
	handlePath := session.HandshakePath(h.store.Dir(), id)

	// The spec travels on fd 3: the child reads the launch specification from a pipe, so the
	// command line carries nothing but the session id.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return zero, fmt.Errorf("hub: spec pipe: %w", err)
	}
	// A worker's stderr is a file it owns, under the store's logs directory. The daemon closes its
	// own handle right after the fork: workers outlive daemons, and a worker whose next log line
	// found a pipe whose reader had died with the daemon was killed by SIGPIPE, agent and all.
	logPath := h.workerLogPath(id)
	logFile, logOffset, err := openWorkerLog(logPath)
	if err != nil {
		readEnd.Close()
		writeEnd.Close()
		return zero, fmt.Errorf("hub: open worker log %s: %w", logPath, err)
	}
	log := workerLogFile{path: logPath, offset: logOffset}
	cmd := exec.Command(h.executable, "worker", "--session-id", id)
	cmd.ExtraFiles = []*os.File{readEnd}
	cmd.Stderr = logFile
	// A worker leads its own session and process group: a Ctrl-C or a SIGHUP in the daemon's
	// terminal belongs to the daemon, not to the agents it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		readEnd.Close()
		writeEnd.Close()
		return zero, protocol.Errorf(protocol.CodeUnavailable, "start worker %s: %v", h.executable, err)
	}
	readEnd.Close() // the child owns its own descriptor now
	logFile.Close() // and its log: the daemon reads it by path

	if _, err := writeEnd.Write(payload); err != nil {
		writeEnd.Close()
		// No goroutine is waiting for this child yet, so the abort reaps it itself.
		h.abortSpawn(cmd, handlePath, id, nil, log)
		return zero, fmt.Errorf("hub: write launch spec to worker %d: %w", cmd.Process.Pid, err)
	}
	writeEnd.Close()

	// One goroutine reaps the child for every other path out of Spawn, so no abort below can leave a
	// zombie behind, and the log lines of this run reach the daemon log whichever way it ends.
	// exited carries the child's error to whoever is waiting for it; reaped is closed once and can be
	// watched by several callers.
	exited := make(chan error, 1)
	reaped := make(chan struct{})
	go func() {
		err := cmd.Wait()
		forwardWorkerLog(log.path, log.offset, id, h.logf)
		exited <- err
		close(reaped)
	}()

	handshake, err := h.waitHandshake(ctx, handlePath, exited)
	if err != nil {
		h.abortSpawn(cmd, handlePath, id, reaped, log)
		return zero, err
	}

	rec := store.Session{
		ID:         id,
		Name:       spec.Name,
		NodeID:     h.nodeID,
		Cwd:        launch.Dir,
		Command:    append([]string(nil), launch.Command...),
		Harness:    spec.Harness,
		State:      stateRunning,
		Pid:        handshake.Pid,
		Cols:       handshake.Cols,
		Rows:       handshake.Rows,
		SocketPath: launch.SocketPath,
		StartedAt:  handshake.StartedAt,
	}
	if rec.Name == "" {
		rec.Name = handshake.Name
	}
	if rec.Cwd == "" {
		rec.Cwd = handshake.Cwd
	}
	if rec.Pid == 0 {
		rec.Pid = cmd.Process.Pid
	}
	if rec.Cols == 0 {
		rec.Cols = launch.Cols
	}
	if rec.Rows == 0 {
		rec.Rows = launch.Rows
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now().UTC()
	}
	if spec.Decorate != nil {
		spec.Decorate(&rec)
	}
	if err := h.store.Update(func(st *store.State) error {
		if i := findSession(st, id); i >= 0 {
			st.Sessions[i] = rec
			return nil
		}
		st.Sessions = append(st.Sessions, rec)
		return nil
	}); err != nil {
		h.abortSpawn(cmd, handlePath, id, reaped, log)
		return zero, fmt.Errorf("hub: record session %s: %w", id, err)
	}
	h.logf("hub: spawned session %s (worker %d) in %s, log %s", id, rec.Pid, rec.Cwd, logPath)

	h.superviseAsync(id)
	return rec, nil
}

// List returns this node's sessions, newest last, as recorded in state.json.
func (h *Hub) List() []store.Session {
	st := h.store.State()
	out := make([]store.Session, 0, len(st.Sessions))
	for _, s := range st.Sessions {
		if s.NodeID == h.nodeID {
			out = append(out, s)
		}
	}
	return out
}

// Kill stops a session's worker and marks the record exited. The worker signals its PTY child and
// reaps it, so the agent goes away with it. A session that is already gone is left alone and Kill
// succeeds.
//
// Kill signals a process only when the identity checks pass — the pid is alive, its whole command
// line is this daemon's worker invocation for the session, and it started when the handshake says it
// did — which is what stops a recycled pid from taking a stranger with it. It also works for a
// record that says lost: the worker may be alive behind a record something else got wrong.
func (h *Hub) Kill(ctx context.Context, id string) error {
	rec, ok := h.lookup(id)
	if !ok {
		return protocol.Errorf(protocol.CodeNotFound, "no session %s", id)
	}
	ref := h.workerProcess(id, rec)
	if ref.status == procUnknown {
		return protocol.Errorf(protocol.CodeUnavailable, "session %s: cannot check its worker: %s", id, ref.detail)
	}
	if ref.pid <= 0 {
		// Nothing to signal: the worker is gone, or the live pid belongs to somebody else.
		h.logf("hub: session %s: %s", id, ref.detail)
		if rec.State == stateRunning {
			return h.loseSession(id, ref)
		}
		return nil
	}
	if err := signalProcess(ref.pid, true); err != nil {
		return protocol.Errorf(protocol.CodeUnavailable, "signal worker %d: %v", ref.pid, err)
	}
	if !waitForExit(ctx, ref.pid, killGrace) {
		h.logf("hub: worker %d for session %s is still there after SIGTERM; killing", ref.pid, id)
		if err := signalProcess(ref.pid, false); err != nil {
			return protocol.Errorf(protocol.CodeUnavailable, "kill worker %d: %v", ref.pid, err)
		}
		if !waitForExit(ctx, ref.pid, killTimeout) {
			return protocol.Errorf(protocol.CodeUnavailable, "worker %d for session %s did not exit", ref.pid, id)
		}
	}
	// The pid was proven ours and is gone: the socket and the handshake are debris now.
	h.removeWorkerFiles(id)
	if cur, ok := h.lookup(id); ok && cur.State == stateExited {
		// The worker signalled its child and handed supervision the exit code before it went; keep it.
		return nil
	}
	if err := h.settle(id, stateExited, nil, time.Now().UTC()); err != nil {
		return err
	}
	h.logf("hub: killed session %s (worker %d)", id, ref.pid)
	return nil
}

// Dial returns a protocol connection to a running session's worker, with the worker-hop hello
// handshake already done. The caller relays frames on it; Hub.Dial does not send any further
// control message.
func (h *Hub) Dial(ctx context.Context, id string) (net.Conn, error) {
	rec, ok := h.lookup(id)
	if !ok {
		return nil, protocol.Errorf(protocol.CodeNotFound, "no session %s", id)
	}
	if rec.State != stateRunning {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "session %s is %s", id, rec.State)
	}
	c, err := dialWorker(ctx, h.socketPath(rec))
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "dial worker for session %s: %v", id, err)
	}
	return c, nil
}

// Close stops supervision. It never touches a worker: workers are meant to outlive the daemon.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()

	h.cancel()
	h.wg.Wait()
	return nil
}

// reconcile brings the store in line with the run directory: it adopts live workers that have no
// record, brings records in line with their worker's handshake, and restores a lost record whose
// worker turns out to be alive after all.
func (h *Hub) reconcile() error {
	found, err := h.handshakes()
	if err != nil {
		return err
	}
	for _, s := range h.List() {
		hd, has := found[s.ID]
		if !has {
			if err := h.reconcileRecord(s); err != nil {
				return err
			}
			continue
		}
		delete(found, s.ID)
		if err := h.reconcileHandshake(s, hd); err != nil {
			return err
		}
	}

	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		hd := found[id]
		if hd.SessionID != id {
			h.logf("hub: handshake %s names session %s; ignoring", id, hd.SessionID)
			continue
		}
		if hd.ExitCode != nil {
			// A worker the daemon never recorded, whose child already exited: there is nothing to
			// adopt. Drop the file once its worker is gone too.
			if !pidAlive(hd.Pid) {
				h.removeWorkerFiles(id)
			}
			continue
		}
		chk := h.checkWorker(hd.Pid, id, hd.WorkerStartedAt)
		if chk.status != procOurs {
			// No live worker of ours behind it: either the pid is free (a crash) or it belongs to
			// somebody else now. Never adopt it, and remove the file only once the pid is nobody's.
			if chk.status == procGone {
				h.logf("hub: dropping stale handshake for session %s: %s", id, chk.detail)
				h.removeWorkerFiles(id)
			} else {
				h.logf("hub: not adopting session %s: %s", id, chk.detail)
			}
			continue
		}
		h.logf("hub: adopting session %s (worker %d)", id, hd.Pid)
		if err := h.store.Update(func(st *store.State) error {
			if i := findSession(st, id); i >= 0 {
				return nil
			}
			st.Sessions = append(st.Sessions, store.Session{
				ID:         id,
				Name:       hd.Name,
				NodeID:     h.nodeID,
				Cwd:        hd.Cwd,
				Command:    append([]string(nil), hd.Command...),
				State:      stateRunning,
				Pid:        hd.Pid,
				Cols:       hd.Cols,
				Rows:       hd.Rows,
				SocketPath: h.socketPathFor(id),
				StartedAt:  hd.StartedAt,
			})
			return nil
		}); err != nil {
			return fmt.Errorf("hub: adopt session %s: %w", id, err)
		}
		h.superviseAsync(id)
	}
	return nil
}

// reconcileHandshake brings one record in line with the handshake of its worker.
func (h *Hub) reconcileHandshake(s store.Session, hd session.Handshake) error {
	if hd.ExitCode != nil {
		// The worker watched its child exit while this daemon was down and wrote the outcome into
		// the handshake: record the exit instead of inventing a lost worker.
		if err := h.settle(s.ID, stateExited, hd.ExitCode, hd.EndedAt); err != nil {
			return err
		}
		if !pidAlive(hd.Pid) {
			h.removeWorkerFiles(s.ID)
		}
		return nil
	}

	chk := h.checkWorker(hd.Pid, s.ID, hd.WorkerStartedAt)
	switch chk.status {
	case procOurs:
		// A live worker of ours: the record is what was wrong, so put it back to running.
		if s.State != stateRunning {
			if err := h.restore(s.ID); err != nil {
				return err
			}
			h.logf("hub: session %s: worker %d is alive; back to running", s.ID, hd.Pid)
		}
		if err := h.syncPid(s.ID, hd.Pid); err != nil {
			return err
		}
		return h.updateSize(s.ID, hd.Cols, hd.Rows)
	case procGone:
		if s.State == stateRunning {
			h.logf("hub: session %s: %s; marking lost", s.ID, chk.detail)
			if err := h.markLost(s.ID, time.Now().UTC()); err != nil {
				return err
			}
		}
		// The pid is gone, so the socket and handshake are debris.
		h.removeWorkerFiles(s.ID)
		return nil
	case procForeign:
		if s.State == stateRunning {
			h.logf("hub: session %s: %s; marking lost", s.ID, chk.detail)
			if err := h.markLost(s.ID, time.Now().UTC()); err != nil {
				return err
			}
		}
		// The pid is alive and is not ours: never remove files we cannot prove are debris.
		return nil
	default:
		h.logf("hub: session %s: worker identity %s (%s); leaving the record as it is", s.ID, chk.status, chk.detail)
		return nil
	}
}

// reconcileRecord brings one record in line with a missing handshake. A worker only removes its
// handshake while it is finishing, so a record without one is either a worker that just went or one
// that never published; the command line decides which.
func (h *Hub) reconcileRecord(s store.Session) error {
	chk := h.checkWorker(s.Pid, s.ID, time.Time{})
	switch chk.status {
	case procOurs:
		if s.State != stateRunning {
			if err := h.restore(s.ID); err != nil {
				return err
			}
			h.logf("hub: session %s: worker %d is alive without a handshake; back to running", s.ID, s.Pid)
		}
		return nil
	case procGone:
		if s.State == stateRunning {
			h.logf("hub: session %s: %s; marking lost", s.ID, chk.detail)
			if err := h.markLost(s.ID, time.Now().UTC()); err != nil {
				return err
			}
		}
		h.removeWorkerFiles(s.ID)
		return nil
	case procForeign:
		if s.State == stateRunning {
			h.logf("hub: session %s: %s; marking lost", s.ID, chk.detail)
			if err := h.markLost(s.ID, time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
	default:
		h.logf("hub: session %s: worker identity %s (%s); leaving the record as it is", s.ID, chk.status, chk.detail)
		return nil
	}
}

// handshakes reads every handshake file in the run directory, keyed by session id.
func (h *Hub) handshakes() (map[string]session.Handshake, error) {
	dir := session.RunDir(h.store.Dir())
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]session.Handshake{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hub: read run directory: %w", err)
	}
	out := map[string]session.Handshake{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		hd, err := session.ReadHandshake(filepath.Join(dir, e.Name()))
		if err != nil {
			h.logf("hub: unreadable handshake %s: %v", e.Name(), err)
			continue
		}
		out[id] = hd
	}
	return out, nil
}

// waitHandshake waits for the worker to publish its handshake file, giving up when the child exits
// first, when ctx is done, or after five seconds.
func (h *Hub) waitHandshake(ctx context.Context, path string, exited <-chan error) (session.Handshake, error) {
	deadline := time.NewTimer(handshakeTimeout)
	defer deadline.Stop()
	for {
		hd, err := session.ReadHandshake(path)
		if err == nil {
			return hd, nil
		}
		select {
		case werr := <-exited:
			return session.Handshake{}, protocol.Errorf(protocol.CodeUnavailable, "worker exited before its handshake: %v", werr)
		case <-ctx.Done():
			return session.Handshake{}, ctx.Err()
		case <-deadline.C:
			return session.Handshake{}, protocol.Errorf(protocol.CodeUnavailable, "no worker handshake after %s", handshakeTimeout)
		case <-time.After(pollInterval):
		}
	}
}

// workerLogFile is a worker's log and the offset this daemon's own lines start at.
type workerLogFile struct {
	path   string
	offset int64
}

// abortSpawn cleans up after a worker that never became a session. The child is killed and always
// waited for: a worker left unreaped is a zombie, and the launch-spec write is the one path where no
// other goroutine is waiting for it, so this call reaps it and reports what it said.
func (h *Hub) abortSpawn(cmd *exec.Cmd, handlePath, id string, reaped <-chan struct{}, log workerLogFile) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if reaped == nil {
		_ = cmd.Wait()
		forwardWorkerLog(log.path, log.offset, id, h.logf)
	} else {
		select {
		case <-reaped:
		case <-time.After(killTimeout):
			h.logf("hub: worker %d for session %s did not exit after SIGKILL", cmd.Process.Pid, id)
		}
	}
	os.Remove(handlePath)
	os.Remove(h.socketPathFor(id))
}

// workerRef is the process behind a session as far as the hub can prove it: pid is zero unless the
// identity checks passed.
type workerRef struct {
	pid    int
	status procStatus
	detail string
}

// workerProcess identifies the process behind a session. The handshake is the identity: it is the
// only place that carries the worker's own start time, which a recycled pid cannot match. A record
// without a handshake falls back to its pid and the command line alone.
func (h *Hub) workerProcess(id string, rec store.Session) workerRef {
	hd, err := session.ReadHandshake(session.HandshakePath(h.store.Dir(), id))
	if err == nil && hd.SessionID == id && hd.Pid > 0 {
		chk := h.checkWorker(hd.Pid, id, hd.WorkerStartedAt)
		return workerRef{pid: refPid(chk, hd.Pid), status: chk.status, detail: chk.detail}
	}
	if rec.Pid > 0 {
		chk := h.checkWorker(rec.Pid, id, time.Time{})
		return workerRef{pid: refPid(chk, rec.Pid), status: chk.status, detail: chk.detail}
	}
	return workerRef{status: procGone, detail: "the record carries no pid and there is no handshake"}
}

// refPid hands out the pid only when the verdict was procOurs.
func refPid(chk procIdentity, pid int) int {
	if chk.status == procOurs {
		return pid
	}
	return 0
}

// loseSession records a running session as lost because its worker is not there any more: the pid is
// gone, or the live pid belongs to somebody else. The worker's files are removed only once the pid is
// gone — a live pid that is not ours must not cost a stranger the socket path it never used.
func (h *Hub) loseSession(id string, ref workerRef) error {
	if ref.status == procGone {
		h.removeWorkerFiles(id)
	}
	return h.markLost(id, time.Now().UTC())
}

// restore moves a record back to running: a worker of ours is behind it, so the record was wrong, not
// the world. The end time a lost record carried goes with the state.
func (h *Hub) restore(id string) error {
	return h.store.Update(func(st *store.State) error {
		i := findSession(st, id)
		if i < 0 || st.Sessions[i].State == stateRunning {
			return nil
		}
		st.Sessions[i].State = stateRunning
		st.Sessions[i].EndedAt = time.Time{}
		return nil
	})
}

// syncPid points a record at the worker its handshake proved to be ours. A record can carry a stale
// pid (a resume that replaced the worker under the same session id), and every check keyed on the
// record's pid would then call a live session gone.
func (h *Hub) syncPid(id string, pid int) error {
	if pid <= 0 {
		return nil
	}
	return h.store.Update(func(st *store.State) error {
		if i := findSession(st, id); i >= 0 && st.Sessions[i].Pid != pid {
			h.logf("hub: session %s: worker pid %d → %d", id, st.Sessions[i].Pid, pid)
			st.Sessions[i].Pid = pid
		}
		return nil
	})
}

// lookup returns the stored session record for id.
func (h *Hub) lookup(id string) (store.Session, bool) {
	for _, s := range h.List() {
		if s.ID == id {
			return s, true
		}
	}
	return store.Session{}, false
}

// settle moves a record to a terminal state. It does nothing when the record already says so, so
// supervision never rewrites state.json without a reason.
func (h *Hub) settle(id, state string, code *int, ended time.Time) error {
	cur, ok := h.lookup(id)
	if !ok {
		return nil
	}
	if cur.State == state && sameExitCode(cur.ExitCode, code) {
		return nil
	}
	return h.store.Update(func(st *store.State) error {
		i := findSession(st, id)
		if i < 0 {
			return nil
		}
		st.Sessions[i].State = state
		st.Sessions[i].ExitCode = code
		if !ended.IsZero() {
			st.Sessions[i].EndedAt = ended.UTC()
		}
		return nil
	})
}

// markLost records a running session as lost. It never downgrades a record something else already
// settled — a Kill that lands first wins, and the check under the store lock makes that reliable.
func (h *Hub) markLost(id string, ended time.Time) error {
	cur, ok := h.lookup(id)
	if !ok || cur.State != stateRunning {
		return nil
	}
	return h.store.Update(func(st *store.State) error {
		i := findSession(st, id)
		if i < 0 || st.Sessions[i].State != stateRunning {
			return nil
		}
		st.Sessions[i].State = stateLost
		if !ended.IsZero() {
			st.Sessions[i].EndedAt = ended.UTC()
		}
		return nil
	})
}

// updateSize records a PTY size the worker reported.
func (h *Hub) updateSize(id string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	cur, ok := h.lookup(id)
	if !ok || (cur.Cols == cols && cur.Rows == rows) {
		return nil
	}
	return h.store.Update(func(st *store.State) error {
		i := findSession(st, id)
		if i < 0 {
			return nil
		}
		st.Sessions[i].Cols = cols
		st.Sessions[i].Rows = rows
		return nil
	})
}

// refreshSize re-reads the handshake file, which a worker rewrites whenever a client resizes the
// PTY. It is the only way the hub learns about a resize it did not ask for.
func (h *Hub) refreshSize(id string) {
	rec, ok := h.lookup(id)
	if !ok || rec.State != stateRunning {
		return
	}
	hd, err := session.ReadHandshake(session.HandshakePath(h.store.Dir(), id))
	if err != nil || hd.SessionID != id || (rec.Pid > 0 && hd.Pid != rec.Pid) {
		return
	}
	if err := h.updateSize(id, hd.Cols, hd.Rows); err != nil {
		h.logf("hub: session %s: refresh size: %v", id, err)
	}
}

// socketPath is where a record's worker listens.
func (h *Hub) socketPath(rec store.Session) string {
	if rec.SocketPath != "" {
		return rec.SocketPath
	}
	return h.socketPathFor(rec.ID)
}

func (h *Hub) socketPathFor(id string) string { return session.SocketPath(h.store.Dir(), id) }

// removeWorkerFiles drops a worker's socket and handshake, for a worker that is already gone.
func (h *Hub) removeWorkerFiles(id string) {
	os.Remove(session.SocketPath(h.store.Dir(), id))
	os.Remove(session.HandshakePath(h.store.Dir(), id))
}

// findSession returns the index of id in sessions, or -1.
func findSession(st *store.State, id string) int {
	for i, s := range st.Sessions {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func sameExitCode(a, b *int) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
