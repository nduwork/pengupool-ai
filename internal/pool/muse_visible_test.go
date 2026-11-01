package pool

import (
	"context"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A rebuilt team closes the old one: the old record (closed, no iOS access) still lists the LEAD,
// first in state order. A paired device's view follows the session's active membership on an active
// team, so that record neither hides the LEAD nor names its team in the title.
func TestMuseVisibleFollowsActiveMembership(t *testing.T) {
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		s.Domain.Teams[0].IOSAccess = true // app has iOS access; ops does not
		s.Domain.Teams = append([]protocol.Team{{
			ID: "team0", Name: "old", State: "closed", CreatedAt: "2026-01-01T00:00:00Z",
			Members: []protocol.Member{
				{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"},
				{SessionID: "webaaaaaaaaa", Name: "web", Role: "helper", State: "closed"},
			},
		}}, s.Domain.Teams...)
		// One session on no team; one whose only membership (on ops) is closed; one that left app
		// and serves on ops.
		s.Sessions = append(s.Sessions,
			store.Session{ID: "freeaaaaaaaa", Name: "free", State: "running"},
			store.Session{ID: "goneaaaaaaaa", Name: "gone", State: "running"},
			store.Session{ID: "mixaaaaaaaaa", Name: "mix", State: "running"})
		s.Domain.Teams[2].Members = append(s.Domain.Teams[2].Members,
			protocol.Member{SessionID: "goneaaaaaaaa", Name: "gone", Role: "helper", State: "closed"},
			protocol.Member{SessionID: "mixaaaaaaaaa", Name: "mix", Role: "helper", State: "active"})
		s.Domain.Teams[1].Members = append(s.Domain.Teams[1].Members,
			protocol.Member{SessionID: "mixaaaaaaaaa", Name: "mix", Role: "helper", State: "closed"})
		return nil
	})
	for _, tc := range []struct {
		session string
		want    bool
	}{
		{"leadaaaaaaaa", true},  // active on app despite the closed old team listing it first
		{"webaaaaaaaaa", true},  // active on app; closed on the old team
		{"otheraaaaaaa", false}, // active on ops: no iOS access
		{"freeaaaaaaaa", true},  // on no team: the owner's own session
		{"goneaaaaaaaa", true},  // its only membership is closed: on no team now
		{"mixaaaaaaaaa", false}, // left app; serves on ops
	} {
		if got := p.museVisible(tc.session); got != tc.want {
			t.Errorf("museVisible(%s) = %v, want %v", tc.session, got, tc.want)
		}
	}

	res, err := p.museSessions(context.Background(), museCaller(), nil)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, v := range res.([]museSessionView) {
		titles[v.Session] = v.Title
	}
	for id, want := range map[string]string{"leadaaaaaaaa": "lead · app", "goneaaaaaaaa": "gone"} {
		if titles[id] != want {
			t.Errorf("title of %s = %q, want %q (only teams with iOS access are named)", id, titles[id], want)
		}
	}
}
