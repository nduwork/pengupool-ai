package pool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// A decision model of a kind or format no longer spoken is removed at start, and stops being active.
func TestRetiredProvidersDropAtStart(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.Config()
	cfg.Decision.Active = "openrouter"
	cfg.Decision.Providers = map[string]store.ProviderConfig{
		"openrouter": {Type: "openrouter", Format: "openai-chat", Address: "https://openrouter.ai/api/v1", Model: "m", KeyRef: "env:K"},
		"jev":        {Type: "jev", Format: "openai-responses", Address: "https://x/v1", Model: "m", KeyRef: "env:K"},
		"ollama":     {Type: "ollama", Format: "ollama-systemone", Address: "http://127.0.0.1:11434", Model: "m"},
	}
	_ = st.SaveConfig(cfg)
	New(Options{Store: st, Exe: "pengupool"})
	if d := st.Config().Decision; d.Active != "" || len(d.Providers) != 1 || d.Providers["ollama"].Model != "m" {
		t.Fatalf("decision = %+v", d)
	}
}

// The saved key goes only to the saved address: listing models at any other address sends a typed key or none.
func TestProviderModelsKeepsTheKeyToItsAddress(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		io.WriteString(w, `{"data":[{"id":"m"}]}`)
	}))
	defer srv.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PP_TEST_SAVED_KEY", "saved-key")
	p := New(Options{Store: st, Exe: "pengupool"}) // (it drops online providers at start, so save after)
	cfg := st.Config()
	cfg.Decision.Providers = map[string]store.ProviderConfig{"other": {Type: "other", Format: "openai-chat", Address: srv.URL + "/v1", Model: "m", KeyRef: "env:PP_TEST_SAVED_KEY"}}
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, in := range []providerModelsIn{
		{Type: "other"}, // the saved address
		{Type: "other", Address: srv.URL + "/v1/"}, // the same, with a slash
		{Type: "other", Address: srv.URL + "/v2"},  // another address
		{Type: "other", Address: srv.URL + "/v2", Key: "typed"},
	} {
		if _, err := p.providerModels(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"Bearer saved-key", "Bearer saved-key", "", "Bearer typed"}; !slices.Equal(auths, want) {
		t.Fatalf("auth headers %q, want %q", auths, want)
	}
}

// AgentBins validation on save: absolute path to an existing regular
// executable, a known agent id; empty clears.
func TestAgentBinsValidation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	dir := t.TempDir()
	exe := filepath.Join(dir, "pi")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := map[string]map[string]string{
		"relative":  {"pi": "rel/pi"},
		"missing":   {"pi": filepath.Join(dir, "nope")},
		"directory": {"pi": sub},
		"nonexec":   {"pi": plain},
	}
	for name, bins := range bad {
		if _, err := call(t, p, true, "settings.save", map[string]any{"agent_bins": bins}); err == nil {
			t.Fatalf("%s accepted: %v", name, bins)
		}
	}
	if _, err := call(t, p, true, "settings.save", map[string]any{"agent_bins": map[string]string{"pi": exe, "omp": ""}}); err != nil {
		t.Fatal(err)
	}
	if got := st.Config().AgentBins; len(got) != 1 || got["pi"] != exe {
		t.Fatalf("bins = %v", got)
	}
	// An id other than OMP/pi (an older save sent back by the UI) is dropped, never fails the save.
	if _, err := call(t, p, true, "settings.save", map[string]any{"agent_bins": map[string]string{"pi": exe, "claude": exe, "wat": exe}}); err != nil {
		t.Fatal(err)
	}
	if got := st.Config().AgentBins; len(got) != 1 || got["pi"] != exe {
		t.Fatalf("other ids kept: %v", got)
	}
	if _, err := call(t, p, true, "settings.save", map[string]any{"agent_bins": map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if got := st.Config().AgentBins; len(got) != 0 {
		t.Fatalf("clear: %v", got)
	}
}
