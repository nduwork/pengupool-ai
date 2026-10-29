package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
