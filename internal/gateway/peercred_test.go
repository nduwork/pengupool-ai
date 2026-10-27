package gateway

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A unix-socket peer is known by its process id: the daemon uses it to tie an agent's request to its session.
func TestPeerPID(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no peer pid here")
	}
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); done <- c }()
	cl, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	srv := <-done
	defer srv.Close()
	if got := peerPID(srv); got != os.Getpid() {
		t.Errorf("peerPID = %d, want %d", got, os.Getpid())
	}
}
