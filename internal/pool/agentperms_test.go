package pool

import (
	"fmt"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Agent permissions: only the owner's app call writes them; they go into the agent's own config,
// come out exactly, follow a moved app, and show a line removed outside PenguPool.
func TestAgentPermissions(t *testing.T) {
	home := t.TempDir()
	p, st := twoTeams(t)
	p.env = []string{"HOME=" + home} // never the real home
	p.exe = "/Applications/PenguPool AI.app/Contents/Resources/bin/pengupool"
	claude := filepath.Join(home, ".claude", "settings.json")
	codex := filepath.Join(home, ".codex", "rules", "pengupool.rules")
	os.MkdirAll(filepath.Dir(claude), 0o700)
	user := "{\n  \"permissions\": {\n    \"deny\": [\n      \"Bash(git push *)\"\n    ]\n  }\n}\n"
	os.WriteFile(claude, []byte(user), 0o600)

	if _, err := call(t, p, false, "settings.save", map[string]any{"agent_permissions": "allowed"}); err == nil {
		t.Fatal("an agent's call gave the consent")
	}
	if b, _ := os.ReadFile(claude); string(b) != user {
		t.Fatal("an agent's call wrote the rules")
	}
	if p.agentPermissionsView()["choice"] != "" {
		t.Fatal("decided before the owner chose")
	}
	r, err := call(t, p, true, "settings.save", map[string]any{"agent_permissions": "allowed"})
	if err != nil {
		t.Fatal(err)
	}
	res := r.(map[string]any)["agent_permissions"].(map[string]any)
	if res["claude"].(map[string]any)["ok"] != true || res["codex"].(map[string]any)["ok"] != true || len(res) != 2 {
		t.Fatalf("results %v", res)
	}
	if p.agentPermissionsView()["choice"] != "allowed" {
		t.Fatal("the choice was not kept")
	}
	if b, _ := os.ReadFile(claude); !strings.Contains(string(b), "Bash('"+p.exe+"' send *)") || !strings.Contains(string(b), "git push") {
		t.Fatalf("claude settings:\n%s", b)
	}
	if _, err := os.Stat(codex); err != nil {
		t.Fatal(err)
	}
	if rec := st.Config().AgentPermissions; len(rec["claude"].Lines) != len(harness.CLIAllowed) || len(rec["codex"].Lines) != len(harness.CLIAllowed) {
		t.Fatalf("record %+v", rec)
	}

	// a line removed outside PenguPool shows as changed
	b, _ := os.ReadFile(claude)
	os.WriteFile(claude, []byte(strings.Replace(string(b), "\"Bash('"+p.exe+"' search *)\",", "", 1)), 0o600)
	v := p.agentPermissionsView()["agents"].(map[string]any)["claude"].(map[string]any)
	if v["on"] != true || len(v["changed"].([]string)) != 1 {
		t.Fatalf("view %v", v)
	}

	// the app moved: start rewrites PenguPool's lines for the new path
	p.exe = "/Applications/New Place/pengupool"
	p.refreshAgentPermissions()
	b, _ = os.ReadFile(claude)
	if strings.Contains(string(b), "PenguPool AI.app") || strings.Count(string(b), p.exe) != len(harness.CLIAllowed) {
		t.Fatalf("after the move:\n%s", b)
	}
	if c, _ := os.ReadFile(codex); strings.Contains(string(c), "PenguPool AI.app") {
		t.Fatalf("codex after the move:\n%s", c)
	}

	// a file PenguPool cannot parse: not written, with the reason, the file untouched; a later start retries
	codexBefore, _ := os.ReadFile(codex)
	bad := "{ not json"
	os.WriteFile(claude, []byte(bad), 0o600)
	cfgNow := st.Config()
	delete(cfgNow.AgentPermissions, "claude")
	_ = st.SaveConfig(cfgNow)
	p.refreshAgentPermissions()
	v = p.agentPermissionsView()["agents"].(map[string]any)["claude"].(map[string]any)
	if b, _ := os.ReadFile(claude); string(b) != bad || v["on"] != false || !strings.Contains(fmt.Sprint(v["error"]), "not valid JSON") {
		t.Fatalf("unparsable file: %q, view %v", b, v)
	}
	if c, _ := os.ReadFile(codex); string(c) != string(codexBefore) {
		t.Fatal("codex changed because claude failed")
	}
	os.WriteFile(claude, []byte(user), 0o600)
	p.refreshAgentPermissions()
	if v = p.agentPermissionsView()["agents"].(map[string]any)["claude"].(map[string]any); v["on"] != true || v["error"] != nil {
		t.Fatalf("retry at start: %v", v)
	}

	// off: exactly PenguPool's lines go
	if _, err := call(t, p, true, "settings.save", map[string]any{"agent_permissions": "off"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(claude); string(b) != user {
		t.Fatalf("after off:\n%s\nwant:\n%s", b, user)
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatal("codex rules left behind")
	}
	if _, err := call(t, p, true, "settings.save", map[string]any{"agent_permissions": "maybe"}); err == nil {
		t.Fatal("an unknown choice was taken")
	}
	if c := st.Config(); len(c.AgentPermissions) != 0 || c.AgentPermissionsChoice != "off" {
		t.Fatalf("record left %+v", c)
	}
}

// A yes given before the list grew covers only the commands it was given for: the new ones are shown
// as added, and the start-up refresh does not write them without a new yes.
func TestAgentPermissionsListGrew(t *testing.T) {
	home := t.TempDir()
	p, st := twoTeams(t)
	p.env = []string{"HOME=" + home} // never the real home
	p.exe = "/Applications/PenguPool AI.app/Contents/Resources/bin/pengupool"
	p.setAgentPermissions("allowed")
	// What an earlier version recorded: the first four commands only.
	if err := st.UpdateConfig(func(c *store.Config) error {
		for a, r := range c.AgentPermissions {
			r.Lines = r.Lines[:4]
			c.AgentPermissions[a] = r
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.refreshAgentPermissions()
	for _, a := range harness.PermissionAgents {
		if got := st.Config().AgentPermissions[a].Lines; len(got) != 4 {
			t.Fatalf("%s: refresh widened the yes to %d lines", a, len(got))
		}
		v := p.agentPermissionsView()["agents"].(map[string]any)[a].(map[string]any)
		added, _ := v["added"].([]string)
		if len(added) != len(harness.CLIAllowed)-4 || !strings.Contains(strings.Join(added, " "), "ask") {
			t.Fatalf("%s: added = %v", a, v["added"])
		}
	}
}
