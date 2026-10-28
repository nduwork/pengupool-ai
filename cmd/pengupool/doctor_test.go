package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/pool"
)

const v1Settings = `{
  "theme": "dark",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "/u/bin/python -m pengupool.context 2>/dev/null || true"}, {"type": "command", "command": "echo mine && echo > /dev/null"}, {"type": "command", "command": "bash ~/.claude/pengupool/session_start.sh"}]}],
    "PreToolUse": [{"matcher": "SendMessage", "hooks": [{"type": "command", "command": "/u/bin/python -m pengupool.routing || true"}]}],
    "Stop": {"matcher": "x", "hooks": [{"type": "command", "command": "echo not a list"}]}
  },
  "statusLine": {"type": "command", "command": "[ -x /u/bin/python ] && exec /u/bin/python -P -m pengupool.statusline -- 'npx ccstatusline --x' || eval 'npx ccstatusline --x'"},
  "extraKnownMarketplaces": {"pengupool-marketplace": {"source": {}}, "other": {}}
}`

func TestDoctorFindsAndRemovesV1Entries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(v1Settings), 0o600); err != nil {
		t.Fatal(err)
	}
	found, doc, _, err := v1Entries(path)
	if err != nil || len(found) != 4 { // three hooks and the status line
		t.Fatalf("found %v, %v", found, err)
	}
	if err := writeSettings(path, doc); err != nil {
		t.Fatal(err)
	}
	if bak, _ := filepath.Glob(path + ".bak*"); len(bak) != 0 {
		t.Fatalf("a backup was written beside the file: %v", bak)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	for _, gone := range []string{"pengupool.context", "pengupool.routing", "pengupool.statusline", "session_start.sh", "PreToolUse"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q is still in the file:\n%s", gone, s)
		}
	}
	for _, kept := range []string{"echo mine && echo > /dev/null", `"theme": "dark"`, `"other"`, `"command": "npx ccstatusline --x"`,
		"pengupool-marketplace", "echo not a list"} { // v2's own marketplace and a hooks entry doctor can't read stay
		if !strings.Contains(s, kept) {
			t.Errorf("%q was lost:\n%s", kept, s)
		}
	}
	if again, _, _, _ := v1Entries(path); len(again) != 0 {
		t.Errorf("a second pass found %v", again)
	}
}

// v1's status line without the owner's own line behind it has nothing to give back; one whose line
// holds "||" keeps it whole.
func TestDoctorStatusLineForms(t *testing.T) {
	line := func(cmd string) string {
		path := filepath.Join(t.TempDir(), "s.json")
		b := `{"statusLine":{"type":"command","command":` + jsonString(cmd) + `}}`
		_ = os.WriteFile(path, []byte(b), 0o600)
		found, doc, _, err := v1Entries(path)
		if err != nil || len(found) != 1 {
			t.Fatalf("%q: %v %v", cmd, found, err)
		}
		sl, ok := doc["statusLine"].(map[string]any)
		if !ok {
			return ""
		}
		return sl["command"].(string)
	}
	if got := line("[ -x /u/py ] && exec /u/py -P -m pengupool.statusline -- 'ccstatusline || true' || eval 'ccstatusline || true'"); got != "ccstatusline || true" {
		t.Errorf("an inner line with || became %q", got)
	}
	if got := line("[ -x /u/py ] && exec /u/py -P -m pengupool.statusline || true"); got != "" {
		t.Errorf("v1's standalone line left %q", got)
	}
}

