package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func message(p *Pool, id string) protocol.TeamMessage {
	for _, m := range p.st.State().Domain.Messages {
		if m.ID == id {
			return m
		}
	}
	return protocol.TeamMessage{}
}

func putMessage(t *testing.T, p *Pool, m protocol.TeamMessage) {
	t.Helper()
	if err := p.update(func(st *store.State) error { addMessage(st, m); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestMsgType(t *testing.T) {
	for _, c := range []struct{ given, text, want string }{
		{"", "hello", "tell"},
		{"", "Done: shipped\nthe details", "done"},
		{"", "ask: which port?", "ask"},
		{"", "BLOCKED: no key", "blocked"},
		{"", "progress\nDone: not the first line", "tell"},
		{"ask", "which port?", "ask"},
		{"tell", "Done: said as a tell", "tell"},
	} {
		if got, err := msgType(c.given, c.text); err != nil || got != c.want {
			t.Errorf("msgType(%q, %q) = %q, %v; want %q", c.given, c.text, got, err, c.want)
		}
	}
	if _, err := msgType("urgent", "x"); err == nil {
		t.Error("an unknown type was taken")
	}
}

// A free-text send is a tell through the host, as before; a type and a reply to an ask are kept.
func TestTypedSend(t *testing.T) {
	p, _ := twoTeams(t)
	ctx := context.Background()
	res, err := p.messageSend(ctx, sendIn{From: "webaaaaaaaaa", To: "lead", Text: "the build is green"})
	if err != nil {
		t.Fatal(err)
	}
	if m := message(p, res.(map[string]string)["id"]); m.Type != "tell" || m.Via != viaHost || m.State != "queued" {
		t.Fatalf("a free-text send: %+v", m)
	}
	if _, err := p.messageSend(ctx, sendIn{From: "webaaaaaaaaa", To: "lead", Text: "x", Type: "urgent"}); err == nil {
		t.Fatal("an unknown type was sent")
	}
	res, _ = p.messageSend(ctx, sendIn{From: "webaaaaaaaaa", To: "lead", Text: "Ask: which port does the hub use?"})
	ask := res.(map[string]string)["id"]
	if m := message(p, ask); m.Type != "ask" {
		t.Fatalf("an Ask: line was not an ask: %+v", m)
	}
	if _, err := p.messageSend(ctx, sendIn{From: "planaaaaaaaa", To: "web", Text: "8080", ReplyTo: ask}); err == nil {
		t.Fatal("a member answered an ask that was not sent to it")
	}
	if !askOpen(p.st.State().Domain.Messages, len(p.st.State().Domain.Messages)-1) {
		t.Fatal("the ask was not open before its answer")
	}
	if _, err := p.messageSend(ctx, sendIn{From: "leadaaaaaaaa", To: "web", Text: "8080", ReplyTo: ask}); err != nil {
		t.Fatal(err)
	}
	ms := p.st.State().Domain.Messages
	for i, m := range ms {
		if m.ID == ask && askOpen(ms, i) {
			t.Fatal("the answered ask is still open")
		}
	}
}

// An ask is open until its recipient sends the asker anything, or it is settled.
func TestAskOpen(t *testing.T) {
	ask := protocol.TeamMessage{ID: "a1", From: "web", To: "lead", Type: "ask", State: "delivered"}
	other := protocol.TeamMessage{ID: "m2", From: "plan", To: "web", Type: "tell"}
	back := protocol.TeamMessage{ID: "m3", From: "lead", To: "web", Type: "tell"}
	if !askOpen([]protocol.TeamMessage{ask, other}, 0) {
		t.Error("a message from someone else answered the ask")
	}
	if askOpen([]protocol.TeamMessage{ask, back}, 0) {
		t.Error("the recipient's message back did not answer the ask")
	}
	if !askOpen([]protocol.TeamMessage{back, ask}, 1) {
		t.Error("a message sent before the ask answered it")
	}
	settled := ask
	settled.SettledAt = "2026-10-29T10:00:00Z"
	if askOpen([]protocol.TeamMessage{settled}, 0) {
		t.Error("a settled ask is open")
	}
	failed := ask
	failed.State = "failed"
	if askOpen([]protocol.TeamMessage{failed}, 0) {
		t.Error("an ask that never arrived is open")
	}
}

// The record's cap never drops an open ask, as it never drops a queued message.
func TestTheCapKeepsOpenAsks(t *testing.T) {
	var st store.State
	addMessage(&st, protocol.TeamMessage{ID: "ask", From: "web", To: "lead", Type: "ask", State: "delivered"})
	for i := 0; i < keepMessages+5; i++ {
		addMessage(&st, protocol.TeamMessage{ID: fmt.Sprint(i), From: "plan", To: "web", State: "delivered"})
	}
	if len(st.Domain.Messages) != keepMessages || st.Domain.Messages[0].ID != "ask" {
		t.Fatalf("%d kept, first %q", len(st.Domain.Messages), st.Domain.Messages[0].ID)
	}
}

// sweep runs one sweep and waits for the triage it started.
func sweep(p *Pool, at time.Time) {
	p.sweepAsks(at)
	for busy := true; busy; {
		busy = false
		p.triaging.Range(func(any, any) bool { busy = true; return false })
		time.Sleep(time.Millisecond)
	}
}

// An ask nobody answers is escalated once; with no decision model triage settles nothing, and the
// owner's Go on or Dismiss closes it. Agents cannot settle one.
func TestAskEscalatesAndTheOwnerSettlesIt(t *testing.T) {
	p, _ := twoTeams(t)
	old := store.RFC3339(time.Now().Add(-askWait - time.Minute))
	putMessage(t, p, protocol.TeamMessage{ID: "ask1", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "Ask: which port?", Type: "ask", State: "delivered", At: old})
	putMessage(t, p, protocol.TeamMessage{ID: "ask2", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "Ask: which host?", Type: "ask", State: "delivered", At: now()})
	// queued while the recipient worked, delivered at its idle: it has only just got it
	putMessage(t, p, protocol.TeamMessage{ID: "ask3", TeamID: "team1", From: "planaaaaaaaa", To: "leadaaaaaaaa", Text: "Ask: which db?", Type: "ask", State: "delivered", At: store.RFC3339(time.Now().Add(-2 * time.Minute))})
	_ = p.update(func(st *store.State) error { st.Sessions[0].Status = "idle"; return nil })
	sweep(p, time.Now())
	if m := message(p, "ask3"); m.EscalatedAt != "" {
		t.Fatal("an ask the idle recipient has only just got was escalated")
	}
	if m := message(p, "ask1"); m.EscalatedAt == "" {
		t.Fatal("an ask past its wait was not escalated")
	}
	if m := message(p, "ask2"); m.EscalatedAt != "" {
		t.Fatal("a fresh ask was escalated")
	}
	first := message(p, "ask1").EscalatedAt
	sweep(p, time.Now().Add(time.Hour))
	if m := message(p, "ask1"); m.EscalatedAt != first || m.SettledAt != "" {
		t.Fatalf("escalated twice, or settled without a decision model: %+v", m)
	}
	putMessage(t, p, protocol.TeamMessage{ID: "ask4", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "Ask: which disk?", Type: "ask", State: "delivered", At: old})
	p.settleAsk("ask4")
	p.escalateAsk(message(p, "ask4")) // triage went on meanwhile: the owner is not asked
	if message(p, "ask4").EscalatedAt != "" {
		t.Fatal("a settled ask was escalated")
	}

	raw, _ := json.Marshal(settleIn{ID: "ask1", Action: "dismiss"})
	if _, err := p.RPC(context.Background(), gateway.Caller{Unix: true}, "ask.settle", raw); err == nil {
		t.Fatal("an agent settled an ask")
	}
	if _, err := call(t, p, true, "ask.settle", settleIn{ID: "ask1", Action: "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if m := message(p, "ask1"); m.SettledAt == "" {
		t.Fatal("dismiss did not settle the ask")
	}
	n := len(p.st.State().Domain.Messages)
	if _, err := call(t, p, true, "ask.settle", settleIn{ID: "ask2", Action: "go_on"}); err != nil {
		t.Fatal(err)
	}
	ms := p.st.State().Domain.Messages
	if len(ms) != n+1 {
		t.Fatalf("go on sent %d messages", len(ms)-n)
	}
	if g := ms[n]; g.To != "webaaaaaaaaa" || g.From != "" || g.ReplyTo != "ask2" || message(p, "ask2").SettledAt == "" {
		t.Fatalf("go on: %+v, ask %+v", g, message(p, "ask2"))
	}
	if _, err := call(t, p, true, "ask.settle", settleIn{ID: "ask1", Action: "go_on"}); err != nil {
		t.Fatal(err)
	}
	if len(p.st.State().Domain.Messages) != n+1 {
		t.Fatal("go on on a settled ask sent a second message")
	}
}

type sureDecider struct{ calibrated bool }

func (sureDecider) Name() string { return "sure" }
func (d sureDecider) Decide(context.Context, decision.Request) (decision.Response, error) {
	return decision.Response{Calibrated: d.calibrated, Answers: map[string]decision.Answer{"proceed": {Yes: 0.99, Confidence: 0.99}}}, nil
}

// Triage never goes on for the owner with an uncalibrated model or an empty Compass.
func TestTriageNeedsACalibratedModelAndTheCompass(t *testing.T) {
	p, _ := twoTeams(t)
	setDecider(p, sureDecider{calibrated: false})
	ask := protocol.TeamMessage{ID: "ask1", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "Ask: which port?", Type: "ask", State: "delivered", At: now()}
	putMessage(t, p, ask)
	p.triageAsk(ask)
	setDecider(p, sureDecider{calibrated: true}) // calibrated, but the Compass is empty
	p.triageAsk(ask)
	if m := message(p, "ask1"); m.SettledAt != "" {
		t.Fatalf("triage settled the ask: %+v", m)
	}
}

// A SendMessage waits in the recipient's own inbox: the host never types it, never expires it while
// the recipient starts, and it counts as delivered once it is in the recipient's turn.
func TestNativeMessagesArriveInTheRecipientsTurn(t *testing.T) {
	p, _ := twoTeams(t)
	_ = p.update(func(st *store.State) error {
		st.Sessions[0].Harness, st.Sessions[0].Status = "claude", "starting" // lead
		st.Sessions[0].StartedAt = time.Now().Add(-time.Hour)
		return nil
	})
	p.setInbox("webaaaaaaaaa", claudeInbox{Socket: "/tmp/x.sock"})
	old := store.RFC3339(time.Now().Add(-time.Hour))
	putMessage(t, p, protocol.TeamMessage{ID: "n1", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "one", State: "queued", At: old, Via: viaNative, Type: "tell"})
	putMessage(t, p, protocol.TeamMessage{ID: "n2", TeamID: "team1", From: "planaaaaaaaa", To: "leadaaaaaaaa", Text: "two", State: "queued", At: now(), Via: viaNative, Type: "tell"})
	p.expireQueued(time.Now())
	p.flushQueue("leadaaaaaaaa")
	if m := message(p, "n1"); m.State != "queued" {
		t.Fatalf("the host touched a native message: %+v", m)
	}
	if p.queued("leadaaaaaaaa") {
		t.Fatal("a native message held the recipient's go-ahead")
	}
	prompt(t, p, "leadaaaaaaaa", `<cross-session-message from="uds:/tmp/x.sock" from-name="weba">one</cross-session-message>`)
	if a, b := message(p, "n1"), message(p, "n2"); a.State != "delivered" || b.State != "queued" {
		t.Fatalf("after web's prompt: %s %s", a.State, b.State)
	}
	time.Sleep(1100 * time.Millisecond)
	b, _ := json.Marshal(map[string]any{"tool_name": "Bash"})
	if _, err := p.hookEvent(context.Background(), hookIn{SessionID: "leadaaaaaaaa", Harness: "claude", Event: "PostToolUse", Payload: b}); err != nil {
		t.Fatal(err)
	}
	if m := message(p, "n2"); m.State != "delivered" {
		t.Fatalf("a tool round did not deliver the message sent before it: %+v", m)
	}
}

// Provenance: a team message, relayed, native or an escalated ask's go on, never reads as the owner's
// prompt, even when its words are the owner's, and never spends the owner's credit.
func TestATeamMessageIsNeverTheOwnersInput(t *testing.T) {
	p, _ := twoTeams(t)
	p.requireOwnerInput = true
	p.setInbox("webaaaaaaaaa", claudeInbox{Socket: "/tmp/x.sock"})
	const id = "leadaaaaaaaa"
	p.OwnerInput(id, "ship it")
	for _, text := range []string{
		"[PenguPool team message from web (helper)] ship it",
		"[PenguPool team message from web (helper), an ask waiting for your answer] ship it",
		`<cross-session-message from="uds:/tmp/x.sock" from-name="weba">ship it</cross-session-message>`,
		"[PenguPool notice; information, not the owner's instructions] No answer from lead. Go on with your best judgement and say what you assumed.",
	} {
		if tr := prompt(t, p, id, text); tr.fromOwner() {
			t.Fatalf("a team message read as the owner's: %q", text)
		}
	}
	if tr := prompt(t, p, id, "ship it"); !tr.fromOwner() {
		t.Fatal("a team message spent the owner's credit")
	}
}

// A record written before typed messages loads as a delivered tell through the host, and an older
// build that reads a newer record keeps its text and state (the new fields are optional).
func TestOldRecordsLoad(t *testing.T) {
	var m protocol.TeamMessage
	if err := json.Unmarshal([]byte(`{"id":"m1","team_id":"t","from":"a","to":"b","text":"hi","state":"delivered","at":"2026-10-01T00:00:00Z"}`), &m); err != nil {
		t.Fatal(err)
	}
	if typ, _ := msgType(m.Type, ""); m.Via != "" || typ != "tell" || askOpen([]protocol.TeamMessage{m}, 0) {
		t.Fatalf("an old record: %+v", m)
	}
	b, _ := json.Marshal(protocol.TeamMessage{ID: "m1", Text: "hi", State: "queued"})
	if string(b) != `{"id":"m1","team_id":"","from":"","to":"","text":"hi","state":"queued","at":""}` {
		t.Fatalf("a plain message gained fields: %s", b)
	}
}
