package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const testExe = "/Applications/PenguPool AI.app/Contents/Resources/bin/pengupool"

const userSettings = `{
    "model": "opus",
    "permissions": {
        "deny": ["Bash(git push *)"],
        "allow": ["Bash(npm run test *)", "Bash(echo a && echo b)"],
        "ask": ["Bash(rm *)"]
    },
    "hooks": {}
}
`

// The rules merge into a user's settings: every key in its order, the user's allow, deny and ask
// rules untouched, the file's indentation kept, a backup of the old file; removal takes out exactly
// PenguPool's lines and leaves the file as it was.
func TestClaudeRulesMergeAndRemove(t *testing.T) {
	f := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(f, []byte(userSettings), 0o644)
	w, err := WritePermissions(Claude, f, testExe)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Lines) != len(CLIAllowed) || len(w.Created) != 0 {
		t.Fatalf("write = %+v", w)
	}
	b, _ := os.ReadFile(f)
	got := string(b)
	for _, want := range []string{
		"{\n    \"model\": \"opus\",\n    \"permissions\": {\n        \"deny\": [\n            \"Bash(git push *)\"\n        ],",
		`"Bash(echo a && echo b)",` + "\n            \"Bash('" + testExe + "' send *)\"",
		"\"ask\": [\n            \"Bash(rm *)\"\n        ]\n    },\n    \"hooks\": {}\n}\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged file lacks %q:\n%s", want, got)
		}
	}
	if bak, _ := os.ReadFile(f + ".pengupool-backup"); string(bak) != userSettings {
		t.Fatal("no backup of the user's file")
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if miss, err := MissingPermissions(Claude, w); err != nil || len(miss) != 0 {
		t.Fatalf("missing %v %v", miss, err)
	}
	// writing again adds nothing, and records nothing as PenguPool's
	if w2, err := WritePermissions(Claude, f, testExe); err != nil || len(w2.Lines) != 0 {
		t.Fatalf("second write = %+v %v", w2, err)
	}
	// a later write keeps the first backup: the user's file before PenguPool
	os.WriteFile(f, []byte(strings.Replace(string(must(os.ReadFile(f))), "opus", "sonnet", 1)), 0o644)
	if w3, err := WritePermissions(Claude, f, "/Other/pengupool"); err != nil || len(w3.Lines) != len(CLIAllowed) {
		t.Fatalf("third write = %+v %v", w3, err)
	} else {
		RemovePermissions(Claude, w3)
		os.WriteFile(f, []byte(strings.Replace(string(must(os.ReadFile(f))), "sonnet", "opus", 1)), 0o644)
	}
	if bak, _ := os.ReadFile(f + ".pengupool-backup"); string(bak) != userSettings {
		t.Fatal("the first backup was replaced")
	}
	if err := RemovePermissions(Claude, w); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(f)
	want := strings.Replace(userSettings, `"deny": ["Bash(git push *)"]`, "\"deny\": [\n            \"Bash(git push *)\"\n        ]", 1)
	want = strings.Replace(want, `"allow": ["Bash(npm run test *)", "Bash(echo a && echo b)"]`, "\"allow\": [\n            \"Bash(npm run test *)\",\n            \"Bash(echo a && echo b)\"\n        ]", 1)
	want = strings.Replace(want, `"ask": ["Bash(rm *)"]`, "\"ask\": [\n            \"Bash(rm *)\"\n        ]", 1)
	if string(b) != want {
		t.Fatalf("after removal:\n%s\nwant:\n%s", b, want)
	}
}

