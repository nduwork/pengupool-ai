package pool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

// A saved chat-format triage model is removed at start, and the note says so until that type is saved again.
func TestChatTriageModelRemovedWithANote(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.Config()
	cfg.Decision.Active = "ollama"
	cfg.Decision.Providers = map[string]store.ProviderConfig{"ollama": {Type: "ollama", Format: "openai-chat", Address: "http://127.0.0.1:11434", Model: "llama3.1:8b"}}
	_ = st.SaveConfig(cfg)
	p := New(Options{Store: st, Exe: "pengupool"})
	d := st.Config().Decision
	want := store.RemovedProvider{Type: "ollama", Format: "openai-chat", Model: "llama3.1:8b", Active: true}
	if d.Active != "" || len(d.Providers) != 0 || len(d.Removed) != 1 || d.Removed[0] != want {
		t.Fatalf("decision = %+v", d)
	}
	r, _ := call(t, p, true, "triage.status", map[string]any{})
	if m := r.(map[string]any); m["status"] != "missing" || m["removed"] != want {
		t.Fatalf("triage.status = %+v", m)
	}
	r, _ = call(t, p, true, "decision.providers", map[string]any{})
	if rm := r.(map[string]any)["removed"].([]store.RemovedProvider); len(rm) != 1 {
		t.Fatalf("decision.providers removed = %+v", rm)
	}
	if _, err := p.providerSave(context.Background(), providerSaveIn{Provider: store.ProviderConfig{Type: "ollama", Format: "openai-chat", Address: "http://127.0.0.1:11434", Model: "m"}}); err == nil {
		t.Fatal("a chat-format triage model was saved")
	}
	if _, err := p.providerSave(context.Background(), providerSaveIn{Provider: store.ProviderConfig{Type: "ollama", Format: "ollama-systemone", Address: "http://127.0.0.1:11434", Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	if rm := st.Config().Decision.Removed; len(rm) != 0 {
		t.Fatalf("the note outlived a save of its type: %+v", rm)
	}
	r, _ = call(t, p, true, "triage.status", map[string]any{})
	if _, ok := r.(map[string]any)["removed"]; ok {
		t.Fatal("triage.status still reports the removed model")
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
