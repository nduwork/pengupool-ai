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

func TestValidateSpark(t *testing.T) {
	ok := store.SparkConfig{Address: "https://api.meta.ai/v1", Model: "muse-spark-1.3", KeyRef: "keychain:spark-meta"}
	if err := ValidateSpark(ok); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]store.SparkConfig{
		"no key":      {Address: ok.Address, Model: ok.Model},
		"no model":    {Address: ok.Address, KeyRef: ok.KeyRef},
		"bad address": {Address: "api.meta.ai", Model: ok.Model, KeyRef: ok.KeyRef},
		"key in url":  {Address: "https://k:s@api.meta.ai/v1", Model: ok.Model, KeyRef: ok.KeyRef},
		"raw key":     {Address: ok.Address, Model: ok.Model, KeyRef: "sk-123"},
	} {
		if ValidateSpark(c) == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

// chatReply is a chat completion whose text is content.
func chatReply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "muse-spark-1.3", "choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}}})
}

func TestSpark(t *testing.T) {
	var got struct {
		Model          string `json:"model"`
		Messages       []struct{ Content string }
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string `json:"name"`
				Schema struct {
					Required   []string `json:"required"`
					Properties struct {
						Recipient struct {
							Enum []string `json:"enum"`
						} `json:"recipient"`
					} `json:"properties"`
				} `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	var path, auth string
	reply := "Sure.\n```json\n{\"recipient\": \"API-builder\", \"reasoning\": \"It owns\\nthe API.\"}\n```"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		chatReply(w, reply)
	}))
	defer srv.Close()
	t.Setenv("SPARK_TEST_KEY", "k-test")
	c := store.SparkConfig{Address: srv.URL + "/v1/", Model: "muse-spark-1.3", KeyRef: "env:SPARK_TEST_KEY"}
	kids := []SparkMember{{Name: "api-builder", Role: "BUILDER", Description: "the API"}, {Name: "ui-helper", Role: "HELPER", Busy: true}}
	pick, err := Spark(context.Background(), c, "lead", "Rotate the key AKIAABCDEFGHIJKLMNOP and fix the API", kids)
	if err != nil {
		t.Fatal(err)
	}
	if pick.Recipient != "api-builder" || pick.Reasoning != "It owns the API." || pick.Model != "muse-spark-1.3" {
		t.Fatalf("pick %+v", pick)
	}
	if path != "/v1/chat/completions" || auth != "Bearer k-test" || got.Model != "muse-spark-1.3" || len(got.Messages) != 2 {
		t.Fatalf("request: %s %q %+v", path, auth, got)
	}
	// Structured output: a json_schema whose recipient is one of the sender's children's names.
	if rf := got.ResponseFormat; rf.Type != "json_schema" || rf.JSONSchema.Name == "" || strings.Join(rf.JSONSchema.Schema.Required, ",") != "recipient,reasoning" ||
		strings.Join(rf.JSONSchema.Schema.Properties.Recipient.Enum, ",") != "api-builder,ui-helper" {
		t.Fatalf("response_format %+v", rf)
	}
	if user := got.Messages[1].Content; strings.Contains(user, "AKIA") || !strings.Contains(user, "ui-helper") || !strings.Contains(user, `"busy":true`) {
		t.Fatalf("the task was not redacted, or the team not sent: %s", user)
	}

	// An answer naming no child, or no JSON at all, is refused; no answer in time is a timeout.
	for _, r := range []string{`{"recipient":"someone-else","reasoning":"x"}`, "I would pick the API builder."} {
		reply = r
		if _, err := Spark(context.Background(), c, "lead", "task", kids); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v", r, err)
		}
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		chatReply(w, `{"recipient":"api-builder"}`)
	}))
	defer slow.Close()
	defer func(d time.Duration) { SparkTimeout = d }(SparkTimeout)
	SparkTimeout = 50 * time.Millisecond
	c.Address = slow.URL
	if _, err := Spark(context.Background(), c, "lead", "task", kids); !errors.Is(err, ErrTimeout) {
		t.Fatalf("slow: %v", err)
	}
}
