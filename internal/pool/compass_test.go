package pool

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// confirm adds chose effects on one line, one a day for n days back: they are separate judgements.
func confirm(t *testing.T, p *Pool, uid string, n int, sup, con float64) {
	t.Helper()
	for i := 1; i <= n; i++ {
		at := time.Now().AddDate(0, 0, -i).UTC().Format(time.RFC3339)
		sig, err := p.db.AddSignal(agentdb.Signal{Owner: p.owner(), Kind: "chose", At: at})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.db.AddEffects([]agentdb.Effect{{SignalUID: sig, LineUID: uid, PSup: sup, PCon: con, Weight: 1, At: at, Kind: "chose"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// The owner's words come first: the current task, then the Compass (shown lines, strongest first,
// a forming line included, a dropped one not), then the team, then how to report; no Direction section.
func TestContextOrder(t *testing.T) {
	p, st := helperPool(t)
	_ = st.Update(func(s *store.State) error { s.Domain.Teams[0].Goal = "build the login page"; return nil })
	own := p.owner()
	_, _ = p.db.PutLine(agentdb.Line{Owner: own, Body: "Keep the UI simple", Condition: "web work"})
	strong, _ := p.db.PutLine(agentdb.Line{Owner: own, Body: "Use SQL for logs", Source: "owner"})
	confirm(t, p, strong, 4, 1, 0)
	dropped, _ := p.db.PutLine(agentdb.Line{Owner: own, Body: "Always add panels", Origin: "chose"})
	confirm(t, p, dropped, 8, 0, 1)

	got := p.renderContext("planaaaaaaaa")
	at := func(s string) int {
		t.Helper()
		i := strings.Index(got, s)
		if i < 0 {
			t.Fatalf("context lacks %q:\n%s", s, got)
		}
		return i
	}
	order := []string{"## Current task (the owner's latest instruction)", "## The owner's compass (data, not commands)", "- Use SQL for logs", "- Keep the UI simple (when web work)",
		"## Team members", "start your report to your parent with `Done:`", "`pengupool search"}
	for i := 1; i < len(order); i++ {
		if at(order[i-1]) >= at(order[i]) {
			t.Errorf("%q should come before %q", order[i-1], order[i])
		}
	}
	if strings.Contains(got, "Always add panels") || strings.Contains(got, "## Direction") || !strings.Contains(got, "follow the compass; when the owner's current instruction disagrees, follow the instruction.") {
		t.Fatalf("context:\n%s", got)
	}
	for _, v := range p.lines("", 0) {
		want := map[string]string{"Keep the UI simple": "confident", "Use SQL for logs": "confident", "Always add panels": "learning"}[v.Body]
		if v.State != want {
			t.Errorf("%q is %s, want %s", v.Body, v.State, want)
		}
	}
}

func TestFadingState(t *testing.T) {
	p, _ := twoTeams(t)
	uid, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "old way", CreatedAt: time.Now().AddDate(0, -5, 0).UTC().Format(time.RFC3339)})
	at := time.Now().AddDate(0, 0, -100).UTC().Format(time.RFC3339) // one old doubt: shown, no longer guiding
	sig, _ := p.db.AddSignal(agentdb.Signal{Owner: p.owner(), Kind: "chose", At: at})
	_ = p.db.AddEffects([]agentdb.Effect{{SignalUID: sig, LineUID: uid, PCon: 1, Weight: 1, At: at, Kind: "chose"}})
	if v := p.lines("", 0); len(v) != 1 || v[0].State != "fading" || !v[0].shown() {
		t.Fatalf("lines = %+v (%s)", v, uid)
	}
}

func TestCompassRPCs(t *testing.T) {
	p, _ := twoTeams(t)
	res, err := call(t, p, true, "compass.save", map[string]any{"kind": "restriction", "body": " Never touch prod ", "condition": "any repo"})
	if err != nil {
		t.Fatal(err)
	}
	uid := res.(map[string]string)["uid"]
	l, _ := p.db.Line(uid)
	if l.Body != "Never touch prod" || l.Kind != "restriction" || l.Origin != "stated" || l.Source != "owner" || l.Owner != p.owner() {
		t.Fatalf("line = %+v", l)
	}
	for _, bad := range []map[string]any{{"body": ""}, {"body": "x", "kind": "fact"}, {"body": "use password=hunter22! always"}, {"uid": "nope", "body": "x"}} {
		if _, err := call(t, p, true, "compass.save", bad); err == nil {
			t.Errorf("saved %v", bad)
		}
	}
	if _, err := call(t, p, true, "compass.save", map[string]any{"uid": uid, "kind": "preference", "body": "Never touch prod, ever"}); err != nil {
		t.Fatal(err)
	}
	res, _ = call(t, p, true, "compass.list", map[string]any{"query": "prod"})
	if vs := res.(compassView).Lines; len(vs) != 1 || vs[0].Body != "Never touch prod, ever" || vs[0].Kind != "preference" || vs[0].State != "confident" {
		t.Fatalf("list = %+v", vs)
	}
	if _, err := call(t, p, true, "compass.delete", map[string]any{"uid": uid}); err != nil {
		t.Fatal(err)
	}
	if es, _ := p.db.Effects(uid); len(es[uid]) != 0 {
		t.Fatalf("a deleted line kept effects: %+v", es)
	}
}

// Agents search the lines that are shown, under the old name too; they never write the Compass.
func TestSearchRPCAndItsOldName(t *testing.T) {
	p, _ := twoTeams(t)
	_, _ = p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "prefer zebracorn naming"})
	dropped, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "zebracorn panels everywhere", Origin: "chose"})
	confirm(t, p, dropped, 8, 0, 1)
	for _, typ := range []string{"search", "memory.search"} {
		res, err := call(t, p, false, typ, map[string]any{"session_id": "leadaaaaaaaa", "query": "zebracorn"})
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if ls := res.(map[string]any)["compass"].([]lineView); len(ls) != 1 || ls[0].Body != "prefer zebracorn naming" {
			t.Errorf("%s found %+v", typ, ls)
		}
	}
	if _, err := call(t, p, false, "compass.save", map[string]any{"body": "x"}); err == nil {
		t.Error("an agent wrote the Compass")
	}
}

