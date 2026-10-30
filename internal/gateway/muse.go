package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Muse app API: the owner's phone reads one waiting session at a time
// and answers or chats with it. Every call arrives on a PhoneHosts host
// (this machine's tailnet name, or the Serve name); every call but /pair
// carries a per-device bearer token. The bootstrap token never works here, and a device token works
// nowhere else.

// museRPCTimeout bounds one pool call from the HTTP layer.
const museRPCTimeout = 30 * time.Second

func museBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(rest)
	}
	if rest, ok := strings.CutPrefix(h, "bearer "); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}

// museAuth checks the host and, except for /pair, the device token.
// It returns the device name, or writes the failure and reports false.
func (s *Server) museAuth(w http.ResponseWriter, r *http.Request, needToken bool) (string, bool) {
	if !s.phoneHost(r) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return "", false
	}
	if !needToken {
		return "", true
	}
	name, ok := s.museDevice(museBearer(r))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	return name, true
}

// museDevice resolves a bearer token to its device name, updating
// last_seen at most once a minute. Unknown, missing and revoked tokens
// fail. The bootstrap token never matches: only device hashes are stored.
func (s *Server) museDevice(token string) (string, bool) {
	if s.opts.Store == nil || token == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])
	for _, d := range s.opts.Store.Config().MuseDevices {
		if subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(want)) != 1 {
			continue
		}
		if last, err := time.Parse(time.RFC3339, d.LastSeen); err != nil || time.Since(last) > time.Minute {
			// Under the store lock, so a revoke racing this touch is never undone.
			_ = s.opts.Store.UpdateConfig(func(cfg *store.Config) error {
				for i := range cfg.MuseDevices {
					if cfg.MuseDevices[i].TokenHash == d.TokenHash {
						cfg.MuseDevices[i].LastSeen = time.Now().UTC().Format(time.RFC3339)
					}
				}
				return nil
			})
		}
		return d.Name, true
	}
	return "", false
}

// museRPC forwards one request to the pool as the device's call.
func (s *Server) museRPC(r *http.Request, device, typ string, params any) (any, error) {
	if s.opts.RPC == nil {
		return nil, fmt.Errorf("no host")
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("bad params: %v", err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), museRPCTimeout)
	defer cancel()
	return s.opts.RPC(ctx, Caller{Phone: true, Device: device}, typ, raw)
}

// museCall forwards one request and writes its result.
func (s *Server) museCall(w http.ResponseWriter, r *http.Request, device, typ string, params any) {
	res, err := s.museRPC(r, device, typ, params)
	if err != nil {
		if err.Error() == "no host" {
			http.Error(w, "no host", http.StatusServiceUnavailable)
			return
		}
		writeMuseError(w, err)
		return
	}
	writeMuseJSON(w, res)
}

func writeMuseJSON(w http.ResponseWriter, v any) {
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeMuseError maps pool errors onto the Muse API's small surface: a
// changed or expired ask is 409, a missing one 404.
func writeMuseError(w http.ResponseWriter, err error) {
	msg := err.Error()
	// A tap that raced another in flight is not a conflict: the device
	// retries once after a second. A changed or consumed ask is final.
	if strings.Contains(msg, "already being answered") {
		w.Header().Set("Retry-After", "1")
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	// Guessing too fast throttles pairing for a minute.
	if strings.Contains(msg, "try again later") {
		w.Header().Set("Retry-After", "60")
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	code := http.StatusBadGateway
	switch {
	// First: a device revoked mid-call is "unauthorized: no device", a 401 like any revoked token.
	case strings.Contains(msg, "unauthorized"):
		code = http.StatusUnauthorized
	case strings.Contains(msg, "no ask"), strings.Contains(msg, "no session"), strings.Contains(msg, "no device"), strings.Contains(msg, "not found"):
		code = http.StatusNotFound
	case strings.Contains(msg, "changed or expired"), strings.Contains(msg, "no longer pending"), strings.Contains(msg, "is not idle"), strings.Contains(msg, "not running"):
		code = http.StatusConflict
	case strings.Contains(msg, "params:"), strings.Contains(msg, "an idempotency key"), strings.Contains(msg, "action is"), strings.Contains(msg, "no words"):
		code = http.StatusBadRequest
	case strings.Contains(msg, "forbidden"), strings.Contains(msg, "not an agent"):
		code = http.StatusForbidden
	}
	// Anything else the caller can fix (a duplicate or missing device name) is not a server fault.
	var pe *protocol.Error
	if code == http.StatusBadGateway && errors.As(err, &pe) {
		switch pe.Code {
		case protocol.CodeConflict:
			code = http.StatusConflict
		case protocol.CodeNotFound:
			code = http.StatusNotFound
		case protocol.CodeInvalidArgument:
			code = http.StatusBadRequest
		}
	}
	http.Error(w, msg, code)
}

// museRoutes serves /api/muse/v1/*.
func (s *Server) museRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/muse/v1/pair", s.handleMusePair)
	mux.HandleFunc("/api/muse/v1/next", s.handleMuseNext)
	mux.HandleFunc("/api/muse/v1/asks/", s.handleMuseAsk)
	mux.HandleFunc("/api/muse/v1/sessions/", s.handleMuseSessionSay)
	mux.HandleFunc("/api/muse/v1/sessions", s.handleMuseSessions)
	mux.HandleFunc("/api/muse/v1/compass", s.handleMuseCompass)
	mux.HandleFunc("/api/muse/v1/openapi.json", s.handleMuseOpenAPI)
}

func museBody(r *http.Request) (map[string]any, bool) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false
	}
	return m, true
}

