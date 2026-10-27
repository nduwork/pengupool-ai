// Package harness hides what differs between agent programs: how each agent is launched, resumed
// and wired to PenguPool's hooks, and how a team message reaches it.
//
// Nothing here edits the user's global harness configuration; every hook arrives per launch.
// Claude gets hooks, status line and inbound-message policy through --settings; OMP and pi load
// PenguPool's extension (--hook / -e); Codex gets hooks through -c overrides. So v1 and v2 can be
// installed side by side.
package harness

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	Claude = "claude"
	Muse   = "muse"
	OMP    = "omp"
	Pi     = "pi"
	Codex  = "codex"
	Shell  = "shell"
	Custom = "custom"
)

// Agent is an agent runtime PenguPool can run, watch and message. Shell and custom sessions are
// plain terminals and are not listed.
type Agent struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Compact string `json:"-"` // slash command that compacts the context
	Clear   string `json:"-"` // slash command that starts a fresh context
	// Typed agents have no inbox PenguPool can write to: a team message is pasted into the
	// terminal (bracketed paste, then Enter) when the agent's Stop hook says it is idle.
	Typed bool `json:"typed"`
	// Ollama: the agent can run on a local Ollama model per launch (no subscription needed).
	Ollama bool `json:"ollama"`
	// Experimental agents work but are not where PenguPool's support is focused (Claude Code is).
	Experimental bool `json:"experimental"`
}

// Agents are the supported runtimes, in menu order: Claude Code (on its account or on Ollama) is
// the focus, the others are experimental. Any of them can share a team with any other:
// team messages go through PenguPool, which delivers each in the recipient's own way.
var Agents = []Agent{
	{ID: Claude, Label: "Claude Code", Hint: "Anthropic's coding agent", Compact: "/compact", Clear: "/clear", Ollama: true},
	{ID: Muse, Label: "Muse Code", Hint: "Meta's coding agent", Typed: true},
	{ID: OMP, Label: "OMP", Hint: "oh-my-pi agent", Compact: "/compact", Clear: "/new", Ollama: true, Experimental: true},
	{ID: Pi, Label: "pi", Hint: "pi coding agent", Compact: "/compact", Clear: "/new", Ollama: true, Experimental: true},
	{ID: Codex, Label: "Codex", Hint: "OpenAI's coding agent", Compact: "/compact", Clear: "/new", Typed: true, Ollama: true, Experimental: true},
}

// Ollama runs an agent on a model served by Ollama, set per launch: no global config is edited.
type Ollama struct {
	URL   string // e.g. http://127.0.0.1:11434
	Model string // e.g. deepseek-v4.1-flash:cloud
}

// Find returns the agent with this id.
func Find(id string) (Agent, bool) {
	for _, a := range Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// IsAgent reports whether id names a supported agent runtime.
func IsAgent(id string) bool { _, ok := Find(id); return ok }

// ClaudeEvents are the Claude Code hooks PenguPool listens to.
var ClaudeEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PermissionRequest", "Stop", "Notification", "SessionEnd",
}

//go:embed omp_extension.ts
var ompExtension []byte

// Params describe one launch.
type Params struct {
	SessionID string
	Name      string
	Cwd       string
	Exe       string // this pengupool executable, for hooks and the extension
	StoreDir  string
	Model     string
	Resume    string   // harness conversation id or OMP session file to resume
	Command   []string // custom command
	Env       []string // base environment
	Ollama    *Ollama  // run on this Ollama model instead of the agent's own account
	// SkillRepo is a helper's skill repository: its skills/ load natively for this one session
	// (Claude --plugin-dir, pi --skill, OMP a per-run config overlay); nothing is installed. A helper
	// works outside it (a worktree or a scratch folder), so the agents that ask before reading
	// outside their working directory are given read access to it (Claude --add-dir);
	// Codex, OMP and pi read anywhere.
	SkillRepo string
	// ReadDirs are folders a helper may read outside its working directory (the team's repositories),
	// given where a clean flag exists: Claude --add-dir. ponytail: Codex, OMP and pi read anywhere already.
	ReadDirs []string
	// Effort is "" or a level the agent takes; Claude, Codex, omp and pi take it,
	// the others ignore it.
	Effort string
}

