//go:build linux

package gateway

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeerUID verifies that the unix-socket peer runs as this process's user. Linux exposes the
// peer credentials as struct ucred via SO_PEERCRED.
func checkPeerUID(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	if cerr != nil {
		return fmt.Errorf("peer credentials: %w", cerr)
	}
	if uid := os.Getuid(); int(cred.Uid) != uid {
		return fmt.Errorf("unix peer uid %d is not this process's uid %d", cred.Uid, uid)
	}
	return nil
}

// peerPID is the unix-socket peer's process id (SO_PEERCRED), or 0 when it cannot be read.
func peerPID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	if err := raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	}); err != nil {
		return 0
	}
	return pid
}
