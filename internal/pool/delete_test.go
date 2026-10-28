package pool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func codeOf(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

// A deprecated helper's clone (made by PenguPool, read-only) goes with it; the owner's work in it
// (untracked files hidden by config, stashes, commits on another branch) needs the owner to confirm
// exactly what was found; its records go, other scopes stay; the sync does not install it again
// until the owner does.
func TestDeleteDeprecatedClone(t *testing.T) {
	ctx := context.Background()
	p, res, err := installFrom(t, `{"name":"h","group":"planning","repo":%q,"sha":%q}`)
	if err != nil {
		t.Fatal(err)
	}
	st := p.st
	// installFrom serves the catalog's URL from a local repository through url.insteadOf, which
	// git remote get-url applies too; without it here the clone's origin is the catalog's URL, as
	// on a real install.
	withRewrite := p.env
	p.env = slices.DeleteFunc(slices.Clone(p.env), func(e string) bool { return strings.HasPrefix(e, "GIT_CONFIG_") })
	sk := res.(map[string]any)["skill"].(protocol.Skill)
	clone, cat := sk.Path, filepath.Dir(sk.Path)
	if filepath.Base(cat) != "planning" {
		t.Fatalf("installed at %s", clone)
	}
	if v, _ := p.marketplaceList(ctx, struct{}{}); v.(marketView).Skills[0].SkillID != sk.ID {
		t.Fatalf("the catalog entry does not match the helper: %+v", v)
	}
	for _, a := range []string{agentOf(sk), "github.com/o/other"} {
		putNotes(t, p, a, "keep "+a)
	}

	// A plain session working in the clone keeps it.
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "shellaaaaaaa", Name: "shell", State: "running", Cwd: filepath.Join(clone, "skills")})
		return nil
	})
	if _, err := p.skillDelete(ctx, skillDeleteIn{ID: sk.ID}); codeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("deleted under a running session: %v", err)
	}
	_ = st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, "shellaaaaaaa")].State = "exited"; return nil })

	_ = setWritable(clone, true) // installing it made the clone read-only
	git := func(args ...string) {
		gitT(t, clone, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	}
	ownerWork := func(want string) string {
		t.Helper()
		_, err := p.skillDelete(ctx, skillDeleteIn{ID: sk.ID})
		if codeOf(err) != protocol.CodeOwnerWork || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), ".DS_Store") {
			t.Fatalf("owner's work (%s) not reported: %v", want, err)
		}
		return protocol.ErrorMessage("", err).Message // what the app receives and sends back
	}
	// An untracked file the owner's config hides from git status (Finder's .DS_Store does not count).
	git("config", "status.showUntrackedFiles", "no")
	os.WriteFile(filepath.Join(clone, "notes.txt"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(clone, ".DS_Store"), []byte("x"), 0o644)
	ownerWork("notes.txt")
	os.Remove(filepath.Join(clone, "notes.txt"))
	// A stash.
	os.WriteFile(filepath.Join(clone, "AGENTS.md"), []byte("# changed\n"), 0o644)
	git("stash", "-q")
	ownerWork("1 stashed changes")
	git("stash", "drop", "-q")
	// A commit on another local branch, HEAD back at the pin.
	pin := strings.TrimSpace(string(must(exec.Command("git", "-C", clone, "rev-parse", "HEAD").Output())))
	git("checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(clone, "AGENTS.md"), []byte("# feat\n"), 0o644)
	git("commit", "-qam", "feat")
	git("checkout", "-q", "--detach", pin)
	seen := ownerWork("1 unpushed commit")
	if _, err := p.skillDelete(ctx, skillDeleteIn{ID: sk.ID, ConfirmOwnerWork: "stale"}); codeOf(err) != protocol.CodeOwnerWork {
		t.Fatalf("deleted on a confirmation of other work: %v", err)
	}
	if d := st.State().Domain; findSkill(&d, sk.ID) == nil {
		t.Fatal("a refused delete removed the entry")
	}
	// A folder of PenguPool's clones is not registered by hand (a delete may be removing it).
	if _, err := p.skillAdd(ctx, skillAddIn{Name: "x", Path: filepath.Join(clone, "skills", "h")}); codeOf(err) != protocol.CodeInvalidArgument {
		t.Fatalf("a hand-added folder of a clone: %v", err)
	}
	if err := setWritable(clone, false); err != nil {
		t.Fatal(err)
	}
	pre, err := p.skillDelete(ctx, skillDeleteIn{ID: sk.ID, DryRun: true})
	if r := pre.(map[string]any); err != nil || realPath(r["folder"].(string)) != realPath(clone) || r["entries"] != 1 {
		t.Fatalf("dry run: %+v %v", pre, err)
	}
	os.WriteFile(filepath.Join(cat, ".DS_Store"), []byte("x"), 0o644) // the owner revealed the group folder
	out, err := p.skillDelete(ctx, skillDeleteIn{ID: sk.ID, ConfirmOwnerWork: seen})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["deleted"] != "h" || r["folder_removed"] != true || r["entries"] != 1 {
		t.Fatalf("out = %+v", r)
	}
	if d := st.State().Domain; findSkill(&d, sk.ID) != nil {
		t.Fatal("entry kept")
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("clone kept: %v", err)
	}
	if _, err := os.Stat(cat); !os.IsNotExist(err) {
		t.Fatalf("group folder holding only .DS_Store kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), "skills")); err != nil {
		t.Fatalf("the skills folder itself went: %v", err)
	}
	if n, _ := p.db.AgentTotals(agentOf(sk)); n != 0 {
		t.Fatalf("records kept: %d entries", n)
	}
	if n, _ := p.db.AgentTotals("github.com/o/other"); n != 1 {
		t.Fatalf("another helper touched: %d entries", n)
	}

	// The catalog still lists it: the sync leaves it deleted, the owner's install brings it back new.
	p.env = withRewrite // the sync could clone it
	p.syncLibrary(ctx)
	if d := st.State().Domain; len(d.Skills) != 0 {
		t.Fatalf("the sync installed the deleted helper again: %+v", d.Skills)
	}
	again, err := p.marketplaceInstall(ctx, marketIn{Name: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if id := again.(map[string]any)["skill"].(protocol.Skill).ID; id == sk.ID {
		t.Fatal("the deleted id came back")
	}
	if k := deletedKey(p.catalogKey(ctx, again.(map[string]any)["skill"].(protocol.Skill).Path)); p.db.Setting(k) != "" {
		t.Fatalf("%s kept after the owner's install", k)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// The owner's own checkout is unregistered, never deleted.
func TestDeleteKeepsOwnersCheckout(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q")
	os.MkdirAll(filepath.Join(repo, "skills", "gcp"), 0o755)
	os.WriteFile(filepath.Join(repo, "skills", "gcp", "SKILL.md"), []byte("---\nname: gcp\ndescription: Use for GCP work.\n---\n# gcp\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("mine"), 0o644)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	res, err := p.skillAdd(ctx, skillAddIn{Name: "gcp-agent", Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	sk := res.(map[string]any)["skill"].(protocol.Skill)
	out, err := p.skillDelete(ctx, skillDeleteIn{ID: sk.ID}) // no owner_work: nothing of it is deleted
	if err != nil {
		t.Fatal(err)
	}
	if r := out.(map[string]any); r["folder_removed"] != false || !strings.Contains(r["folder_kept"].(string), "your own checkout") {
		t.Fatalf("out = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(repo, "wip.txt")); err != nil {
		t.Fatalf("the owner's checkout was touched: %v", err)
	}
	if d := st.State().Domain; findSkill(&d, sk.ID) != nil {
		t.Fatal("entry kept")
	}
}

// A helper on an active team or in a running session is not deleted; once its team closes, the
// closed team keeps the member row and its context still works.
func TestDeleteRefusedInUseAndClosedTeamStillRenders(t *testing.T) {
	ctx := context.Background()
	p, st := twoTeams(t) // sk-plan (planning-agent) is on team1, in the running session planaaaaaaaa
	if _, err := p.skillDelete(ctx, skillDeleteIn{ID: "sk-plan"}); codeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "active team app") {
		t.Fatalf("deleted from an active team: %v", err)
	}
	_ = st.Update(func(s *store.State) error {
		findTeam(&s.Domain, "team1").State = "closed"
		for i := range s.Sessions {
			if s.Sessions[i].ID == "planaaaaaaaa" {
				s.Sessions[i].SkillID = "sk-plan"
			}
		}
		return nil
	})
	if _, err := p.skillDelete(ctx, skillDeleteIn{ID: "sk-plan"}); codeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "running") {
		t.Fatalf("deleted while running: %v", err)
	}
	_ = st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, "planaaaaaaaa")].State = "exited"; return nil })
	if _, err := p.skillDelete(ctx, skillDeleteIn{ID: "sk-plan"}); err != nil {
		t.Fatal(err)
	}
	d := st.State().Domain
	if tm := findTeam(&d, "team1"); tm.Members[2].SkillID != "sk-plan" || tm.Members[2].Name != "plan" {
		t.Fatalf("history rewritten: %+v", tm.Members)
	}
	if _, err := p.compassList(ctx, compassListIn{}); err != nil {
		t.Fatal(err)
	}
	_ = p.renderContext("planaaaaaaaa")
}

// A deleted helper's id does not come back: its sessions are not resumed and an old launch
// proposal naming it does not start.
func TestDeletedHelperNotRevived(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	repo := t.TempDir()
	a := protocol.Approval{ID: "ap1", Kind: "launch", TaskID: "tk1", Version: 1, State: "pending"}
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "planaaaaaaaa", Name: "plan", State: "exited", Harness: "claude", ConvID: "c1", SkillID: "gone", Cwd: repo})
		s.Domain.Repos = append(s.Domain.Repos, protocol.Repo{ID: "r1", Path: repo})
		s.Domain.Tasks = append(s.Domain.Tasks, protocol.Task{ID: "tk1", Title: "t", RepoID: "r1", Harness: "claude", State: "proposed"})
		s.Domain.Proposals = append(s.Domain.Proposals, protocol.Proposal{ID: "pr1", TaskID: "tk1", Version: 1, Members: []protocol.ProposedMember{
			{Name: "main", Role: "main"}, {Name: "plan", Role: "helper", SkillID: "gone"}}})
		s.Domain.Approvals = append(s.Domain.Approvals, a)
		return nil
	})
	if _, err := p.resume(ctx, sessionIn{SessionID: "planaaaaaaaa"}); codeOf(err) != protocol.CodeInvalidArgument || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("resumed a deleted helper's session: %v", err)
	}
	if _, err := p.launchTeam(ctx, a); codeOf(err) != protocol.CodeInvalidArgument || !strings.Contains(err.Error(), "plan was deleted") {
		t.Fatalf("launched a deleted helper: %v", err)
	}
	if p.helperOf("planaaaaaaaa") != nil {
		t.Fatal("a deleted helper's session still records as it")
	}
}
