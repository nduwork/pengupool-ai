package pool

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// compassDecider answers every line predicate with yes and the option choice with option_1 at opt.
type compassDecider struct {
	calibrated bool
	yes, opt   float64
}

func (compassDecider) Name() string { return "compass" }
func (d compassDecider) Decide(_ context.Context, r decision.Request) (decision.Response, error) {
	ans := map[string]decision.Answer{}
	for _, q := range r.Questions {
		if q.Type == decision.Choice {
			ans[q.Name] = decision.Answer{Type: decision.Choice, Choice: "option_1", Probabilities: map[string]float64{"option_1": d.opt}}
		} else {
			ans[q.Name] = decision.Answer{Type: decision.Predicate, Yes: d.yes}
		}
	}
	return decision.Response{Calibrated: d.calibrated, Answers: ans}, nil
}

// perCompassTeam: lead ─ web, both Claude, web running and idle; returns the pool and a way to file
// web's ask.
func perCompassTeam(t *testing.T) (*Pool, func(q string, ownerOnly bool) string) {
	t.Helper()
	p, _ := twoTeams(t)
	const lead, web = "leadaaaaaaaa", "webaaaaaaaaa"
	_ = p.st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Harness = "claude"
		}
		for i, m := range s.Domain.Teams[0].Members {
			if m.SessionID == web {
				s.Domain.Teams[0].Members[i].Parent = lead
			}
		}
		return nil
	})
	p.setStatus(web, "idle")
	ask := func(q string, ownerOnly bool) string { // straight into the store: agentAsk caps pending asks at 3
		a := protocol.Approval{ID: protocol.NewID(), Kind: "ask", TeamID: p.teamID(web), SessionID: web, Detail: q,
			Options: []string{"Yes", "Hold"}, OwnerOnly: ownerOnly, State: "pending", CreatedAt: now()}
		_ = p.st.Update(func(s *store.State) error { s.Domain.Approvals = append(s.Domain.Approvals, a); return nil })
		return a.ID
	}
	return p, ask
}

