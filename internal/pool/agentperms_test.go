package pool

import (
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

	if _, err := call(t, p, false, "settings.save", map[string]any{"agent_permissions": map[string]bool{"claude": true}}); err == nil {
		t.Fatal("an agent's call gave the consent")
	}
	if b, _ := os.ReadFile(claude); string(b) != user {
		t.Fatal("an agent's call wrote the rules")
	}
	r, err := call(t, p, true, "settings.save", map[string]any{"agent_permissions": map[string]bool{"claude": true, "codex": true, "pi": true}})
	if err != nil {
		t.Fatal(err)
	}
	res := r.(map[string]any)["agent_permissions"].(map[string]any)
	if res["claude"].(map[string]any)["ok"] != true || res["codex"].(map[string]any)["ok"] != true || res["pi"].(map[string]any)["ok"] != false {
		t.Fatalf("results %v", res)
	}
	if b, _ := os.ReadFile(claude); !strings.Contains(string(b), "Bash('"+p.exe+"' send *)") || !strings.Contains(string(b), "git push") {
		t.Fatalf("claude settings:\n%s", b)
	}
	if _, err := os.Stat(codex); err != nil {
		t.Fatal(err)
	}
	if rec := st.Config().AgentPermissions; len(rec["claude"].Lines) != 4 || len(rec["codex"].Lines) != 4 {
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
	if strings.Contains(string(b), "PenguPool AI.app") || strings.Count(string(b), p.exe) != 4 {
		t.Fatalf("after the move:\n%s", b)
	}
	if c, _ := os.ReadFile(codex); strings.Contains(string(c), "PenguPool AI.app") {
		t.Fatalf("codex after the move:\n%s", c)
	}

	// off: exactly PenguPool's lines go
	if _, err := call(t, p, true, "settings.save", map[string]any{"agent_permissions": map[string]bool{"claude": false, "codex": false}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(claude); string(b) != user {
		t.Fatalf("after off:\n%s\nwant:\n%s", b, user)
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatal("codex rules left behind")
	}
	if rec := st.Config().AgentPermissions; len(rec) != 0 {
		t.Fatalf("record left %+v", rec)
	}
}
