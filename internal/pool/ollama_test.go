package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The old Claude-on-Ollama setting moves to Claude Code (Ollama) once: the sessions that ran on
// Ollama resume through it, and "every Claude Code session on Ollama" makes it the default.
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
			store.Session{ID: "plain", Harness: "claude", Model: "opus"},
			store.Session{ID: "pi", Harness: "pi"})
		return nil
	})
	p.MigrateAgentModels()
	if cfg := st.Config(); cfg.AgentModels != nil || !cfg.OllamaDefault {
		t.Fatalf("config after migration: %+v", cfg)
	}
	want := map[string][2]string{"picked": {ViaOllama, ""}, "plain": {ViaOllama, "opus"}, "pi": {"", ""}}
	for _, s := range st.State().Sessions {
		if w := want[s.ID]; s.Via != w[0] || s.Model != w[1] {
			t.Errorf("%s: via %q model %q", s.ID, s.Via, s.Model)
		}
	}
	// Without the old setting, nothing changes; an Ollama session from before still moves.
	_ = st.UpdateConfig(func(c *store.Config) error { c.OllamaDefault = false; return nil })
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "late", Harness: "claude", Model: "ollama/x"}, store.Session{ID: "own", Harness: "claude"})
		return nil
	})
	p.MigrateAgentModels()
	if st.Config().OllamaDefault {
		t.Fatal("migrated twice")
	}
	for _, s := range st.State().Sessions {
		if s.ID == "late" && s.Via != ViaOllama || s.ID == "own" && s.Via != "" {
			t.Errorf("%s: via %q", s.ID, s.Via)
		}
	}
}

// The model is the one `ollama launch claude` would preselect, and must be one Ollama has.
func TestOllamaModel(t *testing.T) {
	tags := `{"models":[{"name":"qwen3-coder:latest"},{"name":"glm"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(tags))
		}
	}))
	defer srv.Close()
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ollama"), 0o700)
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "ollama"), []byte("#!/bin/sh\n"), 0o755)
	st, _ := store.Open(t.TempDir())
	p := New(Options{Store: st, Exe: "pengupool", Env: []string{"HOME=" + home, "OLLAMA_HOST=" + strings.TrimPrefix(srv.URL, "http://"), "PATH=" + bin}})
	record := func(s string) { _ = os.WriteFile(filepath.Join(home, ".ollama", "config.json"), []byte(s), 0o600) }
	for _, c := range []struct{ record, want, err string }{
		{"", "qwen3-coder:latest", ""}, // no record: the first model Ollama lists
		{`{"last_model":"glm"}`, "glm", ""},
		{`{"last_model":"glm","integrations":{"claude":{"models":["qwen3-coder","glm"]}}}`, "qwen3-coder", ""},
		{`{"integrations":{"claude":{"models":["kimi:cloud"]}}}`, "kimi:cloud", ""},
		{`{"last_model":"llama9"}`, "", "ollama pull llama9"},
	} {
		record(c.record)
		m, err := p.ollamaModel(context.Background())
		if m != c.want || c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("record %s: %q %v", c.record, m, err)
		}
	}
	record(`{"last_model":"glm"}`)
	if w, err := p.ollamaWrap(context.Background()); err != nil || w != filepath.Join(bin, "ollama")+" launch claude --model glm --yes --" {
		t.Fatalf("wrap %q %v", w, err)
	}
	tags = `{"models":[]}`
	record("")
	if _, err := p.ollamaModel(context.Background()); err == nil || !strings.Contains(err.Error(), "ollama pull") {
		t.Fatalf("no model: %v", err)
	}
	srv.Close()
	if _, err := p.ollamaModel(context.Background()); err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Fatalf("Ollama down: %v", err)
	}
}

// The install check covers the agents New session offers, plus Claude Code (Ollama): Copilot and
// Gemini are hidden, still known (their old sessions resume), never probed.
func TestProbeOffersListedAgents(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool", Env: []string{"PATH=" + t.TempDir(), "OLLAMA_HOST=127.0.0.1:1"}})
	r, _ := p.harnessProbe(context.Background(), struct{}{})
	var got []string
	for k, v := range r.(map[string]map[string]string) {
		got = append(got, k)
		if e := v["error"]; e == "" || regexp.MustCompile(`^[a-z_]+: `).MatchString(e) {
			t.Errorf("%s: the owner reads %q (no install here, so a message without its code)", k, e)
		}
	}
	slices.Sort(got)
	if want := []string{"claude", "codex", "muse", "ollama", "omp", "pi"}; !slices.Equal(got, want) {
		t.Fatalf("probed %v, want %v", got, want)
	}
	if !harness.IsAgent("copilot") || !harness.IsAgent("gemini") {
		t.Fatal("a hidden agent is no longer known")
	}
}

// Claude Code (Ollama) launches as Claude marked via Ollama, so a resume goes through Ollama again;
// no other agent runs on it.
func TestLaunchViaOllama(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	var got []spawnReq
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		got = append(got, r)
		return store.Session{ID: "s", Harness: r.Harness, Via: r.Via}, nil
	}
	dir := t.TempDir()
	if _, err := p.launch(context.Background(), launchIn{Harness: "claude", Via: ViaOllama, Source: sourceIn{Kind: "local", Path: dir}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Harness != "claude" || got[0].Via != ViaOllama {
		t.Fatalf("spawned %+v", got)
	}
	p.testSpawn = nil
	for _, r := range []spawnReq{{Harness: "codex", Via: ViaOllama, Cwd: dir}, {Harness: "claude", Via: "lmstudio", Cwd: dir}} {
		if _, err := p.spawn(context.Background(), r); err == nil {
			t.Errorf("%s via %s started", r.Harness, r.Via)
		}
	}
}
