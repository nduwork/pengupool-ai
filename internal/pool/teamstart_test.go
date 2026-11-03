package pool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// startPool is a pool whose spawns are recorded, not run; fail names the sessions that do not start.
func startPool(t *testing.T, fail map[string]bool) (*Pool, *store.Store) {
	t.Helper()
	agentOnPath(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		if fail[r.Name] {
			return store.Session{}, errors.New("spawn failed")
		}
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running", Status: "starting", RunLog: filepath.Join(t.TempDir(), "run.log")}
		if r.Decorate != nil {
			r.Decorate(&s)
		}
		return s, st.Update(func(x *store.State) error { x.Sessions = append(x.Sessions, s); return nil })
	}
	local := func() string { d := t.TempDir(); return d }
	_ = st.Update(func(s *store.State) error {
		s.Domain.Skills = []protocol.Skill{
			{ID: "sk-lead", Name: "team-lead-agent", Path: local(), Source: "local", Capabilities: []string{"team-hub"}},
			{ID: "sk-res", Name: "research-agent", Path: local(), Source: "local"},
			{ID: "sk-rev", Name: "pr-review-agent", Path: local(), Source: "local"},
		}
		return nil
	})
	return p, st
}

// Start with a helper: a new team led by team-lead-agent, the picked helpers below it.
func TestTeamStart(t *testing.T) {
	p, st := startPool(t, nil)
	folder := t.TempDir()
	r, err := call(t, p, true, "team.start", map[string]any{"skill_ids": []string{"sk-res", "sk-rev"}, "folder": folder})
	if err != nil {
		t.Fatal(err)
	}
	out := r.(map[string]any)
	members := out["members"].([]teamStartMember)
	if len(members) != 2 || !members[0].OK || !members[1].OK {
		t.Fatalf("members %+v", members)
	}
	d := st.State().Domain
	team := findTeam(&d, out["team_id"].(string))
	if team == nil || team.State != "active" || team.RepoID == "" || len(team.Members) != 3 {
		t.Fatalf("team %+v", team)
	}
	lead := team.Members[0]
	if lead.Role != "main" || !lead.Coordinator || lead.SkillID != "sk-lead" || lead.SessionID != out["lead"] {
		t.Fatalf("lead %+v", lead)
	}
	if s, _ := p.session(lead.SessionID); s.Cwd != filepath.Join(st.Dir(), "work", team.ID, "team-lead-agent") {
		t.Fatalf("lead runs in %q", s.Cwd)
	}
	for i, want := range []string{"sk-res", "sk-rev"} {
		m := team.Members[i+1]
		if m.SkillID != want || m.Parent != lead.SessionID || m.Role != "helper" {
			t.Fatalf("member %d %+v", i, m)
		}
	}
	if r := findRepo(&d, team.RepoID); r == nil || r.Path != folder {
		t.Fatalf("repo %+v", r)
	}
	// the lead alone needs no folder
	if r, err := call(t, p, true, "team.start", map[string]any{"skill_ids": []string{}}); err != nil || len(r.(map[string]any)["members"].([]teamStartMember)) != 0 {
		t.Fatalf("lead only: %v %v", r, err)
	}
}

// Everything is checked before anything starts; an agent cannot start a team.
func TestTeamStartRefusals(t *testing.T) {
	// Only the fake claude is installed: an agent on this machine's real PATH or in
	// ~/.local/bin (where harness.Lookup also looks) must not turn a refusal into a start.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	p, st := startPool(t, nil)
	teams := func() int { return len(st.State().Domain.Teams) }
	for _, c := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"skill_ids": []string{"sk-res"}}, "choose a folder"},
		{map[string]any{"skill_ids": []string{"nope"}}, "no helper"},
		{map[string]any{"skill_ids": []string{"sk-lead"}}, "leads the team"},
		{map[string]any{"skill_ids": []string{}, "harness": "muse"}, ""}, // not installed (agentOnPath puts only claude first)
	} {
		if _, err := call(t, p, true, "team.start", c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.in, err)
		}
	}
	if _, err := call(t, p, false, "team.start", map[string]any{"skill_ids": []string{}}); err == nil {
		t.Error("an agent started a team")
	}
	if teams() != 0 {
		t.Fatalf("%d teams left after refusals", teams())
	}
	_ = st.Update(func(s *store.State) error { s.Domain.Skills = s.Domain.Skills[1:]; return nil })
	if _, err := call(t, p, true, "team.start", map[string]any{"skill_ids": []string{}}); err == nil || !strings.Contains(err.Error(), "team-lead-agent isn't installed") {
		t.Fatalf("no lead: %v", err)
	}
}

// A lead that does not start leaves no team behind; a helper that does not start is reported, and
// the team keeps its lead.
func TestTeamStartFailures(t *testing.T) {
	p, st := startPool(t, map[string]bool{"team-lead-agent": true})
	if _, err := call(t, p, true, "team.start", map[string]any{"skill_ids": []string{}}); err == nil || !strings.Contains(err.Error(), "no team was created") {
		t.Fatalf("lead failure: %v", err)
	}
	if n := len(st.State().Domain.Teams); n != 0 {
		t.Fatalf("%d ghost teams", n)
	}
	if es, _ := os.ReadDir(filepath.Join(st.Dir(), "work")); len(es) != 0 {
		t.Fatalf("work dirs left: %v", es)
	}

	p, st = startPool(t, map[string]bool{"pr-review-agent": true})
	r, err := call(t, p, true, "team.start", map[string]any{"skill_ids": []string{"sk-res", "sk-rev"}, "folder": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ms := r.(map[string]any)["members"].([]teamStartMember)
	if !ms[0].OK || ms[1].OK || ms[1].Error == "" {
		t.Fatalf("members %+v", ms)
	}
	d := st.State().Domain
	if team := findTeam(&d, r.(map[string]any)["team_id"].(string)); team == nil || len(team.Members) != 2 {
		t.Fatalf("team %+v", team)
	}
}
