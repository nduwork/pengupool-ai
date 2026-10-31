package pool

import (
	"context"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The triage model is required: it cannot be set to none, a model that fails its check is not made
// active, and the app's prompt follows whether one is set and answering.
func TestTriageModelRequired(t *testing.T) {
	p, st := twoTeams(t)
	status := func() string {
		r, _ := call(t, p, true, "triage.status", map[string]any{})
		return r.(map[string]string)["status"]
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
	p.mu.Lock()
	p.testDecider = failDecider{}
	p.mu.Unlock()
	_, _ = p.decider().Decide(context.Background(), decision.Request{})
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
