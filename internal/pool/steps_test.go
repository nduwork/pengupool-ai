package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadChainAndStatusDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "web", "src")
	_ = os.MkdirAll(sub, 0o755)
	if got := statusDir(sub); got != filepath.Join(root, ".step-status") {
		t.Fatalf("statusDir(subfolder) = %s", got)
	}
	// A linked worktree shares the main repo's tracker.
	wt := filepath.Join(t.TempDir(), "wt")
	_ = os.MkdirAll(wt, 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+root+"/.git/worktrees/wt\n"), 0o644)
	if got := statusDir(wt); got != filepath.Join(root, ".step-status") {
		t.Fatalf("statusDir(worktree) = %s", got)
	}

	dir := filepath.Join(root, ".step-status", "sessions", "abc")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "current"), []byte("ship\n"), 0o644)
	state := filepath.Join(dir, "ship.state")
	_ = os.WriteFile(state, []byte("done\tbuild\t\nactive\ttest\tgo \x1bvet\nplanned\tdeploy\t\n"), 0o644)
	c := readChain(dir)
	if c == nil || c.Name != "ship" || len(c.Steps) != 3 || c.Steps[1].Status != "active" || c.Steps[1].Detail != "go vet" {
		t.Fatalf("readChain = %+v", c)
	}
	// A finished chain expires after the TTL; an active one never does.
	_ = os.WriteFile(state, []byte("done\tbuild\t\ndone\ttest\t\n"), 0o644)
	old := time.Now().Add(-2 * stepsTTL)
	_ = os.Chtimes(state, old, old)
	if c := readChain(dir); c != nil {
		t.Fatalf("expired chain still shown: %+v", c)
	}
	if c := readChain(filepath.Join(root, "missing")); c != nil {
		t.Fatalf("missing tracker = %+v", c)
	}
}
