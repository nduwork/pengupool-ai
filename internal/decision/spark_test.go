package decision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

func metaConfig(addr string) store.ProviderConfig {
	return store.ProviderConfig{Type: "meta", Format: MetaFormat, Address: addr, Model: "muse-spark-1.3", KeyRef: "env:META_TEST_KEY"}
}

func TestValidateMeta(t *testing.T) {
	ok := metaConfig("https://api.meta.ai/v1")
	ok.KeyRef = "keychain:provider-meta"
	for _, a := range []string{ok.Address, "http://127.0.0.1:8080/v1", "http://localhost:8080/v1", "http://[::1]:8080/v1"} {
		c := ok
		c.Address = a
		if err := Validate(c); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	for name, set := range map[string]func(c *store.ProviderConfig){
		"no key":      func(c *store.ProviderConfig) { c.KeyRef = "" },
		"no model":    func(c *store.ProviderConfig) { c.Model = "" },
		"bad address": func(c *store.ProviderConfig) { c.Address = "api.meta.ai" },
		"key in url":  func(c *store.ProviderConfig) { c.Address = "https://k:s@api.meta.ai/v1" },
		"raw key":     func(c *store.ProviderConfig) { c.KeyRef = "sk-123" },
		"plain http":  func(c *store.ProviderConfig) { c.Address = "http://api.meta.ai/v1" },
	} {
		c := ok
		set(&c)
		if Validate(c) == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

// chatReply is a chat completion whose text is content.
func chatReply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "muse-spark-1.3", "choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}}})
}

type schemaProp struct {
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

// Muse Spark answers the three question types in one chat completion with a JSON schema and a fixed
// minimal reasoning effort; its answers carry no confidence (Calibrated false) and its reasoning.
func TestMetaDecide(t *testing.T) {
	var got struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
		Messages        []struct{ Role, Content string }
		ResponseFormat  struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string `json:"name"`
				Schema struct {
					Type                 string                `json:"type"`
					Required             []string              `json:"required"`
					AdditionalProperties *bool                 `json:"additionalProperties"`
					Properties           map[string]schemaProp `json:"properties"`
				} `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	var path, auth string
	reply := "Sure.\n```json\n" + `{"ready": "yes", "recipient": "api-builder", "risk": "high", "reasoning": "It owns` + `\n` + `the API."}` + "\n```"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		chatReply(w, reply)
	}))
	defer srv.Close()
	t.Setenv("META_TEST_KEY", "k-test")
	prov, err := New(metaConfig(srv.URL + "/v1/"))
	if err != nil {
		t.Fatal(err)
	}
	req := Request{State: map[string]any{"task": "Rotate the key AKIAABCDEFGHIJKLMNOP and fix the API"}, Questions: []Question{
		{Name: "ready", Type: Predicate, Instructions: "Is it ready?"},
		{Name: "recipient", Type: Choice, Instructions: "Who does it?", Options: map[string]string{"ui-helper": "the UI (busy)", "api-builder": "the API"}},
		{Name: "risk", Type: Score, Instructions: "How risky?", Levels: []string{"low", "medium", "high"}},
	}}
	res, err := prov.Decide(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Calibrated || res.Model != "muse-spark-1.3" || res.Reasoning != "It owns the API." ||
		res.Answers["ready"].Yes != 1 || res.Answers["recipient"].Choice != "api-builder" || res.Answers["risk"].Score != 2 {
		t.Fatalf("response %+v", res)
	}
	for name, a := range res.Answers {
		if a.Confidence != 0 || a.Probabilities != nil {
			t.Fatalf("%s carries a confidence: %+v", name, a)
		}
	}
	if path != "/v1/chat/completions" || auth != "Bearer k-test" || got.Model != "muse-spark-1.3" || got.ReasoningEffort != "minimal" || len(got.Messages) != 2 {
		t.Fatalf("request: %s %q %+v", path, auth, got)
	}
	rf := got.ResponseFormat
	s := rf.JSONSchema.Schema
	if rf.Type != "json_schema" || rf.JSONSchema.Name == "" || s.Type != "object" || s.AdditionalProperties == nil || *s.AdditionalProperties ||
		strings.Join(s.Required, ",") != "reasoning,ready,recipient,risk" || s.Properties["reasoning"].Type != "string" {
		t.Fatalf("response_format %+v", rf)
	}
	for q, want := range map[string]string{"ready": "yes,no", "recipient": "api-builder,ui-helper", "risk": "low,medium,high"} {
		if p := s.Properties[q]; p.Type != "string" || strings.Join(p.Enum, ",") != want {
			t.Fatalf("%s: %+v, want enum %s", q, p, want)
		}
	}
	if user := got.Messages[1].Content; strings.Contains(user, "AKIA") || !strings.Contains(user, "ui-helper") || !strings.Contains(user, "Who does it?") {
		t.Fatalf("the state was not redacted, or the questions not sent: %s", user)
	}

	// "no" is a 0; an answer outside its enum, or no JSON at all, is refused.
	reply = `{"ready":"no","recipient":"ui-helper","risk":"low","reasoning":"x"}`
	if res, err = prov.Decide(context.Background(), req); err != nil || res.Answers["ready"].Yes != 0 || res.Answers["risk"].Score != 0 {
		t.Fatalf("no: %+v %v", res, err)
	}
	for _, r := range []string{
		`{"ready":"maybe","recipient":"ui-helper","risk":"low","reasoning":"x"}`,
		`{"ready":"yes","recipient":"someone-else","risk":"low","reasoning":"x"}`,
		`{"ready":"yes","recipient":"ui-helper","risk":"extreme","reasoning":"x"}`,
		`{"ready":"yes","risk":"low","reasoning":"x"}`,
		"I would pick the API builder.",
	} {
		reply = r
		if _, err := prov.Decide(context.Background(), req); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v", r, err)
		}
	}

	// No answer in time is a timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		chatReply(w, `{}`)
	}))
	defer slow.Close()
	prov, _ = New(metaConfig(slow.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := prov.Decide(ctx, req); !errors.Is(err, ErrTimeout) {
		t.Fatalf("slow: %v", err)
	}
}

// Check connection runs a normal Decide: an uncalibrated model passes, with no probability item.
func TestMetaCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatReply(w, `{"build_failed":"yes","next":"fix_errors","errors":"three or more","reasoning":"the owner said fix them"}`)
	}))
	defer srv.Close()
	t.Setenv("META_TEST_KEY", "k-test")
	res, items, err := Check(context.Background(), metaConfig(srv.URL))
	if err != nil || res.Calibrated || len(items) != 3 {
		t.Fatalf("check %+v %+v %v", res, items, err)
	}
	for _, it := range items {
		if !it.OK {
			t.Fatalf("%+v", it)
		}
	}
}
