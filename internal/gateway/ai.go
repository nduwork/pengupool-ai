package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/ingest"
	"github.com/nduwork/pengupool-ai/internal/owner"
	pengustore "github.com/nduwork/pengupool-ai/internal/pengustore"
)

// The PenguPool AI improvement, served by the host daemon: the owner's
// confirmed Compass (pengu store), its proposals and questions, and the
// experiment results. Owner-token guarded like every API route; the
// Compass is written only here (owner paths) and by `pengu confirm` —
// agents only ever read it through their session context and MCP.

func init() {
	// Proposals live in the owner package; ingest only proposes.
	ingest.Propose = owner.SaveProposalSimple
}

// aiGuard gates an AI route on the bootstrap token and the app origin.
func (s *Server) aiGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-PenguPool-Token")
		if tok == "" {
			tok = r.URL.Query().Get(TokenQuery)
		}
		if len(tok) != len(s.token) || !hmacEqual(tok, s.token) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if !originAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden origin"})
			return
		}
		next(w, r)
	}
}

func hmacEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func aiBody(r *http.Request) (map[string]any, bool) {
	if r.ContentLength < 0 || r.ContentLength > 1<<20 {
		return nil, false
	}
	if r.ContentLength == 0 {
		return map[string]any{}, true
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&body); err != nil {
		return nil, false
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, true
}

// aiRoutes registers the Compass AI endpoints on the host's HTTP surface.
func (s *Server) aiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/ai/lines", s.aiGuard(s.handleAILines))
	mux.HandleFunc("/api/ai/lines/", s.aiGuard(s.handleAILines))
	mux.HandleFunc("/api/ai/ingest", s.aiGuard(s.handleAIIngest))
	mux.HandleFunc("/api/ai/proposals", s.aiGuard(s.handleAIProposals))
	mux.HandleFunc("/api/ai/proposals/", s.aiGuard(s.handleAIProposals))
	mux.HandleFunc("/api/ai/questions", s.aiGuard(s.handleAIQuestions))
	mux.HandleFunc("/api/ai/questions/", s.aiGuard(s.handleAIQuestions))
	mux.HandleFunc("/api/ai/results", s.aiGuard(s.handleAIResults))
}

func (s *Server) handleAILines(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/ai/lines" && r.Method == http.MethodGet:
		lines, err := compass.Load()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if lines == nil {
			lines = []*compass.Line{}
		}
		writeJSON(w, http.StatusOK, lines)
	case r.URL.Path == "/api/ai/lines" && r.Method == http.MethodPost:
		body, ok := aiBody(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		rule, _ := body["rule"].(string)
		if strings.TrimSpace(rule) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty rule"})
			return
		}
		// The GUI is an owner path: a line typed here is confirmed here.
		line, err := compass.Add(strings.TrimSpace(rule), "app", 1.0)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, line)
	case strings.HasPrefix(r.URL.Path, "/api/ai/lines/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/api/ai/lines/")
		ok, err := compass.Forget(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such line"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"forgotten": id})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown path"})
	}
}

func (s *Server) handleAIIngest(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/ai/ingest" || r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown path"})
		return
	}
	body, ok := aiBody(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	words, _ := body["words"].(string)
	sid, _ := body["session"].(string)
	pid, msg, err := ingest.Ingest(words, sid, "")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"proposal": pid, "message": msg})
}

func (s *Server) handleAIProposals(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/ai/proposals" && r.Method == http.MethodGet:
		items, err := owner.ListProposals()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if items == nil {
			items = []*owner.Proposal{}
		}
		writeJSON(w, http.StatusOK, items)
	default:
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPost && len(parts) == 5 && parts[0] == "api" && parts[1] == "ai" && parts[2] == "proposals" {
			switch parts[4] {
			case "confirm":
				code, out := owner.ConfirmProposal(parts[3])
				writeJSON(w, code, out)
				return
			case "dismiss":
				code, out := owner.DismissProposal(parts[3])
				writeJSON(w, code, out)
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown path"})
	}
}

func (s *Server) handleAIQuestions(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/ai/questions" && r.Method == http.MethodGet:
		items, err := owner.PendingQuestions()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, items)
	default:
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPost && len(parts) == 5 && parts[0] == "api" && parts[1] == "ai" && parts[2] == "questions" && parts[4] == "answer" {
			body, ok := aiBody(r)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			opt, _ := body["option"].(string)
			hash, _ := body["payload_hash"].(string)
			key, _ := body["idempotency_key"].(string)
			code, out := owner.AnswerQuestion(parts[3], opt, hash, key)
			writeJSON(w, code, out)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown path"})
	}
}

func (s *Server) handleAIResults(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/ai/results" || r.Method != http.MethodGet {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown path"})
		return
	}
	var data []byte
	var err error
	for _, p := range []string{
		filepath.Join(pengustore.Home(), "experiment", "results", "results.json"),
		filepath.Join(pengustore.Home(), "experiment", "results.json"),
	} {
		if data, err = os.ReadFile(p); err == nil {
			break
		}
	}
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no results yet"})
		return
	}
	var rows any
	if err := json.Unmarshal(data, &rows); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bad results file"})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
