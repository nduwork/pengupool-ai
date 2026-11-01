package pool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A helper gets no worktree, even in a team whose repository is not Git: it runs in the team's scratch
// folder (a local skill) or in its installed skill clone, reads the team repositories, and closes
// without a worktree review. An SME still gets a worktree.
func TestHelperHasNoWorktree(t *testing.T) {
	agentOnPath(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Hub: h, Exe: "pengupool"})
	var reads [][]string
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		reads = append(reads, r.ReadDirs)
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running", Status: "starting", RunLog: filepath.Join(t.TempDir(), "run.log")}
		if r.Decorate != nil {
			r.Decorate(&s)
		}
		return s, st.Update(func(x *store.State) error { x.Sessions = append(x.Sessions, s); return nil })
	}
	ctx := context.Background()
	repo, repo2, local := t.TempDir(), t.TempDir(), t.TempDir()
	installed := filepath.Join(st.Dir(), "skills", "ci")
	_ = os.MkdirAll(installed, 0o755)
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: "claude", State: "running", Status: "working"})
		s.Domain.Repos = []protocol.Repo{{ID: "r1", Name: "app", Path: repo}, {ID: "r2", Name: "api", Path: repo2}}
		s.Domain.Skills = []protocol.Skill{
			{ID: "sk-local", Name: "local-helper", Path: local, Source: "local"},
			{ID: "sk-git", Name: "git-helper", Path: installed, Source: "git"},
		}
		s.Domain.Teams = []protocol.Team{{ID: "team1111", Name: "app", RepoID: "r1", Repos: []string{"r2"}, Harness: "claude", State: "active",
			Members: []protocol.Member{{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"}}}}
		return nil
	})
	for _, c := range []struct{ skill, cwd string }{
		{"sk-local", filepath.Join(st.Dir(), "work", "team1111")},
		{"sk-git", installed},
	} {
		res, err := p.placeHelper(ctx, placeIn{TeamID: "team1111", SkillID: c.skill})
		if err != nil {
			t.Fatalf("%s: %v", c.skill, err)
		}
		m := res.(protocol.Member)
		if m.Worktree != "" || m.Branch != "" {
			t.Fatalf("%s has a worktree: %+v", c.skill, m)
		}
		if s, _ := p.session(m.SessionID); s.Cwd != c.cwd {
			t.Fatalf("%s runs in %q, want %q", c.skill, s.Cwd, c.cwd)
		}
		if got := reads[len(reads)-1]; len(got) != 2 || got[0] != repo || got[1] != repo2 {
			t.Fatalf("%s reads %v", c.skill, got)
		}
		if ctxt := p.renderContext(m.SessionID); !strings.Contains(ctxt, "never change a team repository") || !strings.Contains(ctxt, repo2) {
			t.Fatalf("context:\n%s", ctxt)
		}
		// Closing it just closes it.
		if _, err := p.sessionClose(ctx, sessionIn{SessionID: m.SessionID}); err != nil {
			t.Fatalf("close %s: %v", c.skill, err)
		}
		d := st.State().Domain
		for _, x := range findTeam(&d, "team1111").Members {
			if x.SessionID == m.SessionID && x.State != "closed" {
				t.Fatalf("%s still %s", c.skill, x.State)
			}
		}
	}
	// A stray write in a team repository is logged, not denied; elsewhere it is not logged.
	res, _ := p.placeHelper(ctx, placeIn{TeamID: "team1111", SkillID: "sk-local"})
	id := res.(protocol.Member).SessionID
	s, _ := p.session(id)
	for _, path := range []string{filepath.Join(repo2, "a.go"), filepath.Join(local, "tmp.txt")} {
		p.logStrayWrite(s, map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": path}})
	}
	log, _ := os.ReadFile(s.RunLog)
	if !strings.Contains(string(log), "specialist: wrote in a team repository: "+filepath.Join(repo2, "a.go")) || strings.Contains(string(log), "tmp.txt") {
		t.Fatalf("log:\n%s", log)
	}
}

// An SME (no skill, not main) still works in a worktree of its own.
func TestSMEKeepsWorktree(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	p.testSpawn = func(r spawnReq) (store.Session, error) { return store.Session{ID: r.ID, Cwd: r.Cwd}, nil }
	repo := t.TempDir()
	for _, a := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", a, out)
		}
	}
	team := protocol.Team{ID: "team1111", Name: "app", Harness: "claude"}
	m, err := p.startMember(context.Background(), &team, protocol.Repo{ID: "r1", Path: repo, Git: true}, protocol.Task{Title: "t"},
		protocol.ProposedMember{Name: "sme", Role: "helper"}, headCommit(repo))
	if err != nil || m.Worktree == "" || m.Branch == "" {
		t.Fatalf("%+v %v", m, err)
	}
}

