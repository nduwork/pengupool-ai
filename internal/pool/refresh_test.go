package pool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// The host pulls the catalog from its remote and syncs, logging the move; a sync already running
// makes it skip. The remote is a local bare repository standing in for GitHub.
func TestRefreshPullsCatalog(t *testing.T) {
	root := t.TempDir()
	bare, work := filepath.Join(root, "cat.git"), filepath.Join(root, "work")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-q", "--bare", bare)
	git(root, "clone", "-q", bare, work)
	commit := func(desc string) string {
		os.MkdirAll(filepath.Join(work, ".claude-plugin"), 0o755)
		os.WriteFile(filepath.Join(work, marketFile), []byte(`{"name":"m","description":"`+desc+`","plugins":[]}`), 0o644)
		git(work, "add", "-A")
		git(work, "commit", "-qm", desc)
		git(work, "push", "-q", "origin", "HEAD")
		return git(work, "rev-parse", "HEAD")
	}
	first := commit("one")
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	p := New(Options{Store: st, Exe: "pengupool"})
	var lines []string
	p.logf = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(f, a...))
	}
	ctx := context.Background()
	p.refreshLibrary(ctx) // no clone yet: a new host installs nothing on its own
	if _, err := os.Stat(p.marketDir()); !os.IsNotExist(err) {
		t.Fatalf("cloned without the owner: %v", err)
	}
	os.MkdirAll(filepath.Dir(p.marketDir()), 0o755)
	git(root, "clone", "-q", bare, p.marketDir())
	second := commit("two")
	p.syncMu.Lock()
	p.refreshLibrary(ctx)
	p.syncMu.Unlock()
	if head := git(p.marketDir(), "rev-parse", "HEAD"); head != first {
		t.Fatalf("pulled while a sync ran: %s", head)
	}
	p.refreshLibrary(ctx)
	if head := git(p.marketDir(), "rev-parse", "HEAD"); head != second {
		t.Fatalf("not pulled: %s, want %s", head, second)
	}
	want := "pool: library sync: catalog " + first[:7] + "→" + second[:7]
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range lines {
		found = found || l == want
	}
	if !found {
		t.Fatalf("no %q in %q", want, lines)
	}
}
