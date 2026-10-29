package pool

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// New session ▸ New worktree: a worktree in PenguPool's own folder on a new or an existing branch; a
// launch that fails takes its worktree and new branch with it; git's refusal reaches the owner.
func TestLaunchInNewWorktree(t *testing.T) {
	p := testPool(t)
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	git("branch", "old")
	var cwd string
	p.testSpawn = func(r spawnReq) (store.Session, error) { cwd = r.Cwd; return store.Session{ID: r.ID, Cwd: r.Cwd}, nil }
	launch := func(src sourceIn) error {
		src.Kind, src.Path = "worktree", repo
		_, err := p.launch(context.Background(), launchIn{Name: "web", Harness: "claude", Source: src})
		return err
	}

	if err := launch(sourceIn{Branch: "pengupool/web", NewBranch: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cwd, filepath.Join(p.st.Dir(), "worktrees", "sessions")+"/") {
		t.Fatalf("worktree outside PenguPool's folder: %s", cwd)
	}
	if b, _ := exec.Command("git", "-C", cwd, "branch", "--show-current").Output(); strings.TrimSpace(string(b)) != "pengupool/web" {
		t.Fatalf("worktree on %q", b)
	}
	if err := launch(sourceIn{Branch: "old"}); err != nil {
		t.Fatalf("existing branch: %v", err)
	}
	if err := launch(sourceIn{Branch: "old"}); err == nil || !strings.Contains(err.Error(), "git worktree add") {
		t.Fatalf("a branch checked out elsewhere: %v", err)
	}
	if err := launch(sourceIn{Branch: "--force"}); err == nil {
		t.Fatal("an option as a branch name was passed to git")
	}
	// Only the repository's own local branches: not a commit, a remote's or a fork's ref (the folder may be pretrusted).
	sha := git("rev-parse", "HEAD")
	if err := launch(sourceIn{Branch: sha}); err == nil {
		t.Fatal("a commit was checked out as an existing branch")
	}
	if err := launch(sourceIn{Branch: "pengupool/x", NewBranch: true, From: sha}); err == nil {
		t.Fatal("a new branch was made from a commit that is no local branch")
	}

	p.testSpawn = func(r spawnReq) (store.Session, error) { cwd = r.Cwd; return store.Session{}, errors.New("no agent") }
	if err := launch(sourceIn{Branch: "pengupool/gone", NewBranch: true}); err == nil {
		t.Fatal("the failed launch succeeded")
	}
	if list := git("worktree", "list"); strings.Contains(list, cwd) {
		t.Fatalf("a failed launch left its worktree: %s", list)
	}
	if b := git("branch", "--list", "pengupool/gone"); b != "" {
		t.Fatalf("a failed launch left its new branch: %q", b)
	}

	got, _ := p.repoBranches(context.Background(), pathIn{Path: repo})
	if bs := got.([]string); len(bs) != 3 || bs[0] != "main" {
		t.Fatalf("branches %v: want main, old, pengupool/web with the default branch first", bs)
	}
	// With a feature branch checked out in the repository, From still starts at the default branch.
	git("checkout", "-q", "-b", "feat")
	got, _ = p.repoBranches(context.Background(), pathIn{Path: repo})
	if bs := got.([]string); bs[0] != "main" {
		t.Fatalf("branches %v: the checked-out feature branch came first", bs)
	}
	// origin's HEAD names the default branch when there is one.
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/old")
	got, _ = p.repoBranches(context.Background(), pathIn{Path: repo})
	if bs := got.([]string); bs[0] != "old" {
		t.Fatalf("branches %v: origin's HEAD (old) did not come first", bs)
	}
}
