package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// goAheadWithLine makes a team that goes ahead on an owner line, and returns its go-ahead's gate.
func goAheadWithLine(t *testing.T, roll float64) (*Pool, agentdb.Gate, string) {
	t.Helper()
	p, _, _ := gateTeam(t)
	spotRoll = func() float64 { return roll }
	t.Cleanup(func() { spotRoll = func() float64 { return 0.5 } })
	guide(t, p)
	ls, _ := p.db.Lines(p.owner(), "", "", 0)
	uid := ls[0].UID
	endTurn(p, "leadaaaaaaaa", checkInReply)
	g := lastGate(t, p, "leadaaaaaaaa")
	if g.Outcome != "auto_proceeded" {
		t.Fatalf("gate = %+v", g)
	}
	return p, g, uid
}

// effectsOf are the effects of signals of one kind.
func effectsOf(t *testing.T, p *Pool, kind string) (es []agentdb.Effect) {
	t.Helper()
	all, err := p.db.Effects()
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range all {
		for _, e := range list {
			if e.Kind == kind {
				es = append(es, e)
			}
		}
	}
	return es
}

func outcomes(t *testing.T, p *Pool) []agentdb.Outcome {
	t.Helper()
	o, err := p.db.Outcomes(p.owner())
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestGoAheadRecordsItsLinesAndSamplesSpotChecks(t *testing.T) {
	p, g, uid := goAheadWithLine(t, 0)
	if len(g.Lines) != 1 || g.Lines[0] != uid || g.Review != "spot" {
		t.Fatalf("gate = %+v", g)
	}
	if spots, _ := p.db.SpotChecks("2000-01-01T00:00:00Z"); len(spots) != 1 {
		t.Fatalf("spot checks = %+v", spots)
	}
	_, g, _ = goAheadWithLine(t, spotRate) // the draw is below the rate to be sampled
	if g.Review != "" {
		t.Fatalf("sampled at the rate itself: %+v", g)
	}
}

func TestSpotCheckVerdicts(t *testing.T) {
	p, g, uid := goAheadWithLine(t, 0)
	if _, err := call(t, p, true, "compass.spot_check", map[string]any{"gate_id": g.ID, "right": true}); err != nil {
		t.Fatal(err)
	}
	es := effectsOf(t, p, "spot_check")
	if len(es) != 1 || es[0].LineUID != uid || es[0].PSup != 1 || es[0].PCon != 0 || es[0].Weight != agentdb.WeightSpotCheck {
		t.Fatalf("effects = %+v", es)
	}
	if o := outcomes(t, p); len(o) != 1 || o[0] != agentdb.OutcomeRight || holding(p, "leadaaaaaaaa") {
		t.Fatalf("outcomes %v, hold %v", o, holding(p, "leadaaaaaaaa"))
	}
	if _, err := call(t, p, true, "compass.spot_check", map[string]any{"gate_id": g.ID, "right": false}); err == nil {
		t.Fatal("a check answered twice")
	}

	p, g, uid = goAheadWithLine(t, 0)
	if _, err := call(t, p, true, "compass.spot_check", map[string]any{"gate_id": g.ID, "right": false}); err != nil {
		t.Fatal(err)
	}
	es = effectsOf(t, p, "spot_check")
	if len(es) != 1 || es[0].LineUID != uid || es[0].PSup != 0 || es[0].PCon != 1 {
		t.Fatalf("effects = %+v", es)
	}
	if o := outcomes(t, p); len(o) != 1 || o[0] != agentdb.OutcomeWrong || !holding(p, "leadaaaaaaaa") {
		t.Fatalf("outcomes %v, hold %v", o, holding(p, "leadaaaaaaaa"))
	}
	if g, _ := p.db.Gate(g.ID); g.OwnerProceed == nil || *g.OwnerProceed {
		t.Fatalf("a wrong go-ahead is an override: %+v", g)
	}
}

func TestSpotCheckExpiresUnknown(t *testing.T) {
	p, g, _ := goAheadWithLine(t, 0)
	p.clock = func() time.Time { return time.Now().Add(spotExpiry + time.Hour) }
	res, err := call(t, p, true, "compass.spot_checks", struct{}{})
	if err != nil || len(res.([]agentdb.Gate)) != 0 {
		t.Fatalf("an expired check is still asked: %v %v", res, err)
	}
	if _, err := call(t, p, true, "compass.spot_check", map[string]any{"gate_id": g.ID, "right": false}); err == nil {
		t.Fatal("an expired check was answered")
	}
	if o := outcomes(t, p); len(o) != 0 || holding(p, "leadaaaaaaaa") {
		t.Fatalf("expiry left %v", o)
	}
}

func TestGateLines(t *testing.T) {
	p, g, uid := goAheadWithLine(t, 1)
	res, err := call(t, p, true, "gates.lines", map[string]any{"gate_id": g.ID})
	if ls, _ := res.([]gateLine); err != nil || len(ls) != 1 || ls[0].UID != uid || ls[0].Body == "" {
		t.Fatalf("lines = %+v, %v", res, err)
	}
	if _, err := call(t, p, true, "gates.lines", map[string]any{"gate_id": 9999}); err == nil {
		t.Fatal("a gate that is not there has lines")
	}
}

func TestThatWasWrong(t *testing.T) {
	p, g, uid := goAheadWithLine(t, 1)
	for range 2 { // idempotent
		if _, err := call(t, p, true, "compass.wrong", map[string]any{"gate_id": g.ID}); err != nil {
			t.Fatal(err)
		}
	}
	es := effectsOf(t, p, "correction")
	if len(es) != 1 || es[0].LineUID != uid || es[0].PSup != 0 || es[0].PCon != 1 || es[0].Weight != agentdb.WeightCorrect {
		t.Fatalf("effects = %+v", es)
	}
	if o := outcomes(t, p); len(o) != 1 || o[0] != agentdb.OutcomeWrong || !holding(p, "leadaaaaaaaa") {
		t.Fatalf("outcomes %v", o)
	}
	if g, _ := p.db.Gate(g.ID); g.OwnerProceed == nil || *g.OwnerProceed || g.Review != "wrong" {
		t.Fatalf("gate = %+v", g)
	}
	if _, err := call(t, p, true, "compass.wrong", map[string]any{"gate_id": 9999}); err == nil {
		t.Fatal("a gate that is not there was called wrong")
	}
}

func TestMissedIsSetOnlyWhenTriageHadItRight(t *testing.T) {
	for _, c := range []struct {
		name         string
		conf         float64 // triage's confidence in "proceed": below sure it asks
		ownerProceed bool
		reply        string
		missed       bool
	}{
		{"unsure but right", 0.5, true, checkInReply, true},
		{"no forming line", 0.5, true, checkInReply, false},
		{"the owner said no", 0.5, false, checkInReply, false},
		{"predicted ask", 0.5, true, "Which loader should I use, A or B?", false},
		{"the floor", 0.5, true, "Shall I git push the loader?", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, dec, _ := gateTeam(t)
			if c.name != "no forming line" { // shown but not guiding (one doubt): it would have had it right
				_, _ = p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "Go on with the loader once the parser tests pass."})
				confirm(t, p, p.lines("", 0)[0].UID, 1, 0, 1)
			}
			dec.set("proceed", yes(c.conf))
			if c.name == "predicted ask" {
				dec.set("one_proposal", no(0.9))
			}
			if c.ownerProceed {
				dec.set("owner_proceed", yes(0.9))
			} else {
				dec.set("owner_proceed", no(0.9))
			}
			const id = "leadaaaaaaaa"
			lead, _ := p.session(id)
			endTurn(p, id, c.reply)
			p.ownerReplied(lead, "yes", p.promptArrived(lead, true))
			wordRule(t, p, id, "")
			got := false
			for _, o := range outcomes(t, p) {
				got = got || o == agentdb.OutcomeMissed
			}
			if got != c.missed {
				t.Fatalf("missed = %v, gate %+v", got, lastGate(t, p, id))
			}
		})
	}
}

