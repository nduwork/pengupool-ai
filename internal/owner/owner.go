// Package owner is the Muse-app agent's window into questions and
// proposals: pending questions long-polled, answers with hash and
// idempotency, proposals confirmed by the owner. Text from agents is
// untrusted data. Mirrors pengu/owner.py.
package owner

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/chat"
	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// DefaultPort of the owner API.
const DefaultPort = 8471

// PruneAfter drops settled questions and idempotency rows after 7 days;
// proposals older than 30 days go.
const (
	PruneAfter    = 7 * 86400.0
	ProposalAfter = 30 * 86400.0
)

// Question the owner must answer.
type Question struct {
	ID          string   `json:"id"`
	Session     string   `json:"session"`
	AskedAt     float64  `json:"asked_at"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	PayloadHash string   `json:"payload_hash"`
	ExpiresAt   float64  `json:"expires_at"`
	Status      string   `json:"status"`
	Answer      *Answer  `json:"answer"`
}

// Answer to a question.
type Answer struct {
	Option string  `json:"option"`
	At     float64 `json:"at"`
}

// Verdict is a stored per-key outcome for idempotent replay.
type Verdict struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body"`
	At     float64        `json:"at"`
}

// Proposal awaiting the owner's confirmation.
type Proposal struct {
	ID     string         `json:"id"`
	At     float64        `json:"at"`
	Words  string         `json:"words"`
	Kind   string         `json:"kind"`
	Ref    map[string]any `json:"ref"`
	Status string         `json:"status"`
}

type state struct {
	Questions   map[string]*Question `json:"questions"`
	Idempotency map[string]*Verdict  `json:"idempotency"`
	Proposals   []*Proposal          `json:"proposals"`
}

func statePath() string { return filepath.Join(store.Home(), "owner.json") }

func blank() *state {
	return &state{Questions: map[string]*Question{}, Idempotency: map[string]*Verdict{}, Proposals: []*Proposal{}}
}

func quarantine(p string) *state {
	backup := p + fmt.Sprintf(".corrupt-%d.json", time.Now().Unix())
	_ = os.Rename(p, backup) // unmovable: start blank, original stays
	return blank()
}

func validQuestion(q *Question) bool {
	if q == nil || q.ID == "" || (q.Status != "pending" && q.Status != "answered" && q.Status != "expired") {
		return false
	}
	if len(q.Options) == 0 {
		return false
	}
	for _, o := range q.Options {
		if o == "" {
			return false
		}
	}
	if q.Status == "answered" {
		return q.Answer != nil && q.Answer.Option != ""
	}
	return q.Answer == nil
}

func validVerdict(r *Verdict) bool { return r != nil && r.Body != nil }

func normalizeProposal(m map[string]any) *Proposal {
	if m == nil {
		return nil
	}
	id, _ := m["id"].(string)
	if id == "" {
		id = newID()
	}
	kind, _ := m["kind"].(string)
	if kind == "" {
		kind = "note"
	}
	words, _ := m["words"].(string)
	at, _ := m["at"].(float64)
	status, _ := m["status"].(string)
	var ref map[string]any
	if r, ok := m["ref"].(map[string]any); ok {
		ref = r
	}
	return &Proposal{ID: id, At: at, Words: words, Kind: kind, Ref: ref, Status: status}
}

func validProposal(p *Proposal) bool {
	if p == nil || p.ID == "" || p.Words == "" || p.Status == "" {
		return false
	}
	switch p.Kind {
	case "note", "new-line", "reword", "retire":
	default:
		return false
	}
	return true
}

func loadState() *state {
	p := statePath()
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return blank()
	}
	if err != nil {
		return quarantine(p)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return quarantine(p)
	}
	qs, qOK := raw["questions"].(map[string]any)
	idem, iOK := raw["idempotency"].(map[string]any)
	plist, pOK := raw["proposals"].([]any)
	if !qOK || !iOK || !pOK {
		return quarantine(p)
	}
	before, _ := json.Marshal(raw)
	st := blank()
	for k, v := range qs {
		data, _ := json.Marshal(v)
		var q Question
		if err := json.Unmarshal(data, &q); err == nil && validQuestion(&q) {
			qc := q
			st.Questions[k] = &qc
		}
	}
	for k, v := range idem {
		data, _ := json.Marshal(v)
		var r Verdict
		if err := json.Unmarshal(data, &r); err == nil && validVerdict(&r) {
			rc := r
			st.Idempotency[k] = &rc
		}
	}
	for _, v := range plist {
		m, _ := v.(map[string]any)
		if pr := normalizeProposal(m); validProposal(pr) {
			st.Proposals = append(st.Proposals, pr)
		}
	}
	after, _ := json.Marshal(st)
	if string(before) != string(after) {
		_ = saveState(st) // caller holds the lock; migration must persist
	}
	return st
}

