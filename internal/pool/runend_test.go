package pool

import (
	"context"
	"slices"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Sessions a reboot ended are offered once: only those that were running and are now lost, and
// "don't restore" forgets the question without deleting a session.
func TestRestoreQuestion(t *testing.T) {
	ctx := context.Background()
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].State = "lost"
			s.Sessions[i].Harness = "claude"
		}
		s.Sessions[1].Harness = "shell"
		s.Sessions[2].State = "exited" // stopped by the owner before the reboot
		s.Nodes = append(s.Nodes, store.Node{ID: "nd-remote", Name: "box", Kind: "ssh"})
		s.Sessions[3].NodeID = "nd-remote" // another machine's reboot is not this host's question
		return nil
	})
	p.MarkLost([]string{"leadaaaaaaaa", "webaaaaaaaaa", "planaaaaaaaa", "otheraaaaaaa"})
	p.MarkLost([]string{"leadaaaaaaaa", "gone"})
	got := p.restorableSessions()
	if len(got) != 2 || got[0].ID != "leadaaaaaaaa" || !got[0].Agent || got[1].Agent {
		t.Fatalf("restorable = %+v", got)
	}
	if _, err := p.runDismiss(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if got := p.restorableSessions(); len(got) != 0 || len(st.State().Sessions) != 4 {
		t.Fatalf("after dismiss: restorable %v, %d sessions", got, len(st.State().Sessions))
	}
	// Restore takes the list and clears it in one update (a terminal only, so nothing resumes).
	p.MarkLost([]string{"webaaaaaaaaa"})
	if out, err := p.runRestore(ctx, struct{}{}); err != nil || out.(map[string]int)["restoring"] != 0 {
		t.Fatalf("restore = %v, %v", out, err)
	}
	if r := st.State().Restore; len(r) != 0 {
		t.Fatalf("restore list after restore: %v", r)
	}
}

// Start fresh takes the stopped sessions off the map instead of restoring them: a team none of whose
// members runs is closed (kept under Closed groups); a stopped member of a running team stays.
func TestStartFresh(t *testing.T) {
	ctx := context.Background()
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			if s.Sessions[i].ID != "otheraaaaaaa" {
				s.Sessions[i].State = "lost"
			}
		}
		web := s.Domain.Teams[0].Members[1] // web helps "other", whose team still runs
		s.Domain.Teams[0].Members = slices.Delete(s.Domain.Teams[0].Members, 1, 2)
		s.Domain.Teams[1].Members = append(s.Domain.Teams[1].Members, web)
		s.Sessions[2].State = "exited" // plan stopped before the reboot: not offered, but its team goes
		s.Sessions[2].TeamID, s.Sessions[2].Role = "team1", "helper"
		s.Domain.Approvals = append(s.Domain.Approvals, protocol.Approval{ID: "ap1", Kind: "formation", TeamID: "team1", State: "pending"})
		return nil
	})
	p.MarkLost([]string{"leadaaaaaaaa", "webaaaaaaaaa", "planaaaaaaaa", "otheraaaaaaa"})
	out, err := p.runFresh(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	s := st.State()
	if got := out.(map[string]int); got["sessions"] != 1 || got["teams"] != 1 || len(s.Sessions) != 3 || len(s.Restore) != 0 {
		t.Fatalf("fresh = %v, sessions %d, restore %v", got, len(s.Sessions), s.Restore)
	}
	if d := s.Domain; findTeam(&d, "team1").State != "closed" || findTeam(&d, "team2").State != "active" {
		t.Fatalf("teams: %+v", d.Teams)
	}
	if i := sessionIndex(&s, "planaaaaaaaa"); i < 0 || s.Sessions[i].TeamID != "" {
		t.Fatalf("plan kept the closed team: %+v", s.Sessions)
	}
	if s.Domain.Approvals[0].State != "denied" {
		t.Fatalf("formation request: %+v", s.Domain.Approvals[0])
	}
}
