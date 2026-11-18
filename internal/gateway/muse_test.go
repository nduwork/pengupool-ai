package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// museTestServer builds a server with a stub pool and one paired device.
func museTestServer(t *testing.T, rpc func(context.Context, Caller, string, json.RawMessage) (any, error)) (*Server, string) {
	t.Helper()
	return museTestServerOpts(t, Options{RPC: rpc})
}

// museTestServerOpts is museTestServer with more options (Logf, MuseWait).
func museTestServerOpts(t *testing.T, o Options) (*Server, string) {
	t.Helper()
	o.Sessions, o.PhoneHosts = &fakeSessions{}, func() []string { return []string{"127.0.0.1"} }
	srv, st := newTestServerOpts(t, o)
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
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the ask is already being answered", RetryAfter: 1}
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

// A session action and a team change reach the pool with the id from the path, as the device.
func TestMuseTeamRoutes(t *testing.T) {
	var got []string
	srv, token := museTestServer(t, func(_ context.Context, c Caller, typ string, raw json.RawMessage) (any, error) {
		got = append(got, c.Device+" "+typ+" "+string(raw))
		return map[string]string{"state": "pending"}, nil
	})
	base := "http://" + srv.HTTPAddr() + "/api/muse/v1"
	if code, body, _ := museReq(t, http.MethodPost, base+"/sessions/s1/action", token, `{"action":"stop"}`); code != 200 {
		t.Fatalf("action = %d %s", code, body)
	}
	if code, body, _ := museReq(t, http.MethodPost, base+"/teams/team1/members", token, `{"skill":"planner","team":"other"}`); code != 200 {
		t.Fatalf("members = %d %s", code, body)
	}
	if code, _, _ := museReq(t, http.MethodPost, base+"/teams/team1/other", token, `{}`); code != 404 {
		t.Fatalf("unknown team route = %d", code)
	}
	if len(got) != 2 || got[0] != `phone1 muse.action {"action":"stop","session":"s1"}` || !strings.Contains(got[1], `muse.member`) || !strings.Contains(got[1], `"team":"team1"`) {
		t.Fatalf("pool saw %q", got)
	}
}

// The Tailscale Serve name reaches the API; a lookalike name is refused before the token check,
// and the token still gates every call.
func TestMuseServeNameHost(t *testing.T) {
	srv, _ := newTestServerOpts(t, Options{Sessions: &fakeSessions{},
		PhoneHosts: func() []string { return []string{"muse-1.tail1234.ts.net"} }})
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

// The status comes from the error's code alone, whatever its words; an uncoded error is the host's fault.
func TestMuseErrorStatus(t *testing.T) {
	for _, c := range []struct {
		err   error
		want  int
		retry string
	}{
		{protocol.Errorf(protocol.CodeInvalidArgument, "a device name is required"), http.StatusBadRequest, ""},
		{protocol.Errorf(protocol.CodeUnauthorized, "bad or expired code"), http.StatusUnauthorized, ""},
		{protocol.Errorf(protocol.CodeForbidden, "x is not an agent session"), http.StatusForbidden, ""},
		{protocol.Errorf(protocol.CodeNotFound, "no open check-in 7"), http.StatusNotFound, ""},
		{protocol.Errorf(protocol.CodeConflict, "a device named phone1 is already paired"), http.StatusConflict, ""},
		{protocol.Errorf(protocol.CodeConflict, "words that once read as not found or unauthorized"), http.StatusConflict, ""},
		{protocol.Errorf(protocol.CodeUnavailable, "no host"), http.StatusServiceUnavailable, ""},
		{&protocol.Error{Code: protocol.CodeUnavailable, Message: "the ask is already being answered", RetryAfter: 1}, http.StatusServiceUnavailable, "1"},
		{&protocol.Error{Code: protocol.CodeUnavailable, Message: "too many wrong codes, try again later", RetryAfter: 60}, http.StatusServiceUnavailable, "60"},
		{protocol.Errorf(protocol.CodeInternal, "worker exploded"), http.StatusBadGateway, ""},
		{protocol.Errorf(protocol.CodeUnsupported, "unsupported message type"), http.StatusBadGateway, ""},
		{errors.New("forbidden, not found and unauthorized, but uncoded"), http.StatusBadGateway, ""},
	} {
		rec := httptest.NewRecorder()
		writeMuseError(rec, c.err)
		if rec.Code != c.want || rec.Header().Get("Retry-After") != c.retry {
			t.Errorf("%v = %d retry %q, want %d retry %q", c.err, rec.Code, rec.Header().Get("Retry-After"), c.want, c.retry)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != c.err.Error() {
			t.Errorf("%v: body %q", c.err, body)
		}
	}
}

// An answer or a say is logged as done only when the pool did it; a refusal logs its code, never the body.
func TestMuseLogsTheOutcome(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	fail := true
	srv, token := museTestServerOpts(t, Options{Logf: logf, RPC: func(context.Context, Caller, string, json.RawMessage) (any, error) {
		if fail {
			return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
		}
		return map[string]string{"ok": "yes"}, nil
	}})
	base := "http://" + srv.HTTPAddr() + "/api/muse/v1"
	museReq(t, http.MethodPost, base+"/asks/approval:a1/answer", token, `{"action":"allow","payload_hash":"h","idempotency_key":"secret-body"}`)
	museReq(t, http.MethodPost, base+"/sessions/s1/say", token, `{"words":"secret-words"}`)
	fail = false
	museReq(t, http.MethodPost, base+"/asks/approval:a2/answer", token, `{"action":"allow","payload_hash":"h","idempotency_key":"k"}`)
	mu.Lock()
	all := strings.Join(lines, "\n")
	mu.Unlock()
	for _, want := range []string{"owner via phone1: answer approval:a1 refused: conflict", "owner via phone1: say to s1 refused: conflict", "owner via phone1: answered approval:a2"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in:\n%s", want, all)
		}
	}
	for _, never := range []string{"answered approval:a1", "said to s1", "secret", token} {
		if strings.Contains(all, never) {
			t.Errorf("%q logged:\n%s", never, all)
		}
	}
}

// A waiting /next answers as soon as the pool signals a change, not on a timer.
func TestMuseNextWakesOnChange(t *testing.T) {
	var mu sync.Mutex
	wake := make(chan struct{})
	ready := false
	srv, token := museTestServerOpts(t, Options{
		MuseWait: func() <-chan struct{} { mu.Lock(); defer mu.Unlock(); return wake },
		RPC: func(context.Context, Caller, string, json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			if ready {
				return map[string]any{"ask_id": "approval:a1"}, nil
			}
			return nil, nil
		}})
	base := "http://" + srv.HTTPAddr() + "/api/muse/v1"
	go func() {
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		ready = true
		close(wake)
		wake = make(chan struct{})
		mu.Unlock()
	}()
	began := time.Now()
	code, body, _ := museReq(t, http.MethodGet, base+"/next?wait=30", token, "")
	if took := time.Since(began); code != 200 || !strings.Contains(body, "approval:a1") || took > 300*time.Millisecond+200*time.Millisecond {
		t.Fatalf("next = %d %s after %v", code, body, took)
	}
	// wait=0 answers at once.
	mu.Lock()
	ready = false
	mu.Unlock()
	began = time.Now()
	if code, _, _ := museReq(t, http.MethodGet, base+"/next?wait=0", token, ""); code != http.StatusNoContent || time.Since(began) > 200*time.Millisecond {
		t.Fatalf("wait=0 = %d after %v", code, time.Since(began))
	}
	// A cancelled request stops waiting.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/next?wait=30", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	began = time.Now()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("a cancelled /next took %v", took)
	}
}

// With no wake signal, a waiting /next still looks again every museNextTick.
func TestMuseNextFallsBackToATick(t *testing.T) {
	defer func(d time.Duration) { museNextTick = d }(museNextTick)
	museNextTick = 50 * time.Millisecond
	var mu sync.Mutex
	calls := 0
	srv, token := museTestServer(t, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls++; calls >= 3 {
			return map[string]any{"ask_id": "approval:a1"}, nil
		}
		return nil, nil
	})
	if code, body, _ := museReq(t, http.MethodGet, "http://"+srv.HTTPAddr()+"/api/muse/v1/next?wait=5", token, ""); code != 200 || !strings.Contains(body, "approval:a1") {
		t.Fatalf("next = %d %s", code, body)
	}
}