// handleMusePair exchanges a one-time code for a device token. Host-gated
// only; the code is the credential here.
func (s *Server) handleMusePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.museAuth(w, r, false); !ok {
		return
	}
	body, ok := museBody(r)
	if !ok {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.museCall(w, r, "", "muse.pair", body)
}

// handleMuseNext returns one card: the oldest waiting session, or 204.
func (s *Server) handleMuseNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device, ok := s.museAuth(w, r, true)
	if !ok {
		return
	}
	wait := 0
	if q := r.URL.Query().Get("wait"); q != "" {
		fmt.Sscanf(q, "%d", &wait)
		if wait < 0 {
			wait = 0
		}
		if wait > 60 {
			wait = 60
		}
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		res, err := s.museRPC(r, device, "muse.next", nil)
		if err != nil {
			writeMuseError(w, err)
			return
		}
		if res != nil {
			writeMuseJSON(w, res)
			return
		}
		if time.Until(deadline) <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// handleMuseAsk answers one ask: /api/muse/v1/asks/{id}/answer.
func (s *Server) handleMuseAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device, ok := s.museAuth(w, r, true)
	if !ok {
		return
	}
	rest, found := strings.CutPrefix(r.URL.Path, "/api/muse/v1/asks/")
	id, action, ok := strings.Cut(rest, "/")
	if !found || !ok || action != "answer" || id == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, ok := museBody(r)
	if !ok {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	body["ask_id"] = id
	s.museCall(w, r, device, "muse.answer", body)
	s.logf("owner via %s: answered %s", device, id)
}

// handleMuseSessionSay sends the owner's words to one session:
// /api/muse/v1/sessions/{id}/say.
func (s *Server) handleMuseSessionSay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device, ok := s.museAuth(w, r, true)
	if !ok {
		return
	}
	rest, found := strings.CutPrefix(r.URL.Path, "/api/muse/v1/sessions/")
	id, action, ok := strings.Cut(rest, "/")
	// A blank id (%20, slashes) never reaches the pool.
	if !found || !ok || action != "say" || strings.TrimSpace(id) == "" {
		http.Error(w, "no session", http.StatusBadRequest)
		return
	}
	body, ok := museBody(r)
	if !ok {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	body["session"] = id
	s.museCall(w, r, device, "muse.say", body)
	s.logf("owner via %s: said to %s", device, id)
}

// handleMuseSessions lists visible sessions.
func (s *Server) handleMuseSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device, ok := s.museAuth(w, r, true)
	if !ok {
		return
	}
	s.museCall(w, r, device, "muse.sessions", map[string]any{})
}

// handleMuseCompass reads the owner's shown lines.
func (s *Server) handleMuseCompass(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device, ok := s.museAuth(w, r, true)
	if !ok {
		return
	}
	s.museCall(w, r, device, "muse.compass", map[string]any{})
}

// handleMuseOpenAPI serves the hand-written spec of this surface.
func (s *Server) handleMuseOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.museAuth(w, r, false); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, museOpenAPI)
}

const museOpenAPI = `{
  "openapi": "3.0.0",
  "info": {"title": "PenguPool Muse app API", "version": "1"},
  "servers": [{"url": "/api/muse/v1"}],
  "paths": {
    "/pair": {
      "post": {
        "summary": "Exchange a one-time pairing code for a device token. Host-gated only.",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"code": {"type": "string"}, "device_name": {"type": "string"}}, "required": ["code", "device_name"]}}}}
      }
    },
    "/next": {
      "get": {
        "summary": "Walk the cards one session at a time. Returns the oldest waiting session's card, or 204 when nothing waits.",
        "parameters": [{"name": "wait", "in": "query", "schema": {"type": "integer"}, "description": "Long-poll seconds, at most 60."}]
      }
    },
    "/asks/{id}/answer": {
      "post": {
        "summary": "Answer one card. A tap works exactly like clicking the prompt's button in the PenguPool app.",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"action": {"type": "string", "enum": ["allow", "deny"]}, "payload_hash": {"type": "string"}, "idempotency_key": {"type": "string"}}, "required": ["action", "payload_hash", "idempotency_key"]}}}}
      }
    },
    "/sessions/{id}/say": {
      "post": {
        "summary": "Chat: send the owner's words to that session.",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"words": {"type": "string"}}, "required": ["words"]}}}}
      }
    },
    "/sessions": {
      "get": {"summary": "Overview of the sessions this device can see: session, title, state, waiting (an ask waits on the owner), and question (what the agent asks in its terminal now, if anything; answered there)."}
    },
    "/compass": {
      "get": {"summary": "Read the owner's shown Compass lines."}
    },
    "/openapi.json": {
      "get": {"summary": "This spec."}
    }
  }
}`
