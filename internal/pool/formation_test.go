package pool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A member's label is derived from the team's tree: the root leads, with a repository or without;
// a helper-pool agent is a HELPER; a member the owner added with a repository is a builder.
func TestTeamLabels(t *testing.T) {
	tm := &protocol.Team{Members: []protocol.Member{
		{SessionID: "root", Role: "main", RepoID: "r1", Worktree: "/w", State: "active"},
		{SessionID: "sme", Role: "sme", RepoID: "r2", Worktree: "/w2", Parent: "root", State: "active"},
		{SessionID: "help", Role: "helper", SkillID: "k", Parent: "root", State: "active"},
		{SessionID: "plain", Role: "helper", Parent: "root", State: "active"},
		{SessionID: "gone", Role: "helper", SkillID: "k2", State: "closed"},
	}}
	// A session the owner grouped in that works in a known repository is a builder too.
	st := &store.State{
		Sessions: []store.Session{{ID: "inrepo", Cwd: "/src/app/sub"}, {ID: "plain", Cwd: "/tmp/x"}},
		Domain:   protocol.Domain{Repos: []protocol.Repo{{ID: "r1", Path: "/src/app"}}},
	}
	tm.Members = append(tm.Members, protocol.Member{SessionID: "inrepo", Role: "helper", Parent: "root", State: "active"})
	want := map[string]string{"root": "LEAD", "sme": "BUILDER", "help": "HELPER", "plain": "", "inrepo": "BUILDER"}
	for _, m := range tm.Members {
		if m.State != "active" {
			continue
		}
		if got := teamLabel(st, tm, m); got != want[m.SessionID] {
			t.Errorf("%s: %q, want %q", m.SessionID, got, want[m.SessionID])
		}
	}
	// No repository member at all: the root still leads.
	bare := &protocol.Team{Members: []protocol.Member{{SessionID: "a", Role: "main", State: "active"}, {SessionID: "b", Role: "helper", Parent: "a", State: "active"}}}
	if leadOf(bare) != "a" || teamLabel(st, bare, bare.Members[0]) != "LEAD" {
		t.Fatalf("lead of a team with no repository = %q", leadOf(bare))
	}
	// No main worker: the member with no active parent leads.
	noMain := &protocol.Team{Members: []protocol.Member{{SessionID: "b", Parent: "a", State: "active"}, {SessionID: "a", SkillID: "hub", State: "active"}}}
	if leadOf(noMain) != "a" {
		t.Fatalf("lead without a main worker = %q", leadOf(noMain))
	}
}

// A record written before labels were derived holds a chosen anchor and may hold a pending request
// to move it: it loads, the chosen anchor is ignored, and approving the old request changes nothing.
func TestOldAnchorRecordLoads(t *testing.T) {
	dir := t.TempDir()
	old := map[string]any{
		"version": store.Version, "node_id": "n1",
		"sessions": []map[string]any{{"id": "mainaaaaaaaa", "name": "main", "state": "running"}, {"id": "helpaaaaaaaa", "name": "help", "state": "running"}},
		"domain": map[string]any{
			"teams": []map[string]any{{"id": "team1", "name": "app", "harness": "claude", "state": "active", "anchor_id": "helpaaaaaaaa",
				"members": []map[string]any{
					{"session_id": "mainaaaaaaaa", "name": "main", "role": "main", "state": "active"},
					{"session_id": "helpaaaaaaaa", "name": "help", "role": "helper", "skill_id": "k", "parent": "mainaaaaaaaa", "state": "active"},
				}}},
			"approvals": []map[string]any{{"id": "ap1", "kind": "formation", "team_id": "team1", "state": "pending",
				"change": map[string]any{"op": "anchor", "session_id": "mainaaaaaaaa"}}},
		},
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, store.StateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("an old record with an anchor did not load: %v", err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	s := st.State()
	d := s.Domain
	tm := findTeam(&d, "team1")
	if leadOf(tm) != "mainaaaaaaaa" || teamLabel(&s, tm, tm.Members[1]) != "HELPER" {
		t.Fatalf("the stored anchor was not ignored: lead %q", leadOf(tm))
	}
	if _, err := p.applyFormation(context.Background(), d.Approvals[0]); err != nil {
		t.Fatalf("approving an old anchor request: %v", err)
	}
	d = st.State().Domain
	if d.Approvals[0].State != "approved" || leadOf(findTeam(&d, "team1")) != "mainaaaaaaaa" {
		t.Fatalf("the old request changed something: %+v", d.Approvals[0])
	}
}
