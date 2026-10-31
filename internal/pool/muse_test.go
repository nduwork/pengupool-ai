package pool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// musePool builds a pool with an iOS-visible team and idle sessions.
func musePool(t *testing.T) (*Pool, *store.Store) {
	t.Helper()
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Status = "idle"
		}
		for i := range s.Domain.Teams {
			s.Domain.Teams[i].IOSAccess = true
		}
		return nil
	})
	return p, st
}

func museCaller() gateway.Caller {
	return gateway.Caller{Phone: true, Device: "muse-test"}
}

func museCall(t *testing.T, p *Pool, typ string, params any) any {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := p.RPC(context.Background(), museCaller(), typ, raw)
	if err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
	return res
}

func museOwnerCall(t *testing.T, p *Pool, typ string, params any) any {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := p.RPC(context.Background(), gateway.Caller{Owner: true}, typ, raw)
	if err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
	return res
}

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMuseDeviceGate(t *testing.T) {
	p, _ := musePool(t)
	if _, err := p.RPC(context.Background(), gateway.Caller{}, "muse.sessions", nil); err == nil {
		t.Fatal("anonymous reached muse.sessions")
	}
	if _, err := p.RPC(context.Background(), gateway.Caller{Phone: true}, "muse.sessions", nil); err == nil {
		t.Fatal("phoneless device reached muse.sessions")
	}
	if _, err := p.RPC(context.Background(), museCaller(), "team.delete", nil); err == nil {
		t.Fatal("device reached an owner route")
	}
}

// Pairing: one use, expiry, brute-force burn, revoke.
func TestMusePairing(t *testing.T) {
	p, _ := musePool(t)
	mk := func() map[string]string {
		m := toMap(t, museOwnerCall(t, p, "muse.pairCode", map[string]any{}))
		out := map[string]string{"code": m["code"].(string), "url": m["url"].(string)}
		if len(out["code"]) != 6 || out["url"] == "" {
			t.Fatalf("pair code: %+v", m)
		}
		return out
	}
	// Pair once.
	c := mk()
	res := museCall(t, p, "muse.pair", map[string]any{"code": c["code"], "device_name": "phone1"})
	tok := res.(map[string]string)["token"]
	if tok == "" {
		t.Fatalf("pair: %+v", res)
	}
	// One use: the same code is dead.
	if _, err := p.RPC(context.Background(), museCaller(), "muse.pair", json.RawMessage(`{"code":"`+c["code"]+`","device_name":"phone1"}`)); err == nil {
		t.Fatal("code reused")
	}
	// A second device may not take the same name.
	c2 := mk()
	if _, err := p.RPC(context.Background(), museCaller(), "muse.pair", json.RawMessage(`{"code":"`+c2["code"]+`","device_name":"phone1"}`)); err == nil {
		t.Fatal("duplicate device name paired")
	}
	// Ten wrong codes in a row throttle pairing for a minute.
	for i := 0; i < 10; i++ {
		_, _ = p.RPC(context.Background(), museCaller(), "muse.pair", json.RawMessage(`{"code":"00000`+string(rune('0'+i%10))+`","device_name":"x"}`))
	}
	if _, err := p.RPC(context.Background(), museCaller(), "muse.pair", json.RawMessage(`{"code":"`+c2["code"]+`","device_name":"other"}`)); err == nil {
		t.Fatal("throttled pairing worked")
	} else if !strings.Contains(err.Error(), "try again later") {
		t.Fatalf("throttle = %v", err)
	}
	p.museMu.Lock()
	p.musePairBlocked = time.Time{}
	p.museMu.Unlock()
	// Expired codes fail: backdate one directly.
	p.museMu.Lock()
	p.museCodes["999999"] = &musePairCode{code: "999999", expires: time.Now().Add(-time.Minute)}
	p.museMu.Unlock()
	if _, err := p.RPC(context.Background(), museCaller(), "muse.pair", json.RawMessage(`{"code":"999999","device_name":"x"}`)); err == nil {
		t.Fatal("expired code worked")
	}
	// Revoke takes effect on the next request.
	museOwnerCall(t, p, "muse.revoke", map[string]any{"name": "phone1"})
	if devs := p.st.Config().MuseDevices; len(devs) != 0 {
		t.Fatalf("revoked device still paired: %+v", devs)
	}
}

