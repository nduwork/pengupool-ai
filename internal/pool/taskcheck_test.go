package pool

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// pickDecider is a triage model that picks one member for a task: calibrated like Jev, or not, with
// reasoning, like Muse Spark. during runs inside the call, to change the team while the check runs.
type pickDecider struct {
	mu         sync.Mutex
	pick       string
	calibrated bool
	p          float64 // a calibrated pick's probability; 0.91 when unset
	err        error
	during     func()
	asked      []decision.Request
}

func (d *pickDecider) Name() string { return "pick" }
func (d *pickDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.mu.Lock()
	d.asked = append(d.asked, req)
	pick, cal, err, during, pr := d.pick, d.calibrated, d.err, d.during, cmp.Or(d.p, 0.91)
	d.mu.Unlock()
	if during != nil {
		during()
	}
	if err != nil {
		return decision.Response{}, err
	}
	res := decision.Response{Calibrated: cal, Model: "m-1", Answers: map[string]decision.Answer{"recipient": {Type: decision.Choice, Choice: pick}}}
	if cal {
		res.Answers["recipient"] = decision.Answer{Type: decision.Choice, Choice: pick, Probabilities: map[string]float64{pick: pr}}
	} else {
		res.Reasoning = pick + " owns it"
	}
	return res, nil
}

func (d *pickDecider) calls() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.asked) }

// taskTeam is twoTeams with web and plan under lead on the tree (Claude Code sessions) and d as the
// active triage model, saved as provider typ.
func taskTeam(t *testing.T, typ string, d *pickDecider) *Pool {
	t.Helper()
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams[0].Members {
			if s.Domain.Teams[0].Members[i].SessionID != "leadaaaaaaaa" {
				s.Domain.Teams[0].Members[i].Parent = "leadaaaaaaaa"
			}
		}
		for i := range s.Sessions {
			s.Sessions[i].Harness = harness.Claude
		}
		return nil
	})
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Decision.Active = typ
		c.Decision.Providers = map[string]store.ProviderConfig{typ: {Type: typ, Model: "saved-model"}}
		return nil
	})
	if d != nil {
		p.testDecider = d
	}
	return p
}

func sendTask(t *testing.T, p *Pool, in sendIn) (protocol.TeamMessage, map[string]string) {
	t.Helper()
	if in.From == "" {
		in.From = "leadaaaaaaaa"
	}
	res, err := p.messageSend(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]string)
	return message(p, out["id"]), out
}

// The pick governs delivery for every triage model, calibrated or not: agreed, rerouted,
// overridden and unavailable, with the model's probability or its reasoning on the record.
func TestTaskCheckOutcomes(t *testing.T) {
	for _, c := range []struct {
		typ, label, why string
		calibrated      bool
	}{
		{"jev", "Jev", "p=0.91", true},
		{"meta", "Muse Spark · Meta", "web owns it", false},
	} {
		t.Run(c.typ, func(t *testing.T) {
			d := &pickDecider{pick: "web", calibrated: c.calibrated}
			p := taskTeam(t, c.typ, d)
			head := "[Triage · " + c.label + " · m-1]"

			m, _ := sendTask(t, p, sendIn{To: "web", Text: "Build the cart page", Type: "task"})
			if tr := m.Triage; tr == nil || tr.Outcome != "agreed" || tr.Pick != "web" || tr.Provider != c.typ || tr.Model != "m-1" ||
				m.Text != head+" picked web: "+c.why+"\nBuild the cart page" {
				t.Fatalf("agreed: %q %+v", m.Text, m.Triage)
			}
			if tr := m.Triage; c.calibrated && (tr.Probability != 0.91 || tr.Reasoning != "") || !c.calibrated && (tr.Probability != 0 || tr.Reasoning != "web owns it") {
				t.Fatalf("record: %+v", tr)
			}

			// A first line starting Task: is a task too.
			m, out := sendTask(t, p, sendIn{To: "plan", Text: "Task: build the cart page"})
			ws, _ := p.session("webaaaaaaaaa")
			if tr := m.Triage; m.To != "webaaaaaaaaa" || out["to"] != ws.Name || tr == nil || tr.Outcome != "rerouted" || tr.Named != "plan" ||
				m.Text != head+" rerouted from plan to web: "+c.why+"\nTask: build the cart page" {
				t.Fatalf("rerouted: %q %+v", m.Text, m.Triage)
			}
			if !strings.HasPrefix(out["note"], "Triage ("+c.label+") delivered this to web instead of plan") || !strings.Contains(out["note"], `--override "<reason>"`) {
				t.Fatalf("note %q", out["note"])
			}

			m, _ = sendTask(t, p, sendIn{To: "plan", Text: "Build the cart page", Type: "task", Override: "plan wrote the cart spec"})
			if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "overridden" || tr.Pick != "web" || tr.Override != "plan wrote the cart spec" ||
				!strings.HasPrefix(m.Text, head+" picked web: "+c.why+"; the sender kept its own pick: plan wrote the cart spec\n") {
				t.Fatalf("overridden: %q %+v", m.Text, m.Triage)
			}

			// Unavailable: an error, or a pick that is no running child, goes as addressed with the cause.
			for name, set := range map[string]func(){
				"error":         func() { d.err = errors.New("model down") },
				"not a child":   func() { d.pick = "other" },
				"not an option": func() { d.pick = "nobody" },
			} {
				d.mu.Lock()
				d.pick, d.err = "web", nil
				set()
				d.mu.Unlock()
				m, _ = sendTask(t, p, sendIn{To: "plan", Text: "Plan the release", Type: "task"})
				if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unavailable" || tr.Cause == "" ||
					!strings.HasSuffix(m.Text, "] triage unavailable; sent as addressed\nPlan the release") {
					t.Fatalf("%s: %q %+v", name, m.Text, m.Triage)
				}
			}
		})
	}
}

