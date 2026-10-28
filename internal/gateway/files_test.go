package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestWorkspaceFiles(t *testing.T) {
	srv, st := newTestServer(t, &fakeSessions{})
	ws := shortDir(t)
	outside := shortDir(t)
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello"), 0o600)
	os.WriteFile(filepath.Join(outside, "secret"), []byte("nope"), 0o600)
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "esc"))
	if err := st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "s1", Name: "s1", Cwd: ws, State: "running"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	get := func(token, p string) (int, map[string]any) {
		q := url.Values{"session": {"s1"}, "path": {p}}
		req, _ := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/api/files?"+q.Encode(), nil)
		req.Header.Set("X-PenguPool-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if code, _ := get("wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("bad token = %d", code)
	}
	if code, body := get(srv.Token(), ""); code != 200 || len(body["entries"].([]any)) != 2 {
		t.Errorf("root list = %d %v", code, body)
	}
	if code, body := get(srv.Token(), "a.txt"); code != 200 || body["content"] != "hello" {
		t.Errorf("file = %d %v", code, body)
	}
	for _, p := range []string{"../" + filepath.Base(outside) + "/secret", "esc"} {
		if code, body := get(srv.Token(), p); code == 200 && body["content"] == "nope" {
			t.Errorf("%s escaped the workspace", p)
		}
	}
	// Listings and files carry their modification time; a file carries its preview URL.
	_, list := get(srv.Token(), "")
	if m, _ := list["entries"].([]any)[0].(map[string]any)["mtime"].(string); m == "" {
		t.Errorf("no mtime in %v", list)
	}
	_, file := get(srv.Token(), "a.txt")
	raw, _ := file["raw"].(string)
	if file["mtime"] == nil || raw == "" {
		t.Fatalf("file = %v", file)
	}
	fetch := func(u string) (int, string, http.Header) {
		resp, err := http.Get("http://" + srv.HTTPAddr() + u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	if code, body, h := fetch(raw); code != 200 || body != "hello" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("raw = %d %q %v", code, body, h)
	}
	base := strings.TrimSuffix(raw, "a.txt")
	for _, u := range []string{
		"/api/raw/s1/" + strings.Repeat("0", 32) + "/a.txt", // a wrong capability
		base + "esc", // a link out of the workspace
		base + "..%2f" + filepath.Base(outside) + "%2fsecret",
		base, // the workspace root, a directory
	} {
		if code, body, _ := fetch(u); code == 200 || body == "nope" {
			t.Errorf("%s = %d %q", u, code, body)
		}
	}
	// Scripts run only in an HTML page asked with ?run=1, still sandboxed; a PDF goes to the viewer.
	os.WriteFile(filepath.Join(ws, "p.html"), []byte("<p>x</p>"), 0o600)
	os.WriteFile(filepath.Join(ws, "d.pdf"), []byte("%PDF-1.4"), 0o600)
	os.WriteFile(filepath.Join(ws, "x.xhtml"), []byte("<html/>"), 0o600)
	for u, want := range map[string]string{
		base + "a.txt?run=1":   "sandbox; script-src 'none'; object-src 'none'",
		base + "p.html":        "sandbox; script-src 'none'; object-src 'none'",
		base + "p.html?run=1":  "sandbox allow-scripts",
		base + "x.xhtml?run=1": "sandbox allow-scripts",
		base + "d.pdf":         "",
	} {
		if code, _, h := fetch(u); code != 200 || h.Get("Content-Security-Policy") != want || h.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s = %d %q", u, code, h.Get("Content-Security-Policy"))
		}
	}
}

func TestDesktopOrigin(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSessions{})
	for origin, want := range map[string]int{DesktopOrigin: http.StatusOK, "http://evil.example": http.StatusForbidden} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/api/files?session=none", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-PenguPool-Token", srv.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		got := resp.StatusCode
		if got == http.StatusNotFound {
			got = http.StatusOK // past the origin check; the session simply does not exist
		}
		if got != want {
			t.Errorf("origin %s: status %d, want %d", origin, resp.StatusCode, want)
		}
		if origin == DesktopOrigin && resp.Header.Get("Access-Control-Allow-Origin") != DesktopOrigin {
			t.Error("no CORS header for the desktop app")
		}
	}
}
