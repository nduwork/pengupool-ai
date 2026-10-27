package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHomeFollowsEnv(t *testing.T) {
	t.Setenv("PENGUPOOL_AI_HOME", "/x/y")
	if Home() != "/x/y" {
		t.Fatalf("home %q", Home())
	}
	t.Setenv("PENGUPOOL_AI_HOME", "")
	t.Setenv("HOME", "/h")
	if Home() != filepath.Join("/h", ".pengupool-ai") {
		t.Fatalf("default home %q", Home())
	}
}

func TestEnsureCopiesALegacyCompassOnce(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "old")
	_ = os.MkdirAll(legacy, 0o755)
	_ = os.WriteFile(filepath.Join(legacy, "compass.jsonl"), []byte("{\"id\":\"a\"}\n"), 0o644)
	t.Setenv("PENGUPOOL_AI_HOME", filepath.Join(root, "new"))
	t.Setenv("PENGUPOOL_MUSE_HOME", legacy)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "new", "compass.jsonl")
	if b, _ := os.ReadFile(dst); string(b) != "{\"id\":\"a\"}\n" {
		t.Fatalf("copied %q", b)
	}
	_ = os.WriteFile(dst, []byte("mine\n"), 0o644)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "mine\n" {
		t.Fatalf("an existing Compass was overwritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(legacy, "compass.jsonl")); string(b) != "{\"id\":\"a\"}\n" {
		t.Fatal("the legacy file was written")
	}
}

// Locked nests on one goroutine and excludes others.
func TestLockedIsReentrantAndExclusive(t *testing.T) {
	t.Setenv("PENGUPOOL_AI_HOME", t.TempDir())
	t.Setenv("PENGUPOOL_MUSE_HOME", t.TempDir())
	nested := false
	if err := Locked(func() error { return Locked(func() error { nested = true; return nil }) }); err != nil || !nested {
		t.Fatalf("nested: %v %v", nested, err)
	}
	var wg sync.WaitGroup
	inside, worst := 0, 0
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Locked(func() error {
				mu.Lock()
				inside++
				worst = max(worst, inside)
				mu.Unlock()
				time.Sleep(2 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if worst != 1 {
		t.Fatalf("%d goroutines were inside at once", worst)
	}
}
