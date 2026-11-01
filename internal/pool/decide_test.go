package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// typedDecider answers each question by its type: yes, the first option in name order, level 1.
type typedDecider struct{ got decision.Request }

func (d *typedDecider) Name() string { return "fake/systemone" }
func (d *typedDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.got = req
	a := map[string]decision.Answer{}
	for _, q := range req.Questions {
		switch q.Type {
		case decision.Predicate:
			a[q.Name] = decision.Answer{Type: q.Type, Yes: 0.9}
		case decision.Choice:
			a[q.Name] = decision.Answer{Type: q.Type, Choice: "keep"}
		case decision.Score:
			a[q.Name] = decision.Answer{Type: q.Type, Score: 1}
		}
	}
	return decision.Response{Answers: a, Provider: d.Name(), Model: "m1"}, nil
}

func ask(p *Pool, session string, pack string) (any, error) {
	var in askIn
	if err := json.Unmarshal([]byte(pack), &in); err != nil {
		panic(err)
	}
	in.SessionID = session
	return p.decisionAsk(context.Background(), gateway.Caller{}, in)
}

const okPack = `{"state":{"diff":"+1 line"},"questions":[
	{"name":"is_risky","type":"predicate","instructions":"Is the change risky?"},
	{"name":"verdict","type":"choice","instructions":"Keep it?","options":{"keep":"fine","drop":"not fine"}},
	{"name":"jev-quality","type":"score","instructions":"How good?","levels":["poor","ok","great"]}]}`

func TestDecideRoundTrip(t *testing.T) {
	p, _ := twoTeams(t)
	var logs []string
	p.logf = func(f string, a ...any) { logs = append(logs, f) }
	d := &typedDecider{}
	p.testDecider = d
	res, err := ask(p, "planaaaaaaaa", okPack)
	if err != nil {
		t.Fatal(err)
	}
	r := res.(decision.Response)
	if r.Answers["is_risky"].Yes != 0.9 || r.Answers["verdict"].Choice != "keep" || r.Answers["jev-quality"].Score != 1 || r.Model != "m1" {
		t.Fatalf("answers %+v", r)
	}
	if st, _ := d.got.State.(map[string]any); st["diff"] != "+1 line" || len(d.got.Questions) != 3 {
		t.Fatalf("the model got %+v", d.got)
	}
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "pool: decide: ") {
		t.Fatalf("logs %v", logs)
	}
	if _, err := ask(p, "nobodyaaaaaa", okPack); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	// The owner may ask without a session.
	if _, err := p.decisionAsk(context.Background(), gateway.Caller{Owner: true}, askIn{Questions: []decision.Question{{Name: "x", Type: "predicate", Instructions: "?"}}}); err != nil {
		t.Fatalf("owner: %v", err)
	}
}