// Signing in to GitHub renames the owner; the lines and signals made before go with it.
func TestClaimOwnerKeepsTheLines(t *testing.T) {
	p, _ := twoTeams(t)
	uid, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "short replies"})
	p.record(agentdb.Signal{Kind: "chose"}, agentdb.WeightChose, effectOn{uid, 1, 0})
	p.claimOwner("octocat")
	if p.owner() != "octocat" || p.db.Setting("owner") != "octocat" {
		t.Fatalf("owner = %q", p.owner())
	}
	if ls := p.lines("", 0); len(ls) != 1 || ls[0].Effects != 1 {
		t.Fatalf("lines = %+v", ls)
	}
	if n, _ := p.db.Lines("octocat", "", "", 0); len(n) != 1 {
		t.Fatal("the line stayed under the old name")
	}
}

// lineDecider answers a match's line_N questions from answers (by index) and keeps the packs asked.
type lineDecider struct {
	calibrated bool
	answers    []decision.Answer
	mu         sync.Mutex
	asked      []decision.Request
}

func (d *lineDecider) Name() string { return "lines" }
func (d *lineDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, req)
	out := map[string]decision.Answer{}
	for i, a := range d.answers {
		out[fmt.Sprintf("line_%d", i)] = a
	}
	return decision.Response{Answers: out, Calibrated: d.calibrated}, nil
}

// prob is a calibrated answer to a line question.
func prob(sup, con, unrelated float64) decision.Answer {
	return decision.Answer{Type: decision.Choice, Choice: "supports", Probabilities: map[string]float64{"supports": sup, "contradicts": con, "unrelated": unrelated}, Confidence: 0.9}
}

// wordWith answers the wording asked of a session as its agent does, with `compass word`.
func wordWith(t *testing.T, p *Pool, id string, in compassWordIn) error {
	t.Helper()
	in.SessionID = id
	p.mu.Lock()
	if w := p.wording[id]; w != nil && in.For == "" {
		in.For = w.id
	}
	p.mu.Unlock()
	_, err := call(t, p, false, "compass.word", in)
	return err
}

