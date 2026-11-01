package pool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// A saved model is made the triage model only after the host's own connection check of exactly those
// settings passed; saving it again needs a new pass.
func TestActivateNeedsAPassingCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"{\"build_failed\":true,\"next\":\"fix_errors\",\"errors\":2}"}}]}`))
	}))
	defer srv.Close()
	p, st := twoTeams(t)
	pc := map[string]any{"type": "ollama", "format": "openai-chat", "address": srv.URL, "model": "m"}
	checked := func() []string {
		r, _ := call(t, p, true, "decision.providers", map[string]any{})
		return r.(map[string]any)["checked"].([]string)
	}
	if _, err := call(t, p, true, "provider.save", map[string]any{"provider": pc}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, p, true, "provider.activate", map[string]any{"type": "ollama"}); err == nil || !strings.Contains(err.Error(), "Run Check connection first") || st.Config().Decision.Active != "" {
		t.Fatalf("activated without a check: %v", err)
	}
	if r, err := call(t, p, true, "provider.check", map[string]any{"type": "ollama"}); err != nil || r.(map[string]any)["ok"] != true {
		t.Fatalf("check: %v %v", r, err)
	}
	if c := checked(); len(c) != 1 || c[0] != "ollama" {
		t.Fatalf("checked after a pass: %v", c)
	}
	if _, err := call(t, p, true, "provider.save", map[string]any{"provider": pc}); err != nil {
		t.Fatal(err)
	}
	if c := checked(); len(c) != 0 {
		t.Fatalf("checked after a re-save: %v", c)
	}
	if _, err := call(t, p, true, "provider.activate", map[string]any{"type": "ollama"}); err == nil || st.Config().Decision.Active != "" {
		t.Fatalf("activated after a re-save without a new check: %v", err)
	}
	_, _ = call(t, p, true, "provider.check", map[string]any{"type": "ollama"})
	if _, err := call(t, p, true, "provider.activate", map[string]any{"type": "ollama"}); err != nil || st.Config().Decision.Active != "ollama" {
		t.Fatalf("a checked model was not made active: %v", err)
	}
}

// A save while a check runs (a new key under the same key reference looks the same in settings)
// keeps that check's pass from counting.
func TestSaveDuringCheckVoidsItsPass(t *testing.T) {
	asked, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(asked)
		<-release
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"{\"build_failed\":true,\"next\":\"fix_errors\",\"errors\":2}"}}]}`))
	}))
	defer srv.Close()
	p, st := twoTeams(t)
	pc := map[string]any{"type": "ollama", "format": "openai-chat", "address": srv.URL, "model": "m"}
	if _, err := call(t, p, true, "provider.save", map[string]any{"provider": pc}); err != nil {
		t.Fatal(err)
	}
	done := make(chan any, 1)
	go func() { r, _ := call(t, p, true, "provider.check", map[string]any{"type": "ollama"}); done <- r }()
	<-asked
	if _, err := call(t, p, true, "provider.save", map[string]any{"provider": pc}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if r := <-done; r.(map[string]any)["ok"] != false || !strings.Contains(r.(map[string]any)["error"].(string), "saved again while it was checked") {
		t.Fatalf("a check that started before a save: %v", r)
	}
	if _, err := call(t, p, true, "provider.activate", map[string]any{"type": "ollama"}); err == nil || st.Config().Decision.Active != "" {
		t.Fatalf("a check that started before a save made the model active: %v", err)
	}
}
