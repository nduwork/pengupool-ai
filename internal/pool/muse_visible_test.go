package pool

import (
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A paired device's view of a session follows the session's ACTIVE
// memberships: any active membership on an active team with iOS access
// makes the session visible, and only that. The first team record that
// mentions the session must not decide — in the live rebuild a team's
// LEAD was hidden from muse.sessions and unaddressable by muse.say and
// muse.action while its own SME, serving on the visible team, showed
// fine, because an earlier membership record without iOS access won the
// old first-match check. A membership the session has left (closed)
// counts neither way: it neither hides nor leaks visibility.
func TestMuseVisibleFollowsActiveMembership(t *testing.T) {
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		// app (team1) has iOS access; ops (team2) does not.
		s.Domain.Teams[0].IOSAccess = true
		// An earlier active team without iOS access also lists app's
		// lead as its main: that record must not hide the lead.
		s.Domain.Teams = append([]protocol.Team{{
			ID: "team0", Name: "old", State: "active", CreatedAt: "2026-01-01T00:00:00Z",
			Members: []protocol.Member{
				{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"},
			},
		}}, s.Domain.Teams...)
		// A session on no team at all, one whose only membership is
		// closed (on ops, no iOS access), and one that left app (iOS
		// access) but actively serves on ops.
		s.Sessions = append(s.Sessions,
			store.Session{ID: "freeaaaaaaaa", Name: "free", State: "running"},
			store.Session{ID: "goneaaaaaaaa", Name: "gone", State: "running"},
			store.Session{ID: "mixaaaaaaaaa", Name: "mix", State: "running"})
		s.Domain.Teams[2].Members = append(s.Domain.Teams[2].Members,
			protocol.Member{SessionID: "goneaaaaaaaa", Name: "gone", Role: "helper", State: "closed"})
		s.Domain.Teams[2].Members = append(s.Domain.Teams[2].Members,
			protocol.Member{SessionID: "mixaaaaaaaaa", Name: "mix", Role: "helper", State: "active"})
		s.Domain.Teams[1].Members = append(s.Domain.Teams[1].Members,
			protocol.Member{SessionID: "mixaaaaaaaaa", Name: "mix", Role: "helper", State: "closed"})
		return nil
	})
	// After the prepend and appends: teams are [old, app, ops]; the mix
	// member records landed on app (closed) and ops (active).
	for _, tc := range []struct {
		session string
		want    bool
	}{
		{"leadaaaaaaaa", true},  // active on app (iOS) despite the earlier old-team record
		{"webaaaaaaaaa", true},  // active on app only
		{"otheraaaaaaa", false}, // active on ops only: no iOS access
		{"freeaaaaaaaa", true},  // on no team: the owner's own session
		{"goneaaaaaaaa", true},  // its only membership is closed: on no team now
		{"mixaaaaaaaaa", false}, // left the iOS team; actively serves on ops only
	} {
		if got := p.museVisible(tc.session); got != tc.want {
			t.Errorf("museVisible(%s) = %v, want %v", tc.session, got, tc.want)
		}
	}
}
