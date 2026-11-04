package pool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// handDecider answers the hand-off question: yes with probability yes, calibrated or not (Muse
// Spark), or no answer before the deadline when slow.
type handDecider struct {
	mu         sync.Mutex
	yes        float64
	calibrated bool
	slow       bool
	asked      []decision.Request
}

func (d *handDecider) Name() string { return "hand" }
func (d *handDecider) Decide(ctx context.Context, req decision.Request) (decision.Response, error) {
	d.mu.Lock()
	d.asked = append(d.asked, req)
	yes, cal, slow := d.yes, d.calibrated, d.slow
	d.mu.Unlock()
	if slow {
		<-ctx.Done()
		return decision.Response{}, ctx.Err()
	}
	return decision.Response{Calibrated: cal, Model: "m-1", Answers: map[string]decision.Answer{"hand_off": {Type: decision.Predicate, Yes: yes}}}, nil
}

func (d *handDecider) set(yes float64, cal bool) {
	d.mu.Lock()
	d.yes, d.calibrated = yes, cal
	d.mu.Unlock()
}
func (d *handDecider) calls() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.asked) }

// handTeam is taskTeam with the hand-off decider, and the lead's log in a file the test reads.
func handTeam(t *testing.T) (*Pool, *handDecider, store.Session, func() string) {
	t.Helper()
	d := &handDecider{}
	p := taskTeam(t, "jev", nil)
	p.testDecider = d
	runlog := filepath.Join(t.TempDir(), "lead.log")
	_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, "leadaaaaaaaa")].RunLog = runlog; return nil })
	lead, _ := p.session("leadaaaaaaaa")
	return p, d, lead, func() string { b, _ := os.ReadFile(runlog); return string(b) }
}

const teammateTurn = "[PenguPool team message from web (helper)] the cart page needs building next"

func held(p *Pool, lead store.Session, tool string, in map[string]any) bool {
	_, deny := p.guardHandOff(lead, map[string]any{"tool_name": tool, "tool_input": in})
	return deny
}

// A sure yes holds the lead's working tools until it sends a child a task; reads, read-only commands
// and PenguPool's CLI pass, and a message that is no task does not lift it.
func TestHandOffHeldUntilATask(t *testing.T) {
	p, d, lead, log := handTeam(t)
	d.set(0.93, true)
	note := p.handOffNote(lead, teammateTurn)
	if !strings.Contains(note, "[PenguPool triage] Hand this off (p=0.93, m-1): a member should do this.") || strings.Contains(note, "web") || strings.Contains(note, "plan") {
		t.Fatalf("note %q", note)
	}
	if !strings.Contains(log(), "triage: hand-off check jev/m-1 p=0.93 held") {
		t.Fatalf("log: %s", log())
	}
	q := d.asked[0]
	members, _ := q.State.(map[string]any)["members"].([]map[string]any)
	if q.Questions[0].Type != decision.Predicate || len(q.Questions[0].Options) != 0 || len(members) != 2 ||
		!strings.Contains(q.Questions[0].Instructions.(string), "Should the LEAD hand this work to one of its members rather than do it itself?") {
		t.Fatalf("question %+v", q)
	}
	edit := map[string]any{"file_path": "/x"}
	if !held(p, lead, "Edit", edit) || !held(p, lead, "Bash", map[string]any{"command": "go test ./..."}) {
		t.Fatal("working tools not held")
	}
	for tool, in := range map[string]map[string]any{"Read": {"file_path": "/x"}, "Grep": {}, "Bash": {"command": "git status"}} {
		if held(p, lead, tool, in) {
			t.Fatalf("%s held", tool)
		}
	}
	if held(p, lead, "Bash", map[string]any{"command": p.cli() + ` send --to web --type task "Task: build the cart page"`}) {
		t.Fatal("the send that hands it over was held")
	}
	if _, err := p.messageSend(context.Background(), sendIn{From: lead.ID, To: "plan", Text: "FYI: web is on the cart"}); err != nil || !held(p, lead, "Edit", edit) {
		t.Fatalf("a note lifted the hold (%v)", err)
	}
	if _, err := p.messageSend(context.Background(), sendIn{From: lead.ID, To: "plan", Text: "Task: build the cart page"}); err != nil || held(p, lead, "Edit", edit) {
		t.Fatalf("a task to a child did not lift the hold (%v)", err)
	}
	if !strings.Contains(log(), "triage: handed off as a task to") {
		t.Fatalf("log: %s", log())
	}
	// The next prompt decides again: a no lifts a hold left from the turn before.
	p.handOffNote(lead, teammateTurn)
	d.set(0.1, true)
	if note := p.handOffNote(lead, teammateTurn); note != "" || held(p, lead, "Edit", edit) {
		t.Fatalf("no: %q, held %v", note, held(p, lead, "Edit", edit))
	}
}