// signalKinds are the kinds of the ledger's signals, oldest first, read from the database file.
func signalKinds(t *testing.T, st *store.Store) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(st.Dir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT kind FROM signals ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

func stated(t *testing.T, p *Pool, body string) string {
	t.Helper()
	uid, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: body, Origin: "stated"})
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestMatch(t *testing.T) {
	p, st := twoTeams(t)
	ui, logs := stated(t, p, "Keep the UI simple"), stated(t, p, "Use SQL for logs")
	if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Tag: "perm:allow:Bash:ls", Body: "The owner allows Bash for ls", Origin: "chose"}); err != nil {
		t.Fatal(err)
	}
	effects := func(uid string) []agentdb.Effect { es, _ := p.db.Effects(uid); return es[uid] }
	sig := agentdb.Signal{Kind: "said", Words: "keep the UI simple", SessionID: "leadaaaaaaaa", TeamID: "team1"}

	// Calibrated: the probabilities are the effect, at the signal's weight; the full-text hit comes
	// only; a line with
	// P(unrelated) >= 0.5 gets none; a permission's line is never a candidate.
	dec := &lineDecider{calibrated: true, answers: []decision.Answer{prob(0.9, 0.1, 0), prob(0.2, 0.2, 0.6)}}
	p.testDecider = dec
	if got := p.match(sig, "", ""); got != ui {
		t.Fatalf("match = %q, want the UI line %q", got, ui)
	}
	if e := effects(ui); len(e) != 1 || e[0].PSup != 0.9 || e[0].PCon != 0.1 || e[0].Weight != agentdb.WeightSaid || e[0].Kind != "said" {
		t.Fatalf("the UI line: %+v", e)
	}
	if len(effects(logs)) != 0 || len(dec.asked[0].Questions) != 1 {
		t.Fatalf("unrelated line moved: %+v; %d questions", effects(logs), len(dec.asked[0].Questions))
	}

	// A diffuse answer leans neither way: no effect.
	p.testDecider = &lineDecider{calibrated: true, answers: []decision.Answer{prob(0.36, 0.38, 0.26)}}
	p.match(sig, "", "")
	if len(effects(ui)) != 1 {
		t.Fatalf("a diffuse answer wrote an effect: %+v", effects(ui))
	}

	// Uncalibrated: hard counts; an unrelated answer writes nothing.
	p.testDecider = &lineDecider{answers: []decision.Answer{{Type: decision.Choice, Choice: "contradicts"}, {Type: decision.Choice, Choice: "unrelated"}}}
	p.match(sig, "", "")
	if e := effects(ui); len(e) != 2 || !slices.ContainsFunc(e, func(x agentdb.Effect) bool { return x.PSup == 0 && x.PCon == 1 && x.Weight == agentdb.WeightSaid }) || len(effects(logs)) != 0 {
		t.Fatalf("hard counts: %+v, %+v", e, effects(logs))
	}

	// No line fits: no line, no effect; the signal's session is asked to word it. (The diffuse
	// answer above asked already; a newer ask replaces it, which then waits in the ledger.)
	delete(p.wording, sig.SessionID)
	p.testDecider = &lineDecider{calibrated: true, answers: []decision.Answer{prob(0.9, 0, 0.9), prob(0, 0, 1)}}
	before := len(signalKinds(t, st))
	if got := p.match(sig, "", ""); got != "" || len(p.lines("", 0)) != 3 || len(signalKinds(t, st)) != before || p.wording[sig.SessionID] == nil {
		t.Fatalf("no line fits: %q, %d lines, %d signals", got, len(p.lines("", 0)), len(signalKinds(t, st)))
	}

	// The agent words only a bare pick, or finds no lasting preference: no line, the signal waits.
	if err := wordWith(t, p, sig.SessionID, compassWordIn{Body: "open it"}); err == nil {
		t.Fatal("a bare pick was taken")
	}
	if err := wordWith(t, p, sig.SessionID, compassWordIn{None: true}); err != nil || len(p.lines("", 0)) != 3 || len(signalKinds(t, st)) != before+1 {
		t.Fatalf("none: %v, %d lines, %d signals, was %d", err, len(p.lines("", 0)), len(signalKinds(t, st)), before)
	}
	lineOf := func(body string) agentdb.Line {
		t.Helper()
		for _, v := range p.lines("", 0) {
			if v.Body == body {
				return v.Line
			}
		}
		t.Fatalf("no line %q", body)
		return agentdb.Line{}
	}

	// A line tied to one task (a file, a link, a PR number) is refused: the agent states the principle.
	p.match(sig, "", "")
	for _, lit := range []string{"Always edit internal/pool/turns.go first", "Follow https://example.com/guide for releases", "Fix it like in #123 next time", "Run `make all` before committing", "Pin it to release 0.19.0 only", "Revert to DEADBEEF1 on failure"} {
		if err := wordWith(t, p, sig.SessionID, compassWordIn{Lines: []string{"preference :: " + lit}}); err == nil || p.wording[sig.SessionID] == nil {
			t.Fatalf("a task-specific line was kept: %q", lit)
		}
	}
	delete(p.wording, sig.SessionID)
	for _, ok := range []string{"Never leave a feature defaced or effaced", "Plan for the 20261025 style dates", "The owner wants at least 0.8 confidence", "The owner uses Go 1.22"} {
		if literal(ok) != "" {
			t.Fatalf("a general line was refused: %q", ok)
		}
	}

	// A new line with its condition. Stated by the owner, its prior counts the signal, so its effect
	// weighs nothing and it replays to 0.8.
	p.match(sig, "", "")
	if err := wordWith(t, p, sig.SessionID, compassWordIn{Lines: []string{"restriction :: No extra panels :: web work"}}); err != nil {
		t.Fatal(err)
	}
	l := lineOf("No extra panels")
	if l.Condition != "web work" || l.Kind != "restriction" || l.Origin != "stated" || l.Source != "session" {
		t.Fatalf("new line = %+v", l)
	}
	if e := effects(l.UID); len(e) != 1 || e[0].Weight != 0 || e[0].PSup != 1 {
		t.Fatalf("its effects: %+v", e)
	}
	if v := p.beliefs([]agentdb.Line{l})[0]; v.Mu != 0.8 {
		t.Fatalf("mu = %v, want 0.8", v.Mu)
	}
	sig.Kind = "chose"
	p.match(sig, "SQL or JSONL?", "")
	if err := wordWith(t, p, sig.SessionID, compassWordIn{Lines: []string{"preference :: a b " + strings.Repeat("x", 300)}}); err != nil {
		t.Fatal(err)
	}
	l = lineOf("a b " + strings.Repeat("x", 236))
	if e := effects(l.UID); len([]rune(l.Body)) != 240 || len(e) != 1 || e[0].Kind != "chose" {
		t.Fatalf("a long line = %+v, effects %+v", l, e)
	}
	// A line that cannot be kept is refused and the request stays.
	p.match(sig, "", "")
	if err := wordWith(t, p, sig.SessionID, compassWordIn{Lines: []string{"preference :: use key sk-ant-abcdefghijklmnopqrstuvwxyz0123"}}); err == nil || p.wording[sig.SessionID] == nil {
		t.Fatalf("a secret line: %v", err)
	}
	// An unknown kind is a preference.
	if err := wordWith(t, p, sig.SessionID, compassWordIn{Lines: []string{"rule :: Prefer small PRs"}}); err != nil {
		t.Fatal(err)
	}
	if l := lineOf("Prefer small PRs"); l.Kind != "preference" {
		t.Fatalf("unknown kind: %+v", l)
	}
	// A model that fails leaves the signal in the ledger too.
	p.testDecider = failDecider{}
	before = len(signalKinds(t, st))
	if got := p.match(sig, "", ""); got != "" || len(signalKinds(t, st)) != before+1 {
		t.Fatalf("model error: %q", got)
	}
}