func saveState(st *state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), data, 0o644)
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func payload(sessionID, question string, options []string) string {
	canon, _ := json.Marshal(map[string]any{"s": sessionID, "q": question, "o": options})
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])[:16]
}

// CreateQuestion records a pending question; local only, no HTTP needed.
func CreateQuestion(sessionID, question string, options []string, expiresIn float64) (*Question, error) {
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("empty question")
	}
	if len(options) < 2 || len(options) > 3 {
		return nil, errors.New("give 2 or 3 options")
	}
	seen := map[string]bool{}
	for _, o := range options {
		if strings.TrimSpace(o) == "" {
			return nil, errors.New("options are non-empty strings")
		}
		if seen[o] {
			return nil, errors.New("options are unique")
		}
		seen[o] = true
	}
	rows, err := session.Load()
	if err != nil {
		return nil, err
	}
	found := false
	for _, r := range rows {
		if r.ID == sessionID {
			found = true
		}
	}
	if sessionID == "owner" || !found {
		return nil, fmt.Errorf("off the tree: no session %s", sessionID)
	}
	if expiresIn < 1 {
		expiresIn = 1
	}
	now := float64(time.Now().UnixNano()) / 1e9
	q := &Question{ID: newID(), Session: sessionID, AskedAt: now, Question: question,
		Options: append([]string{}, options...), PayloadHash: payload(sessionID, question, options),
		ExpiresAt: now + expiresIn, Status: "pending"}
	err = store.Locked(func() error {
		st := loadSwept()
		st.Questions[q.ID] = q
		return saveState(st)
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

func sweep(st *state, now float64) bool {
	changed := false
	for _, q := range st.Questions {
		if q.Status == "pending" && now > q.ExpiresAt {
			q.Status = "expired"
			changed = true
		}
	}
	for id, q := range st.Questions {
		if q.Status != "pending" && now-q.AskedAt > PruneAfter {
			delete(st.Questions, id)
			changed = true
		}
	}
	for k, r := range st.Idempotency {
		if now-r.At > PruneAfter {
			delete(st.Idempotency, k)
			changed = true
		}
	}
	var fresh []*Proposal
	for _, p := range st.Proposals {
		if now-p.At > ProposalAfter {
			changed = true
			continue
		}
		fresh = append(fresh, p)
	}
	if changed {
		st.Proposals = fresh
		if st.Proposals == nil {
			st.Proposals = []*Proposal{}
		}
		_ = saveState(st)
	}
	return changed
}

// loadSwept loads, expires, prunes. Caller holds the lock.
func loadSwept() *state {
	st := loadState()
	sweep(st, float64(time.Now().UnixNano())/1e9)
	return st
}

// PendingQuestions sweeps and returns pending items.
func PendingQuestions() ([]*Question, error) {
	var out []*Question
	err := store.Locked(func() error {
		for _, q := range loadSwept().Questions {
			if q.Status == "pending" {
				out = append(out, q)
			}
		}
		return nil
	})
	return out, err
}

// AnswerQuestion applies an owner's answer. Every keyed outcome is stored
// per question: the same idempotency key replays the first result.
func AnswerQuestion(qid, option, hash, key string) (int, map[string]any) {
	if key == "" {
		return 400, map[string]any{"error": "idempotency_key required"}
	}
	ik := qid + "\x00" + key
	var status int
	var body map[string]any
	_ = store.Locked(func() error {
		st := loadSwept()
		if r, ok := st.Idempotency[ik]; ok {
			status, body = r.Status, r.Body
			return nil
		}
		decide := func(s int, b map[string]any) {
			st.Idempotency[ik] = &Verdict{Status: s, Body: b, At: float64(time.Now().UnixNano()) / 1e9}
			_ = saveState(st)
			status, body = s, b
		}
		q := st.Questions[qid]
		now := float64(time.Now().UnixNano()) / 1e9
		switch {
		case q == nil:
			decide(404, map[string]any{"error": "no such question"})
		case q.Status == "answered":
			decide(409, map[string]any{"error": "already answered"})
		case q.Status != "pending" || now > q.ExpiresAt:
			q.Status = "expired"
			decide(409, map[string]any{"error": "expired"})
		case hash != q.PayloadHash:
			decide(409, map[string]any{"error": "changed"})
		default:
			offered := false
			for _, o := range q.Options {
				if o == option {
					offered = true
				}
			}
			if !offered {
				decide(400, map[string]any{"error": "not an offered option"})
				return nil
			}
			q.Status = "answered"
			q.Answer = &Answer{Option: option, At: now}
			decide(200, map[string]any{"id": qid, "option": option, "answered_at": now})
		}
		return nil
	})
	return status, body
}

// SaveProposal stores a proposal for the owner to confirm.
func SaveProposal(words, kind string, ref map[string]any) (int, map[string]any) {
	if strings.TrimSpace(words) == "" {
		return 400, map[string]any{"error": "empty proposal"}
	}
	switch kind {
	case "note", "new-line", "reword", "retire":
	case "":
		kind = "note"
	default:
		return 400, map[string]any{"error": "unknown kind"}
	}
	_ = ref // ref carries the payload; kind validation below gates storage
	var id string
	_ = store.Locked(func() error {
		st := loadSwept()
		p := &Proposal{ID: newID(), At: float64(time.Now().UnixNano()) / 1e9,
			Words: words, Kind: kind, Ref: ref, Status: "proposed"}
		id = p.ID
		st.Proposals = append(st.Proposals, p)
		return saveState(st)
	})
	return 202, map[string]any{"status": "proposed", "id": id}
}

// ListProposals returns swept proposals.
func ListProposals() ([]*Proposal, error) {
	var out []*Proposal
	err := store.Locked(func() error {
		out = loadSwept().Proposals
		return nil
	})
	return out, err
}

// ConfirmProposal applies a proposal the owner confirmed.
func ConfirmProposal(pid string) (int, map[string]any) {
	var status int
	var body map[string]any
	_ = store.Locked(func() error {
		st := loadSwept()
		var p *Proposal
		for _, c := range st.Proposals {
			if c.ID == pid {
				p = c
			}
		}
		if p == nil {
			status, body = 404, map[string]any{"error": "no such proposal"}
			return nil
		}
		if p.Status != "proposed" {
			status, body = 409, map[string]any{"error": "already decided"}
			return nil
		}
		ok, out := applyProposal(p.Kind, p.Ref, pid)
		if !ok {
			status, body = 409, out
			return nil
		}
		p.Status = "confirmed"
		_ = saveState(st)
		status, body = 200, out
		return nil
	})
	return status, body
}

func applyProposal(kind string, ref map[string]any, pid string) (bool, map[string]any) {
	if ref == nil {
		ref = map[string]any{}
	}
	str := func(k string) string {
		s, _ := ref[k].(string)
		return s
	}
	switch kind {
	case "new-line":
		text := strings.TrimSpace(str("text"))
		if text == "" {
			return false, map[string]any{"error": "empty line text"}
		}
		lines, _ := compass.Load()
		for _, l := range lines {
			if l.Source == "ingest:"+pid {
				return true, map[string]any{"applied": "new-line", "id": l.ID}
			}
		}
		l, err := compass.Add(text, "ingest:"+pid, 1.0)
		if err != nil {
			return false, map[string]any{"error": err.Error()}
		}
		return true, map[string]any{"applied": "new-line", "id": l.ID}
	case "reword":
		if strings.TrimSpace(str("text")) == "" || str("line_id") == "" {
			return false, map[string]any{"error": "reword needs a line id and text"}
		}
		ok, err := compass.UpdateRule(str("line_id"), strings.TrimSpace(str("text")))
		if err != nil {
			return false, map[string]any{"error": err.Error()}
		}
		if !ok {
			return false, map[string]any{"error": "line gone"}
		}
		return true, map[string]any{"applied": "reword", "id": str("line_id")}
	case "retire":
		if str("line_id") == "" {
			return false, map[string]any{"error": "retire needs a line id"}
		}
		ok, err := compass.Forget(str("line_id"))
		if err != nil {
			return false, map[string]any{"error": err.Error()}
		}
		if !ok {
			return true, map[string]any{"applied": "retire", "id": str("line_id"), "note": "already gone"}
		}
		return true, map[string]any{"applied": "retire", "id": str("line_id")}
	case "note":
		return true, map[string]any{"applied": "note"}
	}
	return false, map[string]any{"error": "unknown kind"}
}

// WaitAnswer polls until answered: option, "expired", "no such question",
// or "timeout" when still pending.
func WaitAnswer(qid string, timeout float64) (string, string) {
	if timeout < 0 {
		timeout = 0
	}
	end := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	for time.Now().Before(end) {
		var opt, problem string
		var done bool
		_ = store.Locked(func() error {
			q := loadSwept().Questions[qid]
			if q == nil {
				problem, done = "no such question", true
			} else if q.Status == "answered" {
				opt, done = q.Answer.Option, true
			} else if q.Status == "expired" || float64(time.Now().UnixNano())/1e9 > q.ExpiresAt {
				problem, done = "expired", true
			}
			return nil
		})
		if done {
			return opt, problem
		}
		time.Sleep(500 * time.Millisecond)
	}
	var opt, problem string
	_ = store.Locked(func() error {
		q := loadSwept().Questions[qid]
		if q == nil {
			problem = "no such question"
		} else if q.Status == "answered" {
			opt = q.Answer.Option
		} else if q.Status == "expired" || float64(time.Now().UnixNano())/1e9 > q.ExpiresAt {
			problem = "expired"
		} else {
			problem = "timeout"
		}
		return nil
	})
	return opt, problem
}

// Handler serves the owner API. Token authenticates every endpoint.
func Handler(token string) http.Handler {
	mux := http.NewServeMux()
	send := func(w http.ResponseWriter, code int, body any) {
		raw, _ := json.Marshal(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.WriteHeader(code)
		_, _ = w.Write(raw)
	}
	authed := func(r *http.Request) bool {
		return hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token))
	}
	readBody := func(r *http.Request) (map[string]any, bool) {
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
	mux.HandleFunc("/questions", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		if r.URL.Path != "/questions" {
			send(w, 404, map[string]any{"error": "unknown path"})
			return
		}
		wait, err := strconv.ParseFloat(r.URL.Query().Get("wait"), 64)
		if r.URL.Query().Get("wait") != "" && err != nil || math.IsNaN(wait) || math.IsInf(wait, 0) {
			send(w, 400, map[string]any{"error": "bad wait"})
			return
		}
		wait = math.Max(0, math.Min(120, wait))
		end := time.Now().Add(time.Duration(wait * float64(time.Second)))
		items, _ := PendingQuestions()
		for len(items) == 0 && time.Now().Before(end) {
			time.Sleep(250 * time.Millisecond)
			items, _ = PendingQuestions()
		}
		send(w, 200, items)
	})
	mux.HandleFunc("/questions/", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == "POST" && len(parts) == 3 && parts[0] == "questions" && parts[2] == "answer" {
			body, ok := readBody(r)
			if !ok {
				send(w, 400, map[string]any{"error": "bad json"})
				return
			}
			opt, _ := body["option"].(string)
			hash, _ := body["payload_hash"].(string)
			key, _ := body["idempotency_key"].(string)
			code, out := AnswerQuestion(parts[1], opt, hash, key)
			send(w, code, out)
			return
		}
		send(w, 404, map[string]any{"error": "unknown path"})
	})
	proposals := func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		switch {
		case r.URL.Path == "/proposals" && r.Method == "GET":
			items, _ := ListProposals()
			send(w, 200, items)
		case r.URL.Path == "/proposals" && r.Method == "POST":
			body, ok := readBody(r)
			if !ok {
				send(w, 400, map[string]any{"error": "bad json"})
				return
			}
			words, _ := body["words"].(string)
			kind, _ := body["kind"].(string)
			if kind == "" {
				kind = "note"
			}
			ref, _ := body["ref"].(map[string]any)
			code, out := SaveProposal(words, kind, ref)
			send(w, code, out)
		default:
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if r.Method == "POST" && len(parts) == 3 && parts[0] == "proposals" && parts[2] == "confirm" {
				code, out := ConfirmProposal(parts[1])
				send(w, code, out)
				return
			}
			send(w, 404, map[string]any{"error": "unknown path"})
		}
	}
	mux.HandleFunc("/proposals", proposals)
	mux.HandleFunc("/proposals/", proposals)
	// Read endpoints for the desktop renderer: lines, sessions, inbox,
	// and the last experiment results. Same loopback + token guard.
	mux.HandleFunc("/lines", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		if r.URL.Path != "/lines" || r.Method != "GET" {
			send(w, 404, map[string]any{"error": "unknown path"})
			return
		}
		lines, err := compass.Load()
		if err != nil {
			send(w, 500, map[string]any{"error": err.Error()})
			return
		}
		if lines == nil {
			lines = []*compass.Line{}
		}
		send(w, 200, lines)
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		if r.URL.Path != "/sessions" || r.Method != "GET" {
			send(w, 404, map[string]any{"error": "unknown path"})
			return
		}
		rows, err := session.Ls()
		if err != nil {
			send(w, 500, map[string]any{"error": err.Error()})
			return
		}
		if rows == nil {
			rows = []*session.Row{}
		}
		send(w, 200, rows)
	})
	mux.HandleFunc("/inbox", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		if r.URL.Path != "/inbox" || r.Method != "GET" {
			send(w, 404, map[string]any{"error": "unknown path"})
			return
		}
		sid := r.URL.Query().Get("session")
		if sid == "" {
			send(w, 400, map[string]any{"error": "session query required"})
			return
		}
		entries, err := chat.Inbox(sid)
		if err != nil {
			send(w, 404, map[string]any{"error": err.Error()})
			return
		}
		if entries == nil {
			entries = []*chat.Entry{}
		}
		send(w, 200, entries)
	})
	mux.HandleFunc("/results", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			send(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		if r.URL.Path != "/results" || r.Method != "GET" {
			send(w, 404, map[string]any{"error": "unknown path"})
			return
		}
		data, err := os.ReadFile(filepath.Join(store.Home(), "experiment", "results.json"))
		if err != nil {
			send(w, 404, map[string]any{"error": "no results yet"})
			return
		}
		var rows any
		if err := json.Unmarshal(data, &rows); err != nil {
			send(w, 500, map[string]any{"error": "bad results file"})
			return
		}
		send(w, 200, rows)
	})
	return mux
}

