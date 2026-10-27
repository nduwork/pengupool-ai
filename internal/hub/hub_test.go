package hub_test

import (
	"bytes"
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The tests spawn real workers. They use this test binary as the pengupool binary: the hub
// re-executes `os.Args[0] worker --session-id <id>`, exactly as it would re-execute the installed
// pengupool binary, and TestMain turns that re-execution into session.RunWorker.
//
// Two other modes serve specific findings in this file:
//
//	`test-daemon` — a whole daemon in one process that starts one worker and then dies without
//	cleaning up, which is the daemon death a worker must survive.
//	`worker` with PGP_TEST_FAKE_WORKER — a worker-shaped process whose socket the hub must
//	reconnect to after the first connection is dropped.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "worker":
			if os.Getenv(envFakeWorker) != "" {
				os.Exit(fakeWorkerMain())
			}
			os.Exit(workerMain())
		case "test-daemon":
			os.Exit(daemonMain())
		}
	}
	os.Exit(m.Run())
}

const (
	// envTestDaemonStore, envTestDaemonReady, envTestDaemonCwd and envTestDaemonCommand drive the
	// `test-daemon` mode.
	envTestDaemonStore   = "PGP_TEST_DAEMON_STORE"
	envTestDaemonReady   = "PGP_TEST_DAEMON_READY"
	envTestDaemonCwd     = "PGP_TEST_DAEMON_CWD"
	envTestDaemonCommand = "PGP_TEST_DAEMON_COMMAND"
	// envFakeWorker turns the test binary into the fake worker; envFakeSocket is the socket it
	// listens on and envFakeTrace the file it records its connections in.
	envFakeWorker = "PGP_TEST_FAKE_WORKER"
	envFakeSocket = "PGP_TEST_FAKE_SOCKET"
	envFakeTrace  = "PGP_TEST_FAKE_TRACE"
)

// testExecutable is this test binary, the executable the hub re-executes as a worker.
func testExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// workerMain is the worker side of a spawned session: read the launch specification from fd 3,
// record the argv the hub gave us, then run the real worker.
func workerMain() int {
	fd := os.NewFile(3, "launch-spec")
	if fd == nil {
		fmt.Fprintln(os.Stderr, "worker: no launch spec on fd 3")
		return 2
	}
	raw, err := io.ReadAll(fd)
	fd.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: read launch spec: %v\n", err)
		return 2
	}
	var spec session.LaunchSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "worker: decode launch spec: %v\n", err)
		return 2
	}
	argv, err := json.Marshal(os.Args[1:])
	if err != nil {
		return 2
	}
	if err := os.WriteFile(argvPath(spec.SocketPath), argv, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "worker: record argv: %v\n", err)
		return 2
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
	if err := session.RunWorker(context.Background(), session.WorkerConfig{Spec: spec, Logf: logf}); err != nil {
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		return 1
	}
	return 0
}

// daemonMain is a whole daemon in one process: it starts one worker and exits immediately, without
// closing the hub and without touching the worker. That is the daemon death a worker must survive.
func daemonMain() int {
	storeDir, ready := os.Getenv(envTestDaemonStore), os.Getenv(envTestDaemonReady)
	if storeDir == "" || ready == "" {
		fmt.Fprintln(os.Stderr, "test-daemon: a store and a ready file are required")
		return 2
	}
	st, err := store.Open(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test-daemon: store: %v\n", err)
		return 2
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		fmt.Fprintf(os.Stderr, "test-daemon: hub: %v\n", err)
		return 2
	}
	if err := h.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "test-daemon: start: %v\n", err)
		return 2
	}
	agent := os.Getenv(envTestDaemonCommand)
	if agent == "" {
		agent = "sleep 120"
	}
	rec, err := h.Spawn(context.Background(), hub.SpawnSpec{
		Name:    "survivor",
		Cwd:     os.Getenv(envTestDaemonCwd),
		Harness: "shell",
		Command: []string{"/bin/sh", "-c", agent},
		Cols:    90,
		Rows:    25,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "test-daemon: spawn: %v\n", err)
		return 2
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test-daemon: marshal: %v\n", err)
		return 2
	}
	if err := os.WriteFile(ready, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "test-daemon: ready file: %v\n", err)
		return 2
	}
	fmt.Printf("test-daemon: session %s (worker %d)\n", rec.ID, rec.Pid)
	return 0
}

// fakeWorkerMain stands in for a worker whose socket the hub must reconnect to: it publishes a
// handshake the way a real worker does, drops the first connection right after the attach, and
// answers the second one with a child exit. Its command line is a worker's, so the hub's identity
// checks apply to it unchanged.
func fakeWorkerMain() int {
	socket := os.Getenv(envFakeSocket)
	id := sessionIDArg(os.Args)
	if socket == "" || id == "" {
		fmt.Fprintln(os.Stderr, "fake worker: a socket and a session id are required")
		return 2
	}
	started := time.Now()
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "fake worker: remove socket: %v\n", err)
		return 2
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake worker: listen: %v\n", err)
		return 2
	}
	defer ln.Close()
	if err := session.WriteHandshake(filepath.Join(filepath.Dir(socket), id+".json"), session.Handshake{
		SessionID:       id,
		Pid:             os.Getpid(),
		StartedAt:       started,
		WorkerStartedAt: started,
		Protocol:        protocol.Version,
		Cols:            80,
		Rows:            24,
		Socket:          socket,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "fake worker: handshake: %v\n", err)
		return 2
	}
	trace := os.Getenv(envFakeTrace)
	serveFakeConn(ln, id, 1, trace, false)
	serveFakeConn(ln, id, 2, trace, true)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	return 0
}