func TestOwnerPromptsBecomeSignals(t *testing.T) {
	p, st := twoTeams(t)
	lead, _ := p.session("leadaaaaaaaa")
	agent := "Which do you want, SQL or JSONL?"
	word := func(line string) {
		t.Helper()
		if err := wordWith(t, p, lead.ID, compassWordIn{Lines: []string{line}}); err != nil {
			t.Fatal(err)
		}
	}
	place := func(f fakeTriage, prompt string) {
		p.mu.Lock()
		p.testDecider = f
		p.mu.Unlock()
		p.placeTurn(context.Background(), lead, turn{Prompt: prompt}, agent)
	}
	kinds := func() string { return strings.Join(signalKinds(t, st), ",") }

	place(fakeTriage{signal: "instruction"}, "add a login page")
	if kinds() != "" {
		t.Fatalf("an instruction became a signal: %s", kinds())
	}
	place(fakeTriage{signal: "preference"}, "keep the UI simple, no extra panels")
	word("preference :: Keep the UI simple")
	if ls := p.lines("", 0); kinds() != "said" || len(ls) != 1 || ls[0].Origin != "stated" {
		t.Fatalf("a preference: %s, %+v", kinds(), ls)
	}
	// A correction moves the Compass only when it changes a preference.
	place(fakeTriage{signal: "correction", changes: "no"}, "no, that panel was a mistake")
	if kinds() != "said" {
		t.Fatalf("a one-off correction: %s", kinds())
	}
	place(fakeTriage{signal: "correction", changes: "yes", line: "supports"}, "no, keep the UI simple, drop the panel")
	es, _ := p.db.Effects()
	uid := p.lines("", 0)[0].UID
	if kinds() != "said,correction" || len(es[uid]) != 2 || !slices.ContainsFunc(es[uid], func(e agentdb.Effect) bool { return e.Kind == "correction" && e.Weight == agentdb.WeightCorrect }) {
		t.Fatalf("a correction that changes a preference: %s, %+v", kinds(), es[uid])
	}
	// A choice is a chose signal, except an answer to a recorded check-in, which learnDecision counts.
	place(fakeTriage{signal: "instruction"}, "which log format?")
	agent = "Done: SQL it is, and the migration is written."
	place(fakeTriage{signal: "choice", line: "unrelated"}, "SQL")
	word("preference :: Use SQL for logs")
	if kinds() != "said,correction,chose" {
		t.Fatalf("a choice: %s", kinds())
	}
	id, _ := p.db.AddGate(agentdb.Gate{TeamID: "team1", SessionID: lead.ID, Kind: "checkin", Question: "SQL or JSONL?"})
	if ok, _ := p.db.ClaimGate(id, "JSONL"); !ok {
		t.Fatal("the gate was not claimed")
	}
	place(fakeTriage{signal: "choice", line: "unrelated"}, "JSONL")
	if kinds() != "said,correction,chose" {
		t.Fatalf("a check-in answer was counted twice: %s", kinds())
	}
}

func TestOwnerChoicesInTheApp(t *testing.T) {
	p, st := twoTeams(t)
	p.testDecider = &lineDecider{}
	rpc := func(typ string, params any) {
		t.Helper()
		raw, _ := json.Marshal(params)
		if _, err := p.RPC(context.Background(), gateway.Caller{Owner: true}, typ, raw); err != nil {
			t.Fatal(err)
		}
	}
	// An approval has no session to ask: it waits in the ledger.
	want := func(words string, n int) {
		t.Helper()
		var k []string
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if k = signalKinds(t, st); len(k) >= n {
				break
			}
		}
		if len(k) != n || k[n-1] != "chose" || !slices.ContainsFunc(signalWords(p), func(w string) bool { return strings.Contains(w, words) }) {
			t.Fatalf("signals = %v, %v; want %q", k, signalWords(p), words)
		}
	}
	_ = st.Update(func(s *store.State) error {
		s.Domain.Approvals = append(s.Domain.Approvals,
			protocol.Approval{ID: "ap1", Kind: "formation", TeamID: "team1", State: "pending", Detail: "app: add web", Change: &protocol.FormationChange{Op: "anchor", SessionID: "webaaaaaaaaa"}}, // an older record's request to choose the lead: approving it changes nothing
			protocol.Approval{ID: "ap2", Kind: "formation", TeamID: "team1", State: "pending", Detail: "app: add a third"})
		return nil
	})
	rpc("approval.decide", decideIn{ID: "ap1", Decision: "approve"})
	want("approved: app: add web", 1)
	rpc("approval.decide", decideIn{ID: "ap2", Decision: "deny"})
	want("denied: app: add a third", 2)
}

// sameDecider answers a match's line questions as unrelated and its same_N questions with same.
type sameDecider struct {
	calibrated bool
	same       decision.Answer
}

func (d sameDecider) Name() string { return "same" }
func (d sameDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	out := map[string]decision.Answer{}
	for _, q := range req.Questions {
		if strings.HasPrefix(q.Name, "same_") {
			out[q.Name] = d.same
		} else {
			out[q.Name] = decision.Answer{Type: decision.Choice, Choice: "unrelated", Probabilities: map[string]float64{"unrelated": 1}}
		}
	}
	return decision.Response{Answers: out, Calibrated: d.calibrated}, nil
}

func pYes(p float64) decision.Answer { return decision.Answer{Type: decision.Predicate, Yes: p} }

// A new line the decision model judges the same preference joins its older neighbour (plan C2).
func TestMatchMergesTheSamePreference(t *testing.T) {
	for _, c := range []struct {
		name       string
		calibrated bool
		same       decision.Answer
		merged     bool
	}{
		{"calibrated", true, pYes(0.9), true},
		{"uncalibrated yes", false, pYes(1), true},
		{"below 0.8", true, pYes(0.79), false},
		{"another condition", false, pYes(0), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := twoTeams(t)
			old, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Prefer standard library over third-party packages", Origin: "stated", CreatedAt: "2026-01-01T00:00:00Z"})
			p.record(agentdb.Signal{Kind: "said", Words: "earlier"}, agentdb.WeightSaid, effectOn{old, 1, 0})
			p.testDecider = sameDecider{c.calibrated, c.same}
			p.match(agentdb.Signal{Kind: "said", Words: "no new third-party packages please", SessionID: "leadaaaaaaaa"}, "", "")
			if err := wordWith(t, p, "leadaaaaaaaa", compassWordIn{Lines: []string{"preference :: The owner prohibits adding new third-party packages"}}); err != nil {
				t.Fatal(err)
			}
			ls, _ := p.db.Lines(p.owner(), "", "", 0)
			if !c.merged {
				if len(ls) != 2 {
					t.Fatalf("%d lines", len(ls))
				}
				return
			}
			es, _ := p.db.Effects()
			if len(ls) != 1 || ls[0].UID != old || ls[0].Body != "Prefer standard library over third-party packages" || len(es) != 1 || len(es[old]) != 2 {
				t.Fatalf("lines %+v, effects %+v", ls, es)
			}
		})
	}
}

