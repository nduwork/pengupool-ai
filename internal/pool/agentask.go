package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// An agent's ask: a question the agent puts to the owner itself (`pengupool ask`), with optional
// choices. It is an approval of kind ask, so it reaches the owner where the other asks do (the app's
// Approvals, the Muse app's /next card) and is answered once, by the owner or a paired device, never
// by an agent. The agent waits for the answer and gets it as data; one answered while it was not
// waiting reaches it as a notice instead. One the owner leaves unanswered for askExpiry expires: the
// agent is told, from its wait or as a notice, and goes on with a safe default of its own.

const (
	askExpiry     = 15 * time.Minute // the owner's time to answer; change it here
	askMaxOptions = 8
	askMaxPending = 3                // per session: an agent asking in a loop fills no list
	askMaxWait    = 60 * time.Second // one ask.wait call; the CLI calls again
)

type agentAskIn struct {
	SessionID string   `json:"session_id"`
	Question  string   `json:"question"`
	Options   []string `json:"options"`
	OwnerOnly bool     `json:"owner_only,omitempty"` // for the owner alone: never decided from the Compass
}

// agentAsk records the calling session's question as a pending ask.
func (p *Pool) agentAsk(_ context.Context, in agentAskIn) (any, error) {
	s, ok := p.session(in.SessionID)
	if !ok || s.State != "running" || !harness.IsAgent(s.Harness) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only an agent session can ask the owner")
	}
	q := strings.TrimSpace(in.Question)
	if q == "" {
		return nil, invalid("no question to ask")
	}
	if len(in.Options) > askMaxOptions {
		return nil, invalid("at most %d options", askMaxOptions)
	}
	var opts []string
	for _, o := range in.Options {
		if o = clipRunes(redact(oneLine(strings.TrimSpace(o))), 80); o != "" && !slices.Contains(opts, o) {
			opts = append(opts, o)
		}
	}
	a := protocol.Approval{ID: protocol.NewID(), Kind: "ask", TeamID: s.TeamID, SessionID: s.ID,
		Detail: clipRunes(redact(q), 2000), Options: opts, OwnerOnly: in.OwnerOnly, State: "pending", CreatedAt: now()}
	cut := time.Now().Add(-24 * time.Hour)
	if err := p.update(func(st *store.State) error {
		// An ask whose session is not running expires: its waiter went with it (a session restored
		// after a crash or update asks again). Settled asks stay a day, for the app's record.
		keep, mine := st.Domain.Approvals[:0], 0
		for _, x := range st.Domain.Approvals {
			if x.Kind == "ask" && x.State == "pending" {
				if i := sessionIndex(st, x.SessionID); i < 0 || st.Sessions[i].State != "running" {
					x.State = "expired"
				} else if x.SessionID == s.ID && !askStale(x, time.Now()) {
					mine++
				}
			}
			if t, _ := time.Parse(time.RFC3339, x.CreatedAt); x.Kind != "ask" || x.State == "pending" || t.After(cut) {
				keep = append(keep, x)
			}
		}
		if mine >= askMaxPending {
			return conflict("%d of your asks already wait on the owner; wait for those (pengupool ask --wait ID)", mine)
		}
		st.Domain.Approvals = append(keep, a)
		return nil
	}); err != nil {
		return nil, err
	}
	p.logLine(s.ID, "ask: "+clipRunes(oneLine(a.Detail), 200))
	p.setQuestion(s.ID, clipRunes(oneLine(a.Detail), 300), "") // shown with the session while it waits
	return map[string]string{"id": a.ID}, nil
}

type askWaitIn struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
	Wait      int    `json:"wait"` // seconds, at most 60
}