// Serve builds the owner API server. Refuses to start without a token,
// and refuses a non-loopback bind without an explicit public opt-in.
func Serve(bind string, port int) (*http.Server, error) {
	token := strings.TrimSpace(os.Getenv("PENGUPOOL_OWNER_TOKEN"))
	if token == "" {
		// Same-machine fallback: the desktop app writes the store token
		// (0600) when it spawns the server; the CLI shares it.
		if raw, err := os.ReadFile(filepath.Join(store.Home(), "owner-token")); err == nil {
			token = strings.TrimSpace(string(raw))
		}
	}
	if token == "" {
		return nil, errors.New("set PENGUPOOL_OWNER_TOKEN first (from the keychain)")
	}
	if bind != "127.0.0.1" && bind != "::1" && bind != "localhost" {
		if os.Getenv("PENGUPOOL_OWNER_PUBLIC") != "1" {
			return nil, errors.New("refusing a non-loopback bind without PENGUPOOL_OWNER_PUBLIC=1: " +
				"the bearer token travels in cleartext HTTP")
		}
		fmt.Fprintln(os.Stderr, "warning: bearer auth over cleartext HTTP off localhost; use a Tailscale ACL (see README)")
	}
	return &http.Server{Addr: net.JoinHostPort(bind, strconv.Itoa(port)), Handler: Handler(token)}, nil
}

// SaveProposalSimple adapts SaveProposal to the ingest Propose seam.
// Wire it once: ingest.Propose = owner.SaveProposalSimple.
func SaveProposalSimple(words, kind string, ref map[string]any) (string, error) {
	code, out := SaveProposal(words, kind, ref)
	if code != 202 {
		msg, _ := out["error"].(string)
		return "", errors.New("proposal failed: " + msg)
	}
	id, _ := out["id"].(string)
	return id, nil
}