// Next walks waiting sessions oldest first, one card per call.
func TestMuseNextOrder(t *testing.T) {
	p, st := musePool(t)
	addApproval := func(id, session string, created string) {
		_ = st.Update(func(s *store.State) error {
			s.Domain.Approvals = append(s.Domain.Approvals, protocol.Approval{
				ID: id, Kind: "formation", TeamID: "team1", SessionID: session,
				State: "pending", Detail: "add helper " + id, CreatedAt: created,
			})
			return nil
		})
	}
	addApproval("a-old", "leadaaaaaaaa", "2026-10-27T10:00:00Z")
	addApproval("a-new", "webaaaaaaaaa", "2026-10-27T11:00:00Z")
	first := toMap(t, museCall(t, p, "muse.next", map[string]any{}))
	second := toMap(t, museCall(t, p, "muse.next", map[string]any{}))
	if first["ask_id"] != "approval:a-old" || second["ask_id"] != "approval:a-old" {
		t.Fatalf("one card per call: %+v %+v", first, second)
	}
	hash := first["payload_hash"].(string)
	// Answer the oldest with Deny.
	answered := museCall(t, p, "muse.answer", map[string]any{
		"ask_id": "approval:a-old", "action": "deny", "payload_hash": hash, "idempotency_key": "k1",
	})
	if answered == nil {
		t.Fatal("answer returned nothing")
	}
	third := toMap(t, museCall(t, p, "muse.next", map[string]any{}))
	if third["ask_id"] != "approval:a-new" {
		t.Fatalf("next after answer: %+v", third)
	}
}

// A stale hash and an answered ask both conflict; a repeated key replays.
func TestMuseAnswerIdempotent(t *testing.T) {
	p, st := musePool(t)
	_ = st.Update(func(s *store.State) error {
		s.Domain.Approvals = append(s.Domain.Approvals, protocol.Approval{
			ID: "a1", Kind: "formation", TeamID: "team1", SessionID: "leadaaaaaaaa",
			State: "pending", Detail: "add helper", CreatedAt: "2026-10-27T10:00:00Z",
		})
		return nil
	})
	card := toMap(t, museCall(t, p, "muse.next", map[string]any{}))
	hash := card["payload_hash"].(string)
	bad := map[string]any{"ask_id": "approval:a1", "action": "deny", "payload_hash": "deadbeef", "idempotency_key": "k-bad"}
	if _, err := p.RPC(context.Background(), museCaller(), "muse.answer", toJSON(t, bad)); !isConflict(err) {
		t.Fatalf("stale hash: %v", err)
	}
	good := map[string]any{"ask_id": "approval:a1", "action": "deny", "payload_hash": hash, "idempotency_key": "k1"}
	r1 := museCall(t, p, "muse.answer", good)
	r2 := museCall(t, p, "muse.answer", good)
	if !jsonEqual(r1, r2) {
		t.Fatalf("replay differs: %+v vs %+v", r1, r2)
	}
	changed := map[string]any{"ask_id": "approval:a1", "action": "deny", "payload_hash": hash, "idempotency_key": "k2"}
	if _, err := p.RPC(context.Background(), museCaller(), "muse.answer", toJSON(t, changed)); !isConflict(err) {
		t.Fatalf("answered ask: %v", err)
	}
	// The same key on another tap is a conflict, not a free answer.
	other := map[string]any{"ask_id": "approval:a2", "action": "deny", "payload_hash": hash, "idempotency_key": "k1"}
	if _, err := p.RPC(context.Background(), museCaller(), "muse.answer", toJSON(t, other)); !isConflict(err) {
		t.Fatalf("reused key: %v", err)
	}
}

func toJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func isConflict(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "conflict") || strings.Contains(err.Error(), "changed or expired"))
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Saying to a session credits the owner: the words are treated as the
// owner's, and a standing instruction records a Compass signal.
func TestMuseSayCreditsOwner(t *testing.T) {
	p, st := musePool(t)
	p.testKeys = func(id, text string) error { return nil }
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			if s.Sessions[i].ID == "leadaaaaaaaa" {
				s.Sessions[i].Harness = "claude"
				s.Sessions[i].Transcript = path
			}
		}
		return nil
	})
	words := "From now on, always run the tests before committing."
	before := signalKinds(t, st)
	museCall(t, p, "muse.say", map[string]any{"session": "leadaaaaaaaa", "words": words})
	hook := func(event string, pl map[string]any) {
		t.Helper()
		b, _ := json.Marshal(pl)
		if _, err := p.hookEvent(context.Background(), hookIn{SessionID: "leadaaaaaaaa", Harness: "claude", Event: event, Payload: b}); err != nil {
			t.Fatal(err)
		}
	}
	hook("UserPromptSubmit", map[string]any{"prompt": words})
	hook("Stop", map[string]any{"stop_hook_active": false, "last_assistant_message": "ok"})
	// The owner's words stage a wording ask (nothing recorded yet).
	// placeTurn runs after the hook: wait for the ask like a live agent would.
	asked := false
	for i := 0; i < 200 && !asked; i++ {
		p.mu.Lock()
		asked = p.wording["leadaaaaaaaa"] != nil
		p.mu.Unlock()
		if !asked {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !asked {
		t.Fatal("the owner's words asked for no wording")
	}
	// The session answers with no lasting preference: the signal lands.
	if err := wordWith(t, p, "leadaaaaaaaa", compassWordIn{None: true}); err != nil {
		t.Fatal(err)
	}
	kinds := signalKinds(t, st)
	if len(kinds) != len(before)+1 {
		t.Fatalf("signals: %v (was %v)", kinds, before)
	}
}

// A device sees only iOS teams, plus sessions on no team.
func TestMuseSeesOnlyIOSTeams(t *testing.T) {
	p, st := musePool(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams {
			s.Domain.Teams[i].IOSAccess = s.Domain.Teams[i].ID == "team2"
		}
		return nil
	})
	var sessions []museSessionView
	raw, _ := json.Marshal(museCall(t, p, "muse.sessions", map[string]any{}))
	if err := json.Unmarshal(raw, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Session != "otheraaaaaaa" {
		t.Fatalf("device sees wrong sessions: %+v", sessions)
	}
}

// A waiting ask on a team without iOS access cannot be answered by posting its id.
func TestMuseAnswerRespectsVisibility(t *testing.T) {
	p, st := musePool(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams {
			s.Domain.Teams[i].IOSAccess = false
		}
		s.Domain.Approvals = append(s.Domain.Approvals, protocol.Approval{
			ID: "h1", Kind: "formation", TeamID: "team1", SessionID: "leadaaaaaaaa",
			State: "pending", Detail: "add helper", CreatedAt: "2026-10-27T10:00:00Z",
		})
		return nil
	})
	in := map[string]any{"ask_id": "approval:h1", "action": "allow", "payload_hash": "x", "idempotency_key": "kv"}
	if _, err := p.RPC(context.Background(), museCaller(), "muse.answer", toJSON(t, in)); err == nil || !strings.Contains(err.Error(), "no ask") {
		t.Fatalf("hidden team's ask answered: %v", err)
	}
	if a := st.State().Domain.Approvals; a[len(a)-1].State != "pending" {
		t.Fatalf("hidden approval changed: %+v", a[len(a)-1])
	}
	// A launch /next never lists (no session, the team not active yet), posted with a correct hash.
	_ = st.Update(func(s *store.State) error {
		s.Domain.Approvals = append(s.Domain.Approvals, protocol.Approval{
			ID: "h2", Kind: "launch", TeamID: "team1", Version: 1,
			State: "pending", Detail: "launch it", CreatedAt: "2026-10-27T10:00:00Z",
		})
		return nil
	})
	hash := payloadHash("launch\x00h2\x00Launch proposal v1: launch it")
	in = map[string]any{"ask_id": "approval:h2", "action": "allow", "payload_hash": hash, "idempotency_key": "kv2"}
	if _, err := p.RPC(context.Background(), museCaller(), "muse.answer", toJSON(t, in)); err == nil || !strings.Contains(err.Error(), "no ask") {
		t.Fatalf("unlisted hidden launch answered: %v", err)
	}
	if a := st.State().Domain.Approvals; a[len(a)-1].State != "pending" {
		t.Fatalf("hidden launch changed: %+v", a[len(a)-1])
	}
}

// The pairing URL is the HTTPS Serve name, or a placeholder until Serve is set up.
func TestMuseBaseURL(t *testing.T) {
	p, _ := musePool(t)
	if got := p.museBaseURL(); got != "https://<tailnet-name>" {
		t.Errorf("no Serve: %q", got)
	}
	if err := p.st.UpdateConfig(func(c *store.Config) error { c.Network.Tailscale = "https://mac.tail1.ts.net/"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := p.museBaseURL(); got != "https://mac.tail1.ts.net" {
		t.Errorf("Serve: %q", got)
	}
}
