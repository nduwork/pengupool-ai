package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestAgentSpecsWireHooksPerLaunch(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Codex, Pi} {
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
	want := []string{filepath.Join(dir, "muse"), "--workspace", "/repo", "--trust-workspace", "--model", "m1", "--reasoning-effort", "low"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("muse spec: %v", cmd)
	}
	if !strings.Contains(strings.Join(env, " "), "PENGUPOOL_HARNESS=muse") {
		t.Fatal("harness env missing")
	}
	p.Resume = "ref1"
	cmd, _, err = Spec(Muse, p)
	if err != nil || !slices.Equal(cmd, []string{filepath.Join(dir, "muse"), "resume", "ref1", "--trust-workspace"}) {
		t.Fatalf("muse resume: %v %v", cmd, err)
	}
	if _, ok := Find(Muse); !ok {
		t.Fatal("muse not in Agents")
	}
}

func TestOllamaAndNewAgents(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Codex, Claude, "ollama"} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	o := &Ollama{URL: "http://127.0.0.1:11434", Model: "deepseek-v4.1-flash:cloud"}
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir}, Ollama: o}

	cmd, env, _ := Spec(Codex, p)
	if line := strings.Join(cmd, " "); !strings.Contains(line, `base_url="http://127.0.0.1:11434/v1/"`) || !strings.Contains(line, "-m deepseek-v4.1-flash:cloud") {
		t.Fatalf("codex ollama: %s", line)
	}
	cmd, env, _ = Spec(Claude, p) // through Ollama's own launcher, which runs this Claude
	if line := strings.Join(cmd, " "); !strings.HasPrefix(line, filepath.Join(dir, "ollama")+" launch claude --model deepseek-v4.1-flash:cloud --yes -- --teammate-mode in-process") ||
		!slices.Contains(env, "OLLAMA_HOST=http://127.0.0.1:11434") || !strings.Contains(strings.Join(env, " "), "PATH="+dir) {
		t.Fatalf("claude ollama: %v %v", cmd, env)
	}
}

// A helper's skills load for its own session only: a flag or a per-session overlay, never an install.
func TestHelperSkillsLoadPerSession(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Claude, OMP, Pi} {
		os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755)
	}
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir, "HOME=" + t.TempDir()}, SkillRepo: "/skills/ci-debug"}
	// Read access too: a helper works outside its skill repository and must not be asked to read it.
	want := map[string]string{Claude: "--add-dir /skills/ci-debug --plugin-dir /skills/ci-debug", Pi: "--skill /skills/ci-debug/skills", OMP: "--config=" + filepath.Join(dir, "harness", "omp-skills-s1.yml")}
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
	for _, k := range []string{Claude} {
		if cmd, _, _ := Spec(k, p); strings.Contains(strings.Join(cmd, " "), "/skills/ci-debug") {
			t.Errorf("%s: a session without a skill got one: %v", k, cmd)
		}
	}
}

