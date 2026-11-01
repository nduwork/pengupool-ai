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
	for _, k := range []string{Codex, Copilot, Gemini, Claude} {
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
	// Every harness gives a PenguPool hook 15 s, more than the CLI waits for a Stop (12 s).
	if i := slices.Index(cmd, "--settings"); i < 0 || !strings.Contains(cmd[i+1], `"timeout":15`) {
		t.Fatalf("claude hook timeout: %q", cmd)
	}
	if !strings.Contains(string(ompExtension), "{ timeout: 15000 }") {
		t.Fatal("the pi/OMP extension waits less than 15 s for a hook")
	}
	// sh passes the arguments after its $0 through "$@" word for word.
	if out, err := exec.Command("/bin/sh", "-c", `exec printf '%s|' "$@"`, "pp", "a b", "c").Output(); err != nil || string(out) != "a b|c|" {
		t.Fatalf("arguments after the launcher keep their words: %q %v", out, err)
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
	for _, k := range []string{Claude, OMP, Pi, Copilot, Gemini} {
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
// extensions of the ones without hook settings (OMP, pi) block on its answer.
func TestToolCallsReachTheFloor(t *testing.T) {
	if src := string(ompExtension); !strings.Contains(src, `run("PreToolUse"`) || !strings.Contains(src, "permissionDecision") {
		t.Error("omp/pi: no tool-call hook that honours deny or ask")
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

// TestShiftEnterPerHarness pins what the web terminal's Shift+Enter sends for every harness: Ctrl+J
// to pi, ESC CR to every other session (shiftEnter in TerminalPane.tsx). A harness added to Agents
// fails here until someone has checked what its input reads Shift+Enter as and added it below.
func TestShiftEnterPerHarness(t *testing.T) {
	src, err := os.ReadFile("../../web/src/components/TerminalPane.tsx")
	if err != nil {
		t.Fatal(err)
	}
	const decl = `return harness === "pi" ? "\n" : "\x1b\r";`
	if !strings.Contains(string(src), decl) {
		t.Fatalf("TerminalPane.tsx no longer has %s: update sent and the table below", decl)
	}
	sent := func(kind string) string { // what that line sends
		if kind == Pi {
			return "\n"
		}
		return "\x1b\r"
	}
	// What Shift+Enter must send per harness to type a new line rather than submit. Checked live: pi
	// (0.84.3: ESC CR is Alt+Enter, which submits; Ctrl+J is its new line). Established: Claude Code
	// reads ESC CR (what Option+Enter types) as a new line. The rest are not checked live.
	want := map[string]string{
		Claude: "\x1b\r", Muse: "\x1b\r", Codex: "\x1b\r", Pi: "\n", OMP: "\x1b\r",
		Copilot: "\x1b\r", Gemini: "\x1b\r", Shell: "\x1b\r", Custom: "\x1b\r",
	}
	kinds := []string{Shell, Custom}
	for _, a := range Agents {
		kinds = append(kinds, a.ID)
	}
	for _, k := range kinds {
		w, ok := want[k]
		if !ok {
			t.Errorf("harness %q has no Shift+Enter entry: check what its input reads Shift+Enter as", k)
		} else if got := sent(k); got != w {
			t.Errorf("harness %q needs Shift+Enter %q, but the web terminal sends it %q", k, w, got)
		}
	}
	if len(want) != len(kinds) {
		t.Errorf("the table lists %d harnesses, the host runs %d: drop the ones no longer run", len(want), len(kinds))
	}
}

// Every offered harness answers the map's Compact and Clear actions: a harness added to Agents
// without slash commands fails here until its commands are set (or it is hidden and keeps them).
func TestOfferedHarnessesHaveCompactAndClear(t *testing.T) {
	for _, a := range Agents {
		if a.Hidden {
			continue
		}
		if a.Compact == "" || a.Clear == "" {
			t.Errorf("harness %q has no compact/clear commands: Compact=%q Clear=%q", a.ID, a.Compact, a.Clear)
		}
	}
	// The owner's live check: both work in Muse.
	m, ok := Find(Muse)
	if !ok || m.Compact != "/compact" || m.Clear != "/new" {
		t.Errorf("muse wants /compact and /new, got %+v", m)
	}
}

// Codex and OMP report a prompt waiting on the owner through their own events, and its end: Codex's
// PermissionRequest and Interrupt hooks, OMP's approval events and its ask tool's result.
func TestNeedsInputEventsAreWired(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Codex), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, _, err := Spec(Codex, Params{SessionID: "s1", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd, " ")
	for _, want := range []string{`hooks.PermissionRequest=[{hooks=[{type="command",command="'/x/pengupool' hook codex PermissionRequest"`,
		`hooks.Interrupt=[{hooks=[{type="command",command="'/x/pengupool' hook codex Interrupted"`} {
		if !strings.Contains(line, want) {
			t.Errorf("codex: want %s in %s", want, line)
		}
	}
	src := string(ompExtension)
	for _, want := range []string{`pi.on("tool_approval_requested"`, `hook("PermissionRequest"`, `pi.on("tool_approval_resolved"`,
		`hook("ToolFailed", { tool_name: String(e.toolName), tool_use_id: e.toolCallId })`, `pi.on("tool_result"`, `e.toolName === "ask"`} {
		if !strings.Contains(src, want) {
			t.Errorf("omp extension: no %s", want)
		}
	}
}
