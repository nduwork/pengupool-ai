package pool

import (
	"context"
	"encoding/json"
	"testing"
)

// A prompt waiting on the owner keeps the session's needs-input status until that same call goes on:
// an unrelated parallel tool does not clear it. Answered (the call runs), or the turn ending
// (denied, interrupted), clears it. An AskUserQuestion waits from its PreToolUse.
func TestPendingPromptKeepsNeedsInput(t *testing.T) {
	p, _, _ := gateTeam(t)
	const id = "leadaaaaaaaa"
	hook := func(event string, pl map[string]any) {
		t.Helper()
		b, _ := json.Marshal(pl)
		if _, err := p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: "claude", Event: event, Payload: b}); err != nil {
			t.Fatal(err)
		}
	}
	state := func() (string, string) { s, _ := p.session(id); return s.Status, s.Question }
	bash := map[string]any{"tool_name": "Bash", "tool_use_id": "waiting-call", "tool_input": map[string]any{"command": "make deploy"}}
	hook("PermissionRequest", bash)
	hook("PreToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "parallel-call", "tool_input": map[string]any{"file_path": "a.go"}})
	hook("PostToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "parallel-call"})
	if st, q := state(); st != "approval" || q == "" {
		t.Fatalf("an unrelated tool cleared the waiting prompt: status=%s question=%q", st, q)
	}
	hook("PostToolUse", bash) // answered: the call ran
	if st, q := state(); st != "working" || q != "" {
		t.Fatalf("answered prompt still waiting: status=%s question=%q", st, q)
	}
	hook("PermissionRequest", bash)
	hook("Stop", map[string]any{}) // denied or interrupted: the turn ends
	if st, q := state(); st == "approval" || q != "" {
		t.Fatalf("an ended turn still waits: status=%s question=%q", st, q)
	}
	hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_use_id": "question-call", "tool_input": map[string]any{"questions": []any{map[string]any{"question": "Which database?"}}}})
	if st, q := state(); st != "approval" || q == "" {
		t.Fatalf("a pending question has no needs-input: status=%s question=%q", st, q)
	}
	hook("Notification", map[string]any{"notification_type": "idle_prompt"}) // escaped with Esc: back at the prompt
	if st, q := state(); st != "idle" || q != "" {
		t.Fatalf("an escaped question stayed: status=%s question=%q", st, q)
	}
}