func TestLinesFollowLearnedThresholds(t *testing.T) {
	p, _, _ := gateTeam(t)
	guide(t, p)
	ls, _ := p.db.Lines(p.owner(), "", "", 0)
	uid := ls[0].UID
	if v := p.lines("", 0); len(v) != 1 || !v[0].guides() || v[0].State != "confident" {
		t.Fatalf("default: %+v", v)
	}
	// a chat model counts hard: its base is tighter
	cfg := p.st.Config()
	cfg.Decision.Active = "chat"
	cfg.Decision.Providers = map[string]store.ProviderConfig{"chat": {Format: "openai-chat"}}
	_ = p.st.SaveConfig(cfg)
	if got := p.thresholds().SigmaMax; got != agentdb.HardCountSigmaMax {
		t.Fatalf("sigma_max = %v", got)
	}
	cfg.Decision.Active = ""
	_ = p.st.SaveConfig(cfg)
	// 20 reviewed go-aheads, two wrong: the bar rises past this line's 0.8
	for i := range 20 {
		o := agentdb.OutcomeRight
		if i < 2 {
			o = agentdb.OutcomeWrong
		}
		at := time.Now().Add(time.Duration(i-30) * time.Minute).UTC().Format(time.RFC3339)
		p.db.AddSignal(agentdb.Signal{Owner: p.owner(), Kind: "spot_check", Outcome: o, At: at})
	}
	th := p.thresholds()
	if th.Guide <= agentdb.DefaultThresholds.Guide {
		t.Fatalf("thresholds = %+v", th)
	}
	if v := p.lines("", 0); v[0].UID != uid || v[0].guides() || v[0].State == "confident" {
		t.Fatalf("learned: %+v", v[0])
	}
	if res, _ := call(t, p, true, "compass.list", map[string]any{}); res.(compassView).Thresholds != th || !strings.Contains(res.(compassView).Autonomy, "sure") {
		t.Fatalf("list = %+v", res)
	}
}
