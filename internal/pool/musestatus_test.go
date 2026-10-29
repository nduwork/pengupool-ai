package pool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
	age := func() {                // the turn ran a while before it was interrupted
		p.mu.Lock()
		if t := p.turns["museaaaaaaaa"]; t != nil {
			t.Start = t.Start.Add(-time.Minute)
		}
		p.mu.Unlock()
	}
	age()
	step("Interrupted", "idle") // no Stop
	step("UserPromptSubmit", "working")
	p.asking("museaaaaaaaa", "AskUserQuestion", true) // the new turn has a question open
	step("Interrupted", "working") // late: the owner's new turn is not closed by it
	p.mu.Lock()
	asking := p.askingOwner["museaaaaaaaa"]
	p.mu.Unlock()
	if !asking {
		t.Fatal("a late Interrupt cleared the new turn's question")
	}
	step("TurnFailed", "idle")
}

// What the owner types while a Muse approved tool runs (still "approval" to the pool) is the owner's.
func TestMuseApprovalStillCreditsTheOwner(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool", RequireOwnerInput: true})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions,
			store.Session{ID: "museaaaaaaaa", State: "running", Status: "approval", Harness: harness.Muse},
			store.Session{ID: "claudeaaaaaa", State: "running", Status: "approval", Harness: harness.Claude})
		return nil
	})
	p.OwnerInput("museaaaaaaaa", "stop, use the other branch")
	p.OwnerInput("claudeaaaaaa", "1")
	if !p.ownerSent("museaaaaaaaa", "stop, use the other branch") {
		t.Fatal("the owner's words in a Muse approval were not credited")
	}
	if p.ownerSent("claudeaaaaaa", "1") {
		t.Fatal("a Claude permission answer was credited as a prompt")
	}
}