// serveFakeConn answers one hub connection: hello, then the attach. With exit set, the connection
// ends with the child's exit; without it, the connection is dropped — the failure supervision has to
// come back from. The connection is recorded as soon as it is accepted, so a test that watches the
// trace sees it before the reply that ends the session.
func serveFakeConn(ln net.Listener, id string, n int, trace string, exit bool) {
	c, err := ln.Accept()
	if err != nil {
		return
	}
	traceConn(trace, n)
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return
	}
	f, err := protocol.ReadFrame(c)
	if err != nil {
		return
	}
	hello, err := protocol.DecodeControl(f)
	if err != nil || hello.Type != protocol.TypeHello {
		return
	}
	if err := protocol.WriteControl(c, &protocol.Message{
		ID: protocol.NewID(), Type: protocol.TypeHelloOK, V: protocol.Version,
	}); err != nil {
		return
	}
	f, err = protocol.ReadFrame(c)
	if err != nil {
		return
	}
	attach, err := protocol.DecodeControl(f)
	if err != nil || attach.Type != protocol.TypeSessionAttach {
		return
	}
	if err := protocol.WriteControl(c, &protocol.Message{
		ID: attach.ID, Type: protocol.TypeSessionAttached, SessionID: id,
		Channel: attach.Channel, Mode: protocol.ModeSnapshot, Cols: 80, Rows: 24,
	}); err != nil {
		return
	}
	if !exit {
		return
	}
	code := 7
	_ = protocol.WriteControl(c, &protocol.Message{
		ID: protocol.NewID(), Type: protocol.TypeSessionExit, SessionID: id,
		ExitCode: &code, At: time.Now().UTC().Format(time.RFC3339),
	})
	time.Sleep(300 * time.Millisecond)
}

