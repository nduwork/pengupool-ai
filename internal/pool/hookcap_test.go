package pool

import (
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Muse drops a hook answer whose stdout passes 16 KiB: a capped harness keeps the team
// sections whole and lets the compass take what is left, so the roster and the triage
// note survive. Uncapped harnesses keep the whole context.
func TestContextOutFitsMuseCap(t *testing.T) {
	fat := func(t *testing.T, kind string) string {
		t.Helper()
		p, st := twoTeams(t)
		own := p.owner()
		for i := 0; i < 200; i++ {
			body := "Preference " + strings.Repeat("keep it simple. ", 20)
			if _, err := p.db.PutLine(agentdb.Line{Owner: own, Body: body}); err != nil {
				t.Fatal(err)
			}
		}
		_ = st.Update(func(s *store.State) error {
			for i := range s.Sessions {
				if s.Sessions[i].ID == "leadaaaaaaaa" {
					s.Sessions[i].Harness = kind
				}
			}
			return nil
		})
		return p.contextOut("leadaaaaaaaa", "SessionStart").Stdout
	}
	muse := fat(t, harness.Muse)
	if len(muse) >= 16<<10 {
		t.Fatalf("muse hook answer is %d bytes, past the 16 KiB ceiling", len(muse))
	}
	for _, want := range []string{"## Team members", "- web (session ", "send --to", "LEAD", "… more: search"} {
		if !strings.Contains(muse, want) {
			t.Errorf("capped context lacks %q", want)
		}
	}
	claude := fat(t, harness.Claude)
	if len(claude) <= len(muse) {
		t.Fatalf("uncapped context (%d bytes) should keep more than capped (%d)", len(claude), len(muse))
	}
}

// A Muse LEAD under a firm triage note is held like any LEAD: its read tools pass,
// its working tools wait until it sends the routed member the work, and unknown tools
// fail closed into the hold.
func TestGuardRouteMuseTools(t *testing.T) {
	p := routedTeam(t)
	p.setSession("leadaaaaaaaa", func(x *store.Session) { x.Harness = harness.Muse })
	lead, _ := p.session("leadaaaaaaaa")
	call := func(tool string, in map[string]any) bool {
		_, deny := p.guardRoute(lead, map[string]any{"tool_name": tool, "tool_input": in})
		return deny
	}
	sh := func(c string) map[string]any { return map[string]any{"command": c} }
	p.testDecider = &routeDecider{choice: "web", pr: 0.91, calibrated: true}
	p.nextStepNote(lead, routePrompt)

	for _, tool := range []string{"read_file", "search", "web_fetch", "web_search"} {
		if call(tool, nil) {
			t.Fatalf("muse read tool held: %s", tool)
		}
	}
	if call("bash", sh("ls")) {
		t.Fatal("read-only muse shell held")
	}
	for _, tool := range []string{"write_file", "edit_file", "delete_file", "frobnicate"} {
		if !call(tool, nil) {
			t.Fatalf("muse working tool passed: %s", tool)
		}
	}
	if !call("bash", sh("make runner")) {
		t.Fatal("writing muse shell passed")
	}
	if call("bash", sh(`pengupool send --to "web" "take the runner work"`)) || call("write_file", nil) {
		t.Fatal("a send to the routed member must release the hold")
	}
}
