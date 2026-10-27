package rules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

// fakeEndpoint serves queued model answers, like test_pengu.py's Fake.
func fakeEndpoint(t *testing.T, answers ...string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": answers[0]}}},
		})
		answers = answers[1:]
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MUSE_BASE_URL", srv.URL+"/v1")
}

func TestExtract(t *testing.T) {
	// A rule, wrapped in a code fence like models do.
	fakeEndpoint(t, "```json\n{\"is_rule\": true, \"rule\": \"Run the test suite without asking.\"}\n```")
	got, err := Extract("from now on you can run tests without asking")
	if err != nil || got != "Run the test suite without asking." {
		t.Fatalf("extract: %q %v", got, err)
	}
}

func TestExtractNotRule(t *testing.T) {
	fakeEndpoint(t, `{"is_rule": false, "rule": ""}`)
	got, err := Extract("fix the login bug")
	if err != nil || got != "" {
		t.Fatalf("extract: %q %v", got, err)
	}
}

func TestExtractProseFails(t *testing.T) {
	fakeEndpoint(t, "I cannot help with that.")
	if _, err := Extract("anything"); err == nil {
		t.Fatal("prose must fail")
	}
}

func TestAllowedBy(t *testing.T) {
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
	line, err := compass.Add("Run the test suite without asking.", "typed", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	call := map[string]any{"tool": "Bash", "input": map[string]any{"command": "pytest -q"}}
	lines, _ := compass.Load()
	fakeEndpoint(t,
		`{"allowed_by": "`+line.ID+`", "reason": "tests"}`,
		`{"allowed_by": "made-up", "reason": "x"}`,
	)
	got, err := AllowedBy(call, lines)
	if err != nil || got != line.ID {
		t.Fatalf("allowed: %q %v", got, err)
	}
	// An invented id allows nothing.
	got, err = AllowedBy(call, lines)
	if err != nil || got != "" {
		t.Fatalf("invented: %q %v", got, err)
	}
	// No lines: no model call at all (endpoint queue is empty; a call would panic).
	if got, err := AllowedBy(call, nil); err != nil || got != "" {
		t.Fatalf("empty: %q %v", got, err)
	}
	os.Remove(h + "/compass.jsonl")
}
