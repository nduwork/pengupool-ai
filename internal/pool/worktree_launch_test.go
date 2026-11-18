package pool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
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

// symlinkRepo is a repository with one empty commit, for the symlink tests; it returns git's real path.
func symlinkRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return realPath(repo)
}

func addWorktree(t *testing.T, repo, branch string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", branch, wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %s", out)
	}
	return realPath(wt)
}

func runningIn(t *testing.T, p *Pool, cwds ...string) {
	t.Helper()
	_ = p.st.Update(func(s *store.State) error {
		for i, c := range cwds {
			s.Sessions = append(s.Sessions, store.Session{ID: fmt.Sprintf("sess%08d", i), Name: fmt.Sprintf("s%d", i), Harness: "claude", State: "running", Cwd: c})
		}
		return nil
	})
}

func symlink(t *testing.T, target string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestRepoWorktreesNamesASubfolderSessionsWorktree(t *testing.T) {
	p := testPool(t)
	repo := symlinkRepo(t)
	wt := addWorktree(t, repo, "feat")
	if err := os.Mkdir(filepath.Join(wt, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := symlink(t, wt)
	runningIn(t, p, filepath.Join(link, "src"))
	res, _ := p.repoWorktrees(context.Background(), pathIn{Path: repo})
	for _, w := range res.([]Worktree) {
		if w.Path == link && w.Branch == "feat" {
			return
		}
	}
	t.Fatalf("no worktree named %s in %+v", link, res)
}

func TestRepoWorktreesNamesTheMainCheckoutTheSameForEveryCaller(t *testing.T) {
	p := testPool(t)
	repo := symlinkRepo(t)
	for _, d := range []string{"src", "tests"} {
		if err := os.Mkdir(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := symlink(t, repo)
	a, b := filepath.Join(alias, "src"), filepath.Join(repo, "tests")
	runningIn(t, p, a, b)
	ra, _ := p.repoWorktrees(context.Background(), pathIn{Path: a})
	rb, _ := p.repoWorktrees(context.Background(), pathIn{Path: b})
	if wa, wb := ra.([]Worktree), rb.([]Worktree); len(wa) == 0 || len(wb) == 0 || wa[0].Path != wb[0].Path {
		t.Fatalf("main checkout named %+v from a, %+v from b", wa, wb)
	}
}

func TestWorktreeRemoveSeesASessionThroughASymlink(t *testing.T) {
	for _, sub := range []string{"", "src"} {
		p := testPool(t)
		repo := symlinkRepo(t)
		wt := addWorktree(t, repo, "feat")
		if err := os.MkdirAll(filepath.Join(wt, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		runningIn(t, p, filepath.Join(symlink(t, wt), sub))
		if _, err := p.worktreeRemove(context.Background(), worktreeRmIn{Repo: repo, Path: wt}); codeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "s0 is running in that worktree") {
			t.Fatalf("sub %q: %v, want a conflict naming s0", sub, err)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Fatalf("sub %q: worktree gone: %v", sub, err)
		}
	}
}

func TestSamePath(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	link := symlink(t, a)
	for _, c := range []struct {
		x, y string
		want bool
	}{{a, a, true}, {link, a, true}, {a, link, true}, {a, b, false}, {"", a, false}, {a, "", false}, {"", "", false}} {
		if got := samePath(c.x, c.y); got != c.want {
			t.Errorf("samePath(%q, %q) = %v", c.x, c.y, got)
		}
	}
}
