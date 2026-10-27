package pool

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Muse resolver: a ConvID match wins, else the newest unbound Muse
// session in the same directory is bound, else the hook is an empty
// no-op with no state change.
func TestResolveMuseSession(t *testing.T) {
	p, st := helperPool(t)
	dir := t.TempDir()
	old := func(d time.Duration) time.Time { return time.Now().Add(-d).UTC() }
	mk := func(id, cwd string, at time.Time, conv string) {
		_ = st.Update(func(s *store.State) error {
			s.Sessions = append(s.Sessions, store.Session{ID: id, Name: id, State: "running", Harness: harness.Muse, Cwd: cwd, StartedAt: at, ConvID: conv})
			return nil
		})
	}
	mk("muse-old", dir, old(2*time.Hour), "")
	mk("muse-new", dir, old(time.Hour), "")
	mk("muse-bound", dir, old(3*time.Hour), "muse-uuid-1")
	// Newer than the live ones, but exited: never binds.
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "muse-gone", Name: "muse-gone", State: "exited", Harness: harness.Muse, Cwd: dir, StartedAt: old(30 * time.Minute)})
		return nil
	})

	// ConvID wins, whatever the directory.
	out, err := p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: "SessionStart",
		Payload: json.RawMessage(`{"session_id":"muse-uuid-1","cwd":"/elsewhere"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(HookOut).Stdout; !strings.Contains(got, "owner's compass") {
		t.Fatalf("ConvID match did not reach the session: %q", got)
	}
	// Newest unbound in the same directory is bound.
	out, err = p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: "UserPromptSubmit",
		Payload: json.RawMessage(`{"session_id":"muse-uuid-2","cwd":` + strconv.Quote(dir) + `,"prompt":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_ = out
	s, _ := p.session("muse-new")
	if s.ConvID != "muse-uuid-2" {
		t.Fatalf("newest was not bound: %+v", s)
	}
	if s2, _ := p.session("muse-old"); s2.ConvID != "" {
		t.Fatalf("older session bound instead: %+v", s2)
	}
	if s3, _ := p.session("muse-gone"); s3.ConvID != "" {
		t.Fatalf("exited session bound: %+v", s3)
	}

	// Unknown identity: empty HookOut, no state change.
	snap := st.State()
	out, err = p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: "Stop",
		Payload: json.RawMessage(`{"session_id":"muse-uuid-9","cwd":"/nope","stop_hook_active":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(HookOut); got.Stdout != "" || got.Exit != 0 {
		t.Fatalf("unknown session was not a silent no-op: %+v", got)
	}
	after := st.State()
	for i := range snap.Sessions {
		if snap.Sessions[i].ConvID != after.Sessions[i].ConvID || snap.Sessions[i].Status != after.Sessions[i].Status {
			t.Fatalf("state changed on unknown session: %+v -> %+v", snap.Sessions[i], after.Sessions[i])
		}
	}

	// Only the exited session left unbound: a new id still gets nothing.
	out, err = p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: "UserPromptSubmit",
		Payload: json.RawMessage(`{"session_id":"muse-uuid-3","cwd":` + strconv.Quote(dir) + `,"prompt":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_ = out
	if s, _ := p.session("muse-old"); s.ConvID != "muse-uuid-3" {
		t.Fatalf("live session was not bound: %+v", s)
	}
	out, err = p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: "UserPromptSubmit",
		Payload: json.RawMessage(`{"session_id":"muse-uuid-4","cwd":` + strconv.Quote(dir) + `,"prompt":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(HookOut); got.Stdout != "" {
		t.Fatalf("bound an exited session: %+v", got)
	}
	if s3, _ := p.session("muse-gone"); s3.ConvID != "" {
		t.Fatalf("exited session bound: %+v", s3)
	}

	// PreToolUse without a tool shape: allowed, empty.
	out, err = p.hookEvent(context.Background(), hookIn{Harness: "muse", Event: "PreToolUse",
		Payload: json.RawMessage(`{"session_id":"muse-uuid-2","cwd":` + strconv.Quote(dir) + `}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(HookOut); got.Stdout != "" {
		t.Fatalf("unknown tool shape was not allowed empty: %+v", got)
	}
}
