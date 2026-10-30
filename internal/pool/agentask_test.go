package pool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// An agent's ask: the owner's paired device sees it as a card on /next, answers with an option or
// its own words, and the waiting agent gets the answer as data. Nobody else answers it.
func TestAgentAsk(t *testing.T) {
	p, _, _ := gateTeam(t)
	ctx := context.Background()
	const id, other = "leadaaaaaaaa", "webaaaaaaaaa"
	_ = p.st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams {
			s.Domain.Teams[i].IOSAccess = true
		}
		return nil
	})
	ask := func(q string, opts ...string) string {
		t.Helper()
		res, err := p.agentAsk(ctx, agentAskIn{SessionID: id, Question: q, Options: opts})
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]string)["id"]
	}
	card := func(askID string) museCard {
		t.Helper()
		for _, a := range p.museAsks() {
			if a.id == "ask:"+askID {
				return *p.museCard(a)
			}
		}
		t.Fatalf("no card for %s in %+v", askID, p.museAsks())
		return museCard{}
	}
	answer := func(c museCard, action, text, key string) error {
		raw, _ := json.Marshal(map[string]any{"ask_id": c.AskID, "action": action, "text": text, "payload_hash": c.PayloadHash, "idempotency_key": key})
		_, err := p.RPC(ctx, museCaller(), "muse.answer", raw)
		return err
	}

	// An option, while the agent waits.
	a1 := ask("Which database for the cache?", "Postgres", "SQLite")
	c := card(a1)
	if got := []string{c.Actions[0].ID, c.Actions[1].Label, c.Actions[2].ID}; strings.Join(got, ",") != "option:0,SQLite,reply" {
		t.Fatalf("actions = %+v", c.Actions)
	}
	if res, _ := p.askWait(ctx, askWaitIn{SessionID: id, ID: a1}); res.(map[string]string)["pending"] != a1 {
		t.Fatalf("unanswered wait = %+v", res)
	}
	done := make(chan map[string]string)
	go func() {
		res, err := p.askWait(ctx, askWaitIn{SessionID: id, ID: a1, Wait: 10})
		if err != nil {
			t.Error(err)
		}
		r, _ := res.(map[string]string)
		done <- r
	}()
	time.Sleep(100 * time.Millisecond)
	stale := c
	stale.PayloadHash = "x"
	if err := answer(stale, "option:1", "", "k0"); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("changed ask: %v", err)
	}
	if err := answer(c, "option:7", "", "k1"); protocol.CodeOf(err) != protocol.CodeInvalidArgument {
		t.Fatalf("no such option: %v", err)
	}
	if err := answer(c, "option:1", "", "k2"); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r["answer"] != "SQLite" || r["by"] != "muse-test" {
		t.Fatalf("wait = %+v", r)
	}
	if err := answer(c, "option:0", "", "k3"); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("second answer: %v", err)
	}

	// The owner's own words, in the app, with nothing waiting: a notice brings it to the agent.
	a2 := ask("Ship it tonight?")
	if c := card(a2); len(c.Actions) != 1 || c.Actions[0].ID != "reply" {
		t.Fatalf("no options: %+v", c.Actions)
	}
	if err := answer(card(a2), "reply", " ", "k4"); protocol.CodeOf(err) != protocol.CodeInvalidArgument {
		t.Fatalf("empty reply: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{"id": a2, "answer": "Tomorrow morning, after the review."})
	if _, err := p.RPC(ctx, gateway.Caller{Unix: true}, "ask.answer", raw); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("an agent answered: %v", err)
	}
	if _, err := p.RPC(ctx, gateway.Caller{Owner: true}, "ask.answer", raw); err != nil {
		t.Fatal(err)
	}
	if res, err := p.askWait(ctx, askWaitIn{SessionID: id, ID: a2}); err != nil || res.(map[string]string)["by"] != "owner" {
		t.Fatalf("answered wait = %+v %v", res, err)
	}
	noticed := false
	for _, m := range p.st.State().Domain.Messages {
		noticed = noticed || m.To == id && strings.Contains(m.Text, "Tomorrow morning")
	}
	if !noticed {
		t.Fatal("no notice for an answer nobody waited for")
	}

	// Another session never waits on it, and a session not running never asks.
	if _, err := p.askWait(ctx, askWaitIn{SessionID: other, ID: a2}); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("another session waited: %v", err)
	}
	_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, other)].State = "exited"; return nil })
	if _, err := p.agentAsk(ctx, agentAskIn{SessionID: other, Question: "?"}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("an exited session asked: %v", err)
	}
}

// A wait that went (the agent's shell tool stopped the CLI) leaves no grace: the answer comes as a
// notice. At most 3 asks wait per session, and an ask whose session ended expires.
func TestAgentAskLimits(t *testing.T) {
	p, _, _ := gateTeam(t)
	ctx := context.Background()
	const id = "leadaaaaaaaa"
	ask := func(q string) (string, error) {
		res, err := p.agentAsk(ctx, agentAskIn{SessionID: id, Question: q})
		if err != nil {
			return "", err
		}
		return res.(map[string]string)["id"], nil
	}
	a1, _ := ask("one?")
	wctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := p.askWait(wctx, askWaitIn{SessionID: id, ID: a1, Wait: 10}); err == nil {
		t.Fatal("a cancelled wait returned")
	}
	if _, err := p.answerAsk(a1, "yes", "owner"); err != nil {
		t.Fatal(err)
	}
	noticed := false
	for _, m := range p.st.State().Domain.Messages {
		noticed = noticed || m.To == id && strings.Contains(m.Text, a1)
	}
	if !noticed {
		t.Fatal("the answer after a gone wait reached nobody")
	}

	for _, q := range []string{"two?", "three?", "four?"} {
		if _, err := ask(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ask("five?"); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("a fourth pending ask: %v", err)
	}

	// Nobody answers an ask whose session stopped: it expires instead.
	pending := ""
	for _, a := range p.st.State().Domain.Approvals {
		if a.Kind == "ask" && a.State == "pending" && a.SessionID == id {
			pending = a.ID
		}
	}
	_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, id)].State = "exited"; return nil })
	if _, err := p.answerAsk(pending, "late", "owner"); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("answered a stopped session's ask: %v", err)
	}
	if a, _ := p.approval(pending); a.State != "expired" {
		t.Fatalf("stopped session's ask = %+v", a)
	}
	_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, "webaaaaaaaaa")].Harness = "claude"; return nil })
	// Duplicate choices are one; resuming a session ends what it asked before.
	res, err := p.agentAsk(ctx, agentAskIn{SessionID: "webaaaaaaaaa", Question: "pick", Options: []string{"A", "A", " B "}})
	if err != nil {
		t.Fatal(err)
	}
	web, _ := p.approval(res.(map[string]string)["id"])
	if strings.Join(web.Options, ",") != "A,B" {
		t.Fatalf("options = %q", web.Options)
	}
	p.expireAsks("webaaaaaaaaa")
	if a, _ := p.approval(web.ID); a.State != "expired" {
		t.Fatalf("resumed session's ask = %+v", a)
	}
	if _, err := p.agentAsk(ctx, agentAskIn{SessionID: "webaaaaaaaaa", Question: "web?"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range p.st.State().Domain.Approvals {
		if a.Kind == "ask" && a.SessionID == id && a.State == "pending" {
			t.Fatalf("an ask outlived its session: %+v", a)
		}
	}
}
