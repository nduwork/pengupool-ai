//go:build linux

package pool

import (
	"os"
	"strconv"
	"strings"
)

// parentPID is pid's parent, or -1 when it cannot be read.
func parentPID(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return -1
	}
	// "pid (comm) S ppid ...": comm may hold spaces and parentheses, so read after the last ")".
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 2 {
		return -1
	}
	n, err := strconv.Atoi(f[1])
	if err != nil {
		return -1
	}
	return n
}

// procTreeReadable: the process tree can be read here, so a process that cannot be found is gone.
const procTreeReadable = true
