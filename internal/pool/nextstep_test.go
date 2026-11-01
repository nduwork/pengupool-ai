package pool

import (
	"context"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// routeDecider answers the route question with one choice at probability pr.
type routeDecider struct {
	choice     string
	pr         float64
	calibrated bool
	asked      int
}

func (d *routeDecider) Name() string { return "route" }
func (d *routeDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.asked++
	a := decision.Answer{Type: decision.Choice, Choice: d.choice, Probabilities: map[string]float64{d.choice: d.pr}}
	return decision.Response{Answers: map[string]decision.Answer{"route": a}, Calibrated: d.calibrated}, nil
}

// routedTeam is epicTeam with web (a repository-less helper) and plan (a skill helper) assigned work.
func routedTeam(t *testing.T) *Pool {
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		s.Domain.Skills[0].Description = "Turns goals into epics and milestones"
		for i := range s.Domain.Teams[0].Members {
			if m := &s.Domain.Teams[0].Members[i]; m.Name == "web" {
				m.Assignment = "Assist the team with runner-setup, runner-health for: app"
			}
		}
		return nil
	})
	return p
}

const routePrompt = "set up the self-hosted runner and check runner health"

func TestNextStepNote(t *testing.T) {
	p := routedTeam(t)
	sess := func(id string) store.Session { s, _ := p.session(id); return s }
	lead, web := sess("leadaaaaaaaa"), sess("webaaaaaaaaa")
	held := func() string { p.mu.Lock(); defer p.mu.Unlock(); return p.routed[lead.ID] }

	p.testDecider = &routeDecider{choice: "web", pr: 0.91, calibrated: true}
	if got := p.nextStepNote(lead, routePrompt); !strings.Contains(got, "Hand the work to web") || !strings.Contains(got, "p=0.91") || held() != "webaaaaaaaaa" {
		t.Fatalf("firm route: %q", got)
	}
	// Hedged, uncalibrated: a note naming the member, no hold, no demand for a reason.
	for _, d := range []*routeDecider{{choice: "web", pr: 0.6, calibrated: true}, {choice: "web", pr: 0.95}} {
		p.testDecider = d
		got := p.nextStepNote(lead, routePrompt)
		if !strings.Contains(got, "may fit web") || strings.Contains(got, "say why") || held() != "" {
			t.Fatalf("hedged: %q (held %q)", got, held())
		}
	}
	p.testDecider = &routeDecider{choice: "web", pr: 0.4, calibrated: true}
	if got := p.nextStepNote(lead, routePrompt); got != "" || held() != "" {
		t.Fatalf("calibrated under even odds: %q", got)
	}
	// A teammate's message is a step; a short answer is not.
	p.testDecider = &routeDecider{choice: "web", pr: 0.91, calibrated: true}
	if got := p.nextStepNote(lead, "[PenguPool team message from plan] "+routePrompt); !strings.Contains(got, "web") {
		t.Fatalf("team message: %q", got)
	}
	if got := p.nextStepNote(lead, "yes, go ahead"); got != "" || held() != "" {
		t.Fatalf("short answer: %q", got)
	}
	for _, c := range []string{"self", "nobody"} {
		p.testDecider = &routeDecider{choice: c, pr: 0.95, calibrated: true}
		if got := p.nextStepNote(lead, routePrompt); got != "" || held() != "" {
			t.Fatalf("%s: %q", c, got)
		}
	}
	// Only the LEAD is triaged, and a PenguPool notice is not a step.
	dec := &routeDecider{choice: "web", pr: 0.91, calibrated: true}
	p.testDecider = dec
	if p.nextStepNote(web, routePrompt) != "" || p.nextStepNote(lead, "[PenguPool notice] x") != "" || dec.asked != 0 {
		t.Fatalf("a helper or a notice was triaged (%d asks)", dec.asked)
	}
	// The owner naming a member, or saying yourself, skips the question.
	for _, pr := range []string{"ask web to " + routePrompt, "do it yourself: " + routePrompt} {
		if got := p.nextStepNote(lead, pr); got != "" || dec.asked != 0 || held() != "" {
			t.Fatalf("owner chose (%q): %q, %d asks", pr, got, dec.asked)
		}
	}
	// A teammate's message names its sender: that is not the owner choosing.
	if got := p.nextStepNote(lead, `<cross-session-message from="uds:/x.sock" from-name="web">`+routePrompt+`</cross-session-message>`); dec.asked != 1 || got == "" {
		t.Fatalf("teammate message skipped triage: %q, %d asks", got, dec.asked)
	}
	// Model failure or no model: nothing, no word match.
	p.testDecider = failDecider{}
	if got := p.nextStepNote(lead, routePrompt); got != "" || held() != "" {
		t.Fatalf("failed model: %q", got)
	}
}