func jsonString(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func TestDoctorLeavesACleanOrMissingFileAlone(t *testing.T) {
	dir := t.TempDir()
	if found, _, _, err := v1Entries(filepath.Join(dir, "none.json")); err != nil || found != nil {
		t.Fatalf("missing: %v %v", found, err)
	}
	path := filepath.Join(dir, "s.json")
	_ = os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo done"}]}]}}`), 0o600)
	if found, _, _, err := v1Entries(path); err != nil || len(found) != 0 {
		t.Fatalf("clean: %v %v", found, err)
	}
}

func TestDoctorWarnsOnAnSSHLogin(t *testing.T) {
	warn := func(env string) bool {
		for _, c := range doctorChecks(t.TempDir(), func(string) string { return env }) {
			if c.Name == "Keychain" {
				return c.Warn
			}
		}
		return false
	}
	if !warn("1.2.3.4 5 6.7.8.9 22") || warn("") {
		t.Fatal("the keychain warning does not follow SSH_CONNECTION")
	}
}

// A dotfile link to settings.json stays a link: the file it points to is the one cleaned, and a
// number too big for a float comes back as written.
func TestDoctorKeepsASymlinkAndBigNumbers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repo", "settings.json")
	link := filepath.Join(dir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(strings.Replace(v1Settings, `"theme"`, `"big": 9007199254740993, "theme"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, doc, _, err := v1Entries(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSettings(link, doc); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file")
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "9007199254740993") || strings.Contains(string(b), "pengupool.context") {
		t.Errorf("target not cleaned, or a number changed:\n%s", b)
	}
}

func TestDoctorFixRefusedInAnAgentSession(t *testing.T) {
	t.Setenv("PENGUPOOL_SESSION_ID", "s1")
	if err := cmdDoctor([]string{"--fix", "--yes", "--home", t.TempDir()}); err == nil {
		t.Error("doctor --fix ran inside an agent session")
	}
}

// What doctor hands over to be kept is the removed v1 entries alone: nothing else of the file (an
// env token, the owner's other hooks, the owner's own status line command), and not the user name.
func TestDoctorRemovedHoldsOnlyTheV1Entries(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	settings := strings.NewReplacer(`"theme": "dark"`, `"theme": "dark", "env": {"API_TOKEN": "sk-secret-123"}`, "/u/bin/python", home+"/bin/python").Replace(v1Settings)
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, removed, err := v1Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(forRecord(removed, home))
	for _, leak := range []string{"sk-secret-123", "API_TOKEN", "dark", "echo mine", "other", "echo not a list", "ccstatusline", home} {
		if strings.Contains(s, leak) {
			t.Errorf("%q is in what would be stored: %s", leak, s)
		}
	}
	for _, want := range []string{"pengupool.context", "pengupool.routing", "session_start.sh", "pengupool.statusline", `"matcher":"SendMessage"`, "~/bin/python"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q is missing from what would be stored: %s", want, s)
		}
	}
}

func v1Hooks(doc map[string]any) (n int) {
	hooks, _ := doc["hooks"].(map[string]any)
	for _, e := range hooks {
		groups, _ := e.([]any) // "Stop" is the file's unreadable entry, not a list
		for _, g := range groups {
			for _, h := range g.(map[string]any)["hooks"].([]any) {
				if pool.IsV1Command(h.(map[string]any)["command"].(string)) {
					n++
				}
			}
		}
	}
	return n
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A record put back onto the cleaned file brings the three v1 hooks and v1's exact status line; onto
// a file that still holds the original (with its mixed group) it adds nothing; twice is once.
func TestDoctorRestore(t *testing.T) {
	home := "/home/me"
	orig := strings.ReplaceAll(v1Settings, "/u/bin/python", home+"/bin/python")
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cleaned, removed, err := v1Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := decodeJSON(t, string(forRecord(removed, home)))

	if n := v1Hooks(cleaned); n != 0 {
		t.Fatalf("%d v1 hooks left after the clean-up", n)
	}
	for i := 0; i < 2; i++ { // twice: nothing doubled
		restoreEntries(cleaned, entries, home)
	}
	if n := v1Hooks(cleaned); n != 3 {
		t.Errorf("%d v1 hooks after restoring twice, want 3", n)
	}
	want := decodeJSON(t, orig)["statusLine"].(map[string]any)["command"]
	if got := cleaned["statusLine"].(map[string]any)["command"]; got != want {
		t.Errorf("status line restored as\n%v\nwant\n%v", got, want)
	}
	if _, has := cleaned["statusLine"].(map[string]any)["wraps"]; has {
		t.Error("the record's own wraps key leaked into the settings")
	}

	mixed := decodeJSON(t, orig) // the file as it was: v1 hooks beside the owner's in one group
	restoreEntries(mixed, entries, home)
	if n := v1Hooks(mixed); n != 3 {
		t.Errorf("%d v1 hooks on the original file after a restore, want 3 (none doubled)", n)
	}
	if got := mixed["statusLine"].(map[string]any)["command"]; got != want {
		t.Errorf("an already v1 status line was changed to %v", got)
	}
}

func TestPyQuoteRoundTrips(t *testing.T) {
	for _, w := range []string{"npx ccstatusline --x", "plain", "it's", "a b || c", ""} {
		if got := unquoteWord(pyQuote(w)); got != w {
			t.Errorf("%q came back as %q", w, got)
		}
	}
}
