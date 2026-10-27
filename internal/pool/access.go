package pool

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Group access for phone connections. A team is the group. The desktop owner's access never
// changes; an iOS connection sees only teams that allow iOS access, and a password-protected
// team's screens, history, messages and input stay closed until that connection unlocks it.
// Ungrouped sessions follow no group policy. Passwords are kept only as scrypt verifiers.

const (
	maxFailures   = 5
	failureWindow = 5 * time.Minute
)

// Revoker closes a phone connection's live attachments when access to a session is withdrawn.
type Revoker func(match func(c gateway.Caller, sessionID string) bool)

// SetRevoker connects the gateway's revocation hook.
func (p *Pool) SetRevoker(r Revoker) {
	p.mu.Lock()
	p.revoker = r
	p.mu.Unlock()
}

type policyIn struct {
	TeamID        string  `json:"team_id"`
	IOSAccess     *bool   `json:"ios_access,omitempty"`
	Password      *string `json:"password,omitempty"`
	ClearPassword bool    `json:"clear_password,omitempty"`
}

func (p *Pool) teamPolicy(_ context.Context, in policyIn) (any, error) {
	var verifier string
	if in.Password != nil {
		if len(*in.Password) < 4 {
			return nil, invalid("use a password of at least 4 characters")
		}
		v, err := makeVerifier(*in.Password)
		if err != nil {
			return nil, err
		}
		verifier = v
	}
	err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil {
			return notFound("no team %s", in.TeamID)
		}
		if in.IOSAccess != nil {
			t.IOSAccess = *in.IOSAccess
		}
		if st.Secrets == nil {
			st.Secrets = map[string]string{}
		}
		switch {
		case in.ClearPassword:
			delete(st.Secrets, t.ID)
			t.PasswordSet = false
		case verifier != "":
			st.Secrets[t.ID] = verifier
			t.PasswordSet = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Any change relocks the group: a revoked or re-passworded team closes phone views now.
	p.revokeTeam(in.TeamID)
	return nil, nil
}

func (p *Pool) revokeTeam(teamID string) {
	p.mu.Lock()
	for _, teams := range p.unlocks {
		delete(teams, teamID)
	}
	r := p.revoker
	p.mu.Unlock()
	if r == nil {
		return
	}
	r(func(c gateway.Caller, sessionID string) bool {
		return c.Kind == protocol.KindIOS && p.iosMayReach(c, sessionID) != nil
	})
}

func makeVerifier(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(pw), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return "scrypt$32768$8$1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func checkVerifier(v, pw string) bool {
	parts := strings.Split(v, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := scrypt.Key([]byte(pw), salt, 1<<15, 8, 1, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

type unlockIn struct {
	TeamID   string `json:"team_id"`
	Password string `json:"password"`
}

func (p *Pool) teamUnlock(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in unlockIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	st := p.st.State()
	t := findTeam(&st.Domain, in.TeamID)
	if t == nil || (c.Kind == protocol.KindIOS && !t.IOSAccess) {
		return nil, notFound("no such group")
	}
	if !t.PasswordSet {
		return map[string]bool{"unlocked": true}, nil
	}
	key := c.ConnectionID + "/" + t.ID
	p.mu.Lock()
	var recent []time.Time
	for _, at := range p.failures[key] {
		if time.Since(at) < failureWindow {
			recent = append(recent, at)
		}
	}
	p.failures[key] = recent
	p.mu.Unlock()
	if len(recent) >= maxFailures {
		return nil, protocol.Errorf(protocol.CodeForbidden, "too many wrong passwords; try again in a few minutes")
	}
	if !checkVerifier(st.Secrets[t.ID], in.Password) {
		p.mu.Lock()
		p.failures[key] = append(p.failures[key], time.Now())
		p.mu.Unlock()
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "wrong group password")
	}
	p.mu.Lock()
	if p.unlocks[c.ConnectionID] == nil {
		p.unlocks[c.ConnectionID] = map[string]bool{}
	}
	p.unlocks[c.ConnectionID][t.ID] = true
	delete(p.failures, key)
	p.mu.Unlock()
	return map[string]bool{"unlocked": true}, nil
}

func (p *Pool) teamLock(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in unlockIn
	_ = json.Unmarshal(raw, &in)
	p.mu.Lock()
	delete(p.unlocks[c.ConnectionID], in.TeamID)
	p.mu.Unlock()
	p.revokeTeam("") // closes nothing for other teams; re-evaluates this connection's attachments
	return nil, nil
}

// ConnectionClosed drops a connection's unlocks.
func (p *Pool) ConnectionClosed(c gateway.Caller) {
	p.mu.Lock()
	delete(p.unlocks, c.ConnectionID)
	for k := range p.failures {
		if strings.HasPrefix(k, c.ConnectionID+"/") {
			delete(p.failures, k)
		}
	}
	p.mu.Unlock()
}

func (p *Pool) unlocked(c gateway.Caller, teamID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unlocks[c.ConnectionID][teamID]
}

// iosMayReach is the backend check for a phone's screen, history and input on one session.
func (p *Pool) iosMayReach(c gateway.Caller, sessionID string) error {
	if c.Kind != protocol.KindIOS {
		return nil
	}
	st := p.st.State()
	i := sessionIndex(&st, sessionID)
	if i < 0 {
		return notFound("no session %s", sessionID)
	}
	teamID := st.Sessions[i].TeamID
	if teamID == "" {
		return nil
	}
	t := findTeam(&st.Domain, teamID)
	if t == nil {
		return nil
	}
	if !t.IOSAccess {
		return notFound("no session %s", sessionID)
	}
	if t.PasswordSet && !p.unlocked(c, t.ID) {
		return protocol.Errorf(protocol.CodeForbidden, "unlock group %s first", t.Name)
	}
	return nil
}

// Authorize is the gateway's attach check.
func (p *Pool) Authorize(c gateway.Caller, sessionID string) error {
	return p.iosMayReach(c, sessionID)
}

// Filter narrows state for a phone: disallowed teams vanish; locked teams keep only the metadata
// needed to pick them. The desktop owner sees everything.
func (p *Pool) Filter(c gateway.Caller, st store.State) store.State {
	if c.Kind != protocol.KindIOS {
		return st
	}
	hidden, locked := map[string]bool{}, map[string]bool{}
	var teams []protocol.Team
	for _, t := range st.Domain.Teams {
		if t.State != "active" {
			continue
		}
		if !t.IOSAccess {
			hidden[t.ID] = true
			continue
		}
		if t.PasswordSet && !p.unlocked(c, t.ID) {
			locked[t.ID] = true
			t.Goal = ""
			for i := range t.Members {
				t.Members[i].Assignment, t.Members[i].Worktree, t.Members[i].Branch = "", "", ""
			}
		}
		teams = append(teams, t)
	}
	var sessions []store.Session
	for _, s := range st.Sessions {
		if hidden[s.TeamID] {
			continue
		}
		if locked[s.TeamID] {
			s = store.Session{ID: s.ID, Name: s.Name, NodeID: s.NodeID, State: s.State, TeamID: s.TeamID, Role: s.Role, Cols: s.Cols, Rows: s.Rows}
		}
		s.Env = nil
		sessions = append(sessions, s)
	}
	var msgs []protocol.TeamMessage
	for _, m := range st.Domain.Messages {
		if !hidden[m.TeamID] && !locked[m.TeamID] {
			msgs = append(msgs, m)
		}
	}
	var approvals []protocol.Approval
	for _, a := range st.Domain.Approvals {
		if a.Kind == "permission" && p.iosMayReach(c, a.SessionID) == nil {
			approvals = append(approvals, a)
		}
	}
	out := st
	out.Sessions = sessions
	out.Secrets = nil
	out.Domain = protocol.Domain{Teams: teams, Messages: msgs, Approvals: approvals, Skills: st.Domain.Skills}
	return out
}
