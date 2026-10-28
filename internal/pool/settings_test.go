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
