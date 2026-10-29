package pool

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// subagentPlan is triage for a helper's turn that starts with a teammate's request: one request, three
// Choices (the subagent's kind, model and effort from the helper's harness lists), turned into an
// instruction line the helper follows; no hook enforces it. Only a calibrated answer at or above the
// model threshold counts; no kind answer that counts, no plan. ponytail: one ask per such turn, no cache.
func (p *Pool) subagentPlan(s store.Session, prompt string) string {
	if s.SkillID == "" || len(strings.Fields(request(prompt))) < 4 {
		return ""
	}
	kinds, models, efforts := harness.Subagents(s.Harness)
	if len(kinds) == 0 {
		return ""
	}
	d := p.st.State().Domain
	t := teamOf(&d, s.ID)
	if t == nil || leadOf(t) == s.ID || !p.teammateMessage(t, prompt) {
		return ""
	}
	desc := ""
	for _, m := range t.Members {
		if m.SessionID == s.ID {
			desc = p.routingDesc(&d, m)
		}
	}
	ps, _ := p.session(effectiveParent(t, s.ID))
	parent := ps.Name
	if parent == "" {
		return ""
	}
	dec := p.decider()
	if dec == nil {
		return ""
	}
	opts := func(l []string) map[string]string {
		o := map[string]string{}
		for _, x := range l {
			o[x] = x
		}
		return o
	}
	qs := []decision.Question{{Name: "subagent_kind", Type: decision.Choice, Options: opts(kinds),
		Instructions: "Which kind of subagent should do the work this request asks for? Explore only reads and searches, Plan designs, general-purpose does the work."}}
	if len(models) > 0 {
		qs = append(qs, decision.Question{Name: "model", Type: decision.Choice, Options: opts(models),
			Instructions: "Which model is enough for the subagent's work? The smallest that can do it well."})
	}
	if len(efforts) > 0 {
		qs = append(qs, decision.Question{Name: "effort", Type: decision.Choice, Options: opts(efforts),
			Instructions: "How much reasoning effort does the subagent's work need? The least that does it well."})
	}
	ctx, cancel := context.WithTimeout(context.Background(), nextStepTimeout)
	res, err := dec.Decide(ctx, decision.Request{
		State:     map[string]any{"brief": redactSecrets(clipRunes(prompt, 1500)), "helper": redactSecrets(desc), "harness": s.Harness},
		Questions: qs,
	})
	cancel()
	if err != nil {
		p.logLine(s.ID, "triage: subagent plan: "+err.Error())
		return ""
	}
	pick := func(name string, list []string) string {
		a, ok := res.Answers[name]
		if !ok || !res.Calibrated || a.Probabilities[a.Choice] < decision.Thresholds["model"] || !slices.Contains(list, a.Choice) {
			return ""
		}
		return a.Choice
	}
	kind := pick("subagent_kind", kinds)
	if kind == "" {
		return ""
	}
	model, effort := pick("model", models), pick("effort", efforts)
	plan := "start a subagent of kind " + kind
	if model != "" {
		plan += " with model " + model
	}
	if effort != "" {
		plan += map[bool]string{true: " and", false: " with"}[model != ""] + " effort " + effort
	}
	use := ""
	if effort != "" {
		use = " Use effort " + effort + "."
	}
	p.logLine(s.ID, fmt.Sprintf("triage: subagent plan kind=%s model=%s effort=%s", kind, model, effort))
	return fmt.Sprintf("\n[PenguPool triage] Subagent plan (the owner's decision model): %s.%s Tell it to send its result straight to %s (SendMessage, or `%s send --to %s` on a shell) and to return only \"done, sent to %s\" to you; then add a short follow-up only if something is missing.\n",
		plan, use, parent, p.exe, parent, parent)
}

// teammateMessage: a prompt a team member sent, relayed by PenguPool or delivered natively between
// Claude sessions (a cross-session message naming a member).
func (p *Pool) teammateMessage(t *protocol.Team, prompt string) bool {
	if strings.HasPrefix(prompt, "[PenguPool team message") {
		return true
	}
	from, _, ok := outsideMessage(prompt)
	if !ok {
		return false
	}
	for _, m := range t.Members {
		if ms, _ := p.session(m.SessionID); m.Name == from || ms.Name == from {
			return true
		}
	}
	return false
}
