package pool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// The agent's pending question rides on the session record from its own hooks, and goes with the
// next hook about something else.
func TestPendingQuestion(t *testing.T) {
	p, _, _ := gateTeam(t)
	const id = "leadaaaaaaaa"
	hook := func(event string, pl map[string]any) {
		b, _ := json.Marshal(pl)
		if _, err := p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: "claude", Event: event, Payload: b}); err != nil {
			t.Fatal(err)
		}
	}
	question := func() string { s, _ := p.session(id); return s.View().Question }
	want := func(step, q string) {
		t.Helper()
		if got := question(); got != q {
			t.Fatalf("%s: question = %q, want %q", step, got, q)
		}
	}

	hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git push origin main"}})
	want("permission", "Bash: git push origin main")
	hook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})
	want("its notification keeps the fuller summary", "Bash: git push origin main")
	hook("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git push origin main"}})
	want("answered", "")

	hook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Write"})
	want("notification alone", "Claude needs your permission to use Write")
	hook("Stop", map[string]any{})
	want("turn ended", "")

	hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which database?", "options": []any{map[string]any{"label": "Postgres"}, map[string]any{"label": "SQLite"}}},
	}}})
	want("question", "Which database? (Postgres / SQLite)")
	hook("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which database?", "options": []any{map[string]any{"label": "Postgres"}, map[string]any{"label": "SQLite"}}},
	}}})
	want("its permission twin says the same", "Which database? (Postgres / SQLite)")
	hook("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"})
	want("chosen", "")

	// Parallel calls: another tool's hooks leave the question; its own call's hook ends it.
	hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_use_id": "tuA", "tool_input": map[string]any{"command": "make deploy"}})
	hook("PreToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "tuB", "tool_input": map[string]any{"file_path": "a.go"}})
	hook("PostToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "tuB", "tool_input": map[string]any{"file_path": "a.go"}})
	hook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})
	want("another call ran", "Bash: make deploy")
	hook("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tuA", "tool_input": map[string]any{"command": "make deploy"}})
	want("its own call went ahead", "")

	hook("PreToolUse", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "\n## Add caching to the API\n\n1. ..."}})
	want("plan", "Approve the plan: Add caching to the API")

	// A session that is no longer running asks nothing.
	_ = p.st.Update(func(s *store.State) error {
		s.Sessions[sessionIndex(s, id)].State = "lost"
		return nil
	})
	want("lost", "")
	_ = p.st.Update(func(s *store.State) error {
		s.Sessions[sessionIndex(s, id)].State = "running"
		return nil
	})

	// The paired device's overview carries it too (a team the owner opened to the phone).
	_ = p.st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams {
			s.Domain.Teams[i].IOSAccess = true
		}
		return nil
	})
	res, err := p.museSessions(context.Background(), museCaller(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range res.([]museSessionView) {
		if v.Session == id {
			found = v.Question == "Approve the plan: Add caching to the API"
		}
	}
	if !found {
		t.Fatalf("muse sessions: %+v", res)
	}
}