func TestSpecEffort(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{Claude, Codex, OMP, Pi} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ kind, effort, want string }{
		{Claude, "high", "--effort high"},
		{Codex, "low", `-c model_reasoning_effort="low"`},
		{OMP, "medium", "--thinking medium"},
		{Pi, "medium", "--thinking medium"},
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
	for name, src := range map[string]string{"omp/pi": string(ompExtension)} {
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

// The Muse bundle bakes the hook identity in: the scrubbed hook
// environment carries nothing, so --store and the harness ride in argv.
func TestMusePluginBakesHookIdentity(t *testing.T) {
	dir := t.TempDir()
	sum, err := writeMusePlugin(dir, "/x/pengupool")
	if err != nil {
		t.Fatal(err)
	}
	if sum == "" {
		t.Fatal("empty bundle hash")
	}
	b, err := os.ReadFile(filepath.Join(musePluginDir(dir), ".muse-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Name         string `json:"name"`
		Capabilities struct {
			Hooks []struct {
				ID      string   `json:"id"`
				Event   string   `json:"event"`
				Command []string `json:"command"`
			} `json:"hooks"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Name != "pengupool" || len(raw.Capabilities.Hooks) != len(museHookEvents) {
		t.Fatalf("manifest: %+v", raw.Name)
	}
	for _, h := range raw.Capabilities.Hooks {
		want := []string{"/x/pengupool", "hook", "--store", dir, "muse", h.Event}
		if !slices.Equal(h.Command, want) {
			t.Fatalf("hook %s: %v", h.ID, h.Command)
		}
	}
	// Deterministic: same inputs, same hash.
	sum2, err := writeMusePlugin(dir, "/x/pengupool")
	if err != nil || sum2 != sum {
		t.Fatalf("rewrite changed the hash: %q vs %q (%v)", sum, sum2, err)
	}
}

// withUnsetEnv unsets key for the test, restoring it after.
func withUnsetEnv(t *testing.T, key string) {
	t.Helper()
	if v, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { os.Setenv(key, v) })
	} else {
		t.Cleanup(func() { os.Unsetenv(key) })
	}
	os.Unsetenv(key)
}

// A non-home store never reaches the installer: the exec entry points
// stay untouched, so no test can install into the owner's muse home.
func TestEnsureMusePluginSkipsTempStore(t *testing.T) {
	oldLook, oldRun := museLookPath, museRun
	museLookPath = func(string) (string, error) { t.Error("lookpath called for a temp store"); return "", nil }
	museRun = func(_ context.Context, _ ...string) error { t.Error("installer ran for a temp store"); return nil }
	defer func() { museLookPath, museRun = oldLook, oldRun }()
	var logged []string
	dir := t.TempDir()
	EnsureMusePlugin(dir, "/x/pengupool", func(f string, a ...any) { logged = append(logged, f) })
	if len(logged) != 1 || !strings.Contains(logged[0], "skipped (not the owner's home)") {
		t.Fatalf("log = %q", logged)
	}
	if _, err := os.Stat(filepath.Join(dir, "harness")); !os.IsNotExist(err) {
		t.Fatal("bundle written for a skipped store")
	}
}

// The installer gate is the owner's real home, not DefaultDir: with
// PENGUPOOL_HOME set, even a store equal to DefaultDir installs nothing;
// with PENGUPOOL_HOME unset and HOME in a temp dir, both installers run.
func TestInstallersGateOnRealHome(t *testing.T) {
	oldLook, oldRun := museLookPath, museRun
	oldCLook := codexLookPath
	museLookPath = func(string) (string, error) { return "/bin/muse", nil }
	called := map[string]bool{}
	museRun = func(_ context.Context, args ...string) error { called["muse:"+args[0]] = true; return nil }
	codexLookPath = func(string) (string, error) { called["codex:lookpath"] = true; return "/bin/codex", nil }
	defer func() { museLookPath, museRun, codexLookPath = oldLook, oldRun, oldCLook }()
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }

	// PENGUPOOL_HOME set: even DefaultDir() is not the owner's home.
	t.Setenv("PENGUPOOL_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	EnsureMusePlugin(store.DefaultDir(), "/x/pengupool", logf)
	EnsureCodexRules(store.DefaultDir(), "/x/pengupool", logf)
	if len(called) != 0 {
		t.Fatalf("installer ran under PENGUPOOL_HOME: %v", called)
	}
	for _, l := range logged {
		if !strings.Contains(l, "skipped (not the owner's home)") {
			t.Fatalf("log = %q", logged)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "rules")); !os.IsNotExist(err) {
		t.Fatal("rules written under PENGUPOOL_HOME")
	}

	// PENGUPOOL_HOME unset, HOME in a temp dir: both installers run.
	withUnsetEnv(t, "PENGUPOOL_HOME")
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	t.Setenv("CODEX_HOME", filepath.Join(home2, ".codex"))
	def := store.DefaultDir()
	EnsureMusePlugin(def, "/x/pengupool", logf)
	EnsureCodexRules(def, "/x/pengupool", logf)
	if !called["muse:plugins"] {
		t.Fatalf("muse installer did not run for the real home: %v", called)
	}
	if !called["codex:lookpath"] {
		t.Fatalf("codex installer did not run for the real home: %v", called)
	}
	if _, err := os.Stat(filepath.Join(home2, ".codex", "rules", "pengupool.rules")); err != nil {
		t.Fatalf("codex rules not written: %v", err)
	}
}

// The Codex rules file is written for the default store only, carries the
// exact taught prefixes, and is rewritten only when the content changes.
func TestEnsureCodexRules(t *testing.T) {
	oldLook := codexLookPath
	codexLookPath = func(string) (string, error) { return "/bin/codex", nil }
	defer func() { codexLookPath = oldLook }()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, f) }

	// A temp store writes nothing.
	EnsureCodexRules(t.TempDir(), "/x/pengupool", logf)
	if len(logged) != 1 || !strings.Contains(logged[0], "skipped (not the owner's home)") {
		t.Fatalf("log = %q", logged)
	}
	if _, err := os.Stat(filepath.Join(home, "rules")); !os.IsNotExist(err) {
		t.Fatal("rules written for a skipped store")
	}

	// The owner's home (PENGUPOOL_HOME unset, HOME in a temp dir) writes
	// the file with the exact prefixes.
	withUnsetEnv(t, "PENGUPOOL_HOME")
	t.Setenv("HOME", t.TempDir())
	def := store.DefaultDir()
	EnsureCodexRules(def, "/x/pengupool", logf)
	raw, err := os.ReadFile(filepath.Join(home, "rules", "pengupool.rules"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "Managed by PenguPool") {
		t.Fatal("no managed header")
	}
	for _, want := range []string{`pattern = ["/x/pengupool", "send"]`, `pattern = ["/x/pengupool", "message"]`,
		`pattern = ["/x/pengupool", "experience", "search"]`, `pattern = ["/x/pengupool", "experience", "show"]`,
		`pattern = ["/x/pengupool", "compass", "word"]`, `pattern = ["/x/pengupool", "search"]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("prefix missing: %s", want)
		}
	}
	if strings.Contains(body, "daemon") || strings.Contains(body, "import") {
		t.Fatal("rule wider than the taught prefixes")
	}

	// A second run with the same exe is silent (no rewrite).
	logged = nil
	EnsureCodexRules(def, "/x/pengupool", logf)
	if len(logged) != 0 {
		t.Fatalf("rewrote identical content: %q", logged)
	}

	// A moved exe rewrites.
	EnsureCodexRules(def, "/y/pengupool", logf)
	raw, _ = os.ReadFile(filepath.Join(home, "rules", "pengupool.rules"))
	if !strings.Contains(string(raw), `"/y/pengupool", "send"`) {
		t.Fatal("moved exe did not rewrite the rules")
	}

	// An exe path with a space stays one argv element.
	EnsureCodexRules(def, "/Applications/PenguPool AI.app/pengupool", logf)
	raw, _ = os.ReadFile(filepath.Join(home, "rules", "pengupool.rules"))
	if !strings.Contains(string(raw), `pattern = ["/Applications/PenguPool AI.app/pengupool", "experience", "search"]`) {
		t.Fatal("spaced exe path was split")
	}

	// The checker, when codex is installed, allows send/search/show and
	// not daemon/import.
	if _, err := exec.LookPath("codex"); err == nil {
		ctx := context.Background()
		spaced := "/Applications/PenguPool AI.app/pengupool"
		for _, argv := range [][]string{{spaced, "send", "--to", "x", "hi"}, {spaced, "experience", "search", "w"}, {spaced, "experience", "show", "id"}} {
			if got, err := CheckCodexRules(ctx, argv...); err != nil || got != "allow" {
				t.Fatalf("%v: %q %v", argv, got, err)
			}
		}
		for _, argv := range [][]string{{spaced, "daemon"}, {spaced, "experience", "import", "f"}} {
			if got, err := CheckCodexRules(ctx, argv...); err != nil || got == "allow" {
				t.Fatalf("%v: %q %v", argv, got, err)
			}
		}
	}
}

// A configured binary wins over PATH, and a missing one names its path
// instead of silently falling back.
func TestSpecBinOverride(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	for _, k := range []string{OMP, Pi} {
		for _, d := range []string{dir, other} {
			if err := os.WriteFile(filepath.Join(d, k), []byte("#!/bin/sh\necho "+d+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := Params{SessionID: "s1", Name: "n", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + other}, Bin: filepath.Join(dir, OMP)}
	cmd, _, err := Spec(OMP, p)
	if err != nil || cmd[0] != filepath.Join(dir, OMP) {
		t.Fatalf("override not used: %v %v", cmd, err)
	}
	gone := filepath.Join(dir, "gone-omp")
	p.Bin = gone
	p.Env = []string{"PATH=" + dir + string(os.PathListSeparator) + other}
	if _, _, err := Spec(OMP, p); err == nil || !strings.Contains(err.Error(), gone) {
		t.Fatalf("missing override did not name its path: %v", err)
	}
	// Probe resolves the same way.
	if v, err := Probe(OMP, p.Env, filepath.Join(other, OMP)); err != nil || v == "" {
		t.Fatalf("probe override: %q %v", v, err)
	}
	if _, err := Probe(Pi, p.Env, gone); err == nil || !strings.Contains(err.Error(), gone) {
		t.Fatalf("probe missing did not name its path: %v", err)
	}
	if _, err := Probe(Pi, []string{"PATH=" + dir}, ""); err != nil {
		t.Fatalf("probe PATH lookup: %v", err)
	}
}
