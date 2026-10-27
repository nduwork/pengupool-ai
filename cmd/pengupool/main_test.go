package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

func TestActivityMatchesTheMap(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for _, c := range []struct {
		s    sessionRow
		want string
	}{
		{sessionRow{State: "running", Harness: "omp", Status: "idle", ActiveAt: ago(20 * time.Hour)}, "stale"},
		{sessionRow{State: "running", Harness: "omp", Status: "idle", ActiveAt: ago(time.Minute)}, "waiting"},
		{sessionRow{State: "running", Harness: "omp", Status: "idle", Role: "helper", ActiveAt: ago(20 * time.Hour)}, "waiting"},
		{sessionRow{State: "running", Harness: "claude", Status: "idle", Role: "main", ActiveAt: ago(20 * time.Hour)}, "stale"},
		{sessionRow{State: "running", Harness: "claude", Status: "working", ActiveAt: ago(time.Hour)}, "active"},
		{sessionRow{State: "running", Harness: "claude", Status: "approval"}, "approval"},
		{sessionRow{State: "running", Harness: "shell"}, "terminal"},
		{sessionRow{State: "exited", Harness: "claude"}, "-"},
	} {
		if got := activity(c.s, now); got != c.want {
			t.Errorf("activity(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

// A starting agent stuck on a first-run dialog shows it in ls, so the owner knows to open its terminal.
func TestLsShowsAFirstRunDialog(t *testing.T) {
	s := sessionRow{ID: "abcdef123456", Name: "web", State: "running", Harness: "claude", Status: "starting", Waiting: "folder trust", Cols: 120, Rows: 32, Cwd: "/w"}
	if got := activity(s, time.Now()); got != "dialog" {
		t.Errorf("activity = %q", got)
	}
	if got := describe(s); !strings.Contains(got, "waiting on a dialog: folder trust") {
		t.Errorf("describe = %q", got)
	}
	s.Waiting = ""
	if got := activity(s, time.Now()); got != "starting" || strings.Contains(describe(s), "dialog") {
		t.Errorf("no dialog: %q %q", got, describe(s))
	}
}

func TestExitCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{&rpcError{protocol.CodeNoDecider, "no decision model: set one in Settings → Models & agents"}, 3},
		{fmt.Errorf("decide: %w", &rpcError{protocol.CodeNoDecider, "x"}), 3},
		{&rpcError{protocol.CodeInvalidArgument, "bad"}, 1},
		{errors.New("dial: no daemon"), 1},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	if e := (&rpcError{protocol.CodeConflict, "decision limit reached (30 an hour)"}); e.Error() != "decision limit reached (30 an hour)" {
		t.Errorf("message %q", e.Error())
	}
}

func TestHookWait(t *testing.T) {
	if hookWait("Stop") != 8*time.Second || hookWait("PreToolUse") != 3*time.Second || hookWait("UserPromptSubmit") != 3*time.Second {
		t.Fatal("a Stop waits 8 s for the go-ahead; the other hooks 3 s")
	}
}

// `memory search` is the old name of `search`: same arguments, same path.
func TestMemorySearchIsAnAliasOfSearch(t *testing.T) {
	if err := cmdMemory([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "usage: pengupool search WORDS") {
		t.Errorf("memory nope: %v", err)
	}
	for name, err := range map[string]error{"search": cmdSearch(nil), "memory search": cmdMemory([]string{"search"})} {
		if err == nil || !strings.Contains(err.Error(), "give some words") {
			t.Errorf("%s with no words: %v", name, err)
		}
	}
}

// With no daemon to answer, a PreToolUse fails closed (the floor); other hooks stay silent.
func TestHookFailsClosedForPreToolUse(t *testing.T) {
	t.Setenv("PENGUPOOL_SESSION_ID", "s1")
	t.Setenv("PENGUPOOL_STORE", t.TempDir())
	run := func(h, event, in string) string {
		t.Helper()
		inR, inW, _ := os.Pipe()
		outR, outW, _ := os.Pipe()
		go func() { _, _ = inW.WriteString(in); inW.Close() }()
		oldIn, oldOut := os.Stdin, os.Stdout
		os.Stdin, os.Stdout = inR, outW
		err := cmdHook([]string{h, event})
		os.Stdin, os.Stdout = oldIn, oldOut
		outW.Close()
		b, _ := io.ReadAll(outR)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if out := run("claude", "PreToolUse", `{"tool_name":"Bash"}`); !strings.Contains(out, `"permissionDecision":"ask"`) {
		t.Errorf("claude, no daemon: %q", out)
	}
	if out := run("codex", "PreToolUse", `{"tool_name":"Bash"}`); !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Errorf("codex, no daemon: %q", out)
	}
	if out := run("claude", "PreToolUse", `{"x":"`+strings.Repeat("a", 1<<20)+`"}`); !strings.Contains(out, "too large") {
		t.Errorf("an oversized payload: %.80q", out)
	}
	if out := run("claude", "PreToolUse", "not json"); !strings.Contains(out, "could not be read") {
		t.Errorf("an unreadable payload: %q", out)
	}
	for _, ev := range []string{"PostToolUse", "Stop", "UserPromptSubmit"} {
		if out := run("claude", ev, `{}`); out != "" {
			t.Errorf("%s, no daemon: %q", ev, out)
		}
	}
}
