//go:build darwin

package pool

import "golang.org/x/sys/unix"

// parentPID is pid's parent, or -1 when it cannot be read.
func parentPID(pid int) int {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return -1
	}
	return int(k.Eproc.Ppid)
}

// procTreeReadable: the process tree can be read here, so a process that cannot be found is gone.
const procTreeReadable = true
