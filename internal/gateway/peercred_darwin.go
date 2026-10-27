//go:build darwin

package gateway

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeerUID verifies that the unix-socket peer runs as this process's user. macOS exposes the
// peer credentials as struct xucred via LOCAL_PEERCRED.
func checkPeerUID(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
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

// peerPID is the unix-socket peer's process id (LOCAL_PEERPID), or 0 when it cannot be read.
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
		pid, _ = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0
	}
	return pid
}
