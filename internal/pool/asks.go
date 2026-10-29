package pool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Typed team messages: a message tells, asks, reports a finished job (done) or says the sender is
// blocked. `pengupool send --type` names it; a message without one (and Claude's SendMessage, which
// has no flag) is typed by its first line: "Done:", "Ask:" or "Blocked:", else tell.
// An ask stays open until its recipient sends the asker anything, or a message answers it by
// reply_to. One open past askWait, or whose recipient went idle without answering, is escalated:
// triage goes on for the owner when it is sure the owner would; otherwise it waits in Needs you.

// Via: host and channel messages travel through PenguPool, native ones through Claude's own
// SendMessage (to a teammate, or on a channel the owner opened).
const (
	viaHost    = "host"
	viaNative  = "native"
	viaChannel = "channel"
)

// askWait is how long an ask waits for its answer; askIdle how long a recipient that went idle
// without answering has had it. Tests shorten both.
var (
	askWait = 15 * time.Minute
	askIdle = time.Minute
)

var msgTypes = map[string]bool{"tell": true, "ask": true, "done": true, "blocked": true}

// msgType is the message's type: the one given, else what its first line starts with.
func msgType(given, text string) (string, error) {
	if given != "" {
		if !msgTypes[given] {
			return "", invalid("type is tell, ask, done or blocked, not %q", given)
		}
		return given, nil
	}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	first = strings.ToLower(first)
	for _, t := range []string{"done", "ask", "blocked"} {
		if strings.HasPrefix(first, t+":") {
			return t, nil
		}
	}
	return "tell", nil
}

// askOpen says whether ms[i] is an ask still waiting for its answer.
func askOpen(ms []protocol.TeamMessage, i int) bool {
	a := ms[i]
	if a.Type != "ask" || a.SettledAt != "" || a.State == "failed" {
		return false
	}
	for _, m := range ms[i+1:] {
		if m.ReplyTo == a.ID || m.From == a.To && m.To == a.From {
			return false
		}
	}
	return true
}

// sweepAsks escalates the open asks that waited too long, each once.
func (p *Pool) sweepAsks(at time.Time) {
	st := p.st.State()
	ms := st.Domain.Messages
	for i, m := range ms {
		if m.EscalatedAt != "" || !askOpen(ms, i) {
			continue
		}
		to, _ := sessionByID(&st, m.To)
		idle := m.State == "delivered" && to.Status == "idle" && olderThan(m.At, at, askIdle)
		if !olderThan(m.At, at, askWait) && !idle {
			continue
		}
		claimed := false
		_ = p.update(func(st *store.State) error {
			for j := range st.Domain.Messages {
				if x := &st.Domain.Messages[j]; x.ID == m.ID && x.EscalatedAt == "" {
					x.EscalatedAt, claimed = store.RFC3339(at), true
				}
			}
			return nil
		})
		if claimed {
			p.logLine(m.From, "ask: no answer from "+firstNonEmpty(to.Name, m.To)+"; escalated")
			go p.triageAsk(m)
		}
	}
}

// triageAsk lets triage go on for the owner when it is sure the owner would have the asker carry on
// on its own judgement. Otherwise the ask waits in the owner's Needs you.
func (p *Pool) triageAsk(m protocol.TeamMessage) {
	prov := p.decider()
	if prov == nil || floorHit(m.Text) != "" {
		return
	}
	from, _ := p.session(m.From)
	to, _ := p.session(m.To)
	rules, uids := p.ownerRules(tailRunes(redact(m.Text), 800))
	if len(uids) == 0 { // an empty Compass guides nothing
		return
	}
	state := map[string]any{"asker": from.Name, "recipient": to.Name, "question": gateText(m.Text, 1500),
		"owner_decisions_and_rules": rules}
	res, err := p.ask(prov, state, []decision.Question{{Name: "proceed", Type: decision.Predicate,
		Instructions: "The asker asked the recipient this question and got no answer. Would the owner, by their decisions and rules, have the asker go on with its own best judgement instead of waiting?"}})
	if err != nil {
		return
	}
	a := res.Answers["proceed"]
	if !res.Calibrated || a.Yes < 0.5 || a.Confidence < decision.Thresholds["proceed"] {
		return
	}
	p.goOnAsk(m.ID, fmt.Sprintf("[PenguPool triage] No answer from %s. Go on with your best judgement and say what you assumed (p=%.2f, the owner's decision model).", firstNonEmpty(to.Name, m.To), a.Confidence))
}