func TestDecideLimits(t *testing.T) {
	p, _ := twoTeams(t)
	p.testDecider = &typedDecider{}
	q := func(name, typ, extra string) string {
		return `{"name":"` + name + `","type":"` + typ + `","instructions":"?"` + extra + `}`
	}
	many := func(n int, s string) string { return strings.TrimSuffix(strings.Repeat(s+",", n), ",") }
	opts := func(n int) string {
		var o []string
		for i := range n {
			o = append(o, `"o`+string(rune('a'+i))+`":""`)
		}
		return `,"options":{` + strings.Join(o, ",") + `}`
	}
	var qs13 []string
	for i := range 13 {
		qs13 = append(qs13, q("q"+string(rune('a'+i)), "predicate", ""))
	}
	for name, c := range map[string]struct{ pack, want string }{
		"state":          {`{"state":"` + strings.Repeat("x", 33<<10) + `","questions":[` + q("a", "predicate", "") + `]}`, "32 KiB"},
		"none":           {`{"questions":[]}`, "1 to 12"},
		"too many":       {`{"questions":[` + strings.Join(qs13, ",") + `]}`, "1 to 12"},
		"name chars":     {`{"questions":[` + q("Bad Name", "predicate", "") + `]}`, "a-z, 0-9"},
		"name length":    {`{"questions":[` + q(strings.Repeat("a", 65), "predicate", "") + `]}`, "up to 64"},
		"duplicate":      {`{"questions":[` + q("a", "predicate", "") + `,` + q("a", "predicate", "") + `]}`, "used twice"},
		"questions size": {`{"questions":[` + q("a", "choice", `,"options":{"a":"`+strings.Repeat("x", 33<<10)+`","b":""}`) + `]}`, "questions are limited to 32 KiB"},
		"instructions":   {`{"questions":[{"name":"a","type":"predicate","instructions":"` + strings.Repeat("x", 2049) + `"}]}`, "2 KiB"},
		"options":        {`{"questions":[` + q("a", "choice", opts(13)) + `]}`, "2 to 12 options"},
		"levels":         {`{"questions":[` + q("a", "score", `,"levels":[`+many(11, `"l"`)+`]`) + `]}`, "2 to 10 levels"},
		"type":           {`{"questions":[` + q("a", "guess", "") + `]}`, "predicate, choice or score"},
	} {
		_, err := ask(p, "planaaaaaaaa", c.pack)
		if protocol.CodeOf(err) != protocol.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ask(p, "planaaaaaaaa", `{"questions":[`+q("a", "choice", opts(12))+`]}`); err != nil {
		t.Errorf("12 options: %v", err)
	}
}

func TestDecideWithoutModel(t *testing.T) {
	p, _ := twoTeams(t) // the test store configures no provider
	_, err := ask(p, "planaaaaaaaa", okPack)
	if protocol.CodeOf(err) != protocol.CodeNoDecider || !strings.Contains(err.Error(), "Settings → Decision model") {
		t.Fatalf("no provider: %v", err)
	}
}

func TestDecideRateLimit(t *testing.T) {
	p, _ := twoTeams(t)
	p.testDecider = &typedDecider{}
	for i := range askPerHour {
		if _, err := ask(p, "planaaaaaaaa", okPack); err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
	}
	if _, err := ask(p, "planaaaaaaaa", okPack); protocol.CodeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "decision limit reached (30 an hour)") {
		t.Fatalf("31st ask: %v", err)
	}
	if _, err := ask(p, "webaaaaaaaaa", okPack); err != nil {
		t.Fatalf("another session has its own limit: %v", err)
	}
	if err := p.askAllowed("planaaaaaaaa", time.Now().Add(time.Hour+time.Second)); err != nil {
		t.Fatalf("the limit did not reset after an hour: %v", err)
	}
}

// All sessions together get 120 asks an hour.
func TestDecideHostLimit(t *testing.T) {
	p, _ := twoTeams(t)
	now := time.Now()
	for i := range askHostPerHour {
		if err := p.askAllowed(fmt.Sprintf("s%d", i/askPerHour), now); err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
	}
	if err := p.askAllowed("fresh", now); protocol.CodeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "120 an hour") {
		t.Fatalf("121st ask on the host: %v", err)
	}
}

// A failed ask is logged by its class only, never with what the provider said.
func TestDecideFailureLogsClassOnly(t *testing.T) {
	p, _ := twoTeams(t)
	var logs []string
	p.logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	p.testDecider = failDecider{}
	if _, err := ask(p, "planaaaaaaaa", okPack); protocol.CodeOf(err) != protocol.CodeProviderRefused {
		t.Fatalf("err %v", err)
	}
	if len(logs) != 1 || !strings.HasSuffix(logs[0], ": provider error") {
		t.Fatalf("logs %q", logs)
	}
}

func TestDecideInHelperContextOnly(t *testing.T) {
	p, _ := helperPool(t) // plan is the sk-plan helper's session
	if c := p.renderContext("planaaaaaaaa"); !strings.Contains(c, "`pengupool decide` asks the owner's decision model") {
		t.Fatalf("helper context:\n%s", c)
	}
	if c := p.renderContext("leadaaaaaaaa"); strings.Contains(c, "decide`") {
		t.Fatal("a main worker is taught decide")
	}
}
