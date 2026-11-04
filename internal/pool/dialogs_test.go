package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestDialogReason(t *testing.T) {
	for screen, want := range map[string]string{
		"Do you trust the files in this folder?\n 1. Yes, proceed":           "folder trust",
		"> Do you trust the contents of this directory? Working with ...":    "folder trust",
		"Quick safety check: Is this a project you created or one you trust": "folder trust",
		"Welcome to Codex\n Sign in with ChatGPT":                            "sign-in",
		"Select login method:": "sign-in",
		"Choose the text style that looks best with your terminal": "setup",
		"? for shortcuts":     "",
		"╭ Claude Code ╮\n> ": "",
	} {
		if got := dialogReason(screen); got != want {
			t.Errorf("dialogReason(%q) = %q, want %q", screen, got, want)
		}
	}
}

// A starting session on a first-run dialog is marked, and the mark clears once it is past it.
func TestWatchStartupMarksAndClearsADialog(t *testing.T) {
	p, st := twoTeams(t)
	screen := "Do you trust the files in this folder?"
	p.testScreen = func(string) (string, bool) { return screen, true }
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Status, s.Sessions[i].Harness = "starting", "claude"
		}
		return nil
	})
	p.watchStartup()
	s, _ := p.session("webaaaaaaaaa")
	if s.Waiting != "folder trust" || s.View().Waiting != "folder trust" {
		t.Fatalf("not marked: %+v", s)
	}
	screen = "ready"
	p.watchStartup()
	if s, _ = p.session("webaaaaaaaaa"); s.Waiting != "" {
		t.Fatalf("not cleared: %q", s.Waiting)
	}
	// a session past starting is never read
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Status = "idle"
		}
		return nil
	})
	screen = "Do you trust the files in this folder?"
	p.watchStartup()
	if s, _ = p.session("webaaaaaaaaa"); s.Waiting != "" {
		t.Fatalf("an idle session was marked: %q", s.Waiting)
	}
}

// A message queued for a session that stays "starting" fails after messageWait, naming the dialog, and
// the sender is told; one for a session that ended fails at once; one for a busy session keeps waiting.
func TestQueuedMessagesNeverWaitForeverOnANotReadySession(t *testing.T) {
	p, st := twoTeams(t)
	old := store.RFC3339(time.Now().Add(-messageWait - time.Minute))
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			switch s.Sessions[i].ID {
			case "webaaaaaaaaa":
				s.Sessions[i].Status, s.Sessions[i].Waiting = "starting", "folder trust"
			case "planaaaaaaaa":
				s.Sessions[i].State = "exited"
			case "otheraaaaaaa":
				s.Sessions[i].Status = "working"
			}
		}
		for _, to := range []string{"webaaaaaaaaa", "planaaaaaaaa", "otheraaaaaaa"} {
			s.Domain.Messages = append(s.Domain.Messages, protocol.TeamMessage{ID: "m-" + to, TeamID: "team1", From: "leadaaaaaaaa", To: to, Text: "hi", State: "queued", At: old})
		}
		// a message not yet old for the starting session, and a PenguPool notice, stay queued
		s.Domain.Messages = append(s.Domain.Messages,
			protocol.TeamMessage{ID: "m-new", TeamID: "team1", From: "leadaaaaaaaa", To: "webaaaaaaaaa", Text: "hi", State: "queued", At: store.RFC3339(time.Now())},
			protocol.TeamMessage{ID: "n-old", TeamID: "team1", To: "webaaaaaaaaa", Text: "info", State: "queued", At: old})
		return nil
	})
	p.expireQueued(time.Now())
	state := map[string]protocol.TeamMessage{}
	for _, m := range p.st.State().Domain.Messages {
		state[m.ID] = m
	}
	if m := state["m-webaaaaaaaaa"]; m.State != "failed" || !strings.Contains(m.Reason, "folder trust") {
		t.Errorf("starting session: %+v", m)
	}
	if m := state["m-planaaaaaaaa"]; m.State != "failed" || !strings.Contains(m.Reason, "no longer running") {
		t.Errorf("ended session: %+v", m)
	}
	for _, id := range []string{"m-otheraaaaaaa", "m-new", "n-old"} {
		if state[id].State != "queued" {
			t.Errorf("%s = %q, want queued", id, state[id].State)
		}
	}
	told := false
	for _, m := range state {
		told = told || m.To == "leadaaaaaaaa" && m.From == "" && strings.Contains(m.Text, "was not delivered")
	}
	if !told {
		t.Error("the sender was not told")
	}
}