func TestCompassMergeRPC(t *testing.T) {
	p, _ := twoTeams(t)
	a, b := stated(t, p, "Keep the UI simple"), stated(t, p, "Few panels")
	p.record(agentdb.Signal{Kind: "said", Words: "w"}, agentdb.WeightSaid, effectOn{b, 1, 0})
	if _, err := call(t, p, false, "compass.merge", map[string]any{"keep": a, "merge": b}); err == nil {
		t.Fatal("a non-owner merged")
	}
	for _, bad := range []map[string]any{{"keep": a, "merge": a}, {"keep": a, "merge": "nope"}} {
		if _, err := call(t, p, true, "compass.merge", bad); err == nil {
			t.Errorf("merged %v", bad)
		}
	}
	if _, err := call(t, p, true, "compass.merge", map[string]any{"keep": a, "merge": b}); err != nil {
		t.Fatal(err)
	}
	if es, _ := p.db.Effects(a); len(es[a]) != 1 || len(p.lines("", 0)) != 1 {
		t.Fatalf("effects %+v", es)
	}
}

// An owner's standing preference is a signal even when triage reads it as a task or gives no answer
// (the owner's words that a 4B model took for an instruction); a plain task stays none.
func TestStandingPreferenceDespiteTriage(t *testing.T) {
	p, st := twoTeams(t)
	lead, _ := p.session("leadaaaaaaaa")
	place := func(d decision.Provider, prompt string) {
		p.mu.Lock()
		p.testDecider = d
		p.mu.Unlock()
		p.placeTurn(context.Background(), lead, turn{Prompt: prompt}, "ok")
	}
	kinds := func() string { return strings.Join(signalKinds(t, st), ",") }
	place(fakeTriage{signal: "instruction"}, "fix the failing build and push it")
	if kinds() != "" {
		t.Fatalf("a task became a signal: %s", kinds())
	}
	place(fakeTriage{signal: "instruction"}, "If agent has recommended solution, and the risk is low, just approve for me")
	if err := wordWith(t, p, lead.ID, compassWordIn{Lines: []string{"preference :: Approve a low-risk recommended solution without asking"}}); err != nil {
		t.Fatal(err)
	}
	if ls := p.lines("", 0); kinds() != "said" || len(ls) != 1 || ls[0].Origin != "stated" {
		t.Fatalf("the standing preference: %s, %+v", kinds(), ls)
	}
	// Triage that fails still reaches the session's agent (no line to judge), and its none leaves the
	// signal in the ledger rather than dropping it.
	place(failDecider{}, "from now on, run the tests before every commit")
	if err := wordWith(t, p, lead.ID, compassWordIn{None: true}); err != nil || kinds() != "said,said" {
		t.Fatalf("with triage down: %s", kinds())
	}
}

// standingClause finds the sentence that states a rule, not a word a task happens to use.
func TestStandingClause(t *testing.T) {
	for prompt, want := range map[string]string{
		"If agent has recommended solution, and the risk is low, just approve for me":                    "If agent has recommended solution, and the risk is low, just approve for me",
		"From now on, run the tests before every commit":                                                 "From now on, run the tests before every commit",
		"from now on: approve low-risk changes":                                                          "from now on: approve low-risk changes",
		"Don’t ask me before low-risk edits":                                                             "Don't ask me before low-risk edits",
		"Do not\nask me about formatting":                                                                "Do not ask me about formatting",
		"You should always run the linter first":                                                         "You should always run the linter first",
		"Never push to main without a PR":                                                                "Never push to main without a PR",
		"Whenever the tests pass, go ahead and merge for me":                                             "Whenever the tests pass, go ahead and merge for me",
		strings.Repeat("Refactor the loader and its tests. ", 12) + "Going forward, keep the UI simple.": "Going forward, keep the UI simple",
		"Fix the parser so it never returns nil":                                                         "",
		"never mind, use the old loader":                                                                 "",
		"the test fails every time I run it":                                                             "",
		"make sure the cache always invalidates on write":                                                "",
		"the flag is off by default; add a CLI option":                                                   "",
		"merge it for me":                           "",
		"Why does the agent always ask me twice?":   "",
		"Investigate why retries never stop":        "",
		"Please approve this for me just this once": "",
		"add a login page":                          "",
		"remember my choice for the common route, if the pr review agent is in team and agrees, you can go ahead merge and release": "remember my choice for the common route, if the pr review agent is in team and agrees, you can go ahead merge and release",
		"When CI passes, you can go ahead and merge": "When CI passes, you can go ahead and merge",
		"does the reviewer agree? you can go ahead":  "",
		"remember to run the tests":                  "",
	} {
		if got := standingClause(prompt); got != want {
			t.Errorf("%q: %q, want %q", prompt, got, want)
		}
	}
}

// A task that also states a rule: triage says instruction + states_preference, and the session's
// agent words the rule from the whole message; without the second answer nothing is kept.
func TestMixedMessageStatesPreference(t *testing.T) {
	p, st := twoTeams(t)
	lead, _ := p.session("leadaaaaaaaa")
	place := func(f fakeTriage, prompt string) {
		p.mu.Lock()
		p.testDecider = f
		p.mu.Unlock()
		p.placeTurn(context.Background(), lead, turn{Prompt: prompt}, "ok")
	}
	task := "fix the loader, and keep the diff small to review"
	place(fakeTriage{signal: "instruction"}, task)
	if len(signalKinds(t, st)) != 0 {
		t.Fatal("a plain task became a signal")
	}
	place(fakeTriage{signal: "instruction", states: "yes"}, task+", never touch the vendored code")
	word := compassWordIn{Lines: []string{"preference :: Merge and release when the PR review agent agrees"}}
	if err := wordWith(t, p, lead.ID, word); err != nil {
		t.Fatal(err)
	}
	if ls, ks := p.lines("", 0), signalKinds(t, st); len(ks) != 1 || ks[0] != "said" || len(ls) != 1 || ls[0].Origin != "stated" {
		t.Fatalf("a mixed message: %v, %+v", signalKinds(t, st), ls)
	}
	// The regex alone catches the quoted message, whatever triage says.
	place(fakeTriage{signal: "instruction"}, "remember my choice for the common route, if the pr review agent is in team and agrees, you can go ahead merge and release")
	if err := wordWith(t, p, lead.ID, word); err != nil || len(signalKinds(t, st)) != 2 {
		t.Fatalf("the quoted message: %v", signalKinds(t, st))
	}
}

