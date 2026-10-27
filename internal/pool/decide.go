package pool

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// decision.ask: a helper (or the owner) sends a typed question pack to the owner's decision model,
// so a helper's own checks can use triage instead of a backend of their own. The model only answers
// the typed questions; the helper decides what to do with the answers.

const (
	maxAskState        = 32 << 10
	maxAskQuestions    = 12
	maxAskInstructions = 2 << 10
	maxAskOptions      = 12
	maxAskLevels       = 10 // the decision package's own ceiling for a score's levels
	maxAskQuestionsLen = 32 << 10
	askTimeout         = 90 * time.Second
	askPerHour         = 30  // per session
	askHostPerHour     = 120 // all sessions together
)

var askNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

type askIn struct {
	SessionID string              `json:"session_id,omitempty"`
	State     json.RawMessage     `json:"state,omitempty"`
	Questions []decision.Question `json:"questions"`
}

func checkAsk(in askIn) error {
	if len(in.State) > maxAskState {
		return invalid("state is limited to 32 KiB of JSON (%d bytes)", len(in.State))
	}
	if n := len(in.Questions); n < 1 || n > maxAskQuestions {
		return invalid("ask 1 to %d questions (%d given)", maxAskQuestions, n)
	}
	if b, _ := json.Marshal(in.Questions); len(b) > maxAskQuestionsLen {
		return invalid("the questions are limited to 32 KiB of JSON (%d bytes)", len(b))
	}
	seen := map[string]bool{}
	for _, q := range in.Questions {
		if !askNameRe.MatchString(q.Name) {
			return invalid("question name %q: up to 64 of a-z, 0-9, _ and -", q.Name)
		}
		if seen[q.Name] {
			return invalid("question name %q is used twice; names are unique", q.Name)
		}
		seen[q.Name] = true
		if b, _ := json.Marshal(q.Instructions); len(b) > maxAskInstructions {
			return invalid("%s: instructions are limited to 2 KiB", q.Name)
		}
		switch q.Type {
		case decision.Predicate:
		case decision.Choice:
			if n := len(q.Options); n < 2 || n > maxAskOptions {
				return invalid("%s: a choice takes 2 to %d options (%d given)", q.Name, maxAskOptions, n)
			}
		case decision.Score:
			if n := len(q.Levels); n < 2 || n > maxAskLevels {
				return invalid("%s: a score takes 2 to %d levels (%d given)", q.Name, maxAskLevels, n)
			}
		default:
			return invalid("%s: type is predicate, choice or score, not %q", q.Name, q.Type)
		}
	}
	return nil
}

// decisionAsk answers an agent's question pack with the owner's decision model. The session it
// acts for is named in the request, as for every agent request; the owner may ask without one.
func (p *Pool) decisionAsk(ctx context.Context, c gateway.Caller, in askIn) (any, error) {
	who := "owner"
	if in.SessionID != "" || !c.Owner {
		s, ok := p.session(in.SessionID)
		if !ok {
			return nil, notFound("no session %s", in.SessionID)
		}
		who = s.Name
	}
	if err := checkAsk(in); err != nil {
		return nil, err
	}
	var state any
	if len(in.State) > 0 {
		if err := json.Unmarshal(in.State, &state); err != nil {
			return nil, invalid("state: %v", err)
		}
	}
	prov := p.decider()
	if prov == nil {
		return nil, protocol.Errorf(protocol.CodeNoDecider, "no decision model: set one in Settings → Models & agents")
	}
	if err := p.askAllowed(firstNonEmpty(in.SessionID, "owner"), time.Now()); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	start := time.Now()
	res, err := prov.Decide(ctx, decision.Request{State: state, Questions: in.Questions})
	ms := time.Since(start).Milliseconds()
	if err != nil {
		timeout := errors.Is(err, decision.ErrTimeout) || errors.Is(err, context.DeadlineExceeded)
		class := "provider error"
		if timeout {
			class = "timeout"
		} else if errors.Is(err, decision.ErrRefused) {
			class = "refused"
		}
		p.logf("pool: decide: %s %d questions %s failed after %dms: %s", who, len(in.Questions), prov.Name(), ms, class) // never the provider's body
		if timeout {
			return nil, protocol.Errorf(protocol.CodeProviderTimeout, "the decision model did not answer in time")
		}
		return nil, protocol.Errorf(protocol.CodeProviderRefused, "%v", err)
	}
	p.logf("pool: decide: %s %d questions %s/%s %dms", who, len(in.Questions), firstNonEmpty(res.Provider, prov.Name()), res.Model, ms)
	return res, nil
}

// askAllowed counts one ask for key (a session, or "owner") within the hourly limits: 30 per key
// and 120 for the host. In memory: a host restart starts the count again.
func (p *Pool) askAllowed(key string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asks == nil {
		p.asks = map[string][]time.Time{}
	}
	recent := func(k string) []time.Time {
		out := p.asks[k][:0]
		for _, t := range p.asks[k] {
			if now.Sub(t) < time.Hour {
				out = append(out, t)
			}
		}
		p.asks[k] = out
		return out
	}
	const host = "\x00host" // never a session id
	if len(recent(key)) >= askPerHour {
		return conflict("decision limit reached (%d an hour)", askPerHour)
	}
	if len(recent(host)) >= askHostPerHour {
		return conflict("decision limit reached (%d an hour for the whole host)", askHostPerHour)
	}
	p.asks[key] = append(p.asks[key], now)
	p.asks[host] = append(p.asks[host], now)
	return nil
}
