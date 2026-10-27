package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// twoTeams is a pool with two active teams: app (lead, web, plan) and ops (other).
func twoTeams(t *testing.T) (*Pool, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	dir := t.TempDir()
	_ = st.Update(func(s *store.State) error {
		for _, id := range []string{"leadaaaaaaaa", "webaaaaaaaaa", "planaaaaaaaa", "otheraaaaaaa"} {
			s.Sessions = append(s.Sessions, store.Session{ID: id, Name: id[:4], State: "running", Cwd: dir, Pid: os.Getpid()})
			if id == "planaaaaaaaa" {
				s.Sessions[len(s.Sessions)-1].SkillID = "sk-plan"
			}
		}
		s.Domain.Skills = append(s.Domain.Skills, protocol.Skill{ID: "sk-plan", Name: "planning-agent"})
		s.Domain.Teams = append(s.Domain.Teams,
			protocol.Team{ID: "team1", Name: "app", State: "active", CreatedAt: "2026-01-01T00:00:00Z", Members: []protocol.Member{
				{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"},
				{SessionID: "webaaaaaaaaa", Name: "web", Role: "helper", State: "active"},
				{SessionID: "planaaaaaaaa", Name: "plan", Role: "helper", SkillID: "sk-plan", State: "active"},
			}},
			protocol.Team{ID: "team2", Name: "ops", State: "active", CreatedAt: "2026-01-01T00:00:00Z", Members: []protocol.Member{
				{SessionID: "otheraaaaaaa", Name: "other", Role: "main", State: "active"},
			}})
		return nil
	})
	return p, st
}

func call(t *testing.T, p *Pool, owner bool, typ string, params any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	return p.RPC(context.Background(), gateway.Caller{Owner: owner}, typ, raw)
}

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

type failDecider struct{}

func (failDecider) Name() string { return "fail" }
func (failDecider) Decide(context.Context, decision.Request) (decision.Response, error) {
	return decision.Response{}, fmt.Errorf("boom")
}

func setDecider(p *Pool, d decision.Provider) {
	p.mu.Lock()
	p.testDecider = d
	p.mu.Unlock()
}

func TestContextShowsTask(t *testing.T) {
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error { s.Domain.Teams[0].Goal = "ship login"; return nil })
	lead := p.renderContext("leadaaaaaaaa")
	if want := "## Current task (the owner's latest instruction)\nship login"; !strings.Contains(lead, want) {
		t.Errorf("anchor context lacks %q:\n%s", want, lead)
	}
	if strings.Contains(lead, "## Epics") || strings.Contains(lead, "epic propose") {
		t.Errorf("a team record is in the context:\n%s", lead)
	}
}

func TestFolderListing(t *testing.T) {
	p, _ := twoTeams(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "plain"), 0o755)
	os.MkdirAll(filepath.Join(root, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644)
	r, err := call(t, p, true, "fs.dirs", map[string]any{"path": root})
	if err != nil {
		t.Fatal(err)
	}
	out := r.(dirsOut)
	if len(out.Dirs) != 2 || out.Dirs[0].Name != "plain" || out.Dirs[1].Name != "repo" || !out.Dirs[1].Git || out.Parent != filepath.Dir(root) {
		t.Fatalf("dirs = %+v", out)
	}
	if _, err := call(t, p, true, "fs.dirs", map[string]any{"path": filepath.Join(root, "file.txt")}); err == nil {
		t.Fatal("listed a file")
	}
	if _, err := call(t, p, false, "fs.dirs", map[string]any{"path": root}); err == nil {
		t.Fatal("an agent listed folders")
	}
	if r, err := call(t, p, true, "fs.dirs", map[string]any{"path": ""}); err != nil || r.(dirsOut).Path != r.(dirsOut).Home {
		t.Fatalf("home = %+v %v", r, err)
	}
}

func TestDeleteClosedTeamOnly(t *testing.T) {
	p, st := twoTeams(t)
	ctx := context.Background()
	if _, err := p.teamDelete(ctx, teamIDIn{TeamID: "team1"}); err == nil || !strings.Contains(err.Error(), "close the team first") {
		t.Fatalf("active team: %v", err)
	}
	if _, err := p.teamDelete(ctx, teamIDIn{TeamID: "nope"}); err == nil {
		t.Fatal("unknown team deleted")
	}
	if _, err := p.db.AddGate(agentdb.Gate{TeamID: "team1", SessionID: "s", Kind: "checkin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.AddGate(agentdb.Gate{TeamID: "team2", SessionID: "s", Kind: "checkin"}); err != nil {
		t.Fatal(err)
	}
	putNotes(t, p, "k", "the helper's, not the team's")
	if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "the owner's, not the team's"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Update(func(s *store.State) error {
		findTeam(&s.Domain, "team1").State = "closed"
		s.Domain.Messages = append(s.Domain.Messages, protocol.TeamMessage{ID: "m1", TeamID: "team1"}, protocol.TeamMessage{ID: "m2", TeamID: "team2"})
		s.Domain.Approvals = append(s.Domain.Approvals, protocol.Approval{ID: "a1", TeamID: "team1"}, protocol.Approval{ID: "a2", TeamID: "team2"})
		return nil
	})
	os.MkdirAll(filepath.Join(p.workDir("team1"), "anchor"), 0o755) // an anchor's scratch folder
	os.MkdirAll(p.workDir("team2"), 0o755)
	if _, err := p.teamDelete(ctx, teamIDIn{TeamID: "team1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.workDir("team1")); !os.IsNotExist(err) {
		t.Fatalf("the team's scratch folders stay: %v", err)
	}
	if _, err := os.Stat(p.workDir("team2")); err != nil {
		t.Fatalf("another team's scratch folder went: %v", err)
	}
	if g, _ := p.db.OpenGates(); len(g) != 1 || g[0].TeamID != "team2" {
		t.Fatalf("gates after the delete: %+v", g)
	}
	if n, _ := p.db.AgentTotals("k"); n != 1 {
		t.Fatalf("the helper's journal went with the team: %d", n)
	}
	if m, _ := p.db.Lines(p.owner(), "", "", 0); len(m) != 1 {
		t.Fatalf("the Compass went with the team: %+v", m)
	}
	d := st.State().Domain
	if findTeam(&d, "team1") != nil || findTeam(&d, "team2") == nil {
		t.Fatalf("teams: %+v", d.Teams)
	}
	if len(d.Messages) != 1 || d.Messages[0].ID != "m2" || len(d.Approvals) != 1 || d.Approvals[0].ID != "a2" {
		t.Fatalf("messages %+v approvals %+v", d.Messages, d.Approvals)
	}
}