// Without a decision model every owner prompt reaches the session's own agent, with the lines it may
// touch: it links lines, words up to 3 new ones, once, and only the asked session may.
func TestSessionWordsWithoutDecider(t *testing.T) {
	p, st := twoTeams(t)
	lead, _ := p.session("leadaaaaaaaa")
	ui := stated(t, p, "Keep the UI simple")
	good := compassWordIn{Supports: []string{ui}, Lines: []string{"restriction :: Never merge without the review agent :: ", "preference :: Run the tests before every commit :: on the loader"}}
	if err := wordWith(t, p, lead.ID, good); err == nil {
		t.Fatal("a wording nobody asked for was taken")
	}
	p.placeTurn(context.Background(), lead, turn{Prompt: "From now on keep the UI simple and never merge without the review agent"}, "ok")
	if len(p.lines("", 0)) != 1 || len(signalKinds(t, st)) != 0 {
		t.Fatal("something was kept before the agent answered")
	}
	note := p.contextOut(lead.ID, "UserPromptSubmit", p.wordingNote(lead.ID)).Stdout
	for _, want := range []string{"[PenguPool compass]", "never merge without the review agent", "[" + ui + "] Keep the UI simple", "--supports", "--line", "--for", "--none"} {
		if !strings.Contains(note, want) {
			t.Fatalf("the note lacks %q: %q", want, note)
		}
	}
	if err := wordWith(t, p, "webaaaaaaaaa", good); err == nil {
		t.Fatal("another session's wording was taken")
	}
	if err := wordWith(t, p, lead.ID, compassWordIn{Lines: []string{"restriction :: no"}}); err == nil || len(p.lines("", 0)) != 1 {
		t.Fatalf("a bare pick was taken: %v", err)
	}
	// A line that was not offered cannot be linked, nor one that is no line of the owner's.
	bad := good
	bad.Supports = []string{"nope"}
	if err := wordWith(t, p, lead.ID, bad); err == nil || len(p.lines("", 0)) != 1 {
		t.Fatalf("a line that was not offered was linked: %v", err)
	}
	other := stated(t, p, "Few panels")
	bad.Supports = []string{other}
	if err := wordWith(t, p, lead.ID, bad); err == nil || len(p.lines("", 0)) != 2 {
		t.Fatalf("a line that was not offered was linked: %v", err)
	}
	if err := wordWith(t, p, lead.ID, compassWordIn{Lines: []string{"preference :: one two three", "preference :: one two three", "preference :: one two three", "preference :: one two three"}}); err == nil {
		t.Fatal("four lines were taken")
	}
	// Two lines and a link: both lines kept, one signal, with its effect on the linked line.
	if err := wordWith(t, p, lead.ID, good); err != nil {
		t.Fatal(err)
	}
	ls := p.lines("", 0)
	if len(ls) != 4 || len(signalKinds(t, st)) != 1 {
		t.Fatalf("lines %d, signals %v", len(ls), signalKinds(t, st))
	}
	for _, v := range ls {
		if v.Body == "Never merge without the review agent" && (v.Kind != "restriction" || v.Origin != "stated" || v.Source != "session") {
			t.Fatalf("the session's line: %+v", v)
		}
	}
	if es, _ := p.db.Effects(ui); len(es[ui]) != 1 || es[ui][0].PSup != 1 || es[ui][0].Weight != agentdb.WeightSaid {
		t.Fatalf("the linked line's effects: %+v", es[ui])
	}
	if err := wordWith(t, p, lead.ID, good); err == nil || len(p.lines("", 0)) != 4 {
		t.Fatalf("a second wording was taken: %v", err)
	}
	// --none clears the request and the signal waits in the ledger.
	p.placeTurn(context.Background(), lead, turn{Prompt: "Always use tabs for indentation"}, "ok")
	if err := wordWith(t, p, lead.ID, compassWordIn{None: true}); err != nil {
		t.Fatal(err)
	}
	if err := wordWith(t, p, lead.ID, good); err == nil || len(p.lines("", 0)) != 4 || len(signalKinds(t, st)) != 2 {
		t.Fatalf("after --none: %v, %d lines, %v", err, len(p.lines("", 0)), signalKinds(t, st))
	}
}

// A note nobody answers is offered at the session's next prompts, then dropped: the signal waits in
// the ledger.
func TestWordingGivenUp(t *testing.T) {
	p, st := twoTeams(t)
	lead, _ := p.session("leadaaaaaaaa")
	p.askWording(agentdb.Signal{Kind: "said", Words: "keep the UI simple", SessionID: lead.ID}, nil, 0)
	for i := 0; i < maxOffers; i++ {
		if !strings.Contains(p.wordingNote(lead.ID), "keep the UI simple") || len(signalKinds(t, st)) != 0 {
			t.Fatalf("prompt %d: no note, or the signal was kept", i+1)
		}
	}
	if note := p.wordingNote(lead.ID); note != "" || len(signalKinds(t, st)) != 1 || p.wording[lead.ID] != nil {
		t.Fatalf("not given up: %q, %v", note, signalKinds(t, st))
	}
	if err := wordWith(t, p, lead.ID, compassWordIn{Lines: []string{"preference :: Keep the UI simple"}}); err == nil {
		t.Fatal("a wording given up on was taken")
	}
}

