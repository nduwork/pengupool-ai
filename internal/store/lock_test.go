package store

import (
	"os"
	"strings"
	"testing"
)

func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	second, err := Acquire(dir)
	if err == nil {
		second.Release()
		t.Fatal("a second daemon must not take the lock")
	}
	if !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("unhelpful error: %v", err)
	}
	if first.Path() != dir+"/"+LockFile {
		t.Fatalf("lock lives at %q", first.Path())
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := Acquire(dir)
	if err != nil {
		t.Fatalf("the lock must be reusable after release: %v", err)
	}
	third.Release()
	if err := third.Release(); err != nil {
		t.Fatalf("Release must be idempotent: %v", err)
	}
}

func TestLockRecordsHolderPid(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		t.Fatal("the holder pid must be recorded for the next daemon's error message")
	}
}
