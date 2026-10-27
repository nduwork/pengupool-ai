package owner

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

func testEnv(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
	// One registry row so session s1 is on the tree.
	row, _ := json.Marshal(map[string]any{"id": "s1", "agent": "claude", "tmux": "pengu-s1"})
	if err := os.WriteFile(filepath.Join(h, "sessions.json"), append(row, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionAnswerFlow(t *testing.T) {
	testEnv(t)
	q, err := CreateQuestion("s1", "ship it?", []string{"yes", "no"}, 300)
	if err != nil || q.Status != "pending" {
		t.Fatalf("create: %+v %v", q, err)
	}
	if _, err := CreateQuestion("ghost", "x?", []string{"a", "b"}, 300); err == nil {
		t.Fatal("off-tree must fail")
	}
	if _, err := CreateQuestion("s1", "x?", []string{"only"}, 300); err == nil {
		t.Fatal("one option must fail")
	}
	code, out := AnswerQuestion(q.ID, "yes", q.PayloadHash, "k1")
	if code != 200 || out["option"] != "yes" {
		t.Fatalf("answer: %d %+v", code, out)
	}
	// Same key replays the first result.
	code2, out2 := AnswerQuestion(q.ID, "no", "wrong", "k1")
	if code2 != 200 || out2["option"] != "yes" {
		t.Fatalf("replay: %d %+v", code2, out2)
	}
	// Answered question, new key: 409.
	if code, _ := AnswerQuestion(q.ID, "yes", q.PayloadHash, "k2"); code != 409 {
		t.Fatalf("double answer: %d", code)
	}
	// Changed payload: 409.
	q2, _ := CreateQuestion("s1", "again?", []string{"a", "b"}, 300)
	if code, _ := AnswerQuestion(q2.ID, "a", "badhash", "k3"); code != 409 {
		t.Fatalf("changed: %d", code)
	}
	// Not offered: 400.
	if code, _ := AnswerQuestion(q2.ID, "c", q2.PayloadHash, "k4"); code != 400 {
		t.Fatalf("unoffered: %d", code)
	}
	// Missing key: 400. Unknown id: 404.
	if code, _ := AnswerQuestion(q2.ID, "a", q2.PayloadHash, ""); code != 400 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := AnswerQuestion("missing", "a", q2.PayloadHash, "k5"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
}

func TestQuestionExpiry(t *testing.T) {
	testEnv(t)
	q, err := CreateQuestion("s1", "quick?", []string{"a", "b"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if opt, prob := WaitAnswer(q.ID, 2); prob != "expired" || opt != "" {
		t.Fatalf("expiry: %q %q", opt, prob)
	}
	if code, _ := AnswerQuestion(q.ID, "a", q.PayloadHash, "k"); code != 409 {
		t.Fatalf("expired answer: %d", code)
	}
}

func TestProposalsConfirm(t *testing.T) {
	testEnv(t)
	code, out := SaveProposal("", "note", nil)
	if code != 400 {
		t.Fatalf("empty: %d", code)
	}
	if code, _ := SaveProposal("w", "bogus", nil); code != 400 {
		t.Fatalf("kind: %d", code)
	}
	code, out = SaveProposal("New Compass line: Test often", "new-line", map[string]any{"text": "Test often"})
	if code != 202 {
		t.Fatalf("save: %d %+v", code, out)
	}
	pid := out["id"].(string)
	items, err := ListProposals()
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	code, body := ConfirmProposal(pid)
	if code != 200 || body["applied"] != "new-line" {
		t.Fatalf("confirm: %d %+v", code, body)
	}
	lines, _ := compass.Load()
	if len(lines) != 1 || lines[0].Rule != "Test often" {
		t.Fatalf("applied: %+v", lines)
	}
	// Re-confirm the same id is idempotent via source ingest:pid.
	if code, _ := ConfirmProposal(pid); code != 409 {
		t.Fatalf("reconfirm: %d", code)
	}
	if code, _ := ConfirmProposal("missing"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	// Reword then retire the line.
	lid := lines[0].ID
	code, _ = SaveProposal("reword", "reword", map[string]any{"line_id": lid, "text": "Test always"})
	if code != 202 {
		t.Fatalf("reword save: %d", code)
	}
	items, _ = ListProposals()
	code, _ = ConfirmProposal(items[len(items)-1].ID)
	if code != 200 {
		t.Fatalf("reword: %d", code)
	}
	if code, _ = SaveProposal("retire", "retire", map[string]any{"line_id": lid}); code != 202 {
		t.Fatalf("retire save: %d", code)
	}
	items, _ = ListProposals()
	if code, _ = ConfirmProposal(items[len(items)-1].ID); code != 200 {
		t.Fatalf("retire: %d", code)
	}
	if lines, _ := compass.Load(); len(lines) != 0 {
		t.Fatalf("retired: %+v", lines)
	}
}

func TestHTTPAuthAndAnswer(t *testing.T) {
	testEnv(t)
	srv := httptest.NewServer(Handler("tok"))
	defer srv.Close()
	if resp, _ := http.Get(srv.URL + "/questions"); resp.StatusCode != 401 {
		resp.Body.Close()
		t.Fatalf("no-auth: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	q, err := CreateQuestion("s1", "go?", []string{"y", "n"}, 300)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/questions?wait=0", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil || len(items) != 1 {
		resp.Body.Close()
		t.Fatalf("pending: %+v %v", items, err)
	}
	resp.Body.Close()
	// Answer over HTTP with the payload hash.
	body, _ := json.Marshal(map[string]any{
		"option": "y", "payload_hash": q.PayloadHash, "idempotency_key": "h1"})
	req, _ = http.NewRequest("POST", srv.URL+"/questions/"+q.ID+"/answer", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 {
		t.Fatalf("http answer: %d %+v %v", resp.StatusCode, out, err)
	}
}

func TestServeGuards(t *testing.T) {
	t.Setenv("PENGUPOOL_OWNER_TOKEN", "")
	if _, err := Serve("127.0.0.1", 8471); err == nil {
		t.Fatal("missing token must fail")
	}
	t.Setenv("PENGUPOOL_OWNER_TOKEN", "x")
	if _, err := Serve("0.0.0.0", 8471); err == nil {
		t.Fatal("public bind must fail without opt-in")
	}
}

func authedGet(t *testing.T, srv string, path string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv+path, nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestRendererEndpoints(t *testing.T) {
	testEnv(t)
	if _, err := compass.Add("Renderer line.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler("tok"))
	defer srv.Close()
	if code, raw := authedGet(t, srv.URL, "/lines"); code != 200 || !strings.Contains(string(raw), "Renderer line.") {
		t.Fatalf("lines: %d %s", code, raw)
	}
	if code, raw := authedGet(t, srv.URL, "/sessions"); code != 200 || !strings.Contains(string(raw), "pengu-s1") {
		t.Fatalf("sessions: %d %s", code, raw)
	}
	if code, _ := authedGet(t, srv.URL, "/inbox"); code != 400 {
		t.Fatalf("inbox needs session: %d", code)
	}
	if code, raw := authedGet(t, srv.URL, "/inbox?session=owner"); code != 200 || string(raw) != "[]" {
		t.Fatalf("inbox: %d %s", code, raw)
	}
	if code, _ := authedGet(t, srv.URL, "/results"); code != 404 {
		t.Fatalf("results empty: %d", code)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/lines", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		resp.Body.Close()
		t.Fatalf("no-auth: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

func TestConfirmOverHTTP(t *testing.T) {
	testEnv(t)
	srv := httptest.NewServer(Handler("tok"))
	defer srv.Close()
	code, out := SaveProposal("New Compass line: HTTP line", "new-line", map[string]any{"text": "HTTP line"})
	if code != 202 {
		t.Fatalf("save: %d", code)
	}
	body, _ := json.Marshal(map[string]any{})
	req, _ := http.NewRequest("POST", srv.URL+"/proposals/"+out["id"].(string)+"/confirm", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil || resp.StatusCode != 200 {
		t.Fatalf("confirm: %d %+v %v", resp.StatusCode, decoded, err)
	}
	lines, _ := compass.Load()
	if len(lines) != 1 || lines[0].Rule != "HTTP line" {
		t.Fatalf("applied: %+v", lines)
	}
}
