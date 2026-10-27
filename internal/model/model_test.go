package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, status int, body string, got *map[string]any, auth *string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			_ = json.NewDecoder(r.Body).Decode(got)
		}
		if auth != nil {
			*auth = r.Header.Get("Authorization")
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MUSE_BASE_URL", srv.URL+"/")
	t.Setenv("MUSE_API_KEY", "")
	t.Setenv("META_API", "meta-key")
	t.Setenv("MUSE_MODEL", "")
}

func TestChatSendsModelAndKey(t *testing.T) {
	var got map[string]any
	var auth string
	serve(t, 200, `{"choices":[{"message":{"content":"hi"}}]}`, &got, &auth)
	out, err := Chat([]Message{{Role: "user", Content: "x"}}, "", 0)
	if err != nil || out != "hi" {
		t.Fatalf("chat: %q %v", out, err)
	}
	if got["model"] != "muse-spark-1.3" || auth != "Bearer meta-key" {
		t.Fatalf("model %v auth %q", got["model"], auth)
	}
	t.Setenv("MUSE_API_KEY", "own")
	t.Setenv("MUSE_MODEL", "local")
	_, _ = Chat(nil, "", 0)
	if got["model"] != "local" || auth != "Bearer own" {
		t.Fatalf("env override: %v %q", got["model"], auth)
	}
	_, _ = Chat(nil, "explicit", 0)
	if got["model"] != "explicit" {
		t.Fatalf("explicit model: %v", got["model"])
	}
}

func TestChatErrors(t *testing.T) {
	serve(t, 401, `{"error":"bad key"}`, nil, nil)
	if _, err := Chat(nil, "", 0); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("an HTTP error: %v", err)
	}
	serve(t, 200, `{"choices":[]}`, nil, nil)
	if _, err := Chat(nil, "", 0); err == nil {
		t.Fatal("an empty choices list was taken")
	}
}

func TestAskJSONToleratesFencesAndRejectsProse(t *testing.T) {
	serve(t, 200, `{"choices":[{"message":{"content":"Sure:\n`+"```json"+`\n{\"kind\":\"none\"}\n`+"```"+`"}}]}`, nil, nil)
	v, err := AskJSON("s", "u", "")
	if m, ok := v.(map[string]any); err != nil || !ok || m["kind"] != "none" {
		t.Fatalf("fenced: %v %v", v, err)
	}
	serve(t, 200, `{"choices":[{"message":{"content":"no json here"}}]}`, nil, nil)
	if _, err := AskJSON("s", "u", ""); err == nil {
		t.Fatal("prose was taken as JSON")
	}
	serve(t, 200, `{"choices":[{"message":{"content":"{not json}"}}]}`, nil, nil)
	if _, err := AskJSON("s", "u", ""); err == nil {
		t.Fatal("broken JSON was taken")
	}
}
