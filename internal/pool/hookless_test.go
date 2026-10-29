package pool

import (
	"context"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// An agent launch the owner prompted 2+ minutes ago with no hook since is flagged; its first hook
// clears it for good, so a long quiet turn never brings it back. Shells, a prompt typed into a
// first-run dialog, and a later launch are never flagged.
func TestHooksMissing(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	launched := time.Now().Add(-10 * time.Minute)
	_ = st.Update(func(s *store.State) error {
		for _, x := range []store.Session{
			{ID: "claudeaaaaaa", Harness: harness.Claude, Status: "starting"},
			{ID: "shellaaaaaaa", Harness: "shell"},
			{ID: "dialogaaaaaa", Harness: harness.Codex, Status: "starting", Waiting: "folder trust"},
		} {
			x.Name, x.State, x.StartedAt = x.ID, "running", launched
			s.Sessions = append(s.Sessions, x)
		}
		return nil
	})
	missing := func(id string) bool { s, _ := p.session(id); return s.HooksMissing }
	for _, id := range []string{"claudeaaaaaa", "shellaaaaaaa", "dialogaaaaaa"} {
		p.OwnerInput(id, "fix the loader")
	}
	p.watchHooks(time.Now().Add(time.Minute))
	if missing("claudeaaaaaa") {
		t.Fatal("flagged a minute after the prompt")
	}
	p.watchHooks(time.Now().Add(3 * time.Minute))
	if !missing("claudeaaaaaa") || missing("shellaaaaaaa") || missing("dialogaaaaaa") {
		t.Fatalf("claude %v, shell %v, dialog %v", missing("claudeaaaaaa"), missing("shellaaaaaaa"), missing("dialogaaaaaa"))
	}
	if s, _ := p.session("claudeaaaaaa"); !s.View().HooksMissing {
		t.Fatal("the view does not carry it")
	}
	if _, err := p.hookEvent(context.Background(), hookIn{SessionID: "claudeaaaaaa", Event: "Status"}); err != nil {
		t.Fatal(err)
	}
	if missing("claudeaaaaaa") {
		t.Fatal("the first hook did not clear it")
	}
	p.watchHooks(time.Now().Add(30 * time.Minute)) // a long quiet turn
	if missing("claudeaaaaaa") {
		t.Fatal("came back after a hook arrived")
	}
	// A later launch starts over: the earlier prompt does not count for it.
	_ = st.Update(func(s *store.State) error {
		s.Sessions[sessionIndex(s, "claudeaaaaaa")].StartedAt = time.Now().Add(time.Second)
		return nil
	})
	p.watchHooks(time.Now().Add(5 * time.Minute))
	if missing("claudeaaaaaa") {
		t.Fatal("a new launch was flagged for the last launch's prompt")
	}
}

// Healthy sessions never get the note: a Codex whose SessionStart comes only with the first prompt, and
// a Claude whose turn runs quiet for half an hour after its hooks arrived.
func TestHooksMissingNoFalseAlarm(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	launched := time.Now().Add(-time.Minute)
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions,
			store.Session{ID: "codexaaaaaaa", Name: "x", State: "running", Status: "starting", Harness: harness.Codex, StartedAt: launched},
			store.Session{ID: "quietaaaaaaa", Name: "q", State: "running", Status: "idle", Harness: harness.Claude, StartedAt: launched})
		return nil
	})
	hook := func(id, event string) {
		t.Helper()
		if _, err := p.hookEvent(context.Background(), hookIn{SessionID: id, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	hook("quietaaaaaaa", "SessionStart")
	for _, id := range []string{"codexaaaaaaa", "quietaaaaaaa"} {
		p.OwnerInput(id, "fix the loader")
	}
	hook("codexaaaaaaa", "SessionStart") // Codex's first hooks come with the prompt
	hook("codexaaaaaaa", "UserPromptSubmit")
	for _, after := range []time.Duration{3 * time.Minute, 30 * time.Minute} {
		p.watchHooks(time.Now().Add(after))
		for _, id := range []string{"codexaaaaaaa", "quietaaaaaaa"} {
			if s, _ := p.session(id); s.HooksMissing {
				t.Fatalf("%s flagged %s after the prompt", id, after)
			}
		}
	}
}
