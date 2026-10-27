package mcp

import (
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

func testEnv(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
}

func call(t *testing.T, st *State, method string, id any, params map[string]any) map[string]any {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	return Handle(req, st)
}

func resultText(t *testing.T, resp map[string]any) string {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %+v", resp)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

func TestInitializeCarriesLines(t *testing.T) {
	testEnv(t)
	if _, err := compass.Add("Always run the test suite before committing.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	st := NewState()
	resp := call(t, st, "initialize", 1, map[string]any{
		"roots": []any{map[string]any{"uri": "file:///repo/tests"}}})
	result, _ := resp["result"].(map[string]any)
	instructions, _ := result["instructions"].(string)
	for _, want := range []string{"owner's Compass", "test suite", "experience_for"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q:\n%s", want, instructions)
		}
	}
}

func TestTools(t *testing.T) {
	testEnv(t)
	if _, err := compass.Add("Always run the test suite before committing.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	st := NewState()
	resp := call(t, st, "tools/list", 1, nil)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools: %+v", resp)
	}
	resp = call(t, st, "tools/call", 2, map[string]any{
		"name": "compass_context", "arguments": map[string]any{"topic": "run the tests"}})
	if text := resultText(t, resp); !strings.Contains(text, "test suite") {
		t.Fatalf("context: %q", text)
	}
	resp = call(t, st, "tools/call", 3, map[string]any{
		"name": "compass_observe", "arguments": map[string]any{"words": "prefer small"}})
	if text := resultText(t, resp); text != "noted as a proposal; the owner confirms" {
		t.Fatalf("observe: %q", text)
	}
	resp = call(t, st, "tools/call", 4, map[string]any{
		"name": "compass_observe", "arguments": map[string]any{"words": "  "}})
	if _, ok := resp["error"]; !ok {
		t.Fatalf("empty words must fail: %+v", resp)
	}
	resp = call(t, st, "tools/call", 5, map[string]any{"name": "bogus"})
	if _, ok := resp["error"]; !ok {
		t.Fatalf("unknown tool must fail: %+v", resp)
	}
}

func TestResources(t *testing.T) {
	testEnv(t)
	st := NewState()
	resp := call(t, st, "resources/read", 1, map[string]any{"uri": "compass://lines"})
	result, _ := resp["result"].(map[string]any)
	contents, _ := result["contents"].([]any)
	first, _ := contents[0].(map[string]any)
	if first["text"] != "(no lines yet)" {
		t.Fatalf("read: %+v", resp)
	}
	if _, err := compass.Add("A line.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	resp = call(t, st, "resources/subscribe", 2, map[string]any{"uri": "compass://lines"})
	if _, ok := resp["result"]; !ok {
		t.Fatalf("subscribe: %+v", resp)
	}
	frames := ServeOne(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`, st)
	if len(frames) != 1 {
		t.Fatalf("no file change: %d frames", len(frames))
	}
	if _, err := compass.Add("Another line.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	frames = ServeOne(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`, st)
	if len(frames) != 2 {
		t.Fatalf("list_changed first: %+v", frames)
	}
	if method, _ := frames[0]["method"].(string); method != "notifications/resources/updated" {
		t.Fatalf("notification: %+v", frames[0])
	}
}

func TestNotificationsAndErrors(t *testing.T) {
	testEnv(t)
	st := NewState()
	req := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	if resp := Handle(req, st); resp != nil {
		t.Fatalf("notification must get no reply: %+v", resp)
	}
	req = map[string]any{"jsonrpc": "2.0", "id": 1, "method": "bogus"}
	resp := Handle(req, st)
	errObj, _ := resp["error"].(map[string]any)
	if errObj["code"] != -32601 {
		t.Fatalf("unknown method: %+v", resp)
	}
	frames := ServeOne(`{bad json`, st)
	if len(frames) != 1 {
		t.Fatalf("bad json: %+v", frames)
	}
	if errObj, _ := frames[0]["error"].(map[string]any); errObj["code"] != -32700 {
		t.Fatalf("parse error code: %+v", frames[0])
	}
}