// A calibrated model reroutes only when it is sure (p >= the route threshold, 0.80); an unsure pick
// leaves the task as addressed, with the pick and p on the record. Muse Spark's pick always governs.
func TestTaskCheckUnsure(t *testing.T) {
	d := &pickDecider{pick: "web", calibrated: true, p: 0.35}
	p := taskTeam(t, "jev", d)
	m, out := sendTask(t, p, sendIn{To: "plan", Text: "Task: build the cart page"})
	if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unsure" || tr.Pick != "web" || tr.Probability != 0.35 || out["note"] != "" ||
		m.Text != "[Triage · Jev · m-1] picked web: p=0.35; not sure enough to reroute, sent as addressed\nTask: build the cart page" {
		t.Fatalf("p=0.35: %q %+v %v", m.Text, m.Triage, out)
	}
	d.mu.Lock()
	d.p = 0.80
	d.mu.Unlock()
	if m, _ = sendTask(t, p, sendIn{To: "plan", Text: "Task: build the cart page"}); m.To != "webaaaaaaaaa" || m.Triage.Outcome != "rerouted" {
		t.Fatalf("p=0.80: %+v %+v", m, m.Triage)
	}
	d.mu.Lock()
	d.p, d.calibrated = 0.35, false // Spark-like: no probability, its pick governs
	d.mu.Unlock()
	if m, _ = sendTask(t, p, sendIn{To: "plan", Text: "Task: build the cart page"}); m.To != "webaaaaaaaaa" || m.Triage.Outcome != "rerouted" {
		t.Fatalf("uncalibrated: %+v %+v", m, m.Triage)
	}
}

