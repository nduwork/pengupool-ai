package pool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The owner's words are offered for wording inside the turn they opened: at its next tool round, or
// else by one continuation at its Stop; never a second one, and never on a continued Stop.
func TestWordingIsOfferedInTheSameTurn(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "claudeaaaaaa", Name: "c", State: "running", Status: "idle", Harness: harness.Claude, Cwd: t.TempDir()})
		return nil
	})
	hook := func(event string, pl map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(pl)
		out, err := p.hookEvent(context.Background(), hookIn{SessionID: "claudeaaaaaa", Event: event, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		return out.(HookOut).Stdout
	}
	ask := func() {
		p.askWording(agentdb.Signal{SessionID: "claudeaaaaaa", Kind: "preference", Words: "bug fixes go before features"}, nil, 0)
	}
	status := func() string { s, _ := p.session("claudeaaaaaa"); return s.Status }

	// At the turn's next tool round.
	hook("UserPromptSubmit", map[string]any{"prompt": "yes, bug fixes go before features"})
	ask() // triage classified the owner's words while the turn ran
	if out := hook("PostToolUse", map[string]any{"tool_name": "Bash"}); !strings.Contains(out, "[PenguPool compass]") || !strings.Contains(out, "additionalContext") {
		t.Fatalf("not offered at the tool round: %s", out)
	}
	if out := hook("PostToolUse", map[string]any{"tool_name": "Bash"}); strings.Contains(out, "[PenguPool compass]") {
		t.Fatal("offered twice in a turn")
	}
	if out := hook("Stop", map[string]any{"stop_hook_active": false}); out != "" || status() != "idle" {
		t.Fatalf("a turn that was offered it already went on: %q, %s", out, status())
	}

	// A turn with no tool round left: one continuation at its Stop, and no second.
	delete(p.wording, "claudeaaaaaa")
	hook("UserPromptSubmit", map[string]any{"prompt": "yes, bug fixes go before features"})
	ask()
	out := hook("Stop", map[string]any{"stop_hook_active": false})
	if !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, "[PenguPool compass]") || status() != "working" {
		t.Fatalf("not offered at the turn's end: %q, %s", out, status())
	}
	if out := hook("Stop", map[string]any{"stop_hook_active": true}); out != "" || status() != "idle" {
		t.Fatalf("the continued turn went on again: %q, %s", out, status())
	}
	if err := wordWith(t, p, "claudeaaaaaa", compassWordIn{None: true}); err != nil {
		t.Fatalf("the offered wording could not be answered: %v", err)
	}

	// The usual case: triage reads the owner's words with the reply, after the Stop. The wording comes
	// at once as a notice, not at the next prompt.
	var notices []string
	p.testNotice = func(_, text string) error { notices = append(notices, text); return nil }
	hook("UserPromptSubmit", map[string]any{"prompt": "yes, bug fixes go before features"})
	hook("Stop", map[string]any{"stop_hook_active": false})
	ask()
	if len(notices) != 1 || !strings.Contains(notices[0], "[PenguPool compass]") {
		t.Fatalf("not offered after the turn: %q", notices)
	}
	if out := hook("PostToolUse", map[string]any{"tool_name": "Bash"}); strings.Contains(out, "[PenguPool compass]") {
		t.Fatal("offered again in the notice's own turn")
	}
}

// A wording asked while another ends (a session's end, an answer) is never read outside the lock.
func TestWordingAskedWhileAnotherEnds(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "claudeaaaaaa", Name: "c", State: "running", Status: "working", Harness: harness.Claude})
		return nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			p.wordingEnded("claudeaaaaaa")
		}
	}()
	for range 200 {
		p.askWording(agentdb.Signal{SessionID: "claudeaaaaaa", Kind: "said", Words: "small releases first"}, nil, 0)
	}
	<-done
}