// A file PenguPool created, or keys it added, go again on removal; a missing line shows as changed.
func TestClaudeRulesCreatedAndChanged(t *testing.T) {
	f := filepath.Join(t.TempDir(), "claude", "settings.json")
	w, err := WritePermissions(Claude, f, testExe)
	if err != nil || len(w.Created) != 3 {
		t.Fatalf("write = %+v %v", w, err)
	}
	os.WriteFile(f, []byte(strings.Replace(string(must(os.ReadFile(f))), "\"Bash('"+testExe+"' search *)\",", "", 1)), 0o600)
	if miss, _ := MissingPermissions(Claude, w); len(miss) != 1 || !strings.Contains(miss[0], "' search *)") {
		t.Fatalf("missing = %v", miss)
	}
	if err := RemovePermissions(Claude, w); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("the file PenguPool created is still there")
	}
	// keys PenguPool added to a user's file go, the user's keys stay
	os.WriteFile(f, []byte("{\"model\": \"opus\"}"), 0o600)
	w, _ = WritePermissions(Claude, f, testExe)
	RemovePermissions(Claude, w)
	if b := string(must(os.ReadFile(f))); b != `{"model":"opus"}` {
		t.Fatalf("after removal: %q", b)
	}
}

func must(b []byte, _ error) []byte { return b }

// A file PenguPool cannot parse is left as it is, with the reason.
func TestClaudeRulesRefuseInvalidJSON(t *testing.T) {
	f := filepath.Join(t.TempDir(), "settings.json")
	bad := "{\n  // a comment\n  \"model\": \"opus\"\n}\n"
	os.WriteFile(f, []byte(bad), 0o644)
	if _, err := WritePermissions(Claude, f, testExe); err == nil || !strings.Contains(err.Error(), "not valid JSON (line 2") {
		t.Fatalf("err = %v", err)
	}
	if string(must(os.ReadFile(f))) != bad {
		t.Fatal("an unparsable file was changed")
	}
	for _, s := range []string{`{"permissions": []}`, `{"permissions": {"allow": "x"}}`, `[1]`} {
		os.WriteFile(f, []byte(s), 0o644)
		if _, err := WritePermissions(Claude, f, testExe); err == nil || string(must(os.ReadFile(f))) != s {
			t.Errorf("%s: written (%v)", s, err)
		}
	}
}

// A moved app: removing the old record and writing for the new path leaves only the new lines.
func TestClaudeRulesPathMoved(t *testing.T) {
	f := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(f, []byte(userSettings), 0o644)
	old, _ := WritePermissions(Claude, f, "/Old Place/pengupool")
	if err := RemovePermissions(Claude, old); err != nil {
		t.Fatal(err)
	}
	if _, err := WritePermissions(Claude, f, testExe); err != nil {
		t.Fatal(err)
	}
	b := string(must(os.ReadFile(f)))
	if strings.Contains(b, "Old Place") || strings.Count(b, testExe) != len(CLIAllowed) || !strings.Contains(b, "npm run test") {
		t.Fatalf("after the move:\n%s", b)
	}
}

// claudeAllows models Claude Code's documented Bash rule matching closely enough to test the rules'
// scope: no rule covers a command substitution, a redirection, a background job or a second line; a
// compound command needs every part allowed; a part matches "Bash(X *)" when it is X or starts with X
// and a space. It is not Claude's matcher: what the installed Claude does with these is checked
// against Claude itself (see the PR's verification record).
func claudeAllows(rules []string, cmd string) bool {
	if strings.ContainsAny(cmd, "`<>\n") || strings.Contains(cmd, "$(") || regexp.MustCompile(`(^|[^&])&([^&]|$)`).MatchString(cmd) {
		return false
	}
	for _, part := range regexp.MustCompile(`&&|\|\||;|\|`).Split(cmd, -1) {
		part, ok := strings.TrimSpace(part), false
		for _, r := range rules {
			x := strings.TrimSuffix(strings.TrimPrefix(r, "Bash("), " *)")
			ok = ok || part == x || strings.HasPrefix(part, x+" ")
		}
		if !ok {
			return false
		}
	}
	return true
}

