package pool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func writable(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()&0o200 != 0
}

// A helper's clone is read-only after the sync installs it, the next sync still checks out a new
// pin, and it is read-only again after.
func TestSkillsReadOnlyAroundSync(t *testing.T) {
	c := newTestCatalog(t)
	c.publish(map[string]string{"agents/x": ""})
	sk := c.sync("x")
	file := filepath.Join(sk.Path, "skills", "x", "SKILL.md")
	if writable(t, file) || !writable(t, filepath.Dir(file)) || !writable(t, sk.Path) {
		t.Fatal("the installed skill files are writable, or its folders are not")
	}
	c.publish(map[string]string{"agents/x": ""}) // a new commit: a new pin
	after := c.sync("x")
	if after.Commit == sk.Commit || !slices.Equal(c.last["updated"], []string{"x"}) {
		t.Fatalf("not updated to the new pin: %s → %s, report %v", sk.Commit, after.Commit, c.last)
	}
	if writable(t, file) {
		t.Fatal("writable after the sync")
	}
	ctx := context.Background()
	// The owner's own checkout is never chmod'ed.
	mine := skillDir(t, "mine")
	if _, err := c.p.skillAdd(ctx, skillAddIn{Name: "mine", Path: mine}); err != nil {
		t.Fatal(err)
	}
	c.sync("mine")
	if !writable(t, filepath.Join(mine, "skills", "mine", "SKILL.md")) {
		t.Fatal("the owner's own checkout was made read-only")
	}
}

// The PreToolUse guard refuses a helper's edit of an installed skill file (read-only, in a clone under
// <store>/skills), by an absolute or a relative path; a new file, its own temp file, or any file of a
// checkout the owner works in is fine.
func TestGuardSkillEdits(t *testing.T) {
	p, st := twoTeams(t)
	dir := filepath.Join(st.Dir(), "skills", "plan")
	for _, f := range []string{"AGENTS.md", "skills/x/SKILL.md", "n.ipynb"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "pin"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "notes.tmp"), []byte("x"), 0o644) // the helper's own
	_ = os.Chmod(filepath.Join(dir, "n.ipynb"), 0o644)                    // a helper that chmods an installed file still may not edit it
	setSkill := func(path, source string) {
		_ = st.Update(func(s *store.State) error {
			s.Domain.Skills[0].Path, s.Domain.Skills[0].Source = path, source
			for i := range s.Sessions {
				if s.Sessions[i].ID == "planaaaaaaaa" {
					s.Sessions[i].SkillID, s.Sessions[i].Cwd = "sk-plan", path
				}
			}
			return nil
		})
	}
	setSkill(dir, "git")
	edit := func(tool, path string) string {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": map[string]any{"file_path": path}})
		out, err := p.hookEvent(context.Background(), hookIn{SessionID: "planaaaaaaaa", Harness: "claude", Event: "PreToolUse", Payload: b})
		if err != nil {
			t.Fatal(err)
		}
		return out.(HookOut).Stdout
	}
	for _, c := range []struct{ tool, path string }{{"Edit", filepath.Join(dir, "skills", "x", "SKILL.md")}, {"Write", "skills/x/SKILL.md"}, {"MultiEdit", dir + "/AGENTS.md"}, {"NotebookEdit", dir + "/n.ipynb"}} {
		if got := edit(c.tool, c.path); !strings.Contains(got, `"deny"`) || !strings.Contains(got, "planning-agent's installed skill files are read-only") || !strings.Contains(got, "put temporary files in "+sessionTemp("team1", "planaaaaaaaa")) || !strings.Contains(got, "'pengupool' search") {
			t.Errorf("%s %s: %q", c.tool, c.path, got)
		}
	}
	for _, path := range []string{"skills/new.md", filepath.Join(dir, "notes.tmp"), filepath.Join(dir, "scratch", "out.txt"), filepath.Join(t.TempDir(), "x.go")} {
		if got := edit("Write", path); strings.Contains(got, "deny") {
			t.Errorf("writing %s was refused: %q", path, got)
		}
	}
	if got := edit("Read", filepath.Join(dir, "AGENTS.md")); strings.Contains(got, "deny") {
		t.Errorf("a read was refused: %q", got)
	}
	// A checkout the owner works in (a local helper, the marketplace repository) is not guarded.
	mine := t.TempDir()
	_ = os.WriteFile(filepath.Join(mine, "AGENTS.md"), []byte("x"), 0o444)
	setSkill(mine, "local")
	if got := edit("Edit", filepath.Join(mine, "AGENTS.md")); strings.Contains(got, "deny") {
		t.Errorf("an edit in the owner's checkout was refused: %q", got)
	}
}

