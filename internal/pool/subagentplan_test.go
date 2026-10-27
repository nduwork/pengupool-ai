package pool

import (
	"context"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// planDecider answers the three subagent questions with fixed choices at probability pr.
type planDecider struct {
	pr         float64
	calibrated bool
	asked      int
}

func (d *planDecider) Name() string { return "plan" }
func (d *planDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.asked++
	pick := map[string]string{"subagent_kind": "Explore", "model": "haiku", "effort": "low"}
	out := map[string]decision.Answer{}
	for _, q := range req.Questions {
		out[q.Name] = decision.Answer{Type: decision.Choice, Choice: pick[q.Name], Probabilities: map[string]float64{pick[q.Name]: d.pr}}
	}
	return decision.Response{Answers: out, Calibrated: d.calibrated}, nil
}

func TestSubagentPlan(t *testing.T) {
	p, st := twoTeams(t)
	setHarness := func(h string) {
		_ = st.Update(func(s *store.State) error {
			for i := range s.Sessions {
				s.Sessions[i].Harness = h
				if s.Sessions[i].ID == "planaaaaaaaa" {
					s.Sessions[i].SkillID = "sk-plan"
				}
			}
			return nil
		})
	}
	setHarness("claude")
	sess := func(id string) store.Session { s, _ := p.session(id); return s }
	helper, lead := sess("planaaaaaaaa"), sess("leadaaaaaaaa")
	msg := "[PenguPool team message from lead (main worker)] please survey the runner health checks and report"

	d := &planDecider{pr: 0.9, calibrated: true}
	p.testDecider = d
	got := p.subagentPlan(helper, msg)
	for _, w := range []string{"subagent of kind Explore", "model haiku", "effort low", "Use effort low", "straight to lead", "send --to lead"} {
		if !strings.Contains(got, w) {
			t.Fatalf("plan lacks %q: %q", w, got)
		}
	}
	// Between Claude sessions a teammate's message arrives natively; one from outside the team gets no plan.
	native := `<cross-session-message from="uds:/x.sock" from-name="lead">please survey the runner health checks and report</cross-session-message>`
	if got := p.subagentPlan(helper, native); !strings.Contains(got, "subagent of kind Explore") {
		t.Fatalf("native teammate message: %q", got)
	}
	if got := p.subagentPlan(helper, strings.Replace(native, `"lead"`, `"stranger"`, 1)); got != "" {
		t.Fatalf("outside message got a plan: %q", got)
	}
	for name, d := range map[string]*planDecider{"low confidence": {pr: 0.6, calibrated: true}, "uncalibrated": {pr: 0.99}} {
		p.testDecider = d
		if got := p.subagentPlan(helper, msg); got != "" {
			t.Fatalf("%s: %q", name, got)
		}
	}
	d = &planDecider{pr: 0.9, calibrated: true}
	p.testDecider = d
	for name, c := range map[string]struct {
		s store.Session
		p string
	}{
		"owner prompt": {helper, "survey the runner health checks and report back"},
		"notice":       {helper, "[PenguPool notice] the runner health checks and report"},
		"anchor":       {lead, msg},
		"short":        {helper, "[PenguPool team message from lead] go"},
	} {
		if got := p.subagentPlan(c.s, c.p); got != "" || d.asked != 0 {
			t.Fatalf("%s: %q (%d asks)", name, got, d.asked)
		}
	}
	setHarness("pi")
	if got := p.subagentPlan(sess("planaaaaaaaa"), msg); got != "" || d.asked != 0 {
		t.Fatalf("pi: %q (%d asks)", got, d.asked)
	}
}
