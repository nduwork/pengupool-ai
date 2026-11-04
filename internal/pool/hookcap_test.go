package pool

import (
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Muse drops a hook answer whose stdout passes 16 KiB: a capped harness keeps the team
// sections whole and lets the compass take what is left, so the roster and the triage
// note survive. Uncapped harnesses keep the whole context.
func TestContextOutFitsMuseCap(t *testing.T) {
	fat := func(t *testing.T, kind string) string {
		t.Helper()
		p, st := twoTeams(t)
		own := p.owner()
		for i := 0; i < 200; i++ {
			body := "Preference " + strings.Repeat("keep it simple. ", 20)
			if _, err := p.db.PutLine(agentdb.Line{Owner: own, Body: body}); err != nil {
				t.Fatal(err)
			}
		}
		_ = st.Update(func(s *store.State) error {
			for i := range s.Sessions {
				if s.Sessions[i].ID == "leadaaaaaaaa" {
					s.Sessions[i].Harness = kind
				}
			}
			return nil
		})
		return p.contextOut("leadaaaaaaaa", "SessionStart").Stdout
	}
	muse := fat(t, harness.Muse)
	if len(muse) >= 16<<10 {
		t.Fatalf("muse hook answer is %d bytes, past the 16 KiB ceiling", len(muse))
	}
	for _, want := range []string{"## Team members", "- web (session ", "send --to", "LEAD", "… more: search"} {
		if !strings.Contains(muse, want) {
			t.Errorf("capped context lacks %q", want)
		}
	}
	// The compass has its own budget (maxCompassContext, 8 KiB), under the Muse ceiling: an uncapped
	// harness never keeps less, and its compass section stays within that budget.
	claude := fat(t, harness.Claude)
	if len(claude) < len(muse) {
		t.Fatalf("uncapped context (%d bytes) kept less than capped (%d)", len(claude), len(muse))
	}
	if i, j := strings.Index(claude, "## The owner's compass"), strings.Index(claude, "… more: search"); i < 0 || j < 0 || j-i > maxCompassContext {
		t.Fatalf("uncapped compass section spans %d bytes, past its %d budget", j-i, maxCompassContext)
	}
}