// Spec returns the command and environment for a launch.
func Spec(kind string, p Params) ([]string, []string, error) {
	env := append(append([]string(nil), p.Env...),
		"PENGUPOOL_SESSION_ID="+p.SessionID,
		"PENGUPOOL_SESSION="+p.SessionID, // steps.sh keys its workflow tracker by this when the harness sets nothing
		"PENGUPOOL_STORE="+p.StoreDir,
		"PENGUPOOL_CLI="+p.Exe,
		"PENGUPOOL_SESSION_NAME="+p.Name,
		"PENGUPOOL_HARNESS="+kind,
	)
	switch kind {
	case Claude:
		bin, err := Lookup(Claude, p.Env)
		if err != nil {
			return nil, nil, err
		}
		cmd := []string{bin, "--teammate-mode", "in-process", "--name", p.Name, "--settings", ClaudeSettings(p.Exe)}
		if o := p.Ollama; o != nil {
			// Ollama's own launcher sets everything Claude Code needs to run on it (the API address,
			// the model for every tier and subagents, the compact window); its Claude is ours.
			ollama, err := LookupOllama(p.Env)
			if err != nil {
				return nil, nil, err
			}
			env = append(env, "OLLAMA_HOST="+o.URL, "PATH="+filepath.Dir(bin)+string(filepath.ListSeparator)+firstNonEmptyEnv(p.Env, "PATH"))
			cmd = append([]string{ollama, "launch", "claude", "--model", o.Model, "--yes", "--"}, cmd[1:]...)
		} else if p.Model != "" {
			cmd = append(cmd, "--model", p.Model)
		}
		if p.Effort != "" {
			cmd = append(cmd, "--effort", p.Effort)
		}
		if p.Resume != "" {
			cmd = append(cmd, "--resume", p.Resume)
		}
		if p.SkillRepo != "" {
			cmd = append(cmd, "--add-dir", p.SkillRepo, "--plugin-dir", p.SkillRepo)
		}
		for _, d := range p.ReadDirs {
			cmd = append(cmd, "--add-dir", d)
		}
		return cmd, env, nil
	case Muse:
		// Muse Code: the interactive TUI in the session's terminal. Approval is the
		// TUI's own (or on the wire for muse serve; see internal/musehost).
		bin, err := Lookup(Muse, p.Env)
		if err != nil {
			return nil, nil, err
		}
		if p.Resume != "" {
			return []string{bin, "resume", p.Resume}, env, nil
		}
		cmd := []string{bin, "--workspace", p.Cwd}
		if p.Model != "" {
			cmd = append(cmd, "--model", p.Model)
		}
		if p.Effort != "" {
			cmd = append(cmd, "--reasoning-effort", p.Effort)
		}
		return cmd, env, nil
	case OMP:
		bin, err := Lookup(OMP, p.Env)
		if err != nil {
			return nil, nil, err
		}
		ext, err := WriteExtension(p.StoreDir)
		if err != nil {
			return nil, nil, err
		}
		cmd := []string{bin, "--hook=" + ext}
		if p.Ollama != nil {
			cmd = append(cmd, "--model=ollama/"+p.Ollama.Model)
		} else if p.Model != "" {
			cmd = append(cmd, "--model="+p.Model)
		}
		if p.Effort != "" {
			cmd = append(cmd, "--thinking", p.Effort)
		}
		if p.Resume != "" {
			cmd = append(cmd, "--resume="+p.Resume)
		}
		if p.SkillRepo != "" {
			overlay, err := writeOMPSkills(p.StoreDir, p.SessionID, filepath.Join(p.SkillRepo, "skills"))
			if err != nil {
				return nil, nil, err
			}
			cmd = append(cmd, "--config="+overlay)
		}
		return cmd, env, nil
	case Pi:
		bin, err := Lookup(Pi, p.Env)
		if err != nil {
			return nil, nil, err
		}
		ext, err := WriteExtension(p.StoreDir)
		if err != nil {
			return nil, nil, err
		}
		cmd := []string{bin, "-e", ext}
		if p.Ollama != nil {
			cmd = append(cmd, "--provider", "ollama", "--model", p.Ollama.Model)
		} else if p.Model != "" {
			cmd = append(cmd, "--model", p.Model)
		}
		if p.Effort != "" {
			cmd = append(cmd, "--thinking", p.Effort)
		}
		if p.Resume != "" {
			cmd = append(cmd, "--session", p.Resume)
		}
		if p.SkillRepo != "" {
			cmd = append(cmd, "--skill", filepath.Join(p.SkillRepo, "skills"))
		}
		return cmd, env, nil
	case Codex:
		bin, err := Lookup(Codex, p.Env)
		if err != nil {
			return nil, nil, err
		}
		cmd := []string{bin}
		if p.Resume != "" {
			cmd = append(cmd, "resume", p.Resume)
		}
		// Hooks given with -c are not in Codex's persisted trust list, so they need the bypass;
		// project hooks still load only from a trusted project layer.
		cmd = append(cmd, "--dangerously-bypass-hook-trust")
		for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
			hook := shellQuote(p.Exe) + " hook codex " + ev
			cmd = append(cmd, "-c", fmt.Sprintf("hooks.%s=[{hooks=[{type=\"command\",command=%s,timeout=15}]}]", ev, strconv.Quote(hook)))
		}
		if o := p.Ollama; o != nil {
			cmd = append(cmd, "-c", `model_provider="pengupool-ollama"`,
				"-c", fmt.Sprintf(`model_providers.pengupool-ollama={name="Ollama",base_url=%s,wire_api="responses"}`, strconv.Quote(o.URL+"/v1/")),
				"-m", o.Model)
		} else if p.Model != "" {
			cmd = append(cmd, "-m", p.Model)
		}
		if p.Effort != "" {
			cmd = append(cmd, "-c", fmt.Sprintf("model_reasoning_effort=%q", p.Effort))
		}
		return cmd, env, nil
	case Shell, "":
		sh := envValue(p.Env, "SHELL")
		if sh == "" {
			sh = "/bin/zsh"
		}
		return []string{sh, "-l"}, env, nil
	case Custom:
		if len(p.Command) == 0 {
			return nil, nil, fmt.Errorf("a custom session needs a command")
		}
		return p.Command, env, nil
	}
	return nil, nil, fmt.Errorf("unknown harness %q", kind)
}

