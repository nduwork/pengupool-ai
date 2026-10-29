package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// The desktop app keeps a running host only when run/host.version is the line its bundled binary
// prints for `version` (desktop/src/host.ts). A real daemon must write exactly that line, or every
// update leaves the old host running.
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

// After a reboot (the host and the workers gone) the host offers the sessions that were running for
// restore; a session whose worker survived (the host alone restarted, an update) is adopted and
// never offered.
func TestHostOffersLostSessionsForRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the daemon")
	}
	dir, err := os.MkdirTemp("/tmp", "pp") // unix socket paths are limited to 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "pengupool")
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-f", bin).Run()
		_ = os.RemoveAll(dir)
	})
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	env := append(os.Environ(), "PENGUPOOL_HOME="+filepath.Join(dir, "h"), "PENGUPOOL_SESSION_ID=")
	store := filepath.Join(dir, "h", "v2")
	host := func() *exec.Cmd {
		_ = os.Remove(filepath.Join(store, "run", "hub.sock"))
		d := exec.Command(bin, "daemon", "--http", "127.0.0.1:0")
		d.Env = env
		if err := d.Start(); err != nil {
			t.Fatal(err)
		}
		for range 400 {
			if c, err := net.Dial("unix", filepath.Join(store, "run", "hub.sock")); err == nil {
				c.Close()
				return d
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("the host never listened")
		return nil
	}
	session := func(name string) string {
		nw := exec.Command(bin, "new", "--raw", "--name", name, "--cwd", dir, "--", "/bin/sh", "-c", "sleep 300")
		nw.Env = env
		out, err := nw.CombinedOutput()
		if err != nil {
			t.Fatalf("new: %v\n%s", err, out)
		}
		sid, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
		return sid
	}
	worker := func(sid string) *exec.Cmd {
		return exec.Command("pkill", "-KILL", "-f", bin+" worker --session-id "+sid)
	}
	restore := func() []string {
		b, err := os.ReadFile(filepath.Join(store, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Restore []string `json:"restore"`
		}
		_ = json.Unmarshal(b, &st)
		return st.Restore
	}

	d := host()
	lost, kept := session("lost"), session("kept")
	_ = d.Process.Signal(syscall.SIGTERM) // the host stops; its workers keep running
	_ = d.Wait()
	_ = worker(lost).Run() // a reboot ends this one
	time.Sleep(200 * time.Millisecond)
	d = host()
	defer func() { _ = d.Process.Kill(); _ = d.Wait() }()
	if got := restore(); !slices.Equal(got, []string{lost}) {
		t.Fatalf("restore = %v, want only %s (kept %s)", got, lost, kept)
	}
}