// A skill may assist a team twice: the second copy gets its own name, folder and session, and a
// message reaches each copy by its session id.
func TestDuplicateSpecialist(t *testing.T) {
	agentOnPath(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Hub: h, Exe: "pengupool"})
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running", Status: "idle", RunLog: filepath.Join(t.TempDir(), "run.log")}
		if r.Decorate != nil {
			r.Decorate(&s)
		}
		return s, st.Update(func(x *store.State) error { x.Sessions = append(x.Sessions, s); return nil })
	}
	ctx := context.Background()
	installed := filepath.Join(st.Dir(), "skills", "review")
	_ = os.MkdirAll(installed, 0o755)
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: "claude", State: "running", Status: "working"})
		s.Domain.Repos = []protocol.Repo{{ID: "r1", Name: "app", Path: t.TempDir()}}
		s.Domain.Skills = []protocol.Skill{
			{ID: "sk-rev", Name: "pr-review-agent", Path: installed, Source: "git"},
			{ID: "sk-hub", Name: "hub-agent", Path: t.TempDir(), Source: "local", Capabilities: []string{"team-hub"}},
		}
		s.Domain.Teams = []protocol.Team{{ID: "team1111", Name: "app", RepoID: "r1", Harness: "claude", State: "active",
			Members: []protocol.Member{{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"}}}}
		return nil
	})
	work := filepath.Join(st.Dir(), "work", "team1111")
	for _, c := range []struct{ skill, name, cwd string }{
		{"sk-rev", "pr-review-agent", installed},
		{"sk-rev", "pr-review-agent-2", filepath.Join(work, "pr-review-agent-2")},
		{"sk-hub", "hub-agent", filepath.Join(work, "hub-agent")},
		{"sk-hub", "hub-agent-2", filepath.Join(work, "hub-agent-2")},
	} {
		res, err := p.placeHelper(ctx, placeIn{TeamID: "team1111", SkillID: c.skill})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		m := res.(protocol.Member)
		if m.Name != c.name {
			t.Fatalf("name %q, want %q", m.Name, c.name)
		}
		if s, _ := p.session(m.SessionID); s.Cwd != c.cwd {
			t.Fatalf("%s runs in %q, want %q", c.name, s.Cwd, c.cwd)
		}
		if _, err := p.messageSend(ctx, sendIn{From: "leadaaaaaaaa", To: m.SessionID, Text: "hello " + c.name}); err != nil {
			t.Fatalf("message to %s: %v", c.name, err)
		}
	}
	d := st.State().Domain
	if n := len(findTeam(&d, "team1111").Members); n != 5 {
		t.Fatalf("%d members, want 5", n)
	}
	got := map[string]string{}
	for _, msg := range d.Messages {
		got[msg.To] = msg.Text
	}
	for _, m := range findTeam(&d, "team1111").Members[1:] {
		if got[m.SessionID] != "hello "+m.Name {
			t.Fatalf("%s got %q", m.Name, got[m.SessionID])
		}
	}
}