func ownerLine(t *testing.T, p *Pool, body, source string) string {
	t.Helper()
	uid, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Kind: "preference", Body: body, Origin: "stated", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestPerCompassAsk(t *testing.T) {
	p, ask := perCompassTeam(t)
	ctx := context.Background()
	const lead, web = "leadaaaaaaaa", "webaaaaaaaaa"
	decide := func(id string) (map[string]string, error) {
		res, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: id})
		out, _ := res.(map[string]string)
		return out, err
	}
	pending := func(id string) bool { a, _ := p.approval(id); return a.State == "pending" }
	refused := func(id, why string) {
		t.Helper()
		if _, err := decide(id); protocol.CodeOf(err) != protocol.CodeForbidden || !strings.Contains(err.Error(), why) || !pending(id) {
			t.Fatalf("%s: want refused %q, got %v (pending %v)", id, why, err, pending(id))
		}
	}

	// Not covered: no lines; a perm: line, a session-worded line and a learning line do not count.
	a1 := ask("Merge PR #7 into main?", false)
	refused(a1, "not covered by the Compass")
	if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Tag: "perm:allow:Bash:gh pr merge", Body: "The owner allows Bash for gh pr merge", Origin: "stated", Source: "gate"}); err != nil {
		t.Fatal(err)
	}
	ownerLine(t, p, "Merge PRs into main when the review is clean", "session")
	if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Kind: "preference", Body: "Merge into main after review", Origin: "chose", Source: "owner"}); err != nil {
		t.Fatal(err)
	}
	a2 := ask("Merge PR #8 into main?", false)
	refused(a2, "not covered by the Compass")
	// B1: the session line and the learning line left the version unchanged, so a1 stays decided.
	refused(a1, "already decided per the Compass")

	// A confident owner line, but no decider, an uncalibrated one, or one below the bar: not sure.
	uid := ownerLine(t, p, "Merge PRs into main once the PR review agent approves", "owner")
	a3 := ask("Merge PR #9 into main? The PR review agent approved.", false)
	refused(a3, "not sure")
	setDecider(p, compassDecider{calibrated: false, yes: 1, opt: 1})
	a4 := ask("Merge PR #10 into main? The PR review agent approved.", false)
	refused(a4, "not sure")
	setDecider(p, compassDecider{calibrated: true, yes: 0.85, opt: 1})
	a5 := ask("Merge PR #11 into main? The PR review agent approved.", false)
	refused(a5, "not sure")
	setDecider(p, compassDecider{calibrated: true, yes: 0.95, opt: 0.85})
	refused(a5, "already decided") // the same version: a sample is not retried until it says yes
	_ = a4

	// An owner-only ask is never decided from the Compass.
	setDecider(p, compassDecider{calibrated: true, yes: 0.95, opt: 0.95})
	ao := ask("Merge PR #12 into main? The PR review agent approved.", true)
	if _, err := decide(ao); protocol.CodeOf(err) != protocol.CodeForbidden || !strings.Contains(err.Error(), "owner alone") || !pending(ao) {
		t.Fatalf("an owner-only ask: %v", err)
	}

	// Over the bar: answered with the option, per the line, on the record.
	a6 := ask("Merge PR #13 into main? The PR review agent approved.", false)
	out, err := decide(a6)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := p.approval(a6)
	if a.State != "answered" || a.Answer != "Yes" || a.AnsweredBy != "lead per Compass line "+uid || a.LineUID != uid || !strings.Contains(out["detail"], "answered per Compass line "+uid) {
		t.Fatalf("ask = %+v, out %v", a, out)
	}
	if rec, ok, _ := p.db.PerCompassFor(a6); !ok || rec.Outcome != "answered" || rec.LineUID != uid || rec.Lead != lead {
		t.Fatalf("record = %+v", rec)
	}

	// A new confident owner line changes the version: a refused item is decided again.
	ownerLine(t, p, "Merging into main is fine after the PR review agent approves", "owner")
	if _, err := decide(a5); err != nil {
		t.Fatalf("after a new owner line: %v", err)
	}

	// "That was wrong": a correction against the line, a notice to web, once.
	before, _ := p.db.Effects(uid)
	if _, err := p.answerWrong(ctx, answerWrongIn{ID: a6, Words: "not on a Friday"}); err != nil {
		t.Fatal(err)
	}
	after, _ := p.db.Effects(uid)
	if len(after[uid]) != len(before[uid])+1 || after[uid][len(after[uid])-1].PCon != 1 {
		t.Fatalf("effects on the line: %d -> %+v", len(before[uid]), after[uid])
	}
	if n := p.takeNotices(web); !strings.Contains(n, "was wrong") || !strings.Contains(n, "not on a Friday") {
		t.Fatalf("notices:\n%s", n)
	}
	if _, err := p.answerWrong(ctx, answerWrongIn{ID: a6}); err == nil {
		t.Fatal("called wrong twice")
	}

	// The toggle off: refused before anything else.
	tid := p.teamID(lead)
	if _, err := p.teamLeadCompass(ctx, leadCompassIn{TeamID: tid, On: false}); err != nil {
		t.Fatal(err)
	}
	a7 := ask("Merge PR #14 into main? The PR review agent approved.", false)
	refused(a7, "turned off")
}

// A check-in decided from the Compass: 0.9 for every check-in, floor-worded or not; the go-ahead is
// typed into the member, the gate settles on the line, and "That was wrong" works on it. A
// teammate's message since the lead's last reply, or an item that reply never showed, does not
// matter; a permission prompt is never an item.
func TestPerCompassCheckin(t *testing.T) {
	p, _ := perCompassTeam(t)
	ctx := context.Background()
	const lead, web = "leadaaaaaaaa", "webaaaaaaaaa"
	uid := ownerLine(t, p, mergeLine, "owner")
	var typed []string
	p.testKeys = func(_, text string) error { typed = append(typed, text); return nil }
	_ = p.st.Update(func(s *store.State) error { // a teammate's message to the lead, just now
		addMessage(s, protocol.TeamMessage{ID: protocol.NewID(), From: web, To: lead, Text: "please answer it", State: "delivered", At: now(), DeliveredAt: now(), Via: viaHost})
		return nil
	})
	gate := func(kind string) string {
		t.Helper()
		id, err := p.db.AddGate(agentdb.Gate{TeamID: p.teamID(web), SessionID: web, Agent: "web", Kind: kind,
			Question: "The PR review agent approved. Shall I run git merge feature into main?", Floor: true, FloorHit: "git merge",
			CreatedAt: store.RFC3339(time.Now().Add(-time.Minute))})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("checkin:%d", id)
	}
	setDecider(p, compassDecider{calibrated: true, yes: 0.85})
	c1 := gate("checkin")
	if _, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: c1}); protocol.CodeOf(err) != protocol.CodeForbidden || len(typed) != 0 {
		t.Fatalf("0.85 decided a check-in: %v, typed %q", err, typed)
	}
	setDecider(p, compassDecider{calibrated: true, yes: 0.9})
	c2 := gate("checkin")
	if _, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: c2}); err != nil {
		t.Fatal(err)
	}
	var gid int64
	fmt.Sscan(strings.TrimPrefix(c2, "checkin:"), &gid)
	g, _ := p.db.Gate(gid)
	if g.Outcome != "auto_proceeded" || g.Floor || len(g.Lines) != 1 || g.Lines[0] != uid {
		t.Fatalf("gate = %+v", g)
	}
	if len(typed) != 1 || !strings.HasPrefix(typed[0], "[PenguPool triage]") || !strings.Contains(typed[0], mergeLine) {
		t.Fatalf("typed %q", typed)
	}
	if _, err := p.answerWrong(ctx, answerWrongIn{ID: c2}); err != nil {
		t.Fatal(err)
	}
	if g, _ = p.db.Gate(g.ID); g.Review != "wrong" || !strings.Contains(p.takeNotices(web), "was wrong") {
		t.Fatalf("after wrong: %+v", g)
	}
	// A permission prompt is no item.
	if _, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: gate("permission")}); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("a permission prompt: %v", err)
	}
}

