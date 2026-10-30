package pool

import (
	"context"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// A team's iOS access decides whether a paired device sees it (museTeamVisible); the desktop owner
// always sees every team.

type policyIn struct {
	TeamID    string `json:"team_id"`
	IOSAccess *bool  `json:"ios_access,omitempty"`
}

func (p *Pool) teamPolicy(_ context.Context, in policyIn) (any, error) {
	return nil, p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil {
			return notFound("no team %s", in.TeamID)
		}
		if in.IOSAccess != nil {
			t.IOSAccess = *in.IOSAccess
		}
		return nil
	})
}