func TestPretrustClaude(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	orig := `{"numStartups": 12345678901234567890, "projects": {"/other": {"hasTrustDialogAccepted": false, "allowedTools": ["x"]}}, "theme": "dark"}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pretrustClaude(path, "/helper/wt", ""); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	projects := doc["projects"].(map[string]any)
	if projects["/helper/wt"].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Errorf("not trusted: %s", b)
	}
	if o := projects["/other"].(map[string]any); o["hasTrustDialogAccepted"] != false || o["allowedTools"] == nil {
		t.Errorf("another entry changed: %v", o)
	}
	if doc["theme"] != "dark" || !strings.Contains(string(b), "12345678901234567890") {
		t.Errorf("another key changed: %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode())
	}
	before := string(b)
	_ = pretrustClaude(path, "/helper/wt", "") // again: nothing to do
	if after, _ := os.ReadFile(path); string(after) != before {
		t.Error("a second pass rewrote the file")
	}
	// a missing file is Claude's to create; a broken one is left alone
	if err := pretrustClaude(filepath.Join(t.TempDir(), "none.json"), "/x", ""); err != nil {
		t.Errorf("missing file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o600)
	if err := pretrustClaude(bad, "/x", ""); err == nil {
		t.Error("a broken file was accepted")
	}
	if b, _ := os.ReadFile(bad); string(b) != "{not json" {
		t.Error("a broken file was changed")
	}
}

func TestPretrustCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cx", "config.toml")
	if err := pretrustCodex(path, `/h/we"ird`, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `[projects."/h/we\"ird"]`) || !strings.Contains(string(b), `trust_level = "trusted"`) {
		t.Fatalf("config = %q", b)
	}
	_ = os.WriteFile(path, []byte("model = \"x\"\n[projects.\"/keep\"]\ntrust_level = \"untrusted\""), 0o600)
	_ = pretrustCodex(path, "/keep", "") // the owner's choice stands
	_ = pretrustCodex(path, "/new", "")
	b, _ = os.ReadFile(path)
	if strings.Count(string(b), "/keep") != 1 || !strings.Contains(string(b), `untrusted`) || !strings.Contains(string(b), `[projects."/new"]`) || !strings.HasPrefix(string(b), "model = \"x\"\n") {
		t.Fatalf("config = %q", b)
	}
	for _, bad := range []string{"/a\nb", "/a\x1bb"} {
		if err := pretrustCodex(path, bad, ""); err == nil {
			t.Errorf("a control character in %q was written", bad)
		}
	}
}

// A symlinked config stays a link, and Codex's own spellings of an entry are never duplicated or overridden.
func TestPretrustKeepsTheOwnersFilesAsTheyAre(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "claude.json")
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	_ = os.WriteFile(target, []byte(`{"projects":{}}`), 0o600)
	link := filepath.Join(dir, ".claude.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks")
	}
	if err := pretrustClaude(link, "/helper/wt", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "/helper/wt") {
		t.Errorf("the target was not updated: %s", b)
	}
	cfg := filepath.Join(dir, "config.toml")
	for _, have := range []string{`[projects.'/h/wt']` + "\n" + `trust_level = "untrusted"` + "\n", "[projects]\n\"/h/wt\" = { trust_level = \"untrusted\" }\n", "projects.\"/h/wt\".trust_level = \"untrusted\"\n"} {
		_ = os.WriteFile(cfg, []byte(have), 0o600)
		if err := pretrustCodex(cfg, "/h/wt", ""); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(cfg); string(b) != have {
			t.Errorf("an existing entry was changed:\n%s\n->\n%s", have, b)
		}
	}
}

// A worktree of a repository the owner has not trusted is not trusted for them: it carries that
// repository's own settings and hooks. One the owner has trusted (or a folder above it) is.
func TestPretrustInheritsTheRepositorysTrust(t *testing.T) {
	cc := filepath.Join(t.TempDir(), ".claude.json")
	_ = os.WriteFile(cc, []byte(`{"projects":{"/code":{"hasTrustDialogAccepted":true},"/other/repo":{"hasTrustDialogAccepted":false}}}`), 0o600)
	for from, want := range map[string]bool{"/code/app": true, "/other/repo": false, "/never/seen": false} {
		_ = pretrustClaude(cc, "/wt/"+strings.ReplaceAll(from, "/", "_"), from)
		b, _ := os.ReadFile(cc)
		if got := strings.Contains(string(b), "/wt/"+strings.ReplaceAll(from, "/", "_")); got != want {
			t.Errorf("claude: worktree of %s trusted = %v, want %v", from, got, want)
		}
	}
	cx := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cx, []byte("[projects.\"/code\"]\ntrust_level = \"trusted\"\n\n[projects.\"/other/repo\"]\ntrust_level = \"untrusted\"\n"), 0o600)
	for from, want := range map[string]bool{"/code/app": true, "/other/repo": false, "/never/seen": false} {
		wt := "/wt/" + strings.ReplaceAll(from, "/", "_")
		_ = pretrustCodex(cx, wt, from)
		b, _ := os.ReadFile(cx)
		if got := strings.Contains(string(b), wt); got != want {
			t.Errorf("codex: worktree of %s trusted = %v, want %v", from, got, want)
		}
	}
}

// A message a delivery got to first is not failed, and the sender is not told it failed; a resumed
// session starts the wait again.
func TestExpireQueuedYieldsToADeliveryAndARestart(t *testing.T) {
	p, st := twoTeams(t)
	old := store.RFC3339(time.Now().Add(-messageWait - time.Minute))
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			if s.Sessions[i].ID == "webaaaaaaaaa" {
				s.Sessions[i].Status, s.Sessions[i].StartedAt = "starting", time.Now() // just resumed
			}
		}
		s.Domain.Messages = append(s.Domain.Messages, protocol.TeamMessage{ID: "m1", TeamID: "team1", From: "leadaaaaaaaa", To: "webaaaaaaaaa", Text: "hi", State: "queued", At: old})
		return nil
	})
	p.expireQueued(time.Now())
	if m := p.st.State().Domain.Messages[0]; m.State != "queued" {
		t.Fatalf("a resumed session's message failed at once: %+v", m)
	}
	_ = st.Update(func(s *store.State) error {
		s.Sessions[sessionIndex(s, "webaaaaaaaaa")].StartedAt = time.Now().Add(-time.Hour)
		return nil
	})
	if !p.failQueued("m1", "x") || p.failQueued("m1", "x") {
		t.Fatal("failQueued must fail a queued message once")
	}
	p.setMessage("m1", "delivered", "")
	if p.failQueued("m1", "late") {
		t.Fatal("a delivered message was failed")
	}
}

// TestMain gives the tests a home of their own: nothing a test starts may touch the developer's
// ~/.claude.json or ~/.codex.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "pool-test-home-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	tempRoot = filepath.Join(home, "tmp-pengupool") // the sessions' temp folders go under it, and with it
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
