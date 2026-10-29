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
// reply_to. One open past askWait goes to triage, which goes on for the owner when it is sure the
// owner would; otherwise it is escalated and waits in Needs you. ponytail: a recipient that went idle without
// answering waits out askWait too; telling that apart needs a delivery time on the record.

// Via: host and channel messages travel through PenguPool, native ones through Claude's own
// SendMessage (to a teammate, or on a channel the owner opened).
const (
	viaHost    = "host"
	viaNative  = "native"
	viaChannel = "channel"
)

// askWait is how long an ask waits for its answer.
const askWait = 15 * time.Minute

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

// sweepAsks hands the asks that waited too long to triage, one triage per ask at a time. An ask
// triage does not settle is escalated: it waits in the owner's Needs you, so the owner is never shown
// an ask that triage then closes. A daemon that stops mid-triage leaves it unescalated, and the next one triages it again.
func (p *Pool) sweepAsks(at time.Time) {
	ms := p.st.State().Domain.Messages
	for i, m := range ms {
		if m.EscalatedAt != "" || !askOpen(ms, i) || !olderThan(m.At, at, askWait) {
			continue
		}
		if _, busy := p.triaging.LoadOrStore(m.ID, true); busy {
			continue
		}
		go func() {
			defer p.triaging.Delete(m.ID)
			if !p.askWaiting(m.ID) { // the snapshot is old: escalated, settled or answered since
				return
			}
			if !p.triageAsk(m) {
				p.escalateAsk(m)
			}
		}()
	}
}

// askWaiting says whether an ask is still open and not yet with the owner, in the current state.
func (p *Pool) askWaiting(id string) bool {
	ms := p.st.State().Domain.Messages
	for i, m := range ms {
		if m.ID == id {
			return m.EscalatedAt == "" && askOpen(ms, i)
		}
	}
	return false
}

// escalateAsk passes an ask triage did not settle on to the owner, unless it was answered meanwhile.
func (p *Pool) escalateAsk(m protocol.TeamMessage) {
	escalated := false
	_ = p.update(func(st *store.State) error {
		for j := range st.Domain.Messages {
			if x := &st.Domain.Messages[j]; x.ID == m.ID && x.EscalatedAt == "" && askOpen(st.Domain.Messages, j) {
				x.EscalatedAt, escalated = now(), true
			}
		}
		return nil
	})
	if escalated {
		to, _ := p.session(m.To)
		p.logLine(m.From, "ask: no answer from "+firstNonEmpty(to.Name, m.To)+"; escalated")
	}
}

// triageAsk lets triage go on for the owner when it is sure the owner would have the asker carry on
// on its own judgement; it reports whether it did.
func (p *Pool) triageAsk(m protocol.TeamMessage) bool {
	prov := p.decider()
	if prov == nil || floorHit(m.Text) != "" {
		return false
	}
	from, _ := p.session(m.From)
	to, _ := p.session(m.To)
	rules, uids := p.ownerRules(tailRunes(redact(m.Text), 800))
	if len(uids) == 0 { // an empty Compass guides nothing
		return false
	}
	state := map[string]any{"asker": from.Name, "recipient": to.Name, "question": gateText(m.Text, 1500),
		"owner_decisions_and_rules": rules}
	res, err := p.ask(prov, state, []decision.Question{{Name: "proceed", Type: decision.Predicate,
		Instructions: "The asker asked the recipient this question and got no answer. Would the owner, by their decisions and rules, have the asker go on with its own best judgement instead of waiting?"}})
	if err != nil {
		return false
	}
	a := res.Answers["proceed"]
	if !res.Calibrated || a.Yes < 0.5 || a.Confidence < decision.Thresholds["proceed"] {
		return false
	}
	if !p.askWaiting(m.ID) {
		return true // answered or settled while the model ran: no go on, and nothing for the owner
	}
	p.goOnAsk(m.ID, fmt.Sprintf("[PenguPool triage] No answer from %s. Go on with your best judgement and say what you assumed (p=%.2f, the owner's decision model).", firstNonEmpty(to.Name, m.To), a.Confidence))
	return true
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
// sent before it; or, with no sender, a tool round or the turn's Stop, which carry the messages that
// came in while the turn ran. The sender's PostToolUse records a message only after the socket took
// it, so an idle recipient's prompt usually comes first and finds nothing yet: its Stop counts it.
// ponytail: one recorded under a second before that Stop counts at the next round or prompt.
func (p *Pool) nativeArrived(id, from string, at time.Time) {
	st := p.st.State()
	var ids []string
	for _, m := range st.Domain.Messages {
		if m.To != id || m.Via != viaNative || m.State != "queued" || m.From == "" {
			continue
		}
		if from != "" && m.From != from || from == "" && !olderThan(m.At, at, time.Second) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, m.At); err != nil || t.After(at) { // sent after the prompt: still on its way
			continue
		}
		ids = append(ids, m.ID)
	}
	p.markDelivered(ids)
}
