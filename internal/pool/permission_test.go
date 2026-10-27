package pool

import (
	"context"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

func permissionPool(t *testing.T) *Pool {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "permaaaaaaaa", Name: "perm", State: "running", Cwd: t.TempDir()})
		return nil
	})
	return p
}

func permHook(t *testing.T, p *Pool, event, payload string) HookOut {
	t.Helper()
	out, err := p.hookEvent(context.Background(), hookIn{SessionID: "permaaaaaaaa", Harness: "claude", Event: event, Payload: []byte(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return out.(HookOut)
}

func permStatus(p *Pool) string {
	s, _ := p.session("permaaaaaaaa")
	return s.Status
}

func TestPermissionRequestIsNotIntercepted(t *testing.T) {
	p := permissionPool(t)
	start := time.Now()
	out := permHook(t, p, "PermissionRequest", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
	if out.Stdout != "" || out.Exit != 0 || time.Since(start) > time.Second {
		t.Fatalf("the hook intercepted or waited: %+v after %v", out, time.Since(start))
	}
	if n := len(p.st.State().Domain.Approvals); n != 0 {
		t.Fatalf("%d approvals created", n)
	}
	if got := permStatus(p); got != "approval" {
		t.Fatalf("status %q, want approval", got)
	}
}

func TestPermissionMarkerClearsWhenAgentContinues(t *testing.T) {
	p := permissionPool(t)
	permHook(t, p, "PermissionRequest", `{"tool_name":"Bash"}`)
	permHook(t, p, "PostToolUse", `{"tool_name":"Bash"}`)
	if got := permStatus(p); got != "working" {
		t.Fatalf("after PostToolUse: %q", got)
	}
	permHook(t, p, "PermissionRequest", `{"tool_name":"Bash"}`)
	permHook(t, p, "Stop", `{}`)
	if got := permStatus(p); got != "idle" {
		t.Fatalf("after Stop: %q", got)
	}
}
