package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWriteHostVersion(t *testing.T) {
	dir := t.TempDir()
	if err := writeHostVersion(dir, "pengupool 0.8.0+abc (protocol v1)"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "run", "host.version"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "pengupool 0.8.0+abc (protocol v1)\n" {
		t.Fatalf("host.version = %q", b)
	}
}

// run/host.version names the build serving the store: exactly the line `pengupool version` prints.
func TestDaemonWritesItsVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the daemon")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "pengupool")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	want, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	cmd := exec.Command(bin, "daemon", "--http", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "PENGUPOOL_HOME="+home, "PENGUPOOL_SESSION_ID=")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	file := filepath.Join(home, "v2", "run", "host.version")
	for range 200 {
		if b, err := os.ReadFile(file); err == nil {
			if strings.TrimSpace(string(b)) != strings.TrimSpace(string(want)) {
				t.Fatalf("host.version = %q, version prints %q", b, want)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the daemon never wrote run/host.version")
}

// ownedRun drives app-owned hosts of one store in tests: a built binary, a short store path (unix
// socket paths are limited to 104 bytes) and stand-ins for the desktop app's process.
type ownedRun struct {
	t     *testing.T
	bin   string
	dir   string
	env   []string
	store string
}

func newOwnedRun(t *testing.T) *ownedRun {
	if testing.Short() {
		t.Skip("builds and runs the daemon")
	}
	dir, err := os.MkdirTemp("/tmp", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-f", dir+"/pengupool").Run()
		_ = os.RemoveAll(dir)
	})
	r := &ownedRun{t: t, bin: filepath.Join(dir, "pengupool"), dir: dir, store: filepath.Join(dir, "h", "v2")}
	if out, err := exec.Command("go", "build", "-o", r.bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	r.env = append(os.Environ(), "PENGUPOOL_HOME="+filepath.Join(dir, "h"), "PENGUPOOL_SESSION_ID=")
	return r
}

// app starts a stand-in for the desktop app and its own host; exited reports the host's exit.
func (r *ownedRun) app() (app *exec.Cmd, host *exec.Cmd, exited chan error) {
	r.t.Helper()
	app = exec.Command("sleep", "300")
	if err := app.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = app.Process.Kill() })
	_ = os.Remove(filepath.Join(r.store, "run", "hub.sock"))
	host = exec.Command(r.bin, "daemon", "--http", "127.0.0.1:0", "--owner-pid", strconv.Itoa(app.Process.Pid))
	host.Env = r.env
	if err := host.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = host.Process.Kill() })
	exited = make(chan error, 1)
	go func() { exited <- host.Wait() }()
	for range 400 {
		if c, err := net.Dial("unix", filepath.Join(r.store, "run", "hub.sock")); err == nil {
			c.Close()
			return app, host, exited
		}
		time.Sleep(25 * time.Millisecond)
	}
	r.t.Fatal("the host never listened")
	return
}

func (r *ownedRun) session() string {
	r.t.Helper()
	nw := exec.Command(r.bin, "new", "--raw", "--name", "w", "--cwd", r.dir, "--", "/bin/sh", "-c", "sleep 300")
	nw.Env = r.env
	out, err := nw.CombinedOutput()
	if err != nil {
		r.t.Fatalf("new: %v\n%s", err, out)
	}
	sid, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if !r.worker(sid) {
		r.t.Fatal("no worker for the session")
	}
	return sid
}

func (r *ownedRun) worker(sid string) bool {
	return exec.Command("pgrep", "-f", r.bin+" worker --session-id "+sid).Run() == nil
}

func (r *ownedRun) restore() []string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.store, "state.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	var st struct {
		Restore []string `json:"restore"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		r.t.Fatal(err)
	}
	return st.Restore
}

func waitExit(t *testing.T, exited chan error, what string) {
	t.Helper()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal(what)
	}
}

// An app-owned host (--owner-pid) belongs to one run of the app: when the app's process is gone the
// host ends, ends its sessions with it and records them for the next run's restore question.
func TestOwnedDaemonEndsWithTheApp(t *testing.T) {
	r := newOwnedRun(t)
	app, _, exited := r.app()
	sid := r.session()
	_ = app.Process.Kill() // the app quits, crashes or is force-quit
	_ = app.Wait()
	waitExit(t, exited, "the host outlived the app")
	if r.worker(sid) {
		t.Fatal("the session outlived the app's run")
	}
	if !slices.Contains(r.restore(), sid) {
		t.Fatalf("restore = %v, want %s", r.restore(), sid)
	}
}

// Quitting to install an update hands the sessions over: the host exits on SIGUSR1 and leaves the
// workers running, and the next app's host (the updated one, or the old app reopened after a failed
// or cancelled update) adopts them without a restore question. A hand-over no host took up within
// handoverTTL is a run that ended: its sessions end and are offered for restore.
func TestHandOverToTheNextHost(t *testing.T) {
	r := newOwnedRun(t)
	app, host, exited := r.app()
	sid := r.session()
	_ = host.Process.Signal(syscall.SIGUSR1)
	waitExit(t, exited, "the host did not exit on hand-over")
	_ = app.Process.Kill()
	if !r.worker(sid) {
		t.Fatal("the session ended with a hand-over")
	}

	_, host, exited = r.app() // the next run: updated, or the old app after a failed update
	if !r.worker(sid) || len(r.restore()) != 0 {
		t.Fatalf("not adopted: worker %v, restore %v", r.worker(sid), r.restore())
	}
	if _, err := os.Stat(filepath.Join(r.store, handoverFile)); !os.IsNotExist(err) {
		t.Fatalf("hand-over marker left: %v", err)
	}

	// A hand-over nobody took up in time: the update failed and the app was not reopened.
	_ = host.Process.Signal(syscall.SIGUSR1)
	waitExit(t, exited, "the host did not exit on hand-over")
	old := time.Now().Add(-handoverTTL - time.Minute).UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(r.store, handoverFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r.app()
	if r.worker(sid) || !slices.Contains(r.restore(), sid) {
		t.Fatalf("stale hand-over: worker %v, restore %v", r.worker(sid), r.restore())
	}
}

// The first app run after a host started by hand (or one before 0.9.5, which an update from it
// stops with SIGTERM and no hand-over) adopts the sessions that host left running.
func TestOwnedHostAdoptsAStandaloneHostsSessions(t *testing.T) {
	r := newOwnedRun(t)
	host := exec.Command(r.bin, "daemon", "--http", "127.0.0.1:0")
	host.Env = r.env
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- host.Wait() }()
	for range 400 {
		if c, err := net.Dial("unix", filepath.Join(r.store, "run", "hub.sock")); err == nil {
			c.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	sid := r.session()
	_ = host.Process.Signal(syscall.SIGTERM)
	waitExit(t, exited, "the standalone host did not stop")
	if !r.worker(sid) {
		t.Fatal("a standalone host's session ended with it")
	}
	app, _, exited := r.app()
	if !r.worker(sid) || len(r.restore()) != 0 {
		t.Fatalf("not adopted: worker %v, restore %v", r.worker(sid), r.restore())
	}
	// From now on the store's host is an app run's: quitting ends the sessions.
	_ = app.Process.Kill()
	_ = app.Wait()
	waitExit(t, exited, "the host outlived the app")
	if r.worker(sid) || !slices.Contains(r.restore(), sid) {
		t.Fatalf("after the app quit: worker %v, restore %v", r.worker(sid), r.restore())
	}
}