// askWait waits for the answer to one of the session's own asks: {answer, by}, or {pending: id}
// when the wait runs out first.
func (p *Pool) askWait(ctx context.Context, in askWaitIn) (any, error) {
	wait := min(max(time.Duration(in.Wait)*time.Second, 0), askMaxWait)
	deadline := time.Now().Add(wait)
	for {
		a, ok := p.approval(in.ID)
		if !ok || a.Kind != "ask" || a.SessionID != in.SessionID {
			return nil, notFound("no ask %s of this session", in.ID)
		}
		if a.State == "pending" { // a settled ask's stamp is gone: stamping it again would keep it
			p.askWaited(in.ID)
		}
		if askStale(a, time.Now()) {
			p.expireStaleAsks(time.Now()) // stamped as waited on: the expiry comes here, not as a notice too
			continue
		}
		switch a.State {
		case "answered":
			p.dropAnswersLater(a.SessionID, a.ID) // it has the answer now: the words do not come twice
			return map[string]string{"id": a.ID, "answer": a.Answer, "by": a.AnsweredBy}, nil
		case "expired":
			if s, ok := p.session(a.SessionID); ok && s.State == "running" && olderThan(a.CreatedAt, time.Now(), askExpiry) {
				return map[string]string{"id": a.ID, "expired": askExpiredNote(a)}, nil
			}
			return nil, conflict("the ask %s went with its session", a.ID)
		case "pending":
		default:
			return nil, conflict("the ask %s was %s", a.ID, a.State)
		}
		if time.Until(deadline) <= 0 {
			if s, ok := p.session(a.SessionID); !ok || s.State != "running" {
				return nil, conflict("the ask %s went with its session", a.ID)
			}
			return map[string]string{"pending": a.ID}, nil
		}
		// ponytail: polls the state twice a second; a channel per ask if many agents ever wait at once.
		select {
		case <-ctx.Done(): // the CLI went: an answer from now on comes as a notice
			p.mu.Lock()
			delete(p.askWaitedAt, in.ID)
			p.mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type askAnswerIn struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
}

// askAnswer is the owner's answer in the app: one of the ask's options or their own words.
func (p *Pool) askAnswer(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in askAnswerIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	res, err := p.answerAsk(in.ID, in.Answer, "owner")
	if a, _ := p.approval(in.ID); err != nil && a.State == "expired" {
		return map[string]string{"id": a.ID, "expired": "true"}, nil // expected: its row just went, no error
	}
	return res, err
}

// answerAsk records the answer once: a second answer, or one to an ask that went, is a conflict.
// The session gets it from its wait, or as a notice when nothing waits.
func (p *Pool) answerAsk(id, answer, by string) (any, error) {
	return p.answerAskWith(id, answer, by, "", "")
}

// answerAskWith is answerAsk recording the owner's words that carried the answer (askAnswerFor).
// prompt is those words whole: a member no longer waiting gets them typed in as its next prompt,
// credited to the owner, so its harness sees the owner's own prompt; a waiting member gets the answer
// from its wait.
func (p *Pool) answerAskWith(id, answer, by, words, prompt string) (any, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil, invalid("no answer")
	}
	var a protocol.Approval
	gone, stale := false, false
	err := p.update(func(st *store.State) error {
		for i := range st.Domain.Approvals {
			if x := &st.Domain.Approvals[i]; x.ID == id && x.Kind == "ask" {
				if x.State != "pending" {
					return conflict("the ask changed or expired")
				}
				if stale = askStale(*x, time.Now()); stale {
					return conflict("the ask expired")
				}
				if j := sessionIndex(st, x.SessionID); j < 0 || st.Sessions[j].State != "running" {
					gone = true // answered by nobody: it goes, and the update below is dropped
					return conflict("the ask changed or expired")
				}
				x.State, x.Answer, x.AnsweredBy, x.OwnerWords = "answered", clipRunes(answer, 4000), by, words
				a = *x
				return nil
			}
		}
		return notFound("no ask %s", id)
	})
	if gone {
		p.expireAsks("")
	}
	if stale {
		p.expireStaleAsks(time.Now())
	}
	if err != nil {
		return nil, err
	}
	p.logLine(a.SessionID, "ask answered by "+by+": "+clipRunes(oneLine(a.Answer), 200))
	p.mu.Lock()
	waiting := time.Since(p.askWaitedAt[a.ID]) < askWaitGrace
	delete(p.askWaitedAt, a.ID)
	p.mu.Unlock()
	if waiting {
		return map[string]string{"id": a.ID, "answer": a.Answer}, nil
	}
	note := "The owner answered your ask " + a.ID + " (\"" + clipRunes(oneLine(a.Detail), 120) + "\"): " + a.Answer
	s, _ := p.session(a.SessionID)
	if _, agent := harness.Find(s.Harness); prompt == "" || !agent {
		p.notice(a.TeamID, a.SessionID, note) // an answer in the app, or a member that cannot be typed into
		return map[string]string{"id": a.ID, "answer": a.Answer}, nil
	}
	later := laterAnswer{ask: a.ID, teamID: a.TeamID, text: "Answer to your ask " + a.ID + ": " + prompt, note: note, at: time.Now()}
	if s.Status != "idle" {
		// A notice would reach the member through its inbox, which its harness does not read as the
		// owner's prompt: the owner's words wait for its next Stop instead (typeAnswersLater).
		p.mu.Lock()
		if p.answersLater == nil {
			p.answersLater = map[string][]laterAnswer{}
		}
		p.answersLater[a.SessionID] = append(p.answersLater[a.SessionID], later)
		p.mu.Unlock()
		return map[string]string{"id": a.ID, "answer": a.Answer, "typed": "at its next stop"}, nil
	}
	if !p.typeAnswer(a.SessionID, later) {
		return map[string]string{"id": a.ID, "answer": a.Answer}, nil
	}
	return map[string]string{"id": a.ID, "answer": a.Answer, "typed": "true"}, nil
}

// laterAnswer is an owner's answer, given through an ancestor, waiting to be typed into a member.
type laterAnswer struct {
	ask, teamID, text, note string
	at                      time.Time // when it was queued
}

// typeAnswer types the owner's words into the member as a delivered ask answer; a failed typing
// leaves the notice instead.
func (p *Pool) typeAnswer(id string, x laterAnswer) bool {
	if err := p.typeOwnerPrompt(id, x.text, "ask"); err != nil {
		p.notice(x.teamID, id, x.note)
		return false
	}
	return true
}

// typeAnswersLater types the answers that waited for the member to go idle (its Stop), each once.
// ponytail: in memory: a host restart drops a waiting answer; the ask's record keeps it, and the
// member gets it from `pengupool ask --wait ID`.
func (p *Pool) typeAnswersLater(id string) {
	p.mu.Lock()
	q := p.answersLater[id]
	delete(p.answersLater, id)
	p.mu.Unlock()
	for _, x := range q {
		// Only for this session's answered ask: a session ended and resumed meanwhile has had its asks
		// expired (expireAsks drops what waited). Long after (busy past askExpiry), the owner's words
		// are no longer typed as a fresh prompt; the member still hears the answer, as a notice.
		a, ok := p.approval(x.ask)
		if !ok || a.State != "answered" || a.SessionID != id {
			continue
		}
		if time.Since(x.at) >= askExpiry {
			p.notice(x.teamID, id, x.note)
			continue
		}
		p.typeAnswer(id, x)
	}
}

// dropAnswersLater drops what waits to be typed: one session's (a "" ask: all of it), or one ask's.
func (p *Pool) dropAnswersLater(sessionID, ask string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ask == "" {
		delete(p.answersLater, sessionID)
		return
	}
	q := p.answersLater[sessionID]
	q = slices.DeleteFunc(q, func(x laterAnswer) bool { return x.ask == ask })
	if len(q) == 0 {
		delete(p.answersLater, sessionID)
	} else {
		p.answersLater[sessionID] = q
	}
}

// expireAsks ends the pending asks of one session, or ("") of every session that is not running.
func (p *Pool) expireAsks(sessionID string) {
	// What waited to be typed into the session goes too: it ended, and a resumed one starts afresh.
	if sessionID != "" {
		p.dropAnswersLater(sessionID, "")
	} else {
		p.mu.Lock()
		for id := range p.answersLater {
			if s, ok := p.session(id); !ok || s.State != "running" {
				delete(p.answersLater, id)
			}
		}
		p.mu.Unlock()
	}
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Approvals {
			x := &st.Domain.Approvals[i]
			if x.Kind != "ask" || x.State != "pending" {
				continue
			}
			j := sessionIndex(st, x.SessionID)
			if x.SessionID == sessionID || sessionID == "" && (j < 0 || st.Sessions[j].State != "running") {
				x.State = "expired"
			}
		}
		return nil
	})
}

