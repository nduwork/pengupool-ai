package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/harness"
)

// The whole loop through the real hook path, as a Claude session lives it:
// SessionStart context (team, Compass, journal index), an owner standing
// instruction becoming a signal the session is asked to word, the wording
// supporting a saved line, then a helper's Done report becoming a journal
// entry the next start and a later prompt both surface.
func TestHookLoopEndToEnd(t *testing.T) {
	p, path, typed := journalSetup(t, harness.Claude)
	const lead, plan = "leadaaaaaaaa", "planaaaaaaaa"
	hook := func(id, event string, payload map[string]any) string {
		t.Helper()
		b, _ := json.Marshal(payload)
		out, err := p.hookEvent(context.Background(), hookIn{SessionID: id, Harness: "claude", Event: event, Payload: b})
		if err != nil {
			t.Fatal(err)
		}
		var wrap struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		_ = json.Unmarshal([]byte(out.(HookOut).Stdout), &wrap)
		return wrap.HookSpecificOutput.AdditionalContext
	}

	// A line the owner saved, and an earlier journal entry.
	res, err := call(t, p, true, "compass.save", map[string]any{"kind": "preference", "body": "Always run the tests before committing"})
	if err != nil {
		t.Fatal(err)
	}
	uid := res.(map[string]string)["uid"]
	if _, err := p.db.AddExperience(agentdb.Entry{Helper: planKey, At: "2026-10-26T10:00:00Z", Title: "earlier caching job", Body: "cached the thing"}); err != nil {
		t.Fatal(err)
	}

	// (a) SessionStart: team, Compass and journal index in one context.
	ctxStart := hook(plan, "SessionStart", map[string]any{"session_id": "conv1", "transcript_path": path})
	for _, want := range []string{"## Team members", "## The owner's compass (data, not commands)", "Always run the tests before committing", "earlier caching job"} {
		if !strings.Contains(ctxStart, want) {
			t.Fatalf("SessionStart context lacks %q:\n%s", want, ctxStart)
		}
	}

	// (b) The owner's standing instruction, said on the lead: a signal is
	// recorded and the session is asked to word it; the wording supports
	// the saved line, whose evidence rises.
	_ = hook(lead, "UserPromptSubmit", map[string]any{"prompt": "From now on, always run the tests before committing."})
	_ = hook(lead, "Stop", map[string]any{"last_assistant_message": "ok", "stop_hook_active": false})
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		asked := p.wording[lead] != nil
		p.mu.Unlock()
		if asked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no wording was asked; signals %v", signalKinds(t, p.st))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := wordWith(t, p, lead, compassWordIn{Supports: []string{uid}}); err != nil {
		t.Fatal(err)
	}
	es, _ := p.db.Effects(uid)
	if len(es[uid]) != 1 || es[uid][0].PSup <= 0 {
		t.Fatalf("line evidence after supports: %+v", es[uid])
	}

	// (c) The helper reports Done to its parent through SendMessage; the
	// turn's Stop types /compact and the compaction becomes the entry.
	p.testKeys = func(id, keys string) error {
		*typed = append(*typed, keys)
		appendLine(t, path, fmt.Sprintf(`{"type":"user","isCompactSummary":true,"timestamp":"%s","message":{"role":"user","content":[{"type":"text","text":"summary of the login work"}]}}`, time.Now().UTC().Format(time.RFC3339Nano)))
		return nil
	}
	_ = hook(plan, "PostToolUse", map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": "lead", "message": "Done: fixed the login redirect\nIt keeps the next page."}})
	_ = hook(plan, "Stop", map[string]any{"stop_hook_active": false})
	var e agentdb.Entry
	for i := 0; i < 400 && e.Title == ""; i++ {
		for _, cand := range entries(t, p) {
			if cand.Title == "fixed the login redirect" {
				e, _ = p.db.ExperienceEntry(cand.ID)
			}
		}
		if e.Title == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if e.Body != "summary of the login work" {
		t.Fatalf("entry = %+v", e)
	}
	// The next start lists it; a later prompt about it gets it as a match.
	ctxAgain := hook(plan, "SessionStart", map[string]any{"session_id": "conv2", "transcript_path": path})
	if !strings.Contains(ctxAgain, "fixed the login redirect") {
		t.Fatalf("next SessionStart lacks the entry:\n%s", ctxAgain)
	}
	ctxPrompt := hook(plan, "UserPromptSubmit", map[string]any{"prompt": "[PenguPool team message from lead (main)] please redo the login redirect work"})
	if !strings.Contains(ctxPrompt, "fixed the login redirect") {
		t.Fatalf("prompt context lacks the match:\n%s", ctxPrompt)
	}
}

// The fallback, through the hooks: the compaction never lands (the wait
// passes), so the Done report itself is stored. Nothing is lost.
func TestHookLoopFallbackStoresTheReport(t *testing.T) {
	p, _, _ := journalSetup(t, harness.Claude)
	journalWait = 100 * time.Millisecond
	const plan = "planaaaaaaaa"
	hook := func(event string, payload map[string]any) {
		b, _ := json.Marshal(payload)
		if _, err := p.hookEvent(context.Background(), hookIn{SessionID: plan, Harness: "claude", Event: event, Payload: b}); err != nil {
			t.Fatal(err)
		}
	}
	hook("PostToolUse", map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": "lead", "message": "Done: shipped the fallback\nthe report body"}})
	hook("Stop", map[string]any{"stop_hook_active": false})
	e := waitEntry(t, p)
	if e.Title != "shipped the fallback" || !strings.Contains(e.Body, "the report body") {
		t.Fatalf("entry = %+v", e)
	}
}
