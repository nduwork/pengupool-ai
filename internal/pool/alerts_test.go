package pool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
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

// Codex and OMP raise the same needs-input as Claude, from the events their launch wires
// (harness.codexEvents, omp_extension.ts): the waiting marker and the open permission gate the
// panel lists, both gone once the prompt is answered or the turn moves on.
func TestCodexAndOMPRaiseNeedsInput(t *testing.T) {
	p, _, _ := gateTeam(t)
	const id = "leadaaaaaaaa"
	for _, kind := range []string{harness.Codex, harness.OMP} {
		p.setSession(id, func(x *store.Session) { x.Harness, x.Status = kind, "idle" })
		hook := func(event string, pl map[string]any) {
			t.Helper()
			b, _ := json.Marshal(pl)
			if _, err := p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: kind, Event: event, Payload: b}); err != nil {
				t.Fatal(err)
			}
		}
		waiting := func(want bool, when string) {
			t.Helper()
			s, _ := p.session(id)
			gates, _ := p.db.OpenGates()
			perm := false
			for _, g := range gates {
				perm = perm || g.SessionID == id && g.Kind == "permission"
			}
			if got := s.Status == "approval" && s.Question != ""; got != want || perm != want {
				t.Fatalf("%s %s: status=%s question=%q permission gate=%v, want waiting=%v", kind, when, s.Status, s.Question, perm, want)
			}
		}
		// Codex names no call id on its PermissionRequest; OMP's extension passes the call's.
		bash := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "make deploy"}}
		if kind == harness.OMP {
			bash["tool_use_id"] = "call-1"
		}
		hook("UserPromptSubmit", map[string]any{"prompt": "[PenguPool team message from web (helper)] deploy"})
		hook("PermissionRequest", bash)
		waiting(true, "prompt shown")
		ran := map[string]any{"tool_name": "Bash", "tool_input": bash["tool_input"], "tool_use_id": "call-1"}
		hook("PostToolUse", ran) // approved: the call goes on (Codex's PostToolUse has the id its prompt lacked)
		waiting(false, "approved")
		hook("PermissionRequest", bash)
		waiting(true, "prompt shown again")
		if kind == harness.Codex {
			hook("Interrupted", map[string]any{}) // Esc: the turn is aborted, with no Stop
		} else {
			hook("ToolFailed", map[string]any{"tool_name": "Bash", "tool_use_id": "call-1"}) // denied: the turn goes on without it
		}
		waiting(false, "refused") // the marker and the gate: the panel item goes at once
		if g, _ := p.db.LastGate(id, "permission"); kind == harness.OMP && (g.Outcome != "owner" || g.OwnerProceed == nil || *g.OwnerProceed) {
			t.Fatalf("omp denial not settled as refused: %+v", g)
		}
		hook("Stop", map[string]any{"last_assistant_message": "done"})
	}
	// A ToolFailed naming no call (Muse's): which way it went is unknown, so its prompt stays open until
	// the turn ends. (A Muse hook resolves its session by process, so the shape is sent as OMP's.)
	b, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "make deploy"}})
	for _, ev := range []string{"UserPromptSubmit", "PermissionRequest", "ToolFailed"} {
		pl := b
		if ev == "UserPromptSubmit" {
			pl, _ = json.Marshal(map[string]any{"prompt": "[PenguPool team message from web (helper)] deploy"})
		}
		_, _ = p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: harness.OMP, Event: ev, Payload: pl})
	}
	if g, _ := p.db.LastGate(id, "permission"); g.Outcome != "pending" {
		t.Fatalf("a ToolFailed naming no call settled the prompt: %+v", g)
	}
	// OMP's ask tool waits on the owner from its tool call until its result.
	ask := map[string]any{"tool_name": "ask", "tool_use_id": "ask-1", "tool_input": map[string]any{"questions": []any{
		map[string]any{"id": "db", "question": "Which database?", "options": []any{map[string]any{"label": "pg"}, map[string]any{"label": "sqlite"}}}}}}
	b, _ = json.Marshal(ask)
	_, _ = p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: harness.OMP, Event: "PreToolUse", Payload: b})
	if s, _ := p.session(id); s.Status != "approval" || s.Question != "Which database? (pg / sqlite)" {
		t.Fatalf("omp ask: status=%s question=%q", s.Status, s.Question)
	}
	b, _ = json.Marshal(map[string]any{"tool_name": "ask", "tool_use_id": "ask-1"})
	_, _ = p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: harness.OMP, Event: "PostToolUse", Payload: b})
	if s, _ := p.session(id); s.Status != "working" || s.Question != "" {
		t.Fatalf("omp ask answered: status=%s question=%q", s.Status, s.Question)
	}
}
