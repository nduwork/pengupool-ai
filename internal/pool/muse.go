package pool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Muse app API's pool side: paired devices, one-card-at-a-time asks,
// answers through the same paths as the app's buttons, and chat through
// the owner's input path (so the Compass learns). Thin wrappers over the
// existing RPCs; the allowed actions are a fixed list (museAllowed).

// museAllowed are the pool requests a paired Muse device may call. The
// gateway only routes them with a valid device token; the owner-only
// pairing calls below stay owner-only.
var museAllowed = map[string]bool{
	"muse.next": true, "muse.answer": true, "muse.say": true,
	"muse.sessions": true, "muse.compass": true, "muse.action": true, "muse.member": true,
}

// musePairCode is one pending pairing window.
type musePairCode struct {
	code    string
	expires time.Time
}

// museAnswerDone is one answered tap: the replay cache. The key is the
// device plus its idempotency key, and the ask/action pin a replay to the
// same tap: another ask reusing the key is a conflict, not a free answer.
type museAnswerDone struct {
	res    any
	ask    string
	action string
	at     time.Time
}

// museCard is one waiting session.
type museCard struct {
	Session     string       `json:"session"`
	Team        string       `json:"team"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	AskID       string       `json:"ask_id"`
	PayloadHash string       `json:"payload_hash"`
	Actions     []museAction `json:"actions"`
}

type museAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type museAsk struct {
	id        string // "approval:<uid>" | "gate:<n>"
	kind      string // launch | formation | checkin
	sessionID string
	teamID    string
	title     string
	text      string
	at        time.Time
	payload   string   // canonical content the hash covers
	options   []string // an agent's ask: its choices
}

// askID joins the ask's stable identity.
func askID(kind, id string) string { return kind + ":" + id }

// payloadHash is the ask's content fingerprint: a tap answers what it saw.
func payloadHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// museAsks lists every waiting ask: pending launch/formation approvals and
// open check-ins, oldest first. Permission prompts stay terminal-only,
// like the app.
func (p *Pool) museAsks() []museAsk {
	var out []museAsk
	st := p.st.State()
	for i := range st.Domain.Approvals {
		a := &st.Domain.Approvals[i]
		if a.State != "pending" {
			continue
		}
		if a.Kind == "ask" {
			if s, ok := p.session(a.SessionID); ok && s.State == "running" && !askStale(*a, time.Now()) {
				out = append(out, museAsk{
					id: askID("ask", a.ID), kind: "ask", sessionID: s.ID, teamID: a.TeamID, text: a.Detail,
					at: parseMuseTime(a.CreatedAt), payload: askPayload(*a), options: a.Options,
				})
			}
			continue
		}
		if a.Kind != "launch" && a.Kind != "formation" {
			continue
		}
		sid := a.SessionID
		if sid == "" {
			sid = p.museLeadSession(a.TeamID)
		}
		s, ok := p.session(sid)
		if !ok || s.State != "running" {
			continue
		}
		text := a.Detail
		if a.Kind == "launch" {
			text = fmt.Sprintf("Launch proposal v%d: %s", a.Version, a.Detail)
		}
		out = append(out, museAsk{
			id: askID("approval", a.ID), kind: a.Kind, sessionID: s.ID, teamID: a.TeamID,
			text: text, at: parseMuseTime(a.CreatedAt), payload: a.Kind + "\x00" + a.ID + "\x00" + text,
		})
	}
	if p.db != nil {
		if gs, err := p.db.OpenGates(); err == nil {
			for _, g := range gs {
				if g.Kind != "checkin" || g.Outcome != "pending" {
					continue
				}
				s, ok := p.session(g.SessionID)
				if !ok || s.State != "running" {
					continue
				}
				id := fmt.Sprintf("%d", g.ID)
				out = append(out, museAsk{
					id: askID("gate", id), kind: "checkin", sessionID: s.ID, teamID: g.TeamID,
					text: g.Question, at: parseMuseTime(g.CreatedAt), payload: "checkin\x00" + id + "\x00" + g.Question,
				})
			}
		}
	}
	out = append(out, p.museTeamAsks()...)
	// Oldest first: insertion order and waiting order diverge after a
	// restart replays the store, and the app shows the same order.
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// museLeadSession is the running session an approval without one belongs
// to: the team's LEAD, else its first running member.
func (p *Pool) museLeadSession(teamID string) string {
	st := p.st.State()
	for _, t := range st.Domain.Teams {
		if t.ID != teamID || t.State != "active" {
			continue
		}
		if id := leadOf(&t); id != "" {
			if s, ok := p.session(id); ok && s.State == "running" {
				return id
			}
		}
		for _, m := range t.Members {
			if s, ok := p.session(m.SessionID); ok && s.State == "running" {
				return m.SessionID
			}
		}
	}
	return ""
}

func parseMuseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now().UTC()
}

// museVisible reports whether the device may see the session: only teams
// with iOS access (the owner's existing switch), or sessions on no team.
// A device token is the owner's own paired device, so it skips the per-team
// group password.
func (p *Pool) museVisible(sessionID string) bool {
	st := p.st.State()
	for _, t := range st.Domain.Teams {
		for _, m := range t.Members {
			if m.SessionID == sessionID {
				return t.IOSAccess
			}
		}
	}
	return true
}

// museTeamVisible reports whether a device may act on an ask of this team: only a team with iOS
// access. Checked on the ask's own team, so an ask /next never listed (a team not yet active) is no
// way around it. An ask on no team is the owner's own.
func (p *Pool) museTeamVisible(teamID string) bool {
	if teamID == "" {
		return true
	}
	for _, t := range p.st.State().Domain.Teams {
		if t.ID == teamID {
			return t.IOSAccess
		}
	}
	return false
}

// museCardFor builds the card for one ask.
func (p *Pool) museCard(a museAsk) *museCard {
	s, ok := p.session(a.sessionID)
	if !ok {
		return nil
	}
	st := p.st.State()
	team := ""
	for _, t := range st.Domain.Teams {
		if t.ID == a.teamID {
			team = t.Name
		}
	}
	title := s.Name
	if team != "" {
		title += " · " + team
	} else {
		title += " · " + s.Harness
	}
	desc := clipRunes(oneLine(redactSecrets(a.text)), 500)
	actions := []museAction{{ID: "allow", Label: "Allow"}, {ID: "deny", Label: "Deny"}, {ID: "chat", Label: "Chat"}}
	if a.kind == "ask" { // the agent's own choices, then a reply in the owner's words
		actions = nil
		for i, o := range a.options {
			actions = append(actions, museAction{ID: fmt.Sprintf("option:%d", i), Label: o})
		}
		actions = append(actions, museAction{ID: "reply", Label: "Reply"})
	}
	if a.kind == "team-ask" { // a member's ask the team left to the owner, as the app's Needs you offers it
		actions = []museAction{{ID: "go_on", Label: "Go on"}, {ID: "reply", Label: "Reply"}, {ID: "dismiss", Label: "Dismiss"}}
	}
	return &museCard{
		Session: s.ID, Team: a.teamID, Title: title, Description: desc,
		AskID: a.id, PayloadHash: payloadHash(a.payload), Actions: actions,
	}
}

func (p *Pool) museNext(_ context.Context, c gateway.Caller, _ json.RawMessage) (any, error) {
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	asks := p.museAsks()
	for _, a := range asks {
		if !p.museVisible(a.sessionID) {
			continue
		}
		if card := p.museCard(a); card != nil {
			p.logf("owner via %s: next -> %s", c.Device, card.AskID)
			return card, nil
		}
	}
	return nil, nil
}

type museAnswerIn struct {
	AskID          string `json:"ask_id"`
	Action         string `json:"action"` // allow | deny
	PayloadHash    string `json:"payload_hash"`
	IdempotencyKey string `json:"idempotency_key"`
	Text           string `json:"text,omitempty"` // an agent's ask: the owner's own words (action reply)
}

func (p *Pool) museAnswer(ctx context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in museAnswerIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	if in.Action != "allow" && in.Action != "deny" && !strings.HasPrefix(in.AskID, "ask:") && !strings.HasPrefix(in.AskID, "teamask:") {
		return nil, invalid("action is allow or deny")
	}
	if in.IdempotencyKey == "" {
		return nil, invalid("an idempotency key is required")
	}
	// The key is the device plus its key: another device, or another tap,
	// reusing it is a conflict, not a free copy of the first answer.
	key := c.Device + "\x00" + in.IdempotencyKey
	// Check and claim the key in one step: a retry that arrives while the
	// first tap is still launching a team is told to retry (503), and never
	// answers a second time; once the first finishes, a retry replays it.
	p.museMu.Lock()
	if done, ok := p.museAnswered[key]; ok {
		p.museMu.Unlock()
		if done.ask != in.AskID || done.action != in.Action {
			return nil, conflict("the key was already used for another tap")
		}
		return done.res, nil
	}
	if p.museInflight[key] {
		p.museMu.Unlock()
		return nil, protocol.Errorf(protocol.CodeUnavailable, "the ask is already being answered")
	}
	p.museInflight[key] = true
	p.museMu.Unlock()
	defer func() {
		p.museMu.Lock()
		delete(p.museInflight, key)
		p.museMu.Unlock()
	}()
	kind, id, _ := strings.Cut(in.AskID, ":")
	var res any
	var err error
	switch kind {
	case "approval":
		res, err = p.museAnswerApproval(ctx, c, id, in)
	case "gate":
		res, err = p.museAnswerGate(id, in)
	case "ask":
		res, err = p.museAnswerAsk(c, id, in)
	case "teamask":
		res, err = p.museAnswerTeamAsk(ctx, id, in)
	default:
		return nil, notFound("no ask %s", in.AskID)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"answered": in.AskID, "action": in.Action, "result": res}
	p.museMu.Lock()
	p.sweepMuseAnswered()
	p.museAnswered[key] = museAnswerDone{res: out, ask: in.AskID, action: in.Action, at: time.Now()}
	p.museMu.Unlock()
	return out, nil
}

// sweepMuseAnswered drops replay entries older than a day, then the oldest
// past a thousand: the map is a replay cache, not memory.
func (p *Pool) sweepMuseAnswered() {
	cutoff := time.Now().Add(-24 * time.Hour)
	for k, done := range p.museAnswered {
		if done.at.Before(cutoff) {
			delete(p.museAnswered, k)
		}
	}
	for len(p.museAnswered) > 1000 {
		oldest := ""
		var oldestAt time.Time
		first := true
		for k, done := range p.museAnswered {
			if first || done.at.Before(oldestAt) {
				oldest, oldestAt, first = k, done.at, false
			}
		}
		if oldest == "" {
			break
		}
		delete(p.museAnswered, oldest)
	}
}

// museAnswerApproval answers a launch/formation approval the way the app's
// buttons do: the owner chose (2026-10-27) that their paired Muse device may
// approve these, unlike the iPhone web path, which stays desktop-only. A
// changed hash or version, or an already settled approval, is a 409.
func (p *Pool) museAnswerApproval(ctx context.Context, c gateway.Caller, id string, in museAnswerIn) (any, error) {
	st := p.st.State()
	var a *protocol.Approval
	for i := range st.Domain.Approvals {
		if st.Domain.Approvals[i].ID == id {
			a = &st.Domain.Approvals[i]
			break
		}
	}
	if a == nil || a.State != "pending" {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	if !p.museTeamVisible(a.TeamID) {
		return nil, notFound("no ask approval:%s", id)
	}
	text := a.Detail
	if a.Kind == "launch" {
		text = fmt.Sprintf("Launch proposal v%d: %s", a.Version, a.Detail)
	}
	if payloadHash(a.Kind+"\x00"+a.ID+"\x00"+text) != in.PayloadHash {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	decision := "deny"
	if in.Action == "allow" {
		decision = "approve"
	}
	raw, _ := json.Marshal(decideIn{ID: a.ID, Decision: decision, Version: a.Version})
	res, err := p.approvalDecide(ctx, c, raw)
	p.logf("owner via %s: answer %s %s", c.Device, a.ID, decision)
	return res, err
}

// askPayload is what an agent's ask's hash covers: its question and its choices.
func askPayload(a protocol.Approval) string {
	return "ask\x00" + a.ID + "\x00" + a.Detail + "\x00" + strings.Join(a.Options, "\x00")
}

// museAnswerAsk answers an agent's ask with one of its options (option:N) or the owner's own words
// (reply, with text), the way the app does.
func (p *Pool) museAnswerAsk(c gateway.Caller, id string, in museAnswerIn) (any, error) {
	a, ok := p.approval(id)
	if !ok || a.Kind != "ask" || !p.museTeamVisible(a.TeamID) || !p.museVisible(a.SessionID) {
		return nil, notFound("no ask ask:%s", id) // one the device cannot see does not exist for it
	}
	if a.State == "expired" || askStale(a, time.Now()) {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask expired") // its card already left /next
	}
	if a.State != "pending" {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	if payloadHash(askPayload(a)) != in.PayloadHash {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	answer := ""
	switch n, isOption := strings.CutPrefix(in.Action, "option:"); {
	case isOption:
		i, err := strconv.Atoi(n)
		if err != nil || i < 0 || i >= len(a.Options) {
			return nil, invalid("action is option:N for one of the ask's options, or reply")
		}
		answer = a.Options[i]
	case in.Action == "reply":
		if answer = strings.TrimSpace(in.Text); answer == "" {
			return nil, invalid("a reply needs its text")
		}
	default:
		return nil, invalid("action is option:N for one of the ask's options, or reply")
	}
	res, err := p.answerAsk(id, answer, c.Device)
	p.logf("owner via %s: answered ask %s", c.Device, id)
	return res, err
}

// museAnswerGate answers a check-in with the app's own Go on / Stop words.
func (p *Pool) museAnswerGate(id string, in museAnswerIn) (any, error) {
	if p.db == nil {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	gid, ok := parseGateID(id)
	if !ok {
		return nil, notFound("no ask gate:%s", id)
	}
	g, err := p.db.Gate(gid)
	if err != nil || g.Kind != "checkin" || g.Outcome != "pending" {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	if !p.museTeamVisible(g.TeamID) {
		return nil, notFound("no ask gate:%s", id)
	}
	if payloadHash("checkin\x00"+id+"\x00"+g.Question) != in.PayloadHash {
		return nil, protocol.Errorf(protocol.CodeConflict, "the ask changed or expired")
	}
	text := "Go on."
	if in.Action == "deny" {
		text = "Stop. Do not continue; wait for my next instruction."
	}
	res, err := p.checkinAnswer(context.Background(), answerIn{GateID: gid, Text: text})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func parseGateID(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ---- saying: the owner's words to one session -------------------------------

type museSayIn struct {
	Session string `json:"session"`
	Words   string `json:"words"`
}

// museSay sends the owner's words to a session the device can see, through
// the owner-input path: credited like the app's own typing, so the
// Compass can learn from them.
func (p *Pool) museSay(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in museSayIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	s, ok := p.session(in.Session)
	if !ok || s.State != "running" {
		return nil, notFound("no session %s", in.Session)
	}
	if _, ok := harness.Find(s.Harness); !ok {
		return nil, invalid("%s is not an agent session", s.Name)
	}
	if !p.museVisible(s.ID) {
		return nil, notFound("no session %s", in.Session)
	}
	line := strings.Join(strings.Fields(in.Words), " ")
	if line == "" {
		return nil, invalid("no words to send")
	}
	p.OwnerInput(s.ID, line)
	if err := p.typeInto(s.ID, line+"\r"); err != nil {
		p.withdrawOwnerInput(s.ID, line)
		return nil, protocol.Errorf(protocol.CodeUnavailable, "%v", err)
	}
	p.logf("owner via %s: say to %s", c.Device, s.Name)
	return map[string]string{"sent": s.ID}, nil
}

// ---- overview and compass ---------------------------------------------------

type museSessionView struct {
	Session string `json:"session"`
	Title   string `json:"title"` // the name, then each team it is on: "web · app"
	Name    string `json:"name"`
	State   string `json:"state"`
	// Status is what a running agent is doing: starting, working, idle, or approval (its harness
	// asks in the terminal). Empty for a session that is not running.
	Status  string `json:"status,omitempty"`
	Waiting bool   `json:"waiting"`
	// Question is what the agent asks in its terminal now, if anything (answered there, not here).
	Question string `json:"question,omitempty"`
	// Its place on a team, if on one: the team, LEAD | SME | HELPER, the member it hangs under, its job.
	Team       string `json:"team,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
	Role       string `json:"role,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Assignment string `json:"assignment,omitempty"`
}

func (p *Pool) museSessions(_ context.Context, c gateway.Caller, _ json.RawMessage) (any, error) {
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	waiting := map[string]bool{}
	for _, a := range p.museAsks() {
		waiting[a.sessionID] = true
	}
	st := p.st.State()
	var out []museSessionView
	for _, s := range st.Sessions {
		if s.Harness != "" && !harness.IsAgent(s.Harness) {
			continue
		}
		if !p.museVisible(s.ID) {
			continue
		}
		v := museSessionView{Session: s.ID, Title: s.Name, State: string(s.State), Name: s.Name, Status: running(s.Status, string(s.State)), Waiting: waiting[s.ID], Question: s.View().Question}
		for i := range st.Domain.Teams {
			t := &st.Domain.Teams[i]
			for _, m := range t.Members {
				if m.SessionID == s.ID && t.Name != "" {
					v.Title += " · " + t.Name // as before: every team it was on
				}
				if m.SessionID == s.ID && m.State == "active" && t.State == "active" {
					v.Team, v.TeamID, v.Role, v.Parent, v.Assignment = t.Name, t.ID, teamLabel(&st, t, m), m.Parent, m.Assignment
				}
			}
		}
		out = append(out, v)
	}
	if out == nil {
		out = []museSessionView{}
	}
	return out, nil
}

func (p *Pool) museCompass(_ context.Context, c gateway.Caller, _ json.RawMessage) (any, error) {
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	out := []string{}
	for _, v := range p.lines("", 200) {
		if v.shown() {
			out = append(out, v.text())
		}
	}
	return out, nil
}

// ---- pairing and devices ----------------------------------------------------

// musePairCodeOut is what the app shows the owner: a code and the URL to type.
type musePairCodeOut struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

// musePairCode makes a one-time 6-digit pairing code (5 min, one use).
func (p *Pool) musePairCode(_ context.Context, c gateway.Caller, _ json.RawMessage) (any, error) {
	if c.Device != "" {
		return nil, protocol.Errorf(protocol.CodeForbidden, "devices cannot pair")
	}
	var digits [6]byte
	if _, err := rand.Read(digits[:]); err != nil {
		return nil, err
	}
	for i := range digits {
		digits[i] = byte('0' + digits[i]%10)
	}
	code := string(digits[:])
	p.museMu.Lock()
	if p.museCodes == nil {
		p.museCodes = map[string]*musePairCode{}
	}
	p.museCodes[code] = &musePairCode{code: code, expires: time.Now().Add(5 * time.Minute)}
	p.museMu.Unlock()
	p.logf("muse pairing code made")
	return musePairCodeOut{Code: code, URL: p.museBaseURL() + "/api/muse/v1"}, nil
}

// museBaseURL is the tailnet URL the Muse app reaches: the Serve name over HTTPS when Serve is set
// up, else this machine's own tailnet name over plain HTTP when the listener is a wildcard bind
// (Local network on). The name is the one the host check holds, so the URL always passes it.
func (p *Pool) museBaseURL() string {
	name := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(p.st.Config().Network.Tailscale, "https://"), "http://"), "/")
	if name != "" {
		return "https://" + name
	}
	if HTTPAddr != nil {
		host, port, err := net.SplitHostPort(HTTPAddr())
		if ip := net.ParseIP(host); err == nil && ip != nil && ip.IsUnspecified() {
			if ts := TailnetName(); ts != "" {
				return "http://" + net.JoinHostPort(ts, port)
			}
		}
	}
	return "https://<tailnet-name>"
}

type musePairIn struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

// musePair exchanges a one-time code for a device token, shown once.
func (p *Pool) musePair(_ context.Context, _ gateway.Caller, raw json.RawMessage) (any, error) {
	var in musePairIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	name := strings.TrimSpace(in.DeviceName)
	if name == "" || len([]rune(name)) > 40 {
		return nil, invalid("a device name is required")
	}
	p.museMu.Lock()
	// Wrong guesses throttle the code window, not the guess: a miss never
	// names a live code, so counting per code would count nothing.
	if time.Now().Before(p.musePairBlocked) {
		p.museMu.Unlock()
		return nil, protocol.Errorf(protocol.CodeUnavailable, "too many wrong codes, try again later")
	}
	code := strings.TrimSpace(in.Code)
	pc, ok := p.museCodes[code]
	if ok && time.Now().After(pc.expires) {
		delete(p.museCodes, code)
		ok = false
	}
	if !ok {
		p.musePairFails++
		if p.musePairFails >= 10 {
			p.musePairBlocked = time.Now().Add(time.Minute)
			p.musePairFails = 0
		}
		p.museMu.Unlock()
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "bad or expired code")
	}
	delete(p.museCodes, code)
	p.musePairFails = 0
	p.museMu.Unlock()
	var tok [32]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tok[:])
	sum := sha256.Sum256([]byte(token))
	err := p.st.UpdateConfig(func(cfg *store.Config) error {
		for _, d := range cfg.MuseDevices {
			// One name, one token (the code is burnt already: a code is one
			// pairing attempt, whatever it says).
			if d.Name == name {
				return conflict("a device named %s is already paired", name)
			}
		}
		cfg.MuseDevices = append(cfg.MuseDevices, store.MuseDevice{
			Name: name, TokenHash: hex.EncodeToString(sum[:]), CreatedAt: now(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	p.logf("muse device paired: %s", name)
	return map[string]string{"token": token}, nil
}

type museRevokeIn struct {
	Name string `json:"name"`
}

// museRevoke drops a device; it stops working on the next request.
func (p *Pool) museRevoke(_ context.Context, _ gateway.Caller, raw json.RawMessage) (any, error) {
	var in museRevokeIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	err := p.st.UpdateConfig(func(cfg *store.Config) error {
		i := slices.IndexFunc(cfg.MuseDevices, func(d store.MuseDevice) bool { return d.Name == in.Name })
		if i < 0 {
			return notFound("no device %q", in.Name)
		}
		cfg.MuseDevices = slices.Delete(cfg.MuseDevices, i, i+1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	p.logf("muse device revoked: %s", in.Name)
	return map[string]string{"revoked": in.Name}, nil
}

type museDeviceView struct {
	Name     string `json:"name"`
	Created  string `json:"created_at"`
	LastSeen string `json:"last_seen,omitempty"`
}

// museDevices lists paired devices for the owner's Settings screen.
func (p *Pool) museDevices(_ context.Context, _ gateway.Caller, _ json.RawMessage) (any, error) {
	cfg := p.st.Config()
	out := []museDeviceView{}
	for _, d := range cfg.MuseDevices {
		out = append(out, museDeviceView{Name: d.Name, Created: d.CreatedAt, LastSeen: d.LastSeen})
	}
	return out, nil
}

// running is the status of a running session, and nothing for one that is not.
func running(status, state string) string {
	if state != "running" {
		return ""
	}
	return status
}
