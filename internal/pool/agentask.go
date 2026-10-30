package pool

import (
	"context"
	"encoding/json"
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
// waiting reaches it as a notice instead.

const (
	askMaxOptions = 8
	askMaxPending = 3                // per session: an agent asking in a loop fills no list
	askMaxWait    = 60 * time.Second // one ask.wait call; the CLI calls again
)

type agentAskIn struct {
	SessionID string   `json:"session_id"`
	Question  string   `json:"question"`
	Options   []string `json:"options"`
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
		if o = clipRunes(redact(oneLine(strings.TrimSpace(o))), 80); o != "" {
			opts = append(opts, o)
		}
	}
	a := protocol.Approval{ID: protocol.NewID(), Kind: "ask", TeamID: s.TeamID, SessionID: s.ID,
		Detail: clipRunes(redact(q), 2000), Options: opts, State: "pending", CreatedAt: now()}
	cut := time.Now().Add(-24 * time.Hour)
	if err := p.update(func(st *store.State) error {
		// An ask whose session went expires; settled asks stay a day, for the app's record.
		keep, mine := st.Domain.Approvals[:0], 0
		for _, x := range st.Domain.Approvals {
			if x.Kind == "ask" && x.State == "pending" {
				if i := sessionIndex(st, x.SessionID); i < 0 || st.Sessions[i].State != "running" {
					x.State = "expired"
				} else if x.SessionID == s.ID {
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
		p.askWaited(in.ID)
		switch a.State {
		case "answered":
			return map[string]string{"id": a.ID, "answer": a.Answer, "by": a.AnsweredBy}, nil
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
	return p.answerAsk(in.ID, in.Answer, "owner")
}

// answerAsk records the answer once: a second answer, or one to an ask that went, is a conflict.
// The session gets it from its wait, or as a notice when nothing waits.
func (p *Pool) answerAsk(id, answer, by string) (any, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil, invalid("no answer")
	}
	var a protocol.Approval
	err := p.update(func(st *store.State) error {
		for i := range st.Domain.Approvals {
			if x := &st.Domain.Approvals[i]; x.ID == id && x.Kind == "ask" {
				if x.State != "pending" {
					return conflict("the ask changed or expired")
				}
				x.State, x.Answer, x.AnsweredBy = "answered", clipRunes(answer, 4000), by
				a = *x
				return nil
			}
		}
		return notFound("no ask %s", id)
	})
	if err != nil {
		return nil, err
	}
	p.logLine(a.SessionID, "ask answered by "+by+": "+clipRunes(oneLine(a.Answer), 200))
	p.mu.Lock()
	waiting := time.Since(p.askWaitedAt[a.ID]) < askWaitGrace
	delete(p.askWaitedAt, a.ID)
	p.mu.Unlock()
	if !waiting {
		p.notice(a.TeamID, a.SessionID, "The owner answered your ask "+a.ID+" (\""+clipRunes(oneLine(a.Detail), 120)+"\"): "+a.Answer)
	}
	return map[string]string{"id": a.ID, "answer": a.Answer}, nil
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