// heldDecider counts its calls and holds each one until release is closed; it never says yes.
type heldDecider struct {
	calls   *int32
	release chan struct{}
}

func (heldDecider) Name() string { return "held" }
func (d heldDecider) Decide(ctx context.Context, r decision.Request) (decision.Response, error) {
	atomic.AddInt32(d.calls, 1)
	select {
	case <-d.release:
	case <-ctx.Done():
		return decision.Response{}, ctx.Err()
	}
	ans := map[string]decision.Answer{}
	for _, q := range r.Questions {
		ans[q.Name] = decision.Answer{Type: decision.Predicate, Yes: 0.5}
	}
	return decision.Response{Calibrated: true, Answers: ans}, nil
}

// Parallel --per-compass calls on one item cannot fish for a yes (review probe): while one decision
// is being made the others are refused, the decider is asked once, and the refusal then stands for
// that Compass version. An answered item's record is never replaced by a later refusal; a check-in
// whose member is busy is not decided at all.
func TestPerCompassOneAtATime(t *testing.T) {
	p, ask := perCompassTeam(t)
	ctx := context.Background()
	const lead, web = "leadaaaaaaaa", "webaaaaaaaaa"
	ownerLine(t, p, "Merge PRs into main once the PR review agent approves", "owner")
	id := ask("Merge PR #9 into main? The PR review agent approved.", false)
	var calls int32
	release := make(chan struct{})
	setDecider(p, heldDecider{calls: &calls, release: release})
	first := make(chan error, 1)
	go func() { _, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: id}); first <- err }()
	for atomic.LoadInt32(&calls) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	for range 3 {
		short, cancel := context.WithTimeout(ctx, time.Second) // a call that reached the decider gives up, not hangs
		_, err := p.askPerCompass(short, perCompassIn{SessionID: lead, ID: id})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "being decided") {
			t.Fatalf("a parallel call: %v", err)
		}
	}
	close(release)
	if err := <-first; protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("the first call: %v", err)
	}
	if _, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: id}); err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Fatalf("after it: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("the decider was asked %d times", n)
	}

	// An answered record stays answered.
	if err := p.db.PutPerCompass(agentdb.PerCompass{Item: "x1", TeamID: "t", SessionID: web, Lead: lead, Outcome: "answered", Detail: "Yes", Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	_ = p.db.PutPerCompass(agentdb.PerCompass{Item: "x1", TeamID: "t", SessionID: web, Lead: lead, Outcome: "refused", Detail: "not sure", Version: "v2"})
	if rec, _, _ := p.db.PerCompassFor("x1"); rec.Outcome != "answered" || rec.Version != "v1" {
		t.Fatalf("record = %+v", rec)
	}

	// A busy member's check-in: refused before any decision (the decider is not asked), nothing recorded.
	calls = 0
	setDecider(p, heldDecider{calls: &calls, release: release})
	gid, err := p.db.AddGate(agentdb.Gate{TeamID: p.teamID(web), SessionID: web, Agent: "web", Kind: "checkin", Question: "Shall I merge PR #9 into main? The PR review agent approved.", CreatedAt: store.RFC3339(time.Now().Add(-time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	p.setStatus(web, "working")
	item := fmt.Sprintf("checkin:%d", gid)
	if _, err := p.askPerCompass(ctx, perCompassIn{SessionID: lead, ID: item}); err == nil || !strings.Contains(err.Error(), "idle") {
		t.Fatalf("a busy member's check-in: %v", err)
	}
	if _, ok, _ := p.db.PerCompassFor(item); ok || atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("a busy member's check-in was decided: %d decider calls", calls)
	}
}
