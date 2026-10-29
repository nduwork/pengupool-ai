package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAgentSpecsWireHooksPerLaunch(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Codex, Copilot, Pi} {
		bin := filepath.Join(dir, k)
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir}}
	cmd, env, err := Spec(Codex, p)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd, " ")
	if !strings.Contains(line, `hooks.Stop=[{hooks=[{type="command",command="'/x/pengupool' hook codex Stop",timeout=15}]}]`) {
		t.Fatalf("codex hooks: %s", line)
	}
	if !strings.Contains(strings.Join(env, " "), "PENGUPOOL_HARNESS=codex") {
		t.Fatal("harness env missing")
	}
	cmd, _, err = Spec(Copilot, p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cmd[2], "hooks", "hooks.json"))
	if err != nil || !strings.Contains(string(b), "hook copilot Stop") || !strings.Contains(strings.Join(cmd, " "), "--session-id s1") {
		t.Fatalf("copilot plugin: %v %s %v", err, b, cmd)
	}
	if cmd, _, err = Spec(Pi, p); err != nil || cmd[1] != "-e" {
		t.Fatalf("pi: %v %v", cmd, err)
	}
}

func TestWrapAndNewAgents(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Codex, Copilot, OpenCode, Gemini, Claude} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"hooks":{"AfterAgent":[{"hooks":[{"type":"command","command":"mine"}]}]}}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), []byte(`{}`), 0o600)
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir, "HOME=" + home}}

	// Claude Code (Ollama): the launcher, then Claude's own arguments (its hooks), with Claude's
	// folder first on PATH so the launcher runs this Claude.
	w := p
	w.Wrap = "/opt/ollama launch claude --model m --yes --"
	cmd, env, _ := Spec(Claude, w)
	if len(cmd) < 5 || cmd[0] != "/bin/sh" || cmd[2] != `exec env /opt/ollama launch claude --model m --yes -- "$@"` ||
		cmd[4] != "--teammate-mode" || !slices.Contains(cmd, "--settings") || !slices.Contains(env, "PATH="+dir+":"+dir) {
		t.Fatalf("claude wrapped: %q %v", cmd, env)
	}
	// sh passes the arguments after its $0 through "$@" word for word.
	if out, err := exec.Command("/bin/sh", "-c", `exec printf '%s|' "$@"`, "pp", "a b", "c").Output(); err != nil || string(out) != "a b|c|" {
		t.Fatalf("arguments after the launcher keep their words: %q %v", out, err)
	}
	cmd, env, _ = Spec(OpenCode, p)
	if e := strings.Join(env, " "); !strings.Contains(e, `"plugin":["file://`) || cmd[0] != filepath.Join(dir, OpenCode) {
		t.Fatalf("opencode: %v %s", cmd, e)
	}
	_, env, err := Spec(Gemini, p)
	if err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(dir, "harness", "gemini-home", ".gemini")
	b, _ := os.ReadFile(filepath.Join(gh, "settings.json"))
	if !strings.Contains(string(b), `"mine"`) || !strings.Contains(string(b), "hook gemini Stop") || !strings.Contains(strings.Join(env, " "), "GEMINI_CLI_HOME=") {
		t.Fatalf("gemini settings: %s", b)
	}
	if l, err := os.Readlink(filepath.Join(gh, "oauth_creds.json")); err != nil || l != filepath.Join(home, ".gemini", "oauth_creds.json") {
		t.Fatalf("gemini login link: %q %v", l, err)
	}
}

// A helper's skills load for its own session only: a flag or a per-session overlay, never an install.
func TestHelperSkillsLoadPerSession(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Claude, OMP, Pi, Copilot, Gemini, OpenCode} {
		os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755)
	}
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir, "HOME=" + t.TempDir()}, SkillRepo: "/skills/ci-debug"}
	// Read access too: a helper works outside its skill repository and must not be asked to read it.
	want := map[string]string{Claude: "--add-dir /skills/ci-debug --plugin-dir /skills/ci-debug", Pi: "--skill /skills/ci-debug/skills", OMP: "--config=" + filepath.Join(dir, "harness", "omp-skills-s1.yml"),
		Copilot: "--add-dir /skills/ci-debug", Gemini: "--include-directories /skills/ci-debug"}
	for k, w := range want {
		cmd, _, err := Spec(k, p)
		if err != nil || !strings.Contains(strings.Join(cmd, " "), w) {
			t.Errorf("%s: %v %v, want %q", k, cmd, err, w)
		}
	}
	if _, env, err := Spec(OpenCode, p); err != nil || !strings.Contains(strings.Join(env, "\n"), `"permission":{"external_directory":{"/skills/ci-debug/**":"allow"}}`) {
		t.Errorf("opencode: %v %v", env, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "harness", "omp-skills-s1.yml")); !strings.Contains(string(b), `"customDirectories":["/skills/ci-debug/skills"]`) {
		t.Errorf("overlay %s", b)
	}
	p.SkillRepo = ""
	for _, k := range []string{Claude, Copilot, Gemini} {
		if cmd, _, _ := Spec(k, p); strings.Contains(strings.Join(cmd, " "), "/skills/ci-debug") {
			t.Errorf("%s: a session without a skill got one: %v", k, cmd)
		}
	}
}

func TestSpecEffort(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Claude, Codex, OMP, Pi, Gemini, Copilot} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ kind, effort, want string }{
		{Claude, "high", "--effort high"},
		{Codex, "low", `-c model_reasoning_effort="low"`},
		{OMP, "medium", "--thinking medium"},
		{Pi, "medium", "--thinking medium"},
		{Gemini, "high", ""},
		{Copilot, "high", ""},
		{Claude, "", ""}, {Codex, "", ""}, {OMP, "", ""}, {Pi, "", ""},
	} {
		p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir}, Effort: c.effort}
		cmd, _, err := Spec(c.kind, p)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		line := strings.Join(cmd, " ")
		if c.want != "" && !strings.Contains(line, c.want) {
			t.Errorf("%s/%s: want %q in %s", c.kind, c.effort, c.want, line)
		}
		if c.want == "" && (strings.Contains(line, "effort") || strings.Contains(line, "--thinking")) {
			t.Errorf("%s/%q: unexpected effort in %s", c.kind, c.effort, line)
		}
	}
}

// The floor is enforced at the tool call: every harness gives PenguPool its PreToolUse, and the
// extensions of the ones without hook settings (OMP, pi, OpenCode) block on its answer.
func TestToolCallsReachTheFloor(t *testing.T) {
	for name, src := range map[string]string{"omp/pi": string(ompExtension), "opencode": string(opencodePlugin)} {
		if !strings.Contains(src, `run("PreToolUse"`) || !strings.Contains(src, "permissionDecision") {
			t.Errorf("%s: no tool-call hook that honours deny or ask", name)
		}
	}
	if strings.Contains(ClaudeSettings("/x/pengupool"), `"matcher"`) {
		t.Error("Claude's PreToolUse has a matcher: some tool would skip the hook")
	}
}

func TestSubagentLists(t *testing.T) {
	k, m, e := Subagents(Claude)
	if len(k) != 3 || len(m) != 4 || len(e) != 5 {
		t.Fatalf("claude: %v %v %v", k, m, e)
	}
	if k, m, e := Subagents(Codex); len(k) == 0 || m != nil || len(e) == 0 {
		t.Fatalf("codex: %v %v %v", k, m, e)
	}
	if k, m, e := Subagents(Pi); k != nil || m != nil || e != nil {
		t.Fatalf("pi has no subagents: %v %v %v", k, m, e)
	}
}