// sessionIDArg returns the --session-id argument of a worker command line.
func sessionIDArg(argv []string) string {
	for i, arg := range argv {
		if arg == "--session-id" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// traceConn appends one line per accepted connection, with its time, so a test can see the reconnect
// and how long the hub waited for it.
func traceConn(path string, n int) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%d %s\n", n, time.Now().UTC().Format(time.RFC3339Nano))
}

func argvPath(socketPath string) string { return socketPath + ".argv" }

// shortTempDir is a temporary directory short enough that the store keeps its own run directory.
// t.TempDir() on macOS lives under /var/folders/…, deep enough that session.RunDir has to fall back
// to a hashed directory in the system temp dir; tests read better when the worker files stay
// inside the store they belong to.
func shortTempDir(t *testing.T) string {
	t.Helper()
	root := "/tmp"
	if _, err := os.Stat(root); err != nil {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, "pgphub")
	if err != nil {
		t.Fatalf("short temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newHub returns a started hub over a fresh store, plus a cleanup that stops the hub and kills
// every worker the test left behind.
func newHub(t *testing.T) (*hub.Hub, *store.Store) {
	t.Helper()
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return newHubFor(t, st, testExecutable(t)), st
}

// newHubFor returns a started hub over an existing store, using exe as the worker binary. The
// executable is this test binary unless a test needs a copy of it somewhere else.
func newHubFor(t *testing.T, st *store.Store, exe string) *hub.Hub {
	t.Helper()
	h, err := hub.New(hub.Options{
		Store:      st,
		Executable: exe,
		Logf:       func(format string, args ...any) { t.Logf(time.Now().Format("15:04:05.000 ")+format, args...) },
	})
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("hub.Start: %v", err)
	}
	t.Cleanup(func() {
		for _, s := range h.List() {
			if s.State == "running" {
				if err := h.Kill(context.Background(), s.ID); err != nil {
					t.Errorf("cleanup kill %s: %v", s.ID, err)
				}
			}
		}
		h.Close()
	})
	return h
}

// newUnstartedHub returns a hub that has not reconciled anything, for tests that want the record on
// disk to stay exactly as they left it.
func newUnstartedHub(t *testing.T, st *store.Store) *hub.Hub {
	t.Helper()
	return newUnstartedHubFor(t, st, testExecutable(t))
}

// newUnstartedHubFor is newUnstartedHub with a chosen worker binary.
func newUnstartedHubFor(t *testing.T, st *store.Store, exe string) *hub.Hub {
	t.Helper()
	h, err := hub.New(hub.Options{
		Store:      st,
		Executable: exe,
		Logf:       func(format string, args ...any) { t.Logf(time.Now().Format("15:04:05.000 ")+format, args...) },
	})
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func spawn(t *testing.T, h *hub.Hub, spec hub.SpawnSpec) store.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rec, err := h.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return rec
}

// sleepSpec is the session the acceptance criteria asks for: a real worker whose agent is a shell
// sleeping in a real PTY, in its own temporary directory.
func sleepSpec(t *testing.T) hub.SpawnSpec {
	t.Helper()
	return hub.SpawnSpec{
		Name:    "smoke",
		Cwd:     t.TempDir(),
		Harness: "shell",
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Cols:    100,
		Rows:    30,
	}
}

func stored(t *testing.T, st *store.Store, id string) store.Session {
	t.Helper()
	for _, s := range st.State().Sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s is not in state.json", id)
	return store.Session{}
}

// waitState waits for a session record to reach state.
func waitState(t *testing.T, st *store.Store, id, state string, timeout time.Duration) store.Session {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, s := range st.State().Sessions {
			if s.ID == id && s.State == state {
				return s
			}
		}
		if time.Now().After(deadline) {
			var got string
			for _, s := range st.State().Sessions {
				if s.ID == id {
					got = s.State
				}
			}
			t.Fatalf("session %s is %q, want %q after %s", id, got, state, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSpawnStartsARealWorker checks the whole spawn path: the worker's handshake, its record in
// state.json, the fact that the launch spec travelled on fd 3, and that the socket is reachable.
func TestSpawnStartsARealWorker(t *testing.T) {
	h, st := newHub(t)
	spec := sleepSpec(t)
	rec := spawn(t, h, spec)

	if rec.ID == "" {
		t.Fatal("Spawn returned a session without an id")
	}
	if rec.State != "running" {
		t.Errorf("returned state = %q, want running", rec.State)
	}

	// The handshake the worker writes once its PTY is live.
	hd, err := session.ReadHandshake(session.HandshakePath(st.Dir(), rec.ID))
	if err != nil {
		t.Fatalf("ReadHandshake: %v", err)
	}
	if hd.SessionID != rec.ID {
		t.Errorf("handshake session id = %q, want %q", hd.SessionID, rec.ID)
	}
	if hd.Pid <= 0 || hd.Pid != rec.Pid {
		t.Errorf("handshake pid = %d, record pid = %d", hd.Pid, rec.Pid)
	}
	if hd.Socket != session.SocketPath(st.Dir(), rec.ID) {
		t.Errorf("handshake socket = %q, want %q", hd.Socket, session.SocketPath(st.Dir(), rec.ID))
	}
	if hd.Cols != spec.Cols || hd.Rows != spec.Rows {
		t.Errorf("handshake size = %dx%d, want %dx%d", hd.Cols, hd.Rows, spec.Cols, spec.Rows)
	}

	// The record in state.json.
	onDisk, err := os.ReadFile(st.Path(store.StateFile))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var state store.State
	if err := json.Unmarshal(onDisk, &state); err != nil {
		t.Fatalf("decode state.json: %v", err)
	}
	var persisted *store.Session
	for i := range state.Sessions {
		if state.Sessions[i].ID == rec.ID {
			persisted = &state.Sessions[i]
		}
	}
	if persisted == nil {
		t.Fatalf("state.json has no record for %s", rec.ID)
	}
	switch {
	case persisted.State != "running":
		t.Errorf("state.json state = %q, want running", persisted.State)
	case persisted.Pid != hd.Pid:
		t.Errorf("state.json pid = %d, want %d", persisted.Pid, hd.Pid)
	case persisted.Cols != spec.Cols || persisted.Rows != spec.Rows:
		t.Errorf("state.json size = %dx%d, want %dx%d", persisted.Cols, persisted.Rows, spec.Cols, spec.Rows)
	case persisted.Cwd != spec.Cwd:
		t.Errorf("state.json cwd = %q, want %q", persisted.Cwd, spec.Cwd)
	case persisted.Harness != spec.Harness:
		t.Errorf("state.json harness = %q, want %q", persisted.Harness, spec.Harness)
	case persisted.Name != spec.Name:
		t.Errorf("state.json name = %q, want %q", persisted.Name, spec.Name)
	case persisted.StartedAt.IsZero():
		t.Error("state.json started_at is zero")
	}
	if len(persisted.Command) != len(spec.Command) || persisted.Command[2] != spec.Command[2] {
		t.Errorf("state.json command = %v, want %v", persisted.Command, spec.Command)
	}

	if _, err := os.Stat(session.SocketPath(st.Dir(), rec.ID)); err != nil {
		t.Errorf("worker socket: %v", err)
	}
	if len(h.List()) != 1 {
		t.Errorf("List returned %d sessions, want 1", len(h.List()))
	}

	// The agent is a shell sleeping for 30 seconds: half a second later the worker must still be
	// up and its session still running.
	time.Sleep(500 * time.Millisecond)
	if !pidAlive(rec.Pid) {
		t.Errorf("worker %d went away while its agent was still supposed to be sleeping", rec.Pid)
	}
	if got := stored(t, st, rec.ID); got.State != "running" {
		t.Errorf("after 500ms state = %q, want running", got.State)
	}

	// The worker socket is usable through the hub: Dial has already spoken hello by then.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := h.Dial(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("close dialled conn: %v", err)
	}
	if _, err := h.Dial(ctx, "no-such-session"); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Errorf("Dial of an unknown session = %v, want not_found", err)
	}
}

// TestSpawnNeverPutsTheSpecOnTheCommandLine is the acceptance check for the fd 3 contract: the
// child's own argv and the process table must both show nothing but the session id.
func TestSpawnNeverPutsTheSpecOnTheCommandLine(t *testing.T) {
	h, st := newHub(t)
	spec := sleepSpec(t)
	rec := spawn(t, h, spec)

	raw, err := os.ReadFile(argvPath(session.SocketPath(st.Dir(), rec.ID)))
	if err != nil {
		t.Fatalf("read the worker's recorded argv: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("decode the worker's recorded argv: %v", err)
	}
	want := []string{"worker", "--session-id", rec.ID}
	if len(argv) != len(want) {
		t.Fatalf("worker argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("worker argv = %q, want %q", argv, want)
		}
	}
	assertNoSpec(t, strings.Join(argv, " "), spec)

	line := psCommandLine(t, rec.Pid)
	if line == "" {
		t.Fatalf("could not read the command line of worker %d", rec.Pid)
	}
	if !strings.Contains(line, "--session-id") {
		t.Fatalf("worker %d command line = %q, want the worker invocation", rec.Pid, line)
	}
	assertNoSpec(t, line, spec)
}

// psCommandLine returns the process table's view of a worker's command line.
func psCommandLine(t *testing.T, pid int) string {
	t.Helper()
	if pid <= 0 {
		return ""
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-ww", "-o", "command=")
	cmd.Env = append(os.Environ(), "COLUMNS=4096")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// assertNoSpec fails when a command line carries any part of the launch specification.
func assertNoSpec(t *testing.T, line string, spec hub.SpawnSpec) {
	t.Helper()
	for _, secret := range []string{"sleep 30", spec.Cwd, `"dir"`, `"command"`, `"cols"`, `"ring_bytes"`} {
		if strings.Contains(line, secret) {
			t.Errorf("command line %q carries the launch spec (%q)", line, secret)
		}
	}
}

// TestKillStopsTheWorker: Kill signals the worker, which reaps its agent, and the record says
// exited.
func TestKillStopsTheWorker(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))
	pid := rec.Pid

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := h.Kill(ctx, rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got := waitState(t, st, rec.ID, "exited", 5*time.Second); got.EndedAt.IsZero() {
		t.Error("exited session has no ended_at")
	}
	if pidAlive(pid) {
		t.Errorf("worker %d is still alive after Kill", pid)
	}
	for _, path := range []string{session.HandshakePath(st.Dir(), rec.ID), session.SocketPath(st.Dir(), rec.ID)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after Kill", path)
		}
	}
	// Killing an already finished session is a no-op, and an unknown one is not found.
	if err := h.Kill(ctx, rec.ID); err != nil {
		t.Errorf("second Kill = %v, want nil", err)
	}
	if err := h.Kill(ctx, "no-such-session"); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Errorf("Kill of an unknown session = %v, want not_found", err)
	}
}

// TestAdoptAfterRestart: a hub that finds a live worker it did not start adopts it from the
// handshake file once the record is missing.
func TestAdoptAfterRestart(t *testing.T) {
	h, st := newHub(t)
	spec := sleepSpec(t)
	rec := spawn(t, h, spec)
	pid := rec.Pid

	// The daemon restarts and its store lost the record, while the worker keeps running.
	if err := st.Update(func(s *store.State) error {
		kept := s.Sessions[:0]
		for _, sess := range s.Sessions {
			if sess.ID != rec.ID {
				kept = append(kept, sess)
			}
		}
		s.Sessions = kept
		return nil
	}); err != nil {
		t.Fatalf("drop record: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted, err := hub.New(hub.Options{
		Store:      st,
		Executable: testExecutable(t),
		Logf:       func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatalf("restarted Start: %v", err)
	}
	defer restarted.Close()

	adopted := stored(t, st, rec.ID)
	switch {
	case adopted.State != "running":
		t.Errorf("adopted state = %q, want running", adopted.State)
	case adopted.Pid != pid:
		t.Errorf("adopted pid = %d, want %d", adopted.Pid, pid)
	case adopted.Cwd != spec.Cwd:
		t.Errorf("adopted cwd = %q, want %q", adopted.Cwd, spec.Cwd)
	case adopted.Cols != spec.Cols || adopted.Rows != spec.Rows:
		t.Errorf("adopted size = %dx%d, want %dx%d", adopted.Cols, adopted.Rows, spec.Cols, spec.Rows)
	case len(adopted.Command) == 0 || adopted.Command[2] != "sleep 30":
		t.Errorf("adopted command = %v, want the handshake's command", adopted.Command)
	}

	// The adopted worker is still usable through the restarted hub, and can still be killed.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := restarted.Dial(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Dial after adoption: %v", err)
	}
	c.Close()
	if err := restarted.Kill(ctx, rec.ID); err != nil {
		t.Fatalf("Kill after adoption: %v", err)
	}
	waitState(t, st, rec.ID, "exited", 5*time.Second)
}

// TestLostWhenTheWorkerIsKilled: supervision notices that the process behind a running session is
// gone.
func TestLostWhenTheWorkerIsKilled(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))

	proc, err := os.FindProcess(rec.Pid)
	if err != nil {
		t.Fatalf("find worker %d: %v", rec.Pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill worker %d: %v", rec.Pid, err)
	}

	got := waitState(t, st, rec.ID, "lost", 10*time.Second)
	if got.EndedAt.IsZero() {
		t.Error("lost session has no ended_at")
	}
	if pidAlive(rec.Pid) {
		t.Errorf("worker %d is still alive", rec.Pid)
	}
	if _, err := os.Stat(session.HandshakePath(st.Dir(), rec.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("handshake of a lost session still exists")
	}
}

// TestStartMarksDeadRecordsLost: a record whose process is gone becomes lost when the daemon
// starts, and a handshake whose process is gone is dropped instead of adopted.
func TestStartMarksDeadRecordsLost(t *testing.T) {
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	dead := deadPid(t)
	const id = "session-with-a-dead-worker"
	nodeID := st.NodeID()
	if err := st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{
			ID: id, NodeID: nodeID, Name: "dead", Cwd: t.TempDir(), State: "running",
			Pid: dead, Cols: 80, Rows: 24, StartedAt: time.Now().Add(-time.Minute),
		})
		return nil
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	// A handshake for a worker that is not there either.
	stale := "session-with-a-stale-handshake"
	if err := session.WriteHandshake(session.HandshakePath(st.Dir(), stale), session.Handshake{
		SessionID: stale, Pid: dead, Socket: session.SocketPath(st.Dir(), stale), Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("write stale handshake: %v", err)
	}

	h, err := hub.New(hub.Options{Store: st, Executable: testExecutable(t), Logf: t.Logf})
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	defer h.Close()
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	lost := stored(t, st, id)
	if lost.State != "lost" {
		t.Errorf("state = %q, want lost", lost.State)
	}
	if lost.EndedAt.IsZero() {
		t.Error("lost session has no ended_at")
	}
	if _, err := os.Stat(session.HandshakePath(st.Dir(), stale)); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale handshake was not dropped")
	}
	for _, s := range h.List() {
		if s.ID == stale {
			t.Fatalf("Start adopted a handshake whose worker is gone: %+v", s)
		}
	}
}

// TestAdoptRefusesAForeignProcess: a handshake whose pid is alive but is not this daemon's binary
// must never be adopted.
func TestAdoptRefusesAForeignProcess(t *testing.T) {
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	other := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := other.Start(); err != nil {
		t.Fatalf("start a foreign process: %v", err)
	}
	t.Cleanup(func() {
		other.Process.Kill()
		other.Wait()
	})

	const id = "session-belongs-to-somebody-else"
	handle := session.HandshakePath(st.Dir(), id)
	if err := session.WriteHandshake(handle, session.Handshake{
		SessionID: id, Pid: other.Process.Pid, Socket: session.SocketPath(st.Dir(), id),
		Cols: 80, Rows: 24, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	h, err := hub.New(hub.Options{Store: st, Executable: testExecutable(t), Logf: t.Logf})
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	defer h.Close()
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, s := range h.List() {
		if s.ID == id {
			t.Fatalf("adopted a session owned by pid %d: %+v", other.Process.Pid, s)
		}
	}
	if _, err := os.Stat(handle); err != nil {
		t.Errorf("handshake of a live foreign worker should be left alone: %v", err)
	}
}

// TestSupervisionTracksResize: the hub learns about a PTY size that a client, not the hub, chose.
func TestSupervisionTracksResize(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := h.Dial(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if err := protocol.WriteControl(c, &protocol.Message{
		ID: protocol.NewID(), Type: protocol.TypeSessionAttach,
		SessionID: rec.ID, Channel: 7, ClaimLease: true,
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Wait for the attach to be accepted: a resize from a non-owner is refused.
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	for {
		f, err := protocol.ReadFrame(c)
		if err != nil {
			t.Fatalf("read session.attached: %v", err)
		}
		if f.Header.Type != protocol.FrameControl {
			continue
		}
		m, err := protocol.DecodeControl(f)
		if err != nil {
			t.Fatalf("decode control: %v", err)
		}
		if m.Type == protocol.TypeSessionAttached {
			break
		}
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline: %v", err)
	}

	// Drain what the worker keeps sending, so its writer never blocks on us.
	drained := make(chan struct{})
	defer close(drained)
	go func() {
		for {
			select {
			case <-drained:
				return
			default:
			}
			if _, err := protocol.ReadFrame(c); err != nil {
				return
			}
		}
	}()

	if err := protocol.WriteControl(c, &protocol.Message{
		ID: protocol.NewID(), Type: protocol.TypeSessionResize,
		SessionID: rec.ID, Cols: 120, Rows: 40,
	}); err != nil {
		t.Fatalf("resize: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		got := stored(t, st, rec.ID)
		if got.Cols == 120 && got.Rows == 40 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state.json still says %dx%d, want 120x40", got.Cols, got.Rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNewValidatesOptions covers the two required options.
func TestNewValidatesOptions(t *testing.T) {
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := hub.New(hub.Options{Executable: "/bin/true"}); err == nil {
		t.Error("New without a store = nil error, want an error")
	}
	if _, err := hub.New(hub.Options{Store: st}); err == nil {
		t.Error("New without an executable = nil error, want an error")
	}
}

// --- the findings of review 002 that belong to the hub ---------------------------------------

// TestWorkerSurvivesTheDaemonAndKeepsLogging is K5: a worker's stderr is a file the worker owns, not
// a pipe whose read end dies with the daemon. A real daemon process starts a worker and exits; the
// worker must still be there, and every line it logs afterwards must land in its own log file.
func TestWorkerSurvivesTheDaemonAndKeepsLogging(t *testing.T) {
	storeDir := shortTempDir(t)
	ready := filepath.Join(storeDir, "ready.json")
	var daemon syncBuffer
	cmd := exec.Command(testExecutable(t), "test-daemon")
	cmd.Env = append(os.Environ(),
		envTestDaemonStore+"="+storeDir,
		envTestDaemonReady+"="+ready,
		envTestDaemonCwd+"="+t.TempDir(),
	)
	cmd.Stdout, cmd.Stderr = &daemon, &daemon
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the test daemon: %v", err)
	}
	rec := waitReadySession(t, ready, 20*time.Second, &daemon)
	t.Cleanup(func() {
		if pidAlive(rec.Pid) {
			syscall.Kill(rec.Pid, syscall.SIGKILL)
		}
	})

	// The daemon exits here, taking every descriptor it held for the worker with it.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the test daemon exited with %v:\n%s", err, daemon.String())
	}
	if !pidAlive(rec.Pid) {
		t.Fatalf("worker %d died with the daemon:\n%s", rec.Pid, daemon.String())
	}

	logPath := filepath.Join(storeDir, store.LogsDir, rec.ID+".log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("worker log %s: %v", logPath, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("a worker's stderr is %s, want a regular file", info.Mode())
	}
	before := waitLogContains(t, logPath, "listening on", 5*time.Second)

	// Make the worker log again, with no daemon left. Every one of these lines is a write to a file
	// nobody in the daemon process is reading; the pipe this used to be killed the worker with
	// SIGPIPE on the first of them, taking the agent along.
	if err := syscall.Kill(rec.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("signal worker %d: %v", rec.Pid, err)
	}
	if !waitGone(rec.Pid, 20*time.Second) {
		t.Fatalf("worker %d did not stop on SIGTERM", rec.Pid)
	}
	after := readFile(t, logPath)
	if len(after) <= len(before) {
		t.Fatalf("the worker logged nothing after the daemon exited:\n%s", after)
	}
	for _, want := range []string{"stopping", "worker stopped"} {
		if !strings.Contains(after, want) {
			t.Errorf("the worker's log after the daemon exited lacks %q:\n%s", want, after)
		}
	}
}

// TestWorkerLeadsItsOwnSession is K6: the worker must not share the daemon's process group and
// session, or a Ctrl-C (or a SIGHUP on terminal close) in the daemon's terminal takes the agents
// with it.
func TestWorkerLeadsItsOwnSession(t *testing.T) {
	h, _ := newHub(t)
	rec := spawn(t, h, sleepSpec(t))

	own := strconv.Itoa(rec.Pid)
	if pgid := psField(t, rec.Pid, "pgid"); pgid != own {
		t.Errorf("worker %d is in process group %s, want its own %s: it did not setsid", rec.Pid, pgid, own)
	}
	if pgid := psField(t, os.Getpid(), "pgid"); pgid == own {
		t.Errorf("the worker shares process group %s with the daemon", own)
	}
}

// TestKillNeverSignalsARecycledPid is K7: a pid that is not this daemon's worker must never be
// signalled, however plausible the record looks.
func TestKillNeverSignalsARecycledPid(t *testing.T) {
	t.Run("a pid that runs another program", func(t *testing.T) {
		st, err := store.Open(shortTempDir(t))
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		other := exec.Command("/bin/sh", "-c", "sleep 30")
		if err := other.Start(); err != nil {
			t.Fatalf("start a foreign process: %v", err)
		}
		t.Cleanup(func() {
			other.Process.Kill()
			other.Wait()
		})

		const id = "session-whose-pid-was-recycled"
		writeHandshakeFor(t, st.Dir(), session.Handshake{
			SessionID: id, Pid: other.Process.Pid, Socket: session.SocketPath(st.Dir(), id),
			StartedAt: time.Now().Add(-time.Hour), WorkerStartedAt: time.Now().Add(-time.Hour),
			Protocol: protocol.Version, Cols: 80, Rows: 24,
		})
		seedRunningRecord(t, st, id, other.Process.Pid)
		h := newUnstartedHub(t, st)

		if err := h.Kill(context.Background(), id); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if !pidAlive(other.Process.Pid) {
			t.Fatalf("Kill signalled pid %d, which runs another program", other.Process.Pid)
		}
		if got := stored(t, st, id); got.State != "lost" {
			t.Errorf("state = %q, want lost", got.State)
		}
		if _, err := os.Stat(session.HandshakePath(st.Dir(), id)); err != nil {
			t.Errorf("the handshake of a live foreign pid was removed: %v", err)
		}
	})

	t.Run("the same binary, started at another time", func(t *testing.T) {
		h, st := newHub(t)
		rec := spawn(t, h, sleepSpec(t))
		if err := h.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		handle := session.HandshakePath(st.Dir(), rec.ID)
		hd, err := session.ReadHandshake(handle)
		if err != nil {
			t.Fatalf("ReadHandshake: %v", err)
		}
		// The command line still matches; only the start time gives the reused pid away.
		hd.WorkerStartedAt = time.Now().Add(-24 * time.Hour)
		if err := session.WriteHandshake(handle, hd); err != nil {
			t.Fatalf("WriteHandshake: %v", err)
		}

		fresh := newUnstartedHub(t, st)
		if err := fresh.Kill(context.Background(), rec.ID); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if !pidAlive(rec.Pid) {
			t.Fatalf("Kill signalled worker %d although its start time did not match the handshake", rec.Pid)
		}
		if got := stored(t, st, rec.ID); got.State != "lost" {
			t.Errorf("state = %q, want lost", got.State)
		}
		killWorker(t, rec.Pid)
	})
}

// TestWorkerInAPathWithASpaceIsRecognized is K8: the identity check reads the whole command line, so
// a path that contains a space stays one path. Splitting the command line into fields marked live
// workers lost and deleted the socket and handshake they were still using.
func TestWorkerInAPathWithASpaceIsRecognized(t *testing.T) {
	dir := shortTempDir(t)
	spaced := filepath.Join(dir, "worker binary dir")
	if err := os.MkdirAll(spaced, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", spaced, err)
	}
	exe := filepath.Join(spaced, "pengupool worker")
	if err := copyFile(testExecutable(t), exe); err != nil {
		t.Fatalf("copy the test binary: %v", err)
	}

	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	h := newHubFor(t, st, exe)
	rec := spawn(t, h, sleepSpec(t))
	if line := psCommandLine(t, rec.Pid); !strings.Contains(line, spaced) {
		t.Fatalf("worker %d command line = %q, want the path with the space", rec.Pid, line)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A restarted daemon adopts the worker again: adoption is what needs the whole-command-line
	// match, so this is where a path with a space used to fail.
	restarted := newHubFor(t, st, exe)
	adopted := stored(t, st, rec.ID)
	if adopted.State != "running" {
		t.Fatalf("adopted state = %q, want running", adopted.State)
	}
	if adopted.Pid != rec.Pid {
		t.Errorf("adopted pid = %d, want %d", adopted.Pid, rec.Pid)
	}
	if !pidAlive(rec.Pid) {
		t.Errorf("worker %d is gone", rec.Pid)
	}
	for _, path := range []string{session.HandshakePath(st.Dir(), rec.ID), session.SocketPath(st.Dir(), rec.ID)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the restarted daemon removed %s: %v", path, err)
		}
	}
	if err := restarted.Kill(context.Background(), rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

// TestALiveWorkerIsNeverLostAndKeepsItsFiles is the other half of K8: a reconcile that cannot prove
// the worker is gone must leave the record and the worker's files alone.
func TestALiveWorkerIsNeverLostAndKeepsItsFiles(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i := range 2 {
		next := newHubFor(t, st, testExecutable(t))
		if got := stored(t, st, rec.ID); got.State != "running" {
			t.Fatalf("daemon %d: state = %q, want running", i, got.State)
		}
		for _, path := range []string{session.HandshakePath(st.Dir(), rec.ID), session.SocketPath(st.Dir(), rec.ID)} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("daemon %d removed %s: %v", i, path, err)
			}
		}
		if !pidAlive(rec.Pid) {
			t.Fatalf("daemon %d lost worker %d", i, rec.Pid)
		}
		if err := next.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if err := h.Kill(context.Background(), rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

// TestReconcileRestoresALostRecord is R12: a record that says lost while its worker is alive is
// wrong, and reconcile has to say so — that is what makes a worker behind a lost record usable
// again.
func TestReconcileRestoresALostRecord(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	markLostRecord(t, st, rec.ID)

	restarted := newHubFor(t, st, testExecutable(t))
	got := stored(t, st, rec.ID)
	if got.State != "running" {
		t.Fatalf("state = %q, want running", got.State)
	}
	if !got.EndedAt.IsZero() {
		t.Errorf("restored session kept ended_at %s", got.EndedAt)
	}
	if !pidAlive(rec.Pid) {
		t.Fatalf("worker %d is gone", rec.Pid)
	}
	// And the restored worker is really usable: Kill signals the verified handshake.
	if err := restarted.Kill(context.Background(), rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if pidAlive(rec.Pid) {
		t.Errorf("worker %d is still alive after Kill", rec.Pid)
	}
	waitState(t, st, rec.ID, "exited", 10*time.Second)
}

// TestReconcileFixesAStalePid: a record still carrying a previous worker's pid (a resume under the
// same session id) is pointed back at the worker its handshake proves to be ours.
func TestReconcileFixesAStalePid(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			if s.Sessions[i].ID == rec.ID {
				s.Sessions[i].Pid = 999999
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restarted := newHubFor(t, st, testExecutable(t))
	if got := stored(t, st, rec.ID); got.Pid != rec.Pid || got.State != "running" {
		t.Fatalf("record = pid %d %s, want pid %d running", got.Pid, got.State, rec.Pid)
	}
	if err := restarted.Kill(context.Background(), rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitState(t, st, rec.ID, "exited", 10*time.Second)
}

// TestKillSignalsAWorkerBehindALostRecord is the second half of R12: kill must not report a stop
// while the worker keeps running, so it signals the verified handshake even for a lost record.
func TestKillSignalsAWorkerBehindALostRecord(t *testing.T) {
	h, st := newHub(t)
	rec := spawn(t, h, sleepSpec(t))
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	markLostRecord(t, st, rec.ID)

	// No reconcile in between: the record still says lost when Kill runs.
	fresh := newUnstartedHub(t, st)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := fresh.Kill(ctx, rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if pidAlive(rec.Pid) {
		t.Fatalf("kill reported a stop while worker %d kept running", rec.Pid)
	}
	waitState(t, st, rec.ID, "exited", 10*time.Second)
}

// TestSupervisionReconnectsWhileTheRecordIsRunning is R13: a worker that drops the supervision
// connection is not a session that is over, so supervision has to come back with backoff.
func TestSupervisionReconnectsWhileTheRecordIsRunning(t *testing.T) {
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	id := protocol.NewID()
	socket := session.SocketPath(st.Dir(), id)
	trace := filepath.Join(t.TempDir(), "connections")

	fake := exec.Command(testExecutable(t), "worker", "--session-id", id)
	fake.Env = append(os.Environ(),
		envFakeWorker+"=1",
		envFakeSocket+"="+socket,
		envFakeTrace+"="+trace,
	)
	var out syncBuffer
	fake.Stdout, fake.Stderr = &out, &out
	if err := fake.Start(); err != nil {
		t.Fatalf("start the fake worker: %v", err)
	}
	t.Cleanup(func() {
		if fake.Process != nil {
			fake.Process.Kill()
			fake.Wait()
		}
	})
	waitForFile(t, session.HandshakePath(st.Dir(), id), 20*time.Second, &out)

	newHubFor(t, st, testExecutable(t))
	got := waitState(t, st, id, "exited", 30*time.Second)
	switch {
	case got.ExitCode == nil:
		t.Fatalf("the exit code the worker reported was not recorded:\n%s", out.String())
	case *got.ExitCode != 7:
		t.Errorf("exit code = %d, want 7", *got.ExitCode)
	}

	// Two connections: the hub came back after the first one was dropped.
	lines := strings.Fields(readFile(t, trace))
	if len(lines)%2 != 0 {
		t.Fatalf("unreadable connection trace %q", lines)
	}
	if len(lines)/2 != 2 {
		t.Fatalf("the hub made %d connections, want 2 (one retry after the drop):\n%s", len(lines)/2, readFile(t, trace))
	}
	first, err := time.Parse(time.RFC3339Nano, lines[1])
	if err != nil {
		t.Fatalf("parse the first connection time: %v", err)
	}
	second, err := time.Parse(time.RFC3339Nano, lines[3])
	if err != nil {
		t.Fatalf("parse the second connection time: %v", err)
	}
	if gap := second.Sub(first); gap < superviseRetryFloor {
		t.Errorf("the hub reconnected after %s, want at least %s of backoff", gap, superviseRetryFloor)
	}
}

// superviseRetryFloor is the smallest backoff the hub uses. A reconnect that comes much sooner would
// mean supervision is spinning instead of waiting.
const superviseRetryFloor = 50 * time.Millisecond

// TestReconcileRecordsExitFromTheHandshake is R14: an agent that exited while the daemon was down
// leaves its exit code and end time in the handshake, and reconcile records exited(code) instead of
// losing the session.
func TestReconcileRecordsExitFromTheHandshake(t *testing.T) {
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	const id = "session-whose-agent-exited-while-the-daemon-was-down"
	dead := deadPid(t)
	code := 5
	ended := time.Now().Add(-time.Minute).UTC()
	writeHandshakeFor(t, st.Dir(), session.Handshake{
		SessionID: id, Pid: dead, Socket: session.SocketPath(st.Dir(), id),
		StartedAt: ended, WorkerStartedAt: ended, Protocol: protocol.Version, Cols: 80, Rows: 24,
		ExitCode: &code, EndedAt: ended,
	})
	seedRunningRecord(t, st, id, dead)

	h := newUnstartedHub(t, st)
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := stored(t, st, id)
	if got.State != "exited" {
		t.Errorf("state = %q, want exited", got.State)
	}
	if got.ExitCode == nil || *got.ExitCode != code {
		t.Errorf("exit code = %v, want %d", got.ExitCode, code)
	}
	if !got.EndedAt.Equal(ended) {
		t.Errorf("ended_at = %s, want %s", got.EndedAt, ended)
	}
	if _, err := os.Stat(session.HandshakePath(st.Dir(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the handshake of a worker that is gone was left behind")
	}
}

// TestReconcileRecordsAnExitThatHappenedWhileTheDaemonWasDown is the same contract end to end: a real
// worker whose agent exits with no daemon around leaves its exit code in the handshake, and the next
// daemon records exited(code) instead of losing the session.
func TestReconcileRecordsAnExitThatHappenedWhileTheDaemonWasDown(t *testing.T) {
	storeDir := shortTempDir(t)
	ready := filepath.Join(storeDir, "ready.json")
	var out syncBuffer
	cmd := exec.Command(testExecutable(t), "test-daemon")
	cmd.Env = append(os.Environ(),
		envTestDaemonStore+"="+storeDir,
		envTestDaemonReady+"="+ready,
		envTestDaemonCwd+"="+t.TempDir(),
		envTestDaemonCommand+"=sleep 1; exit 4",
	)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the test daemon: %v", err)
	}
	rec := waitReadySession(t, ready, 20*time.Second, &out)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the test daemon exited with %v:\n%s", err, out.String())
	}
	// The agent exits with the daemon gone; the worker then stops and leaves its handshake behind.
	if !waitGone(rec.Pid, 20*time.Second) {
		t.Fatalf("worker %d is still there after its agent exited:\n%s", rec.Pid, out.String())
	}
	hd, err := session.ReadHandshake(session.HandshakePath(storeDir, rec.ID))
	if err != nil {
		t.Fatalf("the worker left no handshake for the next daemon: %v", err)
	}
	switch {
	case hd.ExitCode == nil:
		t.Fatalf("the handshake carries no exit code: %+v", hd)
	case *hd.ExitCode != 4:
		t.Errorf("handshake exit code = %d, want 4", *hd.ExitCode)
	}
	if hd.EndedAt.IsZero() {
		t.Error("the handshake carries no end time")
	}

	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	newHubFor(t, st, testExecutable(t))
	got := stored(t, st, rec.ID)
	if got.State != "exited" {
		t.Errorf("state = %q, want exited", got.State)
	}
	if got.ExitCode == nil || *got.ExitCode != 4 {
		t.Errorf("exit code = %v, want 4", got.ExitCode)
	}
	if got.EndedAt.IsZero() {
		t.Error("the record has no ended_at")
	}
}

// TestFailedSpecWriteWaitsForTheWorker is N3: when the spec write fails the hub aborts the spawn, and
// the abort has to wait for the child it killed — a worker left unreaped is a zombie that outlives
// the goroutine that would have reaped it.
func TestFailedSpecWriteWaitsForTheWorker(t *testing.T) {
	dir := shortTempDir(t)
	pidFile := filepath.Join(dir, "worker-pid")
	script := filepath.Join(dir, "close-fd-3")
	// A worker that closes fd 3 without reading it and exits: the daemon's spec write then fails.
	body := "#!/bin/sh\necho $$ > " + pidFile + "\nexec 3<&-\nexit 9\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", script, err)
	}
	st, err := store.Open(shortTempDir(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	h := newUnstartedHubFor(t, st, script)

	// A command longer than any pipe buffer, so the write blocks until the worker is gone instead of
	// racing with its exit.
	_, err = h.Spawn(context.Background(), hub.SpawnSpec{
		Name:    "never-started",
		Cwd:     dir,
		Harness: "shell",
		Command: []string{"/bin/sh", "-c", strings.Repeat("x", 1<<20)},
		Cols:    80,
		Rows:    24,
	})
	if err == nil {
		t.Fatal("Spawn = nil error, want the failed spec write")
	}
	if !strings.Contains(err.Error(), "write launch spec") {
		t.Fatalf("Spawn = %v, want the failed spec write", err)
	}
	pid := waitPidFile(t, pidFile, 10*time.Second)
	if !waitGone(pid, 10*time.Second) {
		t.Fatalf("worker %d is still there after the aborted spawn: it was not reaped", pid)
	}
}

// --- helpers ---------------------------------------------------------------------------------

// readFile reads a file or fails the test. It is for files a test has just made.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// waitGone waits for a pid to disappear.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !pidAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// psField reads one field of a pid from the process table.
func psField(t *testing.T, pid int, field string) string {
	t.Helper()
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-ww", "-o", field+"=")
	cmd.Env = append(os.Environ(), "COLUMNS=4096")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps -o %s -p %d: %v", field, pid, err)
	}
	return strings.TrimSpace(string(out))
}

// waitLogContains waits for a worker's log to contain a line, and returns what it holds then.
func waitLogContains(t *testing.T, path, want string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), want) {
			return string(raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q:\n%s", path, want, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncBuffer collects a child process's output for a test that reads it while the child may still be
// running, which a plain bytes.Buffer is not safe for.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitForFile waits for a file to appear.
func waitForFile(t *testing.T, path string, d time.Duration, out fmt.Stringer) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear:\n%s", path, out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitReadySession waits for the test daemon to record the session it spawned.
func waitReadySession(t *testing.T, path string, d time.Duration, out fmt.Stringer) store.Session {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			var rec store.Session
			if err := json.Unmarshal(raw, &rec); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("the test daemon never recorded a session:\n%s", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPidFile waits for a helper process to write its own pid.
func waitPidFile(t *testing.T, path string, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// copyFile copies a file, keeping it executable.
func copyFile(from, to string) error {
	raw, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, raw, 0o755)
}

// writeHandshakeFor publishes a handshake in a store's run directory.
func writeHandshakeFor(t *testing.T, storeDir string, hd session.Handshake) {
	t.Helper()
	if err := session.WriteHandshake(session.HandshakePath(storeDir, hd.SessionID), hd); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
}

// seedRunningRecord puts a running session in the store, as a daemon that spawned a worker would.
func seedRunningRecord(t *testing.T, st *store.Store, id string, pid int) {
	t.Helper()
	started := time.Now().Add(-time.Minute).UTC()
	nodeID := st.NodeID()
	if err := st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{
			ID: id, NodeID: nodeID, Name: "seeded", Cwd: t.TempDir(), State: "running",
			Pid: pid, Cols: 80, Rows: 24, StartedAt: started,
			SocketPath: session.SocketPath(st.Dir(), id),
		})
		return nil
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
}

// markLostRecord records a session as lost, the way a daemon that could not verify its worker did.
func markLostRecord(t *testing.T, st *store.Store, id string) {
	t.Helper()
	ended := time.Now().UTC()
	if err := st.Update(func(s *store.State) error {
		i := -1
		for j := range s.Sessions {
			if s.Sessions[j].ID == id {
				i = j
			}
		}
		if i < 0 {
			return fmt.Errorf("session %s is not in the store", id)
		}
		s.Sessions[i].State = "lost"
		s.Sessions[i].EndedAt = ended
		return nil
	}); err != nil {
		t.Fatalf("mark lost: %v", err)
	}
}

// killWorker stops a worker the way a person would, so it also takes its agent down.
func killWorker(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 || !pidAlive(pid) {
		return
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("signal worker %d: %v", pid, err)
	}
	if !waitGone(pid, 20*time.Second) {
		t.Fatalf("worker %d did not stop", pid)
	}
}

// deadPid returns a pid that is certainly not alive.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("dead pid helper: %v", err)
	}
	if pidAlive(cmd.Process.Pid) {
		t.Skipf("pid %d was recycled immediately", cmd.Process.Pid)
	}
	return cmd.Process.Pid
}

// pidAlive mirrors the hub's liveness probe so the tests can assert a worker is really gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// The hub follows a supervised session's screen from the supervision stream: what the agent printed
// is its visible text, before and after output it draws over it.
func TestSupervisionFollowsTheScreen(t *testing.T) {
	h, _ := newHub(t)
	spec := sleepSpec(t)
	spec.Command = []string{"/bin/sh", "-c", `printf 'Trust this folder?'; sleep 1; printf '\033[2J\033[H? for shortcuts'; sleep 30`}
	rec := spawn(t, h, spec)
	text := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got, ok := h.ScreenText(rec.ID)
			if ok && strings.Contains(got, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("screen %q (followed %v), want %q", got, ok, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	text("Trust this folder?")
	text("? for shortcuts")
	if got, _ := h.ScreenText(rec.ID); strings.Contains(got, "Trust") {
		t.Fatalf("the cleared text is still on screen: %q", got)
	}
}