// askStale says whether a is an agent's ask the owner left unanswered past askExpiry.
func askStale(a protocol.Approval, at time.Time) bool {
	return a.Kind == "ask" && a.State == "pending" && olderThan(a.CreatedAt, at, askExpiry)
}

func askExpiredNote(a protocol.Approval) string {
	return fmt.Sprintf("Your ask %s (\"%s\") expired: the owner did not answer within %d minutes. Go on with a safe default and say what you assumed.",
		a.ID, clipRunes(oneLine(a.Detail), 120), int(askExpiry.Minutes()))
}

// expireStaleAsks expires the asks left unanswered past askExpiry. An agent waiting on one hears it
// from its wait; any other is told with a notice.
func (p *Pool) expireStaleAsks(at time.Time) {
	if !slices.ContainsFunc(p.st.State().Domain.Approvals, func(a protocol.Approval) bool { return askStale(a, at) }) {
		return
	}
	var stale []protocol.Approval
	_ = p.update(func(st *store.State) error {
		stale = stale[:0]
		for i := range st.Domain.Approvals {
			if x := &st.Domain.Approvals[i]; askStale(*x, at) {
				x.State = "expired"
				stale = append(stale, *x)
			}
		}
		return nil
	})
	for _, a := range stale {
		p.logLine(a.SessionID, "ask expired unanswered: "+clipRunes(oneLine(a.Detail), 200))
		p.mu.Lock()
		waiting := time.Since(p.askWaitedAt[a.ID]) < askWaitGrace
		delete(p.askWaitedAt, a.ID)
		p.mu.Unlock()
		if s, ok := p.session(a.SessionID); ok && s.State == "running" && !waiting {
			p.notice(a.TeamID, a.SessionID, askExpiredNote(a))
		}
	}
}

// askWaitGrace: an ask waited on this recently still has its waiter (the CLI calls again between
// waits), so its answer goes to that wait, not as a notice too.
const askWaitGrace = 5 * time.Second

func (p *Pool) askWaited(id string) {
	p.mu.Lock()
	if p.askWaitedAt == nil {
		p.askWaitedAt = map[string]time.Time{}
	}
	p.askWaitedAt[id] = time.Now()
	p.mu.Unlock()
}

func (p *Pool) approval(id string) (protocol.Approval, bool) {
	for _, a := range p.st.State().Domain.Approvals {
		if a.ID == id {
			return a, true
		}
	}
	return protocol.Approval{}, false
}
