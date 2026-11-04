package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Muse's hooks carry no model: its own status line is the evidence, read per turn and logged; with no
// line, the --model it was started with; with neither, no model and no guess.
func TestMuseModelPerTurn(t *testing.T) {
	p, st := twoTeams(t)
	runLog := filepath.Join(t.TempDir(), "lead.log")
	screen := "  Muse Code 1.4.14\n── Voice input (⌥ + v to start) ────────\n❯\n────────\n  muse-spark-1.3-contributor · max · /work/repo · Auto-review"
	p.testScreen = func(id string) (string, bool) { return screen, true }
	_ = st.Update(func(s *store.State) error {
		s.Sessions[0].Harness, s.Sessions[0].RunLog = harness.Muse, runLog
		return nil
	})
	s, _ := p.session("leadaaaaaaaa")
	p.museTurnModel(s)
	if s, _ = p.session("leadaaaaaaaa"); s.Model != "muse-spark-1.3-contributor" || s.View().Agent != "Muse Spark · Meta · muse-spark-1.3-contributor" {
		t.Fatalf("model %q, label %q", s.Model, s.View().Agent)
	}
	screen = "Muse Code 1.4.14"
	_ = st.Update(func(st *store.State) error {
		st.Sessions[0].Model, st.Sessions[0].LaunchModel = "", "muse-spark-1.3"
		return nil
	})
	s, _ = p.session("leadaaaaaaaa")
	p.museTurnModel(s)
	_ = st.Update(func(st *store.State) error { st.Sessions[0].LaunchModel = ""; return nil })
	s, _ = p.session("leadaaaaaaaa")
	p.museTurnModel(s)
	b, _ := os.ReadFile(runLog)
	logged := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"turn: model muse-spark-1.3-contributor (Muse status line)", "turn: model muse-spark-1.3 (--model)", "turn: model not shown"}
	if len(logged) != 3 {
		t.Fatalf("logged %q", logged)
	}
	for i, w := range want {
		if !strings.HasSuffix(logged[i], w) {
			t.Errorf("turn %d: %q, want %q", i, logged[i], w)
		}
	}
	if got := s.View().Agent; got != "Muse Code · Meta" {
		t.Fatalf("no model: %q", got)
	}
	if got := store.AgentLabel(harness.Claude, "opus"); got != "" {
		t.Fatalf("a Claude session got %q", got)
	}
}
