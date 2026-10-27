package store

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockFile is the exclusive lock a running daemon holds. A second daemon on the same store must
// refuse to start rather than fight the first one for state.json, the socket and the token.
const LockFile = "daemon.lock"

// Lock is an exclusive advisory lock on one store directory.
type Lock struct {
	f    *os.File
	path string
}

// Lock takes the store's daemon lock without blocking. It returns an error naming the holder's pid
// when another daemon has it.
func Acquire(dir string) (*Lock, error) {
	path := filepath.Join(dir, LockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		holder := readHolder(f)
		f.Close()
		if holder != "" {
			return nil, fmt.Errorf("another pengupool daemon (pid %s) already owns %s", holder, path)
		}
		return nil, fmt.Errorf("another pengupool daemon already owns %s", path)
	}
	if err := f.Truncate(0); err == nil {
		fmt.Fprintf(f, "%d\n", os.Getpid())
		f.Sync()
	}
	return &Lock{f: f, path: path}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return f.Close()
}

// Path is the lock file.
func (l *Lock) Path() string { return l.path }

// readHolder reads the pid the holder wrote, best effort.
func readHolder(f *os.File) string {
	buf := make([]byte, 32)
	n, err := f.ReadAt(buf, 0)
	if n <= 0 || (err != nil && n == 0) {
		return ""
	}
	s := string(buf[:n])
	for i, c := range s {
		if c == '\n' || c == '\r' || c == ' ' {
			return s[:i]
		}
	}
	return s
}
