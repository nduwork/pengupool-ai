package pool

import (
	"context"
	"testing"

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
		return nil
	})
	p.MarkLost([]string{"leadaaaaaaaa", "webaaaaaaaaa", "planaaaaaaaa"})
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
}
