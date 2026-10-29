package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// museTestServer builds a server with a stub pool and one paired device.
func museTestServer(t *testing.T, rpc func(context.Context, Caller, string, json.RawMessage) (any, error)) (*Server, string) {
	t.Helper()
	srv, st := newTestServerOpts(t, Options{Sessions: &fakeSessions{}, RPC: rpc,
		PhoneHosts: func() []string { return []string{"127.0.0.1"} }})
	const token = "muse-test-token"
	sum := sha256.Sum256([]byte(token))
	if err := st.UpdateConfig(func(cfg *store.Config) error {
		cfg.MuseDevices = append(cfg.MuseDevices, store.MuseDevice{Name: "phone1", TokenHash: hex.EncodeToString(sum[:])})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return srv, token
}

func museReq(t *testing.T, method, url, token, body string) (int, string, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// OpenAPI serves without a device token.
func TestMuseOpenAPI(t *testing.T) {
	srv, _ := museTestServer(t, nil)
	code, body, _ := museReq(t, http.MethodGet, "http://"+srv.HTTPAddr()+"/api/muse/v1/openapi.json", "", "")
	if code != 200 {
		t.Fatalf("openapi = %d", code)
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(body), &spec); err != nil || spec["openapi"] == nil {
		t.Fatalf("openapi is not a spec: %v", err)
	}
}

// A device token reaches the routes; strangers don't.
func TestMuseAuth(t *testing.T) {
	srv, token := museTestServer(t, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		return nil, nil
	})
	base := "http://" + srv.HTTPAddr()
	if code, _, _ := museReq(t, http.MethodGet, base+"/api/muse/v1/next", "", ""); code != 401 {
		t.Fatalf("next without token = %d", code)
	}
	if code, _, _ := museReq(t, http.MethodGet, base+"/api/muse/v1/next", "bogus", ""); code != 401 {
		t.Fatalf("next with bogus token = %d", code)
	}
	// Nothing is waiting: the device long-polls, so the host answers 204.
	if code, _, _ := museReq(t, http.MethodGet, base+"/api/muse/v1/next", token, ""); code != 204 {
		t.Fatalf("empty next = %d", code)
	}
	// next?wait=N caps the wait, and the empty answer is still 204.
	if code, _, _ := museReq(t, http.MethodGet, base+"/api/muse/v1/next?wait=1", token, ""); code != 204 {
		t.Fatalf("empty next?wait=1 = %d", code)
	}
}

// The pool's card passes through untouched.
func TestMuseNextCard(t *testing.T) {
	card := map[string]any{"type": "ask", "ask_id": "approval:a1", "title": "t", "decision": "d"}
	srv, token := museTestServer(t, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		return card, nil
	})
	code, body, _ := museReq(t, http.MethodGet, "http://"+srv.HTTPAddr()+"/api/muse/v1/next", token, "")
	if code != 200 {
		t.Fatalf("next = %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil || got["ask_id"] != "approval:a1" {
		t.Fatalf("card = %s", body)
	}
}

// An in-flight answer is a 503 with a one-second retry, an answered ask a 409.
func TestMuseAnswerBusy(t *testing.T) {
	srv, token := museTestServer(t, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		return nil, errors.New("already being answered")
	})
	code, _, h := museReq(t, http.MethodPost, "http://"+srv.HTTPAddr()+"/api/muse/v1/asks/approval:a1/answer", token, `{"action":"allow","payload_hash":"h","idempotency_key":"k"}`)
	if code != 503 || h.Get("Retry-After") != "1" {
		t.Fatalf("busy answer = %d retry %q", code, h.Get("Retry-After"))
	}
}

// Sessions with a blank id never reach the pool.
func TestMuseSayValidation(t *testing.T) {
	called := false
	srv, token := museTestServer(t, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		called = true
		return nil, nil
	})
	code, body, _ := museReq(t, http.MethodPost, "http://"+srv.HTTPAddr()+"/api/muse/v1/sessions/%20/say", token, `{"words":"hi"}`)
	if code != 400 || called {
		t.Fatalf("blank say = %d %s (pool called: %v)", code, body, called)
	}
}

// The machine's own live tailnet name reaches the API over plain HTTP; a lookalike name is refused
// before the token check, and the token still gates every call.
func TestMuseTailnetNameHost(t *testing.T) {
	srv, _ := newTestServerOpts(t, Options{Sessions: &fakeSessions{},
		PhoneHosts: func() []string { return []string{"", "muse-1.tail1234.ts.net"} }})
	for host, want := range map[string]int{
		"muse-1.tail1234.ts.net":          http.StatusUnauthorized,
		"MUSE-1.tail1234.ts.net":          http.StatusUnauthorized,
		"muse-1.tail1234.ts.net.evil.com": http.StatusForbidden,
		"evil-muse-1.tail1234.ts.net":     http.StatusForbidden,
		"muse-2.tail1234.ts.net":          http.StatusForbidden,
		"":                                http.StatusForbidden,
	} {
		_, port, _ := strings.Cut(srv.HTTPAddr(), ":")
		req, _ := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/api/muse/v1/sessions", nil)
		req.Host = host + ":" + port
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q = %d, want %d", host, resp.StatusCode, want)
		}
	}
}

// A pairing the device can fix is a 4xx, never a 502 that reads as the host being down.
func TestMuseErrorStatus(t *testing.T) {
	for err, want := range map[error]int{
		protocol.Errorf(protocol.CodeConflict, "a device named phone1 is already paired"): http.StatusConflict,
		protocol.Errorf(protocol.CodeInvalidArgument, "a device name is required"):        http.StatusBadRequest,
		protocol.Errorf(protocol.CodeInvalidArgument, "x is not an agent session"):        http.StatusForbidden,
		protocol.Errorf(protocol.CodeUnauthorized, "bad or expired code"):                 http.StatusUnauthorized,
		errors.New("worker exploded"): http.StatusBadGateway,
	} {
		rec := httptest.NewRecorder()
		writeMuseError(rec, err)
		if rec.Code != want {
			t.Errorf("%v = %d, want %d", err, rec.Code, want)
		}
	}
}
