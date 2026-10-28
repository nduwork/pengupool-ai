package pool

import (
	"context"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// Only a session that was running and whose worker died with the machine is resumed: not one the owner
// stopped, not one that exited, not a terminal, not one with no transcript.
func TestRestoreLost(t *testing.T) {
	p := testPool(t)
	restoreGap = 0
	st := p.st
	var resumed []string
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		resumed = append(resumed, r.ID+"@"+r.Resume+"@"+r.Model+"@"+r.Effort)
		return store.Session{ID: r.ID}, st.Update(func(x *store.State) error {
			x.Sessions[sessionIndex(x, r.ID)].State = "running"
			return nil
		})
	}
	_ = st.Update(func(x *store.State) error {
		x.Sessions = []store.Session{
			{ID: "lost-agent", Name: "a", Harness: "claude", State: "lost", ConvID: "c1", Cwd: t.TempDir(), LaunchModel: "ollama/qwen3-coder", LaunchEffort: "high"},
			{ID: "lost-shell", Name: "b", Harness: "shell", State: "lost"},
			{ID: "lost-notx", Name: "c", Harness: "claude", State: "lost"},
			{ID: "exited", Name: "d", Harness: "claude", State: "exited", ConvID: "c4"},
			{ID: "lost-before", Name: "e", Harness: "claude", State: "lost", ConvID: "c5"}, // lost in an earlier run
		}
		return nil
	})
	p.RestoreLost(context.Background(), []string{"lost-agent", "lost-shell", "lost-notx", "exited"})
	if len(resumed) != 1 || resumed[0] != "lost-agent@c1@ollama/qwen3-coder@high" {
		t.Fatalf("resumed %v", resumed)
	}
}
