package pool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A Muse turn end sets the session idle and delivers what waited for it.
func TestMuseTurnEndIdlesAndDelivers(t *testing.T) {
	p, st := helperPool(t)
	dir := t.TempDir()
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "musestuck1", Name: "muse", State: "running",
			Harness: harness.Muse, Cwd: dir, Status: "idle", ConvID: "muse-t1"})
		s.Domain.Messages = append(s.Domain.Messages, protocol.TeamMessage{ID: "m1", TeamID: "team1",
			From: "leadaaaaaaaa", To: "musestuck1", Text: "hello worker", State: "queued", Type: "tell"})
		return nil
	})
	var typed []string
	p.testKeys = func(id, keys string) error { typed = append(typed, id+":"+keys); return nil }
	hook := func(event, payload string) {
		t.Helper()
		if _, err := p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: event,
			Payload: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	status := func() string {
		t.Helper()
		s, _ := p.session("musestuck1")
		return s.Status
	}
	hook("UserPromptSubmit", `{"session_id":"muse-t1","prompt":"do the thing"}`)
	hook("PostToolUse", `{"session_id":"muse-t1","tool_name":"read_file","tool_use_id":"u1"}`)
	if status() != "working" {
		t.Fatalf("turn did not mark working: %s", status())
	}
	hook("Stop", `{"session_id":"muse-t1","stop_hook_active":false,"last_assistant_message":"done"}`)
	if status() != "idle" {
		t.Fatalf("Stop did not idle the Muse session: %s", status())
	}
	if len(typed) != 1 || !strings.Contains(typed[0], "hello worker") {
		t.Fatalf("queued message was not typed on Stop: %q", typed)
	}
	var state string
	for _, m := range st.State().Domain.Messages {
		if m.ID == "m1" {
			state = m.State
		}
	}
	if state != "delivered" {
		t.Fatalf("message state = %q, want delivered", state)
	}
	// A follow-up turn still works: the watermark never jams newer hooks.
	hook("UserPromptSubmit", `{"session_id":"muse-t1","prompt":"once more"}`)
	if status() != "working" {
		t.Fatalf("second turn did not mark working: %s", status())
	}
	hook("Stop", `{"session_id":"muse-t1","stop_hook_active":false,"last_assistant_message":"done again"}`)
	if status() != "idle" {
		t.Fatalf("second Stop did not idle: %s", status())
	}
}

// The watermark: hooks that arrived before the latest processed turn end are stale, except the
// enders themselves, which always process.
func TestStaleHookWatermark(t *testing.T) {
	p := &Pool{}
	p.noteTurnEnd("s", 7)
	for _, c := range []struct {
		event string
		seq   uint64
		want  bool
	}{
		{"PostToolUse", 6, true},
		{"PreToolUse", 6, true},
		{"UserPromptSubmit", 6, true},
		{"ToolFailed", 6, true},
		{"PermissionRequest", 6, true},
		{"Notification", 6, true},
		{"SessionStart", 6, true},
		{"PostToolUse", 7, false},
		{"PostToolUse", 8, false},
		{"Stop", 6, false},
		{"TurnFailed", 6, false},
		{"Interrupted", 6, false},
		{"SessionEnd", 6, false},
		{"Delivered", 6, false},
	} {
		if got := p.staleTurnHook("s", c.event, c.seq); got != c.want {
			t.Errorf("staleTurnHook(%s, %d) = %v, want %v", c.event, c.seq, got, c.want)
		}
	}
	if p.staleTurnHook("other", "PostToolUse", 1) {
		t.Error("unknown session has no watermark")
	}
	// A late duplicate end never moves the watermark back.
	p.noteTurnEnd("s", 5)
	if !p.staleTurnHook("s", "PostToolUse", 6) {
		t.Error("watermark regressed on an older end")
	}
}