type settleIn struct {
	ID     string `json:"id"`
	Action string `json:"action"` // go_on | dismiss
}

// askSettle is the owner closing an escalated ask from Needs you: go on tells the asker to carry on
// without the answer; dismiss closes it and tells nobody.
func (p *Pool) askSettle(_ context.Context, in settleIn) (any, error) {
	var m protocol.TeamMessage
	for _, x := range p.st.State().Domain.Messages {
		if x.ID == in.ID && x.Type == "ask" {
			m = x
		}
	}
	if m.ID == "" {
		return nil, notFound("no ask %s", in.ID)
	}
	switch in.Action {
	case "go_on":
		to, _ := p.session(m.To)
		p.goOnAsk(m.ID, fmt.Sprintf("No answer from %s. Go on with your best judgement and say what you assumed.", firstNonEmpty(to.Name, m.To)))
	case "dismiss":
		p.settleAsk(m.ID)
	default:
		return nil, invalid("action is go_on or dismiss, not %q", in.Action)
	}
	return nil, nil
}

// settleAsk closes an ask without its answer; it reports whether it was still open.
func (p *Pool) settleAsk(id string) (settled bool) {
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Messages {
			if x := &st.Domain.Messages[i]; x.ID == id && x.SettledAt == "" {
				x.SettledAt, settled = now(), true
			}
		}
		return nil
	})
	return settled
}

// goOnAsk settles an ask and tells its asker to go on: a PenguPool notice answering the ask, which
// starts the asker's turn when it is idle and waits for the next idle otherwise.
func (p *Pool) goOnAsk(id, text string) {
	var ask protocol.TeamMessage
	for _, x := range p.st.State().Domain.Messages {
		if x.ID == id {
			ask = x
		}
	}
	if !p.settleAsk(id) {
		return // settled meanwhile: by the owner, or by triage
	}
	msg := protocol.TeamMessage{ID: protocol.NewID(), TeamID: ask.TeamID, To: ask.From, Text: text,
		State: "queued", At: now(), Via: viaHost, Type: "tell", ReplyTo: ask.ID}
	if err := p.update(func(st *store.State) error { addMessage(st, msg); return nil }); err != nil {
		return
	}
	p.logLine(ask.From, "notice: "+text)
	if s, ok := p.session(ask.From); ok && s.Status == "idle" {
		p.deliver(msg)
		return
	}
	p.mu.Lock()
	p.pending[ask.From] = append(p.pending[ask.From], msg)
	p.mu.Unlock()
}

// nativeArrived marks the SendMessage messages to a Claude session delivered once they are in its
// turn: a cross-session prompt from their sender (from, its session id), and everything that sender
// sent before it; or, with no sender, a tool round, which carries the messages that came in while the
// turn ran. ponytail: a message that lands between the last tool round and Stop counts at the
// next round or prompt.
func (p *Pool) nativeArrived(id, from string, at time.Time) {
	st := p.st.State()
	var ids []string
	for _, m := range st.Domain.Messages {
		if m.To != id || m.Via != viaNative || m.State != "queued" || m.From == "" {
			continue
		}
		if from != "" {
			if m.From != from {
				continue
			}
		} else if !olderThan(m.At, at, time.Second) {
			continue
		}
		ids = append(ids, m.ID)
	}
	p.markDelivered(ids)
}
