package pool

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The old Claude-on-Ollama setting becomes a Custom agent once, and the sessions that ran on it
// resume through that entry.
func TestMigrateAgentModels(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.AgentModels = &store.AgentModels{OllamaURL: "http://box:11434/", OllamaModel: "qwen3-coder", Ollama: []string{"claude", "pi"}}
		return nil
	})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions,
			store.Session{ID: "picked", Harness: "claude", Model: "ollama/glm"},
			store.Session{ID: "configured", Harness: "claude", Model: "ollama/"},
			store.Session{ID: "plain", Harness: "claude"},
			store.Session{ID: "pi", Harness: "pi"})
		return nil
	})
	p.MigrateAgentModels()
	cfg := st.Config()
	if cfg.AgentModels != nil || len(cfg.CustomAgents) != 2 {
		t.Fatalf("config after migration: %+v", cfg)
	}
	first, second := cfg.CustomAgents[0], cfg.CustomAgents[1]
	if first.Starts != "claude" || !first.Default || second.Default ||
		first.Command != "env OLLAMA_HOST=http://box:11434 ollama launch claude --model qwen3-coder --yes --" ||
		!strings.Contains(second.Command, "--model glm --yes --") || first.Name != "Claude Code (Ollama: qwen3-coder)" {
		t.Fatalf("entries: %+v", cfg.CustomAgents)
	}
	if _, err := checkCustomAgents(cfg.CustomAgents); err != nil {
		t.Fatalf("a migrated entry does not validate: %v", err)
	}
	want := map[string]string{"picked": second.ID, "configured": first.ID, "plain": first.ID, "pi": ""} // "every Claude Code session on Ollama" was on
	for _, s := range st.State().Sessions {
		if s.Custom != want[s.ID] || strings.HasPrefix(s.Model, ollamaPrefix) {
			t.Errorf("%s: custom %q model %q", s.ID, s.Custom, s.Model)
		}
	}
	// Once: a second start changes nothing.
	p.MigrateAgentModels()
	if n := len(st.Config().CustomAgents); n != 2 {
		t.Fatalf("migrated twice: %d entries", n)
	}
}

// A store with no Ollama setting migrates nothing.
func TestMigrateNothing(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	New(Options{Store: st, Exe: "pengupool"}).MigrateAgentModels()
	if c := st.Config(); len(c.CustomAgents) != 0 || c.AgentModels != nil {
		t.Fatalf("config: %+v", c)
	}
}

func TestCheckCustomAgents(t *testing.T) {
	ok, err := checkCustomAgents([]store.CustomAgent{
		{Name: " Ollama ", Command: "ollama launch claude --model m --yes --", Starts: "claude"},
		{Name: "htop", Command: "htop"},
	})
	if err != nil || ok[0].ID == "" || ok[0].Name != "Ollama" || ok[1].Starts != "" {
		t.Fatalf("%+v %v", ok, err)
	}
	for name, c := range map[string][]store.CustomAgent{
		"no --":       {{Name: "x", Command: "ollama launch claude", Starts: "claude"}},
		"hidden":      {{Name: "x", Command: "copilot --", Starts: "copilot"}},
		"no command":  {{Name: "x"}},
		"no name":     {{Command: "htop"}},
		"same name":   {{Name: "x", Command: "a"}, {Name: "X", Command: "b"}},
		"not a agent": {{Name: "x", Command: "a --", Starts: "bash"}},
	} {
		if _, err := checkCustomAgents(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The install check covers the agents New session offers: Copilot and Gemini are hidden, still
// known (their old sessions resume), never probed.
func TestProbeOffersListedAgents(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool", Env: []string{"PATH=" + t.TempDir()}})
	r, _ := p.harnessProbe(context.Background(), struct{}{})
	var got []string
	for k := range r.(map[string]map[string]string) {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := []string{"claude", "codex", "muse", "omp", "opencode", "pi"}; !slices.Equal(got, want) {
		t.Fatalf("probed %v, want %v", got, want)
	}
	if !harness.IsAgent("copilot") || !harness.IsAgent("gemini") {
		t.Fatal("a hidden agent is no longer known")
	}
}

// A Custom agent launches: one that starts an agent runs as that agent, one that starts nothing as
// its command; the session records the entry, so a resume runs it again.
func TestLaunchCustomAgent(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	var got []spawnReq
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		got = append(got, r)
		return store.Session{ID: "s", Harness: r.Harness, Custom: r.Custom}, nil
	}
	list, _ := checkCustomAgents([]store.CustomAgent{
		{Name: "ollama", Command: "ollama launch claude --model m --yes --", Starts: "claude"},
		{Name: "top", Command: "top -o cpu"},
	})
	_ = st.UpdateConfig(func(c *store.Config) error { c.CustomAgents = list; return nil })
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "x"), 0o755)
	for _, c := range list {
		if _, err := p.launch(context.Background(), launchIn{Custom: c.ID, Harness: "codex", Source: sourceIn{Kind: "local", Path: dir}}); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
	}
	if len(got) != 2 || got[0].Harness != "claude" || got[0].Custom != list[0].ID || got[1].Harness != harness.Custom || got[1].Custom != list[1].ID {
		t.Fatalf("spawned %+v", got)
	}
	if _, err := p.launch(context.Background(), launchIn{Custom: "gone", Source: sourceIn{Kind: "local", Path: dir}}); err == nil {
		t.Fatal("a deleted Custom agent launched")
	}
}