func TestRouteOptions(t *testing.T) {
	p := routedTeam(t)
	lead, _ := p.session("leadaaaaaaaa")
	var seen map[string]string
	p.testDecider = optionsDecider{&seen}
	p.nextStepNote(lead, routePrompt)
	if !strings.Contains(seen["plan"], "epics and milestones") || strings.Contains(seen["web"], "runner-setup") && !strings.Contains(seen["web"], "Assist") {
		t.Fatalf("options: %v", seen)
	}
	if strings.Contains(seen["plan"], "Assist") {
		t.Fatalf("a helper's option is its skill description, not its assignment: %v", seen)
	}
	// Waiting on the owner: left out; working: kept, marked busy.
	p.setSession("webaaaaaaaaa", func(x *store.Session) { x.Status = "waiting" })
	p.setSession("planaaaaaaaa", func(x *store.Session) { x.Status = "working" })
	p.nextStepNote(lead, routePrompt)
	if _, ok := seen["web"]; ok || !strings.Contains(seen["plan"], "busy") {
		t.Fatalf("status: %v", seen)
	}
	// A member the LEAD may not message (not its parent or child) is not an option.
	p.setSession("webaaaaaaaaa", func(x *store.Session) { x.Status = "idle" })
	_ = p.st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams[0].Members {
			if m := &s.Domain.Teams[0].Members[i]; m.Name == "plan" {
				m.Parent = "webaaaaaaaaa"
			}
		}
		return nil
	})
	p.nextStepNote(lead, routePrompt)
	if _, ok := seen["plan"]; ok || seen["web"] == "" {
		t.Fatalf("non-neighbour offered: %v", seen)
	}
}

// optionsDecider records the route question's options.
type optionsDecider struct{ into *map[string]string }

func (optionsDecider) Name() string { return "options" }
func (d optionsDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	*d.into = req.Questions[0].Options
	return decision.Response{}, nil
}

