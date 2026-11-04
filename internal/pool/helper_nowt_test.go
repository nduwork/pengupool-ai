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
// without a worktree review. A builder still gets a worktree.
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
		// Its own temp folder, made at launch and named in its context; no other place for temp files.
		tmp := filepath.Join(tempRoot, "team1111", m.SessionID)
		if fi, err := os.Stat(tmp); err != nil || !fi.IsDir() {
			t.Fatalf("%s: no temp folder %s: %v", c.skill, tmp, err)
		}
		if ctx := p.buildContext(m.SessionID); !strings.Contains(ctx, "Put temporary files (reports, notes, scratch work) in "+tmp+", not in your working directory or a repository; send your parent the path of anything it should read. Anything that must outlive this session goes in the message itself or your Done report") ||
			strings.Contains(ctx, "Put your temporary files in your working directory") || strings.Contains(ctx, "Temp files and folders of your own") {
			t.Fatalf("%s context:\n%s", c.skill, ctx)
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
	if !strings.Contains(string(log), "helper: wrote in a team repository: "+filepath.Join(repo2, "a.go")) || strings.Contains(string(log), "tmp.txt") {
		t.Fatalf("log:\n%s", log)
	}
}

// A builder (no skill, not main) still works in a worktree of its own.
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

// dupPool is a team with a lead and two installable skills (a git helper and a team hub), for the
// duplicate-helper tests. spawns records each spawn's working directory by session name.
func dupPool(t *testing.T) (p *Pool, st *store.Store, clone, work string, spawns map[string]string) {
	t.Helper()
	agentOnPath(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	p = New(Options{Store: st, Hub: h, Exe: "pengupool"})
	spawns = map[string]string{}
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		spawns[r.Name] = r.Cwd
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running", Status: "idle", RunLog: filepath.Join(t.TempDir(), "run.log")}
		if r.Decorate != nil {
			r.Decorate(&s)
		}
		return s, st.Update(func(x *store.State) error {
			if i := sessionIndex(x, s.ID); i >= 0 {
				x.Sessions[i] = s
				return nil
			}
			x.Sessions = append(x.Sessions, s)
			return nil
		})
	}
	clone = filepath.Join(st.Dir(), "skills", "review")
	_ = os.MkdirAll(clone, 0o755)
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: "claude", State: "running", Status: "working"})
		s.Domain.Repos = []protocol.Repo{{ID: "r1", Name: "app", Path: t.TempDir()}}
		s.Domain.Skills = []protocol.Skill{
			{ID: "sk-rev", Name: "pr-review-agent", Path: clone, Source: "git"},
			{ID: "sk-hub", Name: "hub-agent", Path: t.TempDir(), Source: "local", Capabilities: []string{"team-hub"}},
			{ID: "sk-long", Name: "a-very-long-specialist-name-that-fills-forty", Path: t.TempDir(), Source: "local"},
		}
		s.Domain.Teams = []protocol.Team{{ID: "team1111", Name: "app", RepoID: "r1", Harness: "claude", State: "active",
			Members: []protocol.Member{{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"}}}}
		return nil
	})
	return p, st, clone, filepath.Join(st.Dir(), "work", "team1111"), spawns
}

func place(t *testing.T, p *Pool, skill string) protocol.Member {
	t.Helper()
	res, err := p.placeHelper(context.Background(), placeIn{TeamID: "team1111", SkillID: skill})
	if err != nil {
		t.Fatalf("place %s: %v", skill, err)
	}
	return res.(protocol.Member)
}

func cwdOf(t *testing.T, p *Pool, id string) string {
	t.Helper()
	s, _ := p.session(id)
	return s.Cwd
}

// A skill may assist a team twice: the second copy gets its own name, folder and session, and a
// message reaches each copy by its session id.
func TestDuplicateSpecialist(t *testing.T) {
	p, st, installed, work, _ := dupPool(t)
	ctx := context.Background()
	for _, c := range []struct{ skill, name, cwd string }{
		{"sk-rev", "pr-review-agent", installed},
		{"sk-rev", "pr-review-agent-2", filepath.Join(work, "pr-review-agent-2")},
		{"sk-hub", "hub-agent", filepath.Join(work, "hub-agent")},
		{"sk-hub", "hub-agent-2", filepath.Join(work, "hub-agent-2")},
	} {
		m := place(t, p, c.skill)
		if m.Name != c.name {
			t.Fatalf("name %q, want %q", m.Name, c.name)
		}
		if got := cwdOf(t, p, m.SessionID); got != c.cwd {
			t.Fatalf("%s runs in %q, want %q", c.name, got, c.cwd)
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

// A rename never lets a later copy share a folder or the skill clone: folders follow the copy
// number fixed at add time, not the name.
func TestDuplicateSpecialistRename(t *testing.T) {
	p, _, clone, work, _ := dupPool(t)
	ctx := context.Background()
	a := place(t, p, "sk-rev")
	b := place(t, p, "sk-rev")
	if _, err := p.sessionRename(ctx, renameIn{SessionID: b.SessionID, Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.sessionRename(ctx, renameIn{SessionID: a.SessionID, Name: "y"}); err != nil {
		t.Fatal(err)
	}
	c := place(t, p, "sk-rev")
	if c.Copy != 3 || c.Name != "pr-review-agent-3" {
		t.Fatalf("third copy %+v", c)
	}
	seen := map[string]string{}
	for _, m := range []protocol.Member{a, b, c} {
		cwd := cwdOf(t, p, m.SessionID)
		if other, dup := seen[cwd]; dup {
			t.Fatalf("%s shares %s with %s", m.Name, cwd, other)
		}
		seen[cwd] = m.Name
	}
	if cwdOf(t, p, c.SessionID) == clone || cwdOf(t, p, c.SessionID) != filepath.Join(work, "pr-review-agent-3") {
		t.Fatalf("third copy runs in %q", cwdOf(t, p, c.SessionID))
	}
	// A hub renamed to its next copy's name: the next copy still gets a folder of its own.
	h1 := place(t, p, "sk-hub")
	if _, err := p.sessionRename(ctx, renameIn{SessionID: h1.SessionID, Name: "hub-agent-2"}); err != nil {
		t.Fatal(err)
	}
	h2 := place(t, p, "sk-hub")
	if h2.Name == "hub-agent-2" || cwdOf(t, p, h2.SessionID) == cwdOf(t, p, h1.SessionID) {
		t.Fatalf("hub copy %q in %q shares with the renamed first in %q", h2.Name, cwdOf(t, p, h2.SessionID), cwdOf(t, p, h1.SessionID))
	}
}

// Copies of a skill whose name fills slug's 40 characters keep their suffix, so their folders differ.
func TestDuplicateSpecialistLongName(t *testing.T) {
	p, _, _, _, _ := dupPool(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		m := place(t, p, "sk-long")
		cwd := cwdOf(t, p, m.SessionID)
		if seen[cwd] || seen[m.Name] {
			t.Fatalf("copy %d (%s) reuses %q", i+1, m.Name, cwd)
		}
		seen[cwd], seen[m.Name] = true, true
		if i > 0 && !strings.HasSuffix(m.Name, "-"+itoa(i+1)) {
			t.Fatalf("copy %d name %q lost its suffix", i+1, m.Name)
		}
	}
}

// Closing one copy leaves the other running in its folder; a restore resumes each copy where it ran.
func TestDuplicateSpecialistCloseAndRestore(t *testing.T) {
	p, st, _, _, spawns := dupPool(t)
	ctx := context.Background()
	a := place(t, p, "sk-rev")
	b := place(t, p, "sk-rev")
	bCwd := cwdOf(t, p, b.SessionID)
	if _, err := p.sessionClose(ctx, sessionIn{SessionID: a.SessionID}); err != nil {
		t.Fatal(err)
	}
	d := st.State().Domain
	for _, m := range findTeam(&d, "team1111").Members {
		switch m.SessionID {
		case a.SessionID:
			if m.State != "closed" {
				t.Fatalf("closed copy is %s", m.State)
			}
		case b.SessionID:
			if m.State != "active" {
				t.Fatalf("other copy is %s", m.State)
			}
		}
	}
	if _, err := os.Stat(bCwd); err != nil {
		t.Fatalf("closing a copy removed the other's folder: %v", err)
	}
	// A daemon restart ended b; resuming it starts it in its own folder again.
	p.setSession(b.SessionID, func(x *store.Session) { x.State, x.Status, x.ConvID = "ended", "ended", "conv-b" })
	delete(spawns, b.Name)
	if _, err := p.resume(ctx, sessionIn{SessionID: b.SessionID}); err != nil {
		t.Fatal(err)
	}
	if spawns[b.Name] != bCwd {
		t.Fatalf("restored copy runs in %q, want %q", spawns[b.Name], bCwd)
	}
}

// A copy that leaves the team keeps running in its folder: the next copy never reuses its number,
// name or folder, nor the first copy's clone when the first one left.
func TestDuplicateSpecialistLeaveThenAdd(t *testing.T) {
	p, st, clone, _, _ := dupPool(t)
	leave := func(id string) {
		t.Helper()
		if err := st.Update(func(s *store.State) error {
			leaveAlone(s, findTeam(&s.Domain, "team1111"), id)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	a := place(t, p, "sk-rev")
	b := place(t, p, "sk-rev")
	h := place(t, p, "sk-hub") // keeps the team above one member as copies leave
	_ = h
	leave(b.SessionID)
	c := place(t, p, "sk-rev")
	if c.Copy == b.Copy || c.Name == b.Name || cwdOf(t, p, c.SessionID) == cwdOf(t, p, b.SessionID) {
		t.Fatalf("after b left, c reuses it: b %+v in %q, c %+v in %q", b, cwdOf(t, p, b.SessionID), c, cwdOf(t, p, c.SessionID))
	}
	// Every copy leaves; those still running in this team's folders keep their numbers.
	leave(a.SessionID)
	leave(c.SessionID)
	d := place(t, p, "sk-rev")
	for _, x := range []protocol.Member{b, c} {
		if d.Copy == x.Copy || cwdOf(t, p, d.SessionID) == cwdOf(t, p, x.SessionID) {
			t.Fatalf("d %+v reuses %s's number or folder", d, x.Name)
		}
	}
	_ = clone
}

// A copy serving another team (in the shared skill clone) does not make this team's first add a
// second copy: it is copy 1, in the clone, as before.
func TestDuplicateSpecialistOtherTeam(t *testing.T) {
	p, st, clone, _, _ := dupPool(t)
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "otherrev0001", Name: "pr-review-agent", Harness: "claude",
			State: "running", Status: "idle", SkillID: "sk-rev", TeamID: "team2222", Cwd: clone})
		return nil
	})
	m := place(t, p, "sk-rev")
	if m.Copy >= 2 || m.Name != "pr-review-agent" || cwdOf(t, p, m.SessionID) != clone {
		t.Fatalf("first add here: %+v in %q, want copy 1 in the clone %q", m, cwdOf(t, p, m.SessionID), clone)
	}
}