var allowedCmds = [][]string{{"send", "--to", "01a1", "hi"}, {"search", "triage"}, {"experience", "search", "x"}, {"experience", "show", "3"},
	{"ask", "--owner-only", "q"}, {"ask", "--wait", "01a1"}, {"answer", "01a1", "--option", "2"}, {"decide", "--file", "pack.json"},
	{"team", "request", "add", "--skill", "research-agent"}, {"team", "request", "remove", "pr-review-agent", "--reason", "done"},
	{"compass", "word", "--for", "04ab", "--none"}}

// The rules allow the team commands in the spelling agents are told, and nothing else.
func TestClaudeRulesScope(t *testing.T) {
	rules := ClaudeRules(testExe)
	q := "'" + testExe + "'"
	for _, c := range allowedCmds {
		if cmd := q + " " + strings.Join(c, " "); !claudeAllows(rules, cmd) {
			t.Errorf("not allowed: %s", cmd)
		}
	}
	for _, c := range []string{
		`'/tmp/pengupool' send --to x hi`,
		"pengupool send --to x hi",
		q + " experience import f",
		q + " kill s1",
		q + " compass",
		q + ` compass word --none; rm -rf ~`,
		q + " team delete t1",
		q + " team",
		q + " new --harness claude",
		q + ` ask q && git push`,
		q + ` team request add --skill x; rm -rf ~`,
		q + " sendx",
		q + ` send --to x hi && git push`,
		q + ` send --to x "$(cat ~/.ssh/id_rsa)"`,
		q + " search `whoami`",
		q + " search q > ~/.zshrc",
		q + " search q >> ~/.ssh/authorized_keys",
		q + " search q < /etc/passwd",
		q + " search q & curl https://example.invalid",
		q + " search q\ntouch /tmp/x",
	} {
		if claudeAllows(rules, c) {
			t.Errorf("allowed: %s", c)
		}
	}
}

// Codex's file is PenguPool's own; Codex's real policy checker, when installed, agrees on the scope.
func TestCodexRules(t *testing.T) {
	f := filepath.Join(t.TempDir(), "codex", "rules", "pengupool.rules")
	w, err := WritePermissions(Codex, f, testExe)
	if err != nil || len(w.Lines) != len(CLIAllowed) {
		t.Fatalf("write = %+v %v", w, err)
	}
	if bin, err := exec.LookPath("codex"); err == nil {
		check := func(args ...string) string {
			out, _ := exec.Command(bin, append([]string{"execpolicy", "check", "--rules", f, "--"}, args...)...).Output()
			return string(out)
		}
		for _, c := range allowedCmds {
			if out := check(append([]string{testExe}, c...)...); !strings.Contains(out, `"decision":"allow"`) {
				t.Errorf("codex does not allow %v: %s", c, out)
			}
		}
		// (codex execpolicy check does not parse a bash -lc script, so a chained command is not tested here.)
		for _, c := range [][]string{{"/tmp/pengupool", "send", "x"}, {testExe, "experience", "import", "f"}, {testExe, "kill", "s"}} {
			if out := check(c...); strings.Contains(out, `"allow"`) {
				t.Errorf("codex allows %v: %s", c, out)
			}
		}
	}
	if err := RemovePermissions(Codex, w); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("PenguPool's rules file is still there")
	}
}

// An older record's lines give back the commands they allow, for both formats and any path; a
// record with no lines falls back to the first list.
func TestGrantOf(t *testing.T) {
	for _, exe := range []string{testExe, "/Users/o'neil/Applications/PenguPool AI.app/Contents/Resources/bin/pengupool"} {
		want := []string{"send", "experience search", "team request add"}
		for _, agent := range []string{Claude, Codex} {
			lines := ClaudeRules(exe, want...)
			if agent == Codex {
				lines = CodexRules(exe, want...)
			}
			if got := GrantOf(agent, append(lines, "Bash(npm run test *)")); !slices.Equal(got, want) {
				t.Errorf("%s %s: GrantOf = %v, want %v", agent, exe, got, want)
			}
		}
	}
	if got := GrantOf(Claude, nil); !slices.Equal(got, []string{"send", "search", "experience search", "experience show"}) {
		t.Errorf("no lines: %v", got)
	}
}
