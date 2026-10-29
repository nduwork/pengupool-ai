package pool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A Muse session shows what it waits on: Muse asks its permission before PreToolUse, so the call
// stays an approval until it runs or fails; a failed or interrupted turn ends idle with no Stop.
func TestMuseStatusFollowsItsEvents(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "museaaaaaaaa", Name: "m", State: "running", Status: "idle", Harness: harness.Muse, Cwd: t.TempDir(), ConvID: "conv-1"})
		return nil
	})
	step := func(event, want string) {
		t.Helper()
		pl := `{"session_id":"conv-1","tool_name":"bash","tool_input":{"command":"curl x"}}`
		if _, err := p.hookEvent(context.Background(), hookIn{Harness: harness.Muse, Event: event, Payload: json.RawMessage(pl)}); err != nil {
			t.Fatal(err)
		}
		if s, _ := p.session("museaaaaaaaa"); s.Status != want {
			t.Fatalf("after %s: %s, want %s", event, s.Status, want)
		}
	}
	step("UserPromptSubmit", "working")
	step("PermissionRequest", "approval")
	step("PreToolUse", "approval") // the dialog is still open
	step("ToolFailed", "working")  // denied: the turn goes on
	step("PreToolUse", "working")
	step("PermissionRequest", "approval")
	step("PreToolUse", "approval")
	step("PostToolUse", "working") // allowed and ran
	step("TurnEnded", "idle")      // interrupted, no Stop
}
