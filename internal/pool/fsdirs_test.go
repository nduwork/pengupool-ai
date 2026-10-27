package pool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Opening the picker at home looks inside no folder macOS guards with a privacy prompt: a repository
// there is listed but not marked, while one elsewhere is.
func TestDirsLeaveGuardedFoldersAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"Music/.git", "Pictures/.git", "Documents/.git", "code/.git", "plain"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := &Pool{}
	res, err := p.fsDirs(context.Background(), dirsIn{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range res.(dirsOut).Dirs {
		got[d.Name] = d.Git
	}
	want := map[string]bool{"Music": false, "Pictures": false, "Documents": false, "code": true, "plain": false}
	for name, git := range want {
		if g, ok := got[name]; !ok || g != git {
			t.Fatalf("dirs = %v, want %v", got, want)
		}
	}
	// Inside a guarded folder, once the owner opens it, repositories are marked as usual.
	if err := os.MkdirAll(filepath.Join(home, "Documents", "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err = p.fsDirs(context.Background(), dirsIn{Path: "~/Documents"})
	if err != nil {
		t.Fatal(err)
	}
	if ds := res.(dirsOut).Dirs; len(ds) != 1 || !ds[0].Git {
		t.Fatalf("dirs in Documents = %+v", ds)
	}
}
