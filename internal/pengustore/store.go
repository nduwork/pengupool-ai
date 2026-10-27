// Package store owns the on-disk home: paths and the cross-process lock.
//
// Home is $PENGUPOOL_AI_HOME, default ~/.pengupool-ai. On first use a legacy
// ~/.pengupool-muse (or $PENGUPOOL_MUSE_HOME) compass.jsonl is copied over so
// confirmed lines survive the rename; the Python original is never written.
//
// Locked is re-entrant in-process (confirm -> compass writes nest): only the
// outermost entry takes flock; nested entries reuse it. The owner is tracked
// by goroutine, mirroring the Python thread-local depth.
package store

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
)

var (
	mu    sync.Mutex
	cond  = sync.NewCond(&mu)
	owner uint64
	depth int
)

func goid() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	var id uint64
	for i := 10; i < n; i++ {
		if buf[i] < '0' || buf[i] > '9' {
			break
		}
		id = id*10 + uint64(buf[i]-'0')
	}
	return id
}

// Home returns the store directory, creating nothing.
func Home() string {
	if h := os.Getenv("PENGUPOOL_AI_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pengupool-ai"
	}
	return filepath.Join(home, ".pengupool-ai")
}

// Ensure creates the home directory and migrates a legacy Compass file once.
func Ensure() error {
	h := Home()
	if err := os.MkdirAll(h, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(h, "compass.jsonl")
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	legacy := os.Getenv("PENGUPOOL_MUSE_HOME")
	if legacy == "" {
		if home, err := os.UserHomeDir(); err == nil {
			legacy = filepath.Join(home, ".pengupool-muse")
		}
	}
	src := filepath.Join(legacy, "compass.jsonl")
	data, err := os.ReadFile(src)
	if err != nil || len(data) == 0 {
		return nil
	}
	return os.WriteFile(dst, data, 0o644)
}

func release() {
	mu.Lock()
	depth--
	if depth == 0 {
		owner = 0
		cond.Broadcast()
	}
	mu.Unlock()
}

// Locked runs fn while holding an exclusive lock on the store.
// Nested calls on the same goroutine reuse the outer lock.
func Locked(fn func() error) error {
	gid := goid()
	mu.Lock()
	for depth > 0 && owner != gid {
		cond.Wait()
	}
	outer := depth == 0
	owner, depth = gid, depth+1
	mu.Unlock()
	if outer {
		if err := Ensure(); err != nil {
			release()
			return err
		}
		f, err := os.OpenFile(filepath.Join(Home(), ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			release()
			return err
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			release()
			return err
		}
		defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	err := fn()
	release()
	return err
}