func TestGuardRoute(t *testing.T) {
	p := routedTeam(t)
	lead, _ := p.session("leadaaaaaaaa")
	call := func(tool string, in map[string]any) bool {
		_, deny := p.guardRoute(lead, map[string]any{"tool_name": tool, "tool_input": in})
		return deny
	}
	sh := func(c string) map[string]any { return map[string]any{"command": c} }
	firm := func() {
		p.testDecider = &routeDecider{choice: "web", pr: 0.91, calibrated: true}
		p.nextStepNote(lead, routePrompt)
	}

	firm()
	if call("Read", nil) || !call("Bash", sh("make runner")) || !call("Bash", sh("make runner")) || !call("Edit", nil) {
		t.Fatal("want: a read passes, every work call is held, the hold persists")
	}
	// A message to someone else, a read-only shell passes, a writing one is held.
	if call("SendMessage", map[string]any{"to": "plan", "message": "hi"}) || !call("Edit", nil) {
		t.Fatal("a message to another member must not release")
	}
	for _, c := range []string{"pengupool send --to plan 'do it'", "pengupool send --to plan \"ask weba to check the build\""} {
		if !call("Bash", sh(c)) || !call("Write", nil) {
			t.Fatalf("a send to another member released the hold: %s", c)
		}
	}
	for _, c := range []string{"rg runner internal | head -5", "git log --oneline", "cat a b", "find . -name x.go"} {
		if call("shell", sh(c)) {
			t.Fatalf("read-only shell held: %s", c)
		}
	}
	for _, c := range []string{"cat a > b", "git commit -m x", "sed -i s/a/b/ x", "ls; rm x", "cat a | tee b", "rg x $(rm y)",
		"rg --pre ./evil p .", "find . -fprint /tmp/out", "git log --output=/tmp/out", "git show HEAD:f --output=/tmp/out",
		"sed -n 'w /tmp/out' f", "sed -n '/x/e touch /tmp/x' f", "find . -execdir rm {} +"} {
		if !call("shell", sh(c)) {
			t.Fatalf("writing shell passed: %s", c)
		}
	}
	if call("SendMessage", map[string]any{"to": "weba"}) || call("Edit", nil) {
		t.Fatal("a message to the routed member releases the hold")
	}
	firm()
	if call("WebSearch", nil) || call("WebFetch", nil) {
		t.Fatal("web reads are reads")
	}
	if call("Bash", sh("pengupool send --to=web 'do it'")) || call("Write", nil) {
		t.Fatal("pengupool send to the routed member releases the hold")
	}
	firm()
	if call("Bash", sh("pengupool send --to webaaaaa 'do it'")) || call("Write", nil) {
		t.Fatal("pengupool send by a session id prefix releases the hold")
	}
	// Another team runs a session named web whose id shares web's prefix: the send resolves in the
	// lead's team, as messageSend resolves it, so both still release.
	_ = p.st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "webaaaaabbbb", Name: "web", State: "running"})
		s.Domain.Teams[1].Members = append(s.Domain.Teams[1].Members, protocol.Member{SessionID: "webaaaaabbbb", Name: "web", Role: "helper", State: "active"})
		return nil
	})
	for _, to := range []string{"web", "webaaaaa"} {
		firm()
		if call("Bash", sh("pengupool send --to "+to+" 'do it'")) || call("Write", nil) {
			t.Fatalf("send --to %s, a name or prefix another team shares, did not release the hold", to)
		}
	}
	// Hedged: no hold at all.
	p.testDecider = &routeDecider{choice: "web", pr: 0.6, calibrated: true}
	p.nextStepNote(lead, routePrompt)
	if call("Bash", sh("make runner")) {
		t.Fatal("a hedged turn is not held")
	}
	p.testDecider = &routeDecider{choice: "self", pr: 0.95, calibrated: true}
	p.nextStepNote(lead, routePrompt)
	if call("Bash", sh("make runner")) {
		t.Fatal("a turn kept by the LEAD is not held")
	}
}

// Two copies of one skill are one route option: the idle copy when one is busy.
func TestRouteDuplicateSkillPrefersIdle(t *testing.T) {
	p := routedTeam(t)
	_ = p.st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "plan2aaaaaaa", Name: "plan-2", Harness: "claude", State: "running", Status: "idle", SkillID: "sk-plan"})
		s.Domain.Teams[0].Members = append(s.Domain.Teams[0].Members,
			protocol.Member{SessionID: "plan2aaaaaaa", Name: "plan-2", Role: "helper", SkillID: "sk-plan", State: "active", Parent: "leadaaaaaaaa"})
		return nil
	})
	lead, _ := p.session("leadaaaaaaaa")
	var seen map[string]string
	p.testDecider = optionsDecider{&seen}
	offered := func() (n int, name string) {
		for k := range seen {
			if strings.HasPrefix(k, "plan") {
				n, name = n+1, k
			}
		}
		return n, name
	}
	for _, c := range []struct{ plan, plan2, want string }{
		{"working", "idle", "plan-2"},
		{"idle", "working", "plan"},
		{"idle", "idle", "plan"},
	} {
		p.setSession("planaaaaaaaa", func(x *store.Session) { x.Status = c.plan })
		p.setSession("plan2aaaaaaa", func(x *store.Session) { x.Status = c.plan2 })
		p.nextStepNote(lead, routePrompt)
		if n, name := offered(); n != 1 || name != c.want {
			t.Fatalf("plan %s, plan-2 %s: offered %v, want only %s", c.plan, c.plan2, seen, c.want)
		}
	}
}