// Only an installed helper is told its skill files are read-only, and that temp files are fine.
func TestSkillContextSaysReadOnly(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &Pool{exe: "pengupool", st: st}
	installed := filepath.Join(st.Dir(), "skills", "x")
	var b strings.Builder
	p.renderSkill(&b, &protocol.Skill{Name: "x", Path: installed, Source: "git"})
	if !strings.Contains(b.String(), "Your installed skill files in "+installed+" are read-only: never edit them.\n") || strings.Contains(b.String(), "Temp files") {
		t.Fatalf("%s", b.String())
	}
	b.Reset()
	p.renderSkill(&b, &protocol.Skill{Name: "x", Path: "/work/marketplace/agents/x", Source: "local"})
	if strings.Contains(b.String(), "read-only") {
		t.Fatalf("the owner's checkout is called read-only: %s", b.String())
	}
}

// Outside the library sync the locks wait for it: no permission changes in the middle of the
// sync's Git work, and the next lock (the sync's own, at its end) applies them.
func TestLockClonesIdleSkipsDuringSync(t *testing.T) {
	c := newTestCatalog(t)
	c.publish(map[string]string{"agents/x": ""})
	sk := c.sync("x")
	file := filepath.Join(sk.Path, "skills", "x", "SKILL.md")
	c.p.unlockClones()
	c.p.syncMu.Lock()
	c.p.lockClonesIdle(context.Background())
	if !writable(t, file) {
		t.Fatal("permissions changed while a sync ran")
	}
	c.p.syncMu.Unlock()
	c.p.lockClonesIdle(context.Background())
	if writable(t, file) {
		t.Fatal("not locked once the sync ended")
	}
}

// A helper the owner creates in the store stays writable on disk for the owner (the guard keeps its
// agent out); a marketplace install is locked.
func TestOwnHelperStaysWritable(t *testing.T) {
	c := newTestCatalog(t)
	ctx := context.Background()
	if _, err := c.p.skillAdd(ctx, skillAddIn{Name: "made", Path: filepath.Join(c.p.st.Dir(), "skills", "made"), Create: true, Task: "made"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.p.skillAdd(ctx, skillAddIn{Name: "elsewhere", Path: filepath.Join(t.TempDir(), "elsewhere"), Create: true, Task: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	c.publish(map[string]string{"agents/x": ""})
	if _, err := c.p.marketplaceInstall(ctx, marketIn{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	c.p.lockClonesIdle(ctx)
	if writable(t, filepath.Join(skillNamed(c.p, "x").Path, "skills", "x", "SKILL.md")) {
		t.Error("the marketplace install is writable")
	}
	if !writable(t, filepath.Join(c.p.st.Dir(), "skills", "made", "AGENTS.md")) {
		t.Error("the owner's new helper in the store was made read-only")
	}
}

// skill.update refuses fields it would otherwise drop: method, description and harness come with requires.
func TestSkillUpdateNeedsRequires(t *testing.T) {
	p, _ := twoTeams(t)
	if _, err := p.skillUpdate(context.Background(), skillUpdateIn{ID: "sk-plan", Method: "x"}); err == nil || !strings.Contains(err.Error(), "send requires with the other fields") {
		t.Fatalf("method without requires: %v", err)
	}
}

// In a locked clone only the installed (tracked) files are read-only: a helper makes its own files
// and folders, .step-status among them, and they stay out of the clone's status. A clone locked by
// an older PenguPool, folders included, gets its folders back at the next lock.
func TestLockedCloneTakesTempFiles(t *testing.T) {
	c := newTestCatalog(t)
	c.publish(map[string]string{"agents/x": ""})
	sk := c.sync("x")
	ctx := context.Background()
	root, own := c.p.ownClone(ctx, sk.Path)
	if !own {
		t.Fatal("not a clone PenguPool made")
	}
	installed := filepath.Join(sk.Path, "skills", "x", "SKILL.md")
	check := func() {
		t.Helper()
		if writable(t, installed) || !writable(t, root) || !writable(t, filepath.Dir(installed)) {
			t.Fatalf("installed writable %v, root writable %v", writable(t, installed), writable(t, root))
		}
		for _, f := range []string{".step-status/default.state", "scratch/out.txt", filepath.Join("skills", "x", "notes.tmp")} {
			path := filepath.Join(root, f)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output(); err != nil || len(out) != 0 {
			t.Fatalf("git status: %q %v", out, err)
		}
	}
	check()
	if err := os.Chmod(root, 0o555); err != nil { // locked the old way
		t.Fatal(err)
	}
	c.p.lockClones(ctx)
	check()
	// A tracked folder swapped for a link to a checkout of the owner's: the lock never reaches through it.
	mine := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mine, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(mine, "x", "SKILL.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(sk.Path, "skills")
	if err := os.Rename(skills, skills+".aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mine, skills); err != nil {
		t.Fatal(err)
	}
	c.p.lockClones(ctx)
	if !writable(t, outside) {
		t.Fatal("the lock chmod'ed a file outside the clone through a link")
	}
}
