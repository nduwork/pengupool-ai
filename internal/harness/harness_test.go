package harness

import (
	"os"
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

func TestMuseSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "muse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := Params{SessionID: "s1", Name: "n", Cwd: "/repo", Model: "m1", Effort: "low", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir}}
	cmd, env, err := Spec(Muse, p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "muse"), "--workspace", "/repo", "--model", "m1", "--reasoning-effort", "low"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("muse spec: %v", cmd)
	}
	if !strings.Contains(strings.Join(env, " "), "PENGUPOOL_HARNESS=muse") {
		t.Fatal("harness env missing")
	}
	p.Resume = "ref1"
	cmd, _, err = Spec(Muse, p)
	if err != nil || !slices.Equal(cmd, []string{filepath.Join(dir, "muse"), "resume", "ref1"}) {
		t.Fatalf("muse resume: %v %v", cmd, err)
	}
	if _, ok := Find(Muse); !ok {
		t.Fatal("muse not in Agents")
	}
}

func TestOllamaAndNewAgents(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Codex, Copilot, OpenCode, Gemini, Claude, "ollama"} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"hooks":{"AfterAgent":[{"hooks":[{"type":"command","command":"mine"}]}]}}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), []byte(`{}`), 0o600)
	o := &Ollama{URL: "http://127.0.0.1:11434", Model: "deepseek-v4.1-flash:cloud"}
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir, "HOME=" + home}, Ollama: o}

	cmd, _, _ := Spec(Codex, p)
	if line := strings.Join(cmd, " "); !strings.Contains(line, `base_url="http://127.0.0.1:11434/v1/"`) || !strings.Contains(line, "-m deepseek-v4.1-flash:cloud") {
		t.Fatalf("codex ollama: %s", line)
	}
	_, env, _ := Spec(Copilot, p)
	if e := strings.Join(env, " "); !strings.Contains(e, "COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:11434/v1") || !strings.Contains(e, "COPILOT_MODEL=deepseek-v4.1-flash:cloud") {
		t.Fatalf("copilot ollama env: %s", e)
	}
	cmd, env, _ = Spec(Claude, p) // through Ollama's own launcher, which runs this Claude
	if line := strings.Join(cmd, " "); !strings.HasPrefix(line, filepath.Join(dir, "ollama")+" launch claude --model deepseek-v4.1-flash:cloud --yes -- --teammate-mode in-process") ||
		!slices.Contains(env, "OLLAMA_HOST=http://127.0.0.1:11434") || !strings.Contains(strings.Join(env, " "), "PATH="+dir) {
		t.Fatalf("claude ollama: %v %v", cmd, env)
	}
	cmd, env, _ = Spec(OpenCode, p)
	if e := strings.Join(env, " "); !strings.Contains(e, `"plugin":["file://`) || !strings.Contains(e, `"baseURL":"http://127.0.0.1:11434/v1"`) || cmd[len(cmd)-1] != "ollama/deepseek-v4.1-flash:cloud" {
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