// A pick in Claude's AskUserQuestion is a "chose" signal; the session's agent says whether it is lasting.
func TestAskUserQuestionPick(t *testing.T) {
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Harness = harness.Claude
		}
		return nil
	})
	lead, _ := p.session("leadaaaaaaaa")
	p.testDecider = &lineDecider{}
	payload := `{"tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which log format?","header":"Format","multiSelect":false,"options":[{"label":"SQL","description":"queryable"},{"label":"JSONL","description":"simple"}]}]},
	"tool_response":{"answers":{"Which log format?":"SQL"},"annotations":{"Which log format?":{"notes":"always, for every service"}}}}`
	var pl map[string]any
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		t.Fatal(err)
	}
	pick := func() {
		p.askedPicks(lead, pl)
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if note := p.wordingNote(lead.ID); note != "" {
				if !strings.Contains(note, "SQL (always, for every service)") {
					t.Fatalf("the note lacks the pick: %q", note)
				}
				return
			}
		}
		t.Fatal("the pick was not matched")
	}
	pick()
	if err := wordWith(t, p, lead.ID, compassWordIn{Lines: []string{"preference :: Use SQL for logs"}}); err != nil {
		t.Fatal(err)
	}
	if ls := p.lines("", 0); len(ls) != 1 || ls[0].Body != "Use SQL for logs" || ls[0].Origin != "stated" {
		t.Fatalf("lines: %+v", ls)
	}
	pick()
	if err := wordWith(t, p, lead.ID, compassWordIn{None: true}); err != nil || len(p.lines("", 0)) != 1 {
		t.Fatalf("a one-off pick made a line: %v", err)
	}
	delete(pl, "tool_response") // no answers: nothing asked
	p.askedPicks(lead, pl)
	time.Sleep(200 * time.Millisecond)
	if p.wordingNote(lead.ID) != "" {
		t.Fatal("a payload with no answers made a signal")
	}
}

// The agent's question is context, never the owner's words: a claim in it must not reach the signal.
func TestAskUserQuestionClaimNotSignal(t *testing.T) {
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Harness = harness.Claude
		}
		return nil
	})
	lead, _ := p.session("leadaaaaaaaa")
	p.testDecider = &lineDecider{}
	payload := `{"tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"It is the same thing: the marketplace is a plugin marketplace. Rename it?","options":[{"label":"Rename","description":"use the new name"}]}]},
	"tool_response":{"answers":{"It is the same thing: the marketplace is a plugin marketplace. Rename it?":"I doubt that is the same thing"}}}`
	var pl map[string]any
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		t.Fatal(err)
	}
	p.askedPicks(lead, pl)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if note := p.wordingNote(lead.ID); note != "" {
			if !strings.Contains(note, "I doubt that is the same thing") || strings.Contains(note, "plugin marketplace") {
				t.Fatalf("the signal's words: %q", note)
			}
			return
		}
	}
	t.Fatal("the pick was not matched")
}

// words are the owner's signals' words, newest first.
func signalWords(p *Pool) (out []string) {
	ss, _ := p.db.RecentSignals(p.owner(), 100)
	for _, s := range ss {
		out = append(out, s.Words)
	}
	return out
}

// A line the owner's signals drive to dropped is deleted with the signals only it had; a signal that
// still touches another line and one waiting in the ledger stay.
func TestDroppedLineIsDeleted(t *testing.T) {
	p, _ := twoTeams(t)
	own := p.owner()
	gone, _ := p.db.PutLine(agentdb.Line{Owner: own, Body: "Always add panels", Origin: "chose"})
	other := stated(t, p, "Keep it small")
	confirm(t, p, gone, 8, 0, 1) // eight own signals against it, one a day
	_, _ = p.db.AddSignal(agentdb.Signal{Owner: own, Kind: "said", Words: "ledger"})
	p.record(agentdb.Signal{Kind: "said", Words: "shared"}, agentdb.WeightSaid, effectOn{other, 1, 0}, effectOn{gone, 0, 1})
	if _, err := p.db.Line(gone); err != agentdb.ErrNotFound {
		t.Fatalf("the dropped line stayed: %v", err)
	}
	if _, err := p.db.Line(other); err != nil {
		t.Fatal("the other line went")
	}
	if w := signalWords(p); len(w) != 2 || !slices.Contains(w, "shared") || !slices.Contains(w, "ledger") {
		t.Fatalf("signals = %v", w)
	}
	if es, _ := p.db.Effects(gone, other); len(es[gone]) != 0 || len(es[other]) != 1 {
		t.Fatalf("effects = %+v", es)
	}
}

// The owner deleting a line takes the signals only it had.
func TestOwnerDeleteTakesItsOwnSignals(t *testing.T) {
	p, _ := twoTeams(t)
	a := stated(t, p, "short replies")
	b := stated(t, p, "no emojis")
	p.record(agentdb.Signal{Kind: "said", Words: "mine"}, agentdb.WeightSaid, effectOn{a, 1, 0})
	p.record(agentdb.Signal{Kind: "said", Words: "both"}, agentdb.WeightSaid, effectOn{a, 1, 0}, effectOn{b, 1, 0})
	if _, err := call(t, p, true, "compass.delete", map[string]any{"uid": a}); err != nil {
		t.Fatal(err)
	}
	if w := signalWords(p); len(w) != 1 || w[0] != "both" {
		t.Fatalf("signals = %v", w)
	}
}