// ClaudeSettings is the --settings JSON: hooks that call back into PenguPool, the context-percent
// status line, and accept for cross-session messages so a team message starts a turn when idle.
func ClaudeSettings(exe string) string {
	q := shellQuote(exe)
	hooks := map[string]any{}
	for _, ev := range ClaudeEvents {
		entry := map[string]any{"type": "command", "command": q + " hook claude " + ev, "timeout": 10}
		group := map[string]any{"hooks": []any{entry}}
		// no matcher on PreToolUse: every tool reaches the hook (triage's hold, the floor, the guards)
		hooks[ev] = []any{group}
	}
	b, _ := json.Marshal(map[string]any{
		"crossSessionInbound": "accept",
		"hooks":               hooks,
		"statusLine":          map[string]any{"type": "command", "command": q + " statusline"},
	})
	return string(b)
}

// WriteExtension places PenguPool's OMP / pi extension under the store, replacing an older copy.
func WriteExtension(storeDir string) (string, error) {
	return writeFile(filepath.Join(storeDir, "harness", "pengupool-omp.ts"), ompExtension)
}

// writeFile replaces path with b atomically unless it already holds b.
func writeFile(path string, b []byte) (string, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(b) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// Lookup finds a harness binary on the given environment's PATH.
func Lookup(kind string, env []string) (string, error) {
	path := envValue(env, "PATH")
	if path == "" {
		path = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, kind)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	// Installers (muse, claude) put binaries in ~/.local/bin, which a
	// GUI-launched daemon's PATH often lacks.
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", kind)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("harness_unavailable: %s is not on PATH", kind)
}

// Probe reports the harness version, or why it is unavailable.
func Probe(kind string, env []string) (string, error) {
	bin, err := Lookup(kind, env)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = env
	done := make(chan struct{})
	var out []byte
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("%s --version timed out", kind)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ActionBytes maps a named terminal action to the bytes a keypad sends.
func ActionBytes(action string) ([]byte, bool) {
	m := map[string]string{
		"enter": "\r", "escape": "\x1b", "tab": "\t", "backspace": "\x7f",
		"ctrl-c": "\x03", "ctrl-d": "\x04", "up": "\x1b[A", "down": "\x1b[B",
		"right": "\x1b[C", "left": "\x1b[D", "shift-tab": "\x1b[Z",
	}
	s, ok := m[action]
	return []byte(s), ok
}

// ClaudeProjectDir is Claude's hashed transcript directory for a cwd: every non-alphanumeric
// character becomes '-'.
func ClaudeProjectDir(home, cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", b.String())
}

// LookupOllama finds the ollama CLI on PATH, else where its installers put it.
func LookupOllama(env []string) (string, error) {
	if bin, err := Lookup("ollama", env); err == nil {
		return bin, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/ollama", "/usr/local/bin/ollama", "/Applications/Ollama.app/Contents/Resources/ollama"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("harness_unavailable: Claude Code on Ollama needs the ollama CLI (https://ollama.com/download)")
}

// firstNonEmptyEnv is env's value for key, else this process's.
func firstNonEmptyEnv(env []string, key string) string {
	if v := envValue(env, key); v != "" {
		return v
	}
	return os.Getenv(key)
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeOMPSkills writes a per-session OMP config overlay (JSON is valid YAML) that adds one skill
// directory; OMP has no per-launch skill flag, and its user config is never edited.
func writeOMPSkills(storeDir, sessionID, dir string) (string, error) {
	path := filepath.Join(storeDir, "harness", "omp-skills-"+sessionID+".yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"skills": map[string]any{"customDirectories": []string{dir}}})
	return path, os.WriteFile(path, b, 0o600)
}

// Subagents are what a helper's own subagents can be told per start: the kinds, the models and the
// effort levels its harness takes (none for a harness with no native subagents; a nil models list
// means the subagent inherits the parent's). Kept here beside the launch flags; the owner sets none of it.
// ponytail: Codex 0.155 has native subagents (multi_agent on, spawn_agent with agent_type) but its role
// names come from the user's config and are not readable from the binary, so only "default" is
// offered and no model; add roles and models once they can be listed. Skill-defined Claude agents
// (agents/*.md) are not read; add their names to kinds when a skill ships them.
func Subagents(name string) (kinds, models, efforts []string) {
	switch name {
	case Claude:
		return []string{"Explore", "general-purpose", "Plan"}, []string{"haiku", "sonnet", "opus", "fable"}, []string{"low", "medium", "high", "xhigh", "max"}
	case Codex:
		return []string{"default"}, nil, []string{"low", "medium", "high"}
	}
	return nil, nil, nil
}
