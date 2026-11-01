package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The triage model is required: it cannot be set to none, a model that fails its check is not made
// active, and the app's prompt follows whether one is set and answering.
func TestTriageModelRequired(t *testing.T) {
	p, st := twoTeams(t)
	status := func() string {
		r, _ := call(t, p, true, "triage.status", map[string]any{})
		return r.(map[string]any)["status"].(string)
	}
	if s := status(); s != "missing" {
		t.Fatalf("no model: %s", s)
	}
	if _, err := call(t, p, true, "provider.activate", map[string]any{"type": ""}); err == nil || !strings.Contains(err.Error(), "a triage model is required") {
		t.Fatalf("the triage model was set to none: %v", err)
	}
	cfg := st.Config()
	cfg.Decision.Providers = map[string]store.ProviderConfig{"ollama": {Type: "ollama", Format: "ollama-systemone", Address: "http://127.0.0.1:1", Model: "m"}}
	_ = st.SaveConfig(cfg)
	if _, err := call(t, p, true, "provider.activate", map[string]any{"type": "ollama"}); err == nil || st.Config().Decision.Active != "" {
		t.Fatalf("a model that fails its check was made active: %v", err)
	}
	// A broken active model (set before this start, or saved broken since) is found by the start's check.
	cfg = st.Config()
	cfg.Decision.Active = "ollama"
	_ = st.SaveConfig(cfg)
	p.checkTriage(context.Background())
	if s := status(); s != "unreachable" {
		t.Fatalf("the start's check of a broken model: %s", s)
	}
	cfg.Decision.Active = ""
	_ = st.SaveConfig(cfg)
	p.mu.Lock()
	p.triageErr = triageUnchecked
	p.mu.Unlock()
	p.mu.Lock()
	p.testDecider = hangDecider{}
	p.mu.Unlock()
	if s := status(); s != "unchecked" {
		t.Fatalf("a model not asked yet since the start: %s", s)
	}
	// A call its caller's own deadline cut short (a hook's budget) says nothing about the model.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, _ = p.decider().Decide(ctx, decision.Request{})
	cancel()
	if s := status(); s != "unchecked" {
		t.Fatalf("a call past its caller's deadline marked the model: %s", s)
	}
	p.mu.Lock()
	p.testDecider = failDecider{}
	p.mu.Unlock()
	_, _ = p.decider().Decide(context.Background(), decision.Request{}) // e.g. the provider's own client timeout
	if s := status(); s != "unreachable" {
		t.Fatalf("a failing model: %s", s)
	}
	p.mu.Lock()
	p.testDecider = fakeTriage{record: "record"}
	p.mu.Unlock()
	_, _ = p.decider().Decide(context.Background(), decision.Request{})
	if s := status(); s != "ok" {
		t.Fatalf("an answering model: %s", s)
	}
}

// hangDecider answers nothing until its caller gives up.
type hangDecider struct{}

func (hangDecider) Name() string { return "hang" }
func (hangDecider) Decide(ctx context.Context, _ decision.Request) (decision.Response, error) {
	<-ctx.Done()
	return decision.Response{}, ctx.Err()
}

// A model that never answers: a hook's short budget says nothing about it, but a call that ran the
// provider client's whole wait marks it unreachable, even when the caller's own deadline (the
// background callers' 30 s) ended it first.
func TestHungModelMarkedAfterTheClientsWait(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	defer func(d time.Duration) { clientWait = d }(clientWait)
	clientWait = 200 * time.Millisecond // stands for the client's 30 s
	p, st := twoTeams(t)
	cfg := st.Config()
	cfg.Decision.Providers = map[string]store.ProviderConfig{"ollama": {Type: "ollama", Format: "ollama-systemone", Address: srv.URL, Model: "m"}}
	cfg.Decision.Active = "ollama"
	_ = st.SaveConfig(cfg)
	status := func() string {
		r, _ := call(t, p, true, "triage.status", map[string]any{})
		return r.(map[string]any)["status"].(string)
	}
	ask := func(d time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		_, _ = p.decider().Decide(ctx, decision.Request{State: "s", Questions: []decision.Question{{Name: "q", Type: decision.Predicate, Instructions: "q?"}}})
	}
	ask(50 * time.Millisecond) // a hook's budget (8 s), shorter than the client's wait
	if s := status(); s != "unchecked" {
		t.Fatalf("a hook's budget marked the model: %s", s)
	}
	ask(clientWait) // a background caller's deadline, exactly the client's wait (30 s)
	if s := status(); s != "unreachable" {
		t.Fatalf("a model that never answered within the client's wait: %s", s)
	}
}