// Lines dropped before the Compass stopped keeping them go when the pool opens (sweepDropped over all lines).
func TestStartupSweepsDroppedLines(t *testing.T) {
	p, _ := twoTeams(t)
	gone, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Always add panels", Origin: "chose"})
	confirm(t, p, gone, 8, 0, 1)
	ls, _ := p.db.Lines(p.owner(), "", "", 0)
	p.sweepDropped(ls)
	if _, err := p.db.Line(gone); err != agentdb.ErrNotFound {
		t.Fatalf("the dropped line stayed: %v", err)
	}
	if w := signalWords(p); len(w) != 0 {
		t.Fatalf("signals = %v", w)
	}
	// An owner's line goes like any other once belief drops it.
	kept, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Always add charts", Origin: "chose", Source: "owner"})
	confirm(t, p, kept, 8, 0, 1)
	ls, _ = p.db.Lines(p.owner(), "", "", 0)
	p.sweepDropped(ls)
	if _, err := p.db.Line(kept); err != agentdb.ErrNotFound {
		t.Fatalf("the owner's dropped line stayed: %v", err)
	}
}

// An answer must name the note it read, and the agent's contradiction moves neither a restriction nor an owner's line.
func TestSessionWordingGuards(t *testing.T) {
	p, _ := twoTeams(t)
	lead, _ := p.session("leadaaaaaaaa")
	rs, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Kind: "restriction", Body: "Never push to main without asking", Origin: "stated"})
	pin, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Keep the UI simple", Origin: "stated", Source: "owner"})
	sig := agentdb.Signal{Kind: "said", Words: "push to main is fine now", SessionID: lead.ID}
	p.askWording(sig, []agentdb.Line{{UID: rs}, {UID: pin}}, 0)
	if err := wordWith(t, p, lead.ID, compassWordIn{For: "nope", None: true}); err == nil {
		t.Fatal("a wording for another note was taken")
	}
	if err := wordWith(t, p, lead.ID, compassWordIn{Contradicts: []string{rs, pin}}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{rs, pin} {
		if es, _ := p.db.Effects(uid); len(es[uid]) != 0 {
			t.Fatalf("an agent's contradiction moved %s: %+v", uid, es[uid])
		}
	}
}

// An older session-worded line merged with a newer owner line leaves the owner's line, body and uid:
// the agent's wording is never made trusted by a merge.
func TestMergeSameKeepsTheOwnersLine(t *testing.T) {
	p, _ := twoTeams(t)
	_, _ = p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Prefer standard library over third-party packages", Source: "session", CreatedAt: "2026-01-01T00:00:00Z"})
	mine, _ := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Prefer standard library, no third-party packages", Source: "owner", CreatedAt: "2026-02-01T00:00:00Z"})
	if got := p.mergeSame(sameDecider{true, pYes(0.9)}, "leadaaaaaaaa", mine); got != mine {
		t.Fatalf("mergeSame = %s, want %s", got, mine)
	}
	ls, _ := p.db.Lines(p.owner(), "", "", 0)
	if len(ls) != 1 || ls[0].UID != mine || ls[0].Body != "Prefer standard library, no third-party packages" || ls[0].Source != "owner" {
		t.Fatalf("lines = %+v", ls)
	}
}

// A change of mind that only contradicts a line becomes a line of its own (plan PR B step 0): the
// owner's yellow/red case. The contradiction is in the ledger at once, and the worded line links to
// the same signal.
func TestMatchChangeOfMindMakesALine(t *testing.T) {
	p, st := twoTeams(t)
	effects := func(uid string) []agentdb.Effect { es, _ := p.db.Effects(uid); return es[uid] }
	yellow := stated(t, p, "The owner's favorite color is yellow")
	sig := agentdb.Signal{Kind: "said", Words: "red is my favorite color now", SessionID: "leadaaaaaaaa", TeamID: "team1"}
	p.testDecider = &lineDecider{calibrated: true, answers: []decision.Answer{prob(0.05, 0.9, 0.05)}}
	before := len(signalKinds(t, st))
	if got := p.match(sig, "", ""); got != "" {
		t.Fatalf("match = %q", got)
	}
	if e := effects(yellow); len(e) != 1 || e[0].PCon != 0.9 || len(signalKinds(t, st)) != before+1 || p.wording[sig.SessionID] == nil {
		t.Fatalf("the contradiction: %+v, %d signals, wording %v", e, len(signalKinds(t, st))-before, p.wording[sig.SessionID])
	}
	if err := wordWith(t, p, sig.SessionID, compassWordIn{Lines: []string{"preference :: The owner's favorite color is red"}}); err != nil {
		t.Fatal(err)
	}
	var red string
	for _, v := range p.lines("", 0) {
		if v.Body == "The owner's favorite color is red" {
			red = v.UID
		}
	}
	if red == "" || len(signalKinds(t, st)) != before+1 {
		t.Fatalf("no red line (%q), or the signal was recorded twice: %d", red, len(signalKinds(t, st))-before)
	}
	if e := effects(red); len(e) != 1 || e[0].PSup != 1 || e[0].SignalUID != effects(yellow)[0].SignalUID {
		t.Fatalf("the red line's effect: %+v", e)
	}

	// A check-in's decision (already worded) that only contradicts: kept as a line, recorded once
	// with both effects.
	blue := "The owner's favorite color is blue"
	p.testDecider = &lineDecider{calibrated: true, answers: []decision.Answer{prob(0.05, 0.9, 0.05), prob(0.05, 0.9, 0.05)}}
	got := p.matchLines(agentdb.Signal{Kind: "chose", Words: "blue then", SessionID: "leadaaaaaaaa"}, "", blue, true)
	if got == "" || len(effects(yellow)) != 2 || len(signalKinds(t, st)) != before+2 {
		t.Fatalf("a worded change of mind: %q, yellow %+v, %d signals", got, effects(yellow), len(signalKinds(t, st))-before)
	}

	// Without mk (a check-in answer that is no rule) it only weighs on the lines.
	n := len(p.lines("", 0))
	p.matchLines(agentdb.Signal{Kind: "chose", Words: "green favorite color", SessionID: "leadaaaaaaaa"}, "", "", false)
	if len(p.lines("", 0)) != n {
		t.Fatal("a one-off made a line")
	}
}