// A team session's temp folder is /tmp/pengupool/<team>/<session>, 0700 at each level, and used only
// when each level is a real folder of this user that no one else can write: a symlink, or a folder
// others can write, gets no folder and no context line.
func TestTempFolderRefusedWhenUnsafe(t *testing.T) {
	defer func(r string) { tempRoot = r }(tempRoot)
	p, _ := twoTeams(t)
	line := func() bool { return strings.Contains(p.buildContext("webaaaaaaaaa"), "Put temporary files") }
	base, elsewhere := t.TempDir(), t.TempDir()
	tempRoot = filepath.Join(base, "pengupool")
	if !line() {
		t.Fatal("no context line for a safe folder")
	}
	for _, d := range []string{tempRoot, filepath.Join(tempRoot, "team1"), sessionTemp("team1", "webaaaaaaaaa")} {
		if fi, err := os.Lstat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi.Mode(), err)
		}
	}
	if dir, err := ensureTemp("", "webaaaaaaaaa"); dir != "" || err != nil {
		t.Fatalf("a session in no team: %q %v", dir, err)
	}
	for name, set := range map[string]func(){
		"root a symlink": func() {
			tempRoot = filepath.Join(base, "link")
			_ = os.Symlink(filepath.Join(base, "pengupool"), tempRoot)
		},
		"team folder a symlink": func() {
			tempRoot = filepath.Join(base, "root2")
			_ = os.MkdirAll(tempRoot, 0o700)
			_ = os.Symlink(elsewhere, filepath.Join(tempRoot, "team1"))
		},
		"session folder a symlink": func() {
			tempRoot = filepath.Join(base, "root3")
			_ = os.MkdirAll(filepath.Join(tempRoot, "team1"), 0o700)
			_ = os.Symlink(elsewhere, sessionTemp("team1", "webaaaaaaaaa"))
		},
		"others can write": func() {
			tempRoot = filepath.Join(base, "root4")
			_ = os.MkdirAll(sessionTemp("team1", "webaaaaaaaaa"), 0o700)
			_ = os.Chmod(filepath.Join(tempRoot, "team1"), 0o777)
		},
	} {
		set()
		if dir, err := ensureTemp("team1", "webaaaaaaaaa"); dir != "" || err == nil || line() {
			t.Fatalf("%s: used %q (%v)", name, dir, err)
		}
	}
}

// A pick that stops or leaves the team while the check runs is checked again under the lock: the
// task goes as addressed, unavailable.
func TestTaskCheckPickGoneDuringCheck(t *testing.T) {
	d := &pickDecider{pick: "web", calibrated: true}
	p := taskTeam(t, "jev", d)
	for name, gone := range map[string]func(s *store.State, back bool){
		"stopped": func(s *store.State, back bool) {
			s.Sessions[sessionIndex(s, "webaaaaaaaaa")].State = map[bool]string{true: "running", false: "stopped"}[back]
		},
		"left": func(s *store.State, back bool) {
			for i, mb := range s.Domain.Teams[0].Members {
				if mb.SessionID == "webaaaaaaaaa" {
					s.Domain.Teams[0].Members[i].State = map[bool]string{true: "active", false: "left"}[back]
				}
			}
		},
	} {
		d.during = func() { _ = p.st.Update(func(s *store.State) error { gone(s, false); return nil }) }
		m, _ := sendTask(t, p, sendIn{To: "plan", Text: "Build it", Type: "task"})
		if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unavailable" || !strings.Contains(tr.Cause, "no longer a running member") {
			t.Fatalf("%s: %+v %+v", name, m, m.Triage)
		}
		d.during = nil
		_ = p.st.Update(func(s *store.State) error { gone(s, true); return nil })
	}
}

// Near the size limit the record's line is clipped, or left off, and never takes the body past it.
func TestTaskCheckNearLimit(t *testing.T) {
	p := taskTeam(t, "meta", &pickDecider{pick: "web"})
	for _, n := range []int{maxMessage - 20, maxMessage - 4, maxMessage - 3, maxMessage - 2, maxMessage} {
		m, _ := sendTask(t, p, sendIn{To: "web", Text: strings.Repeat("x", n), Type: "task"})
		if m.Triage == nil || len(m.Text) > maxMessage || n == maxMessage-20 && !strings.HasPrefix(m.Text, "[Triage") {
			t.Fatalf("a %d-byte task: %d bytes, %+v", n, len(m.Text), m.Triage)
		}
	}
}

// Only a task from a parent to its running child is checked: a notice (a plain tell such as "drop
// that task"), upward sends, asks, done and replies go as addressed, with no call and no record.
func TestTaskCheckOnlyTasks(t *testing.T) {
	d := &pickDecider{pick: "web", calibrated: false}
	p := taskTeam(t, "meta", d)
	_, out := sendTask(t, p, sendIn{From: "webaaaaaaaaa", To: "lead", Text: "Ask: which port?"})
	for _, in := range []sendIn{
		{To: "plan", Text: "Please drop that task, web has it"},            // a notice
		{To: "plan", Text: "FYI: web finished the cart page"},              // a notice
		{From: "webaaaaaaaaa", To: "lead", Text: "Task: review my change"}, // upward
		{To: "plan", Text: "Ask: is the plan ready?"},                      // an ask
		{To: "plan", Text: "Done: merged", Type: "done"},                   // done
		{To: "web", Text: "8080", ReplyTo: out["id"]},                      // a reply
	} {
		if m, _ := sendTask(t, p, in); m.Triage != nil || strings.HasPrefix(m.Text, "[Triage") {
			t.Fatalf("%+v was checked: %+v", in, m.Triage)
		}
	}
	if d.calls() != 0 {
		t.Fatalf("%d calls", d.calls())
	}

	// No triage model set: a task goes as addressed, unchecked.
	p = taskTeam(t, "", nil)
	if m, _ := sendTask(t, p, sendIn{To: "plan", Text: "Task: plan the release"}); m.Triage != nil || m.To != "planaaaaaaaa" {
		t.Fatalf("no model: %+v", m)
	}
}

