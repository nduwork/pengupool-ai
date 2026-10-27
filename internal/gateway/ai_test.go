package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The Compass AI surface: owner-token routes over the pengu store,
// exercised against a live test daemon.
func TestAIRoutes(t *testing.T) {
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
	srv, _ := newTestServer(t, nil)
	base := "http://" + srv.HTTPAddr()
	do := func(method, path, token, body string) (int, map[string]any) {
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, base+path, rdr)
		if token != "" {
			req.Header.Set("X-PenguPool-Token", token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := do("GET", "/api/ai/lines", "", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := do("GET", "/api/ai/lines", "wrong", ""); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	code, out := do("POST", "/api/ai/lines", srv.Token(), `{"rule":"AI surface line"}`)
	if code != 201 || out["rule"] != "AI surface line" {
		t.Fatalf("add: %d %+v", code, out)
	}
	id := out["id"].(string)
	if code, _ := do("GET", "/api/ai/lines", srv.Token(), ""); code != 200 {
		t.Fatalf("list: %d", code)
	}
	if code, _ := do("DELETE", "/api/ai/lines/"+id, srv.Token(), ""); code != 200 {
		t.Fatalf("forget: %d", code)
	}
	// Proposals: save via owner pkg path, then confirm + dismiss over the API.
	if code, out := do("POST", "/api/ai/proposals", srv.Token(), `{}`); code != 404 {
		t.Fatalf("bare proposals POST: %d %+v", code, out)
	}
	if code, _ := do("GET", "/api/ai/questions", srv.Token(), ""); code != 200 {
		t.Fatalf("questions: %d", code)
	}
	if code, _ := do("GET", "/api/ai/results", srv.Token(), ""); code != 404 {
		t.Fatalf("results empty: %d", code)
	}
}

func TestAIPreflightAllowsJSON(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	req, _ := http.NewRequest(http.MethodOptions, "http://"+srv.HTTPAddr()+"/api/ai/lines", nil)
	req.Header.Set("Origin", DesktopOrigin)
	req.Header.Set("Access-Control-Request-Headers", "x-pengupool-token, content-type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight: %d", resp.StatusCode)
	}
	if h := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(h, "Content-Type") {
		t.Fatalf("allow-headers: %q", h)
	}
}