// A less sure calibrated yes, and any yes from an uncalibrated model (Muse Spark), is a hint only;
// a no, an error or a timeout is nothing. None of them holds.
func TestHandOffHintsAndNothing(t *testing.T) {
	defer func(d time.Duration) { nextStepTimeout = d }(nextStepTimeout)
	nextStepTimeout = 50 * time.Millisecond
	p, d, lead, log := handTeam(t)
	edit := map[string]any{"file_path": "/x"}
	for _, c := range []struct {
		name       string
		yes        float64
		calibrated bool
		slow       bool
		hint       bool
		logged     string
	}{
		{"calibrated p=0.6", 0.6, true, false, true, "p=0.60 hint"},
		{"calibrated p=0.79", 0.79, true, false, true, "p=0.79 hint"},
		{"uncalibrated yes", 1, false, false, true, "p=1.00 hint"},
		{"calibrated no", 0.2, true, false, false, "p=0.20 none"},
		{"uncalibrated no", 0, false, false, false, "p=0.00 none"},
		{"timeout", 0, false, true, false, "none (context deadline exceeded)"},
	} {
		d.mu.Lock()
		d.yes, d.calibrated, d.slow = c.yes, c.calibrated, c.slow
		d.mu.Unlock()
		note := p.handOffNote(lead, teammateTurn)
		if hint := strings.Contains(note, "This may be worth handing off to a member as a task; you decide."); hint != c.hint || !c.hint && note != "" || held(p, lead, "Edit", edit) {
			t.Fatalf("%s: note %q, held %v", c.name, note, held(p, lead, "Edit", edit))
		}
		if !strings.Contains(log(), c.logged) {
			t.Fatalf("%s: log %s", c.name, log())
		}
	}
}

// No question for a short answer, a notice, triage's own note, a member who is not the LEAD, or an
// owner prompt that names a member or says "yourself"; with no decision model, none either.
func TestHandOffSkips(t *testing.T) {
	p, d, lead, _ := handTeam(t)
	d.set(0.95, true)
	web, _ := p.session("webaaaaaaaaa")
	for _, c := range []struct {
		s      store.Session
		prompt string
	}{
		{lead, "yes go ahead"},
		{lead, "[PenguPool notice] web finished its turn and is idle"},
		{lead, "[PenguPool triage] This may be worth handing off to a member as a task; you decide."},
		{lead, "please have web build the cart page"},
		{lead, "build the cart page yourself, it is quick"},
		{web, "[PenguPool team message from lead (main)] build the cart page now please"},
	} {
		if note := p.handOffNote(c.s, c.prompt); note != "" {
			t.Fatalf("%q: %q", c.prompt, note)
		}
	}
	if d.calls() != 0 {
		t.Fatalf("%d questions", d.calls())
	}
	// An owner prompt that names no member is asked.
	if note := p.handOffNote(lead, "the cart page needs building next"); note == "" || d.calls() != 1 {
		t.Fatalf("owner prompt: %q, %d", note, d.calls())
	}
	p.testDecider = nil
	_ = p.st.UpdateConfig(func(c *store.Config) error { c.Decision.Active = ""; return nil })
	if note := p.handOffNote(lead, teammateTurn); note != "" || held(p, lead, "Edit", map[string]any{}) {
		t.Fatalf("no model: %q", note)
	}
}