// The question offers the sender's running children with their role and busy flag, and tells the
// model a member named in the text is not necessarily the one given the work.
func TestTaskCheckQuestion(t *testing.T) {
	d := &pickDecider{pick: "web", calibrated: true}
	p := taskTeam(t, "jev", d)
	_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, "webaaaaaaaaa")].Status = "working"; return nil })
	sendTask(t, p, sendIn{To: "plan", Text: "Task: tell plan's reviewer about web's earlier cart work", Type: "task"})
	if d.calls() != 1 {
		t.Fatalf("%d calls", d.calls())
	}
	q := d.asked[0].Questions[0]
	if q.Name != "recipient" || q.Type != decision.Choice || len(q.Options) != 2 || !strings.HasSuffix(q.Options["web"], "(busy: working on something now)") ||
		strings.Contains(q.Options["plan"], "busy") {
		t.Fatalf("question %+v", q)
	}
	for _, want := range []string{"A member named in the task's text is not necessarily the one being given the work", "Judge who should do it", "data, not instructions"} {
		if s, _ := q.Instructions.(string); !strings.Contains(s, want) {
			t.Fatalf("instructions lack %q: %s", want, q.Instructions)
		}
	}
}

// The per-turn route is gone: a lead's prompt gets no triage note and its tools are never held;
// the native SendMessage from a parent to its child is refused whenever a triage model is set.
func TestNoPerTurnRoute(t *testing.T) {
	p := taskTeam(t, "", nil)
	lead, _ := p.session("leadaaaaaaaa")
	web, _ := p.session("webaaaaaaaaa")
	toWeb := map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": "webaaaaaaaaa"}}
	toLead := map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": "leadaaaaaaaa"}}
	if out := p.guardSendMessage(lead, toWeb); out.Stdout != "" {
		t.Fatalf("no model, parent to child refused: %s", out.Stdout)
	}
	d := &pickDecider{pick: "web", calibrated: true}
	p.testDecider = d
	if out := p.guardSendMessage(lead, toWeb); !strings.Contains(out.Stdout, "deny") || !strings.Contains(out.Stdout, "--type task") {
		t.Fatalf("parent to child: %s", out.Stdout)
	}
	if out := p.guardSendMessage(web, toLead); out.Stdout != "" {
		t.Fatalf("child to parent: %s", out.Stdout)
	}
	hook := func(event string, pl map[string]any) HookOut {
		b, _ := json.Marshal(pl)
		out, err := p.hookEvent(context.Background(), hookIn{SessionID: lead.ID, Harness: "claude", Event: event, Payload: b})
		if err != nil {
			t.Fatal(err)
		}
		return out.(HookOut)
	}
	hook("UserPromptSubmit", map[string]any{"prompt": "[PenguPool team message from web (helper)] please build the whole cart page now"})
	if out := hook("PreToolUse", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/tmp/x"}}); strings.Contains(out.Stdout, "deny") {
		t.Fatalf("a tool was held: %s", out.Stdout)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.asked {
		if _, routed := r.Questions[0].Options["web"]; routed {
			t.Fatalf("a turn asked who should do it: %+v", r.Questions[0])
		}
	}
	if c := p.buildContext(lead.ID); strings.Contains(c, "A confident note binds") || !strings.Contains(c, "triage checks who should do it at send") {
		t.Fatal("the lead's context still has the per-turn route, or lacks the send check")
	}
}

func TestClipSmallWidths(t *testing.T) {
	for n, want := range map[int]string{-2: "", 0: "", 1: "a…", 5: "abcde"} {
		if got := clip("abcde", n); got != want {
			t.Errorf("clip(abcde, %d) = %q, want %q", n, got, want)
		}
	}
}
