package pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// sparkTeam is twoTeams with web and plan under lead on the tree (Claude Code sessions), and a fake
// Meta Model API answering reply (a status, or a chat completion's text). Spark is set up only with key.
func sparkTeam(t *testing.T, key bool, reply *atomic.Pointer[string]) (*Pool, *atomic.Int32) {
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
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r := *reply.Load(); r {
		case "500":
			http.Error(w, "overloaded", 500)
		case "slow":
			time.Sleep(300 * time.Millisecond)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "muse-spark-1.3", "choices": []any{map[string]any{"message": map[string]string{"content": r}}}})
		}
	}))
	t.Cleanup(srv.Close)
	if key {
		t.Setenv("SPARK_TEST_KEY", "k-test")
	}
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Decision.Spark = store.SparkConfig{Address: srv.URL, Model: "muse-spark-1.3", KeyRef: "env:SPARK_TEST_KEY"}
		return nil
	})
	return p, &calls
}

func ptr(s string) *string { return &s }

const pickWeb = `{"recipient":"web","reasoning":"web owns the UI"}`

// Spark not active (the default before a key, or a key that does not resolve, or turned off): a send
// is exactly as before, with no call and no record.
func TestSparkInactiveChangesNothing(t *testing.T) {
	for name, set := range map[string]func(c *store.SparkConfig){
		"default, not set up": func(c *store.SparkConfig) { *c = store.SparkConfig{} },
		"no key":              func(c *store.SparkConfig) {},
		"off":                 func(c *store.SparkConfig) { c.Off = true },
	} {
		t.Run(name, func(t *testing.T) {
			var reply atomic.Pointer[string]
			reply.Store(ptr(pickWeb))
			p, calls := sparkTeam(t, name == "off", &reply)
			_ = p.st.UpdateConfig(func(c *store.Config) error { set(&c.Decision.Spark); return nil })
			res, err := p.messageSend(context.Background(), sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Plan the release"})
			if err != nil {
				t.Fatal(err)
			}
			if m := message(p, res.(map[string]string)["id"]); m.Triage != nil || m.Text != "Plan the release" || calls.Load() != 0 {
				t.Fatalf("message %+v, %d calls", m, calls.Load())
			}
			if _, on := p.sparkActive(); on {
				t.Fatal("active")
			}
		})
	}
}

func TestSparkOutcomes(t *testing.T) {
	defer func(d time.Duration) { decision.SparkTimeout = d }(decision.SparkTimeout)
	decision.SparkTimeout = 50 * time.Millisecond
	ctx := context.Background()
	var reply atomic.Pointer[string]
	reply.Store(ptr(pickWeb))
	p, _ := sparkTeam(t, true, &reply)
	sent := func(in sendIn) protocol.TeamMessage {
		t.Helper()
		res, err := p.messageSend(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return message(p, res.(map[string]string)["id"])
	}

	// Agreed: delivered with the record on it and as its first line.
	m := sent(sendIn{From: "leadaaaaaaaa", To: "web", Text: "Build the cart page"})
	if tr := m.Triage; tr == nil || tr.Outcome != "agreed" || tr.Pick != "web" || tr.Provider != "meta" || tr.Model != "muse-spark-1.3" ||
		m.Text != "[Triage · Muse Spark (Meta) · muse-spark-1.3] picked web: web owns the UI\nBuild the cart page" {
		t.Fatalf("agreed: %+v %+v", m, m.Triage)
	}

	// Rerouted: Spark picked another running child, so the task goes there; the record names both,
	// and the sender is told how to keep its own pick.
	res, err := p.messageSend(ctx, sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Build the cart page"})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]string)
	m = message(p, out["id"])
	ws, _ := p.session("webaaaaaaaaa")
	webName := ws.Name
	if tr := m.Triage; m.To != "webaaaaaaaaa" || out["to"] != webName || tr == nil || tr.Outcome != "rerouted" || tr.Named != "plan" || tr.Pick != "web" || tr.Reasoning != "web owns the UI" ||
		m.Text != "[Triage · Muse Spark (Meta) · muse-spark-1.3] rerouted from plan to web: web owns the UI\nBuild the cart page" {
		t.Fatalf("rerouted: %+v %+v", m, m.Triage)
	}
	if want := `Muse Spark delivered this to web instead of plan: web owns the UI. To have plan do it, resend to plan with --override "<reason>" and tell web to drop it.`; out["note"] != want {
		t.Fatalf("note %q", out["note"])
	}

	// Overridden: the sender's own pick, delivered, with Spark's pick and the reason recorded.
	m = sent(sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Build the cart page", Override: "plan wrote the cart spec"})
	if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "overridden" || tr.Pick != "web" || tr.Override != "plan wrote the cart spec" ||
		!strings.HasPrefix(m.Text, "[Triage · Muse Spark (Meta) · muse-spark-1.3] picked web: web owns the UI; the sender kept its own pick: plan wrote the cart spec\n") {
		t.Fatalf("overridden: %+v %+v", m, m.Triage)
	}

	// A child that is not running is not offered: Spark naming it anyway is no usable answer, so the
	// task goes as addressed, marked unavailable with the cause.
	webState := func(state string) {
		_ = p.st.Update(func(st *store.State) error { st.Sessions[sessionIndex(st, "webaaaaaaaaa")].State = state; return nil })
	}
	webState("stopped")
	m = sent(sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Build the cart page"})
	if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unavailable" || !strings.Contains(tr.Cause, "not one of the sender's children") {
		t.Fatalf("pick not running: %+v %+v", m, m.Triage)
	}
	webState("running")

	// Spark's pick stops while the check runs: checked again under the lock, so as addressed, unavailable.
	addr := p.st.Config().Decision.Spark.Address
	stopper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webState("stopped")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": pickWeb}}}})
	}))
	defer stopper.Close()
	_ = p.st.UpdateConfig(func(c *store.Config) error { c.Decision.Spark.Address = stopper.URL; return nil })
	m = sent(sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Build the cart page"})
	if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unavailable" || !strings.Contains(tr.Cause, "no longer a running member") {
		t.Fatalf("pick stopped during the check: %+v %+v", m, m.Triage)
	}
	webState("running")
	_ = p.st.UpdateConfig(func(c *store.Config) error { c.Decision.Spark.Address = addr; return nil })

	// Spark's pick leaves the team while the check runs (its session still running): as addressed, unavailable.
	webMember := func(state string) {
		_ = p.st.Update(func(st *store.State) error {
			for i, mb := range st.Domain.Teams[0].Members {
				if mb.SessionID == "webaaaaaaaaa" {
					st.Domain.Teams[0].Members[i].State = state
				}
			}
			return nil
		})
	}
	leaver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webMember("left")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": pickWeb}}}})
	}))
	defer leaver.Close()
	_ = p.st.UpdateConfig(func(c *store.Config) error { c.Decision.Spark.Address = leaver.URL; return nil })
	m = sent(sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Build the cart page"})
	if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unavailable" || !strings.Contains(tr.Cause, "no longer a running member") {
		t.Fatalf("pick left during the check: %+v %+v", m, m.Triage)
	}
	webMember("active")
	_ = p.st.UpdateConfig(func(c *store.Config) error { c.Decision.Spark.Address = addr; return nil })

	// Near the size limit the record's line is clipped, or left off, and never takes the body past it.
	for _, n := range []int{maxMessage - 20, maxMessage - 4, maxMessage - 3, maxMessage - 2, maxMessage} {
		m = sent(sendIn{From: "leadaaaaaaaa", To: "web", Text: strings.Repeat("x", n)})
		if m.Triage == nil || len(m.Text) > maxMessage || n == maxMessage-20 && !strings.HasPrefix(m.Text, "[Triage") {
			t.Fatalf("a %d-byte task: %d bytes, %+v", n, len(m.Text), m.Triage)
		}
	}

	// Unavailable: delivered as addressed, marked, with the cause on the record.
	for _, r := range []string{"500", "slow", "not json at all", `{"recipient":"other","reasoning":"x"}`} {
		reply.Store(&r)
		m = sent(sendIn{From: "leadaaaaaaaa", To: "plan", Text: "Plan the release"})
		if tr := m.Triage; m.To != "planaaaaaaaa" || tr == nil || tr.Outcome != "unavailable" || tr.Cause == "" ||
			m.Text != "[Triage · Muse Spark (Meta) · muse-spark-1.3] triage unavailable; sent as addressed\nPlan the release" {
			t.Fatalf("%s: %+v %+v", r, m, m.Triage)
		}
	}
}

// Only a tell from a parent to its child is checked: upward sends, asks, done and replies are not.
func TestSparkChecksOnlyParentToChildTells(t *testing.T) {
	var reply atomic.Pointer[string]
	reply.Store(ptr(pickWeb))
	p, calls := sparkTeam(t, true, &reply)
	ctx := context.Background()
	res, err := p.messageSend(ctx, sendIn{From: "webaaaaaaaaa", To: "lead", Text: "Ask: which port?"})
	if err != nil {
		t.Fatal(err)
	}
	ask := res.(map[string]string)["id"]
	for _, in := range []sendIn{
		{From: "webaaaaaaaaa", To: "lead", Text: "the build is green"},         // upward
		{From: "leadaaaaaaaa", To: "plan", Text: "Ask: is the plan ready?"},    // an ask
		{From: "leadaaaaaaaa", To: "plan", Text: "Done: merged", Type: "done"}, // done
		{From: "leadaaaaaaaa", To: "web", Text: "8080", ReplyTo: ask},          // a reply
	} {
		res, err := p.messageSend(ctx, in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		if m := message(p, res.(map[string]string)["id"]); m.Triage != nil {
			t.Fatalf("%+v was checked", in)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d calls", calls.Load())
	}
}

// With Spark active, a Claude SendMessage from a parent to its child is refused (it would pass the
// check by), and System One's route hold is off; neither changes while Spark is not active.
func TestSparkNativeSendAndRouteHold(t *testing.T) {
	var reply atomic.Pointer[string]
	reply.Store(ptr(pickWeb))
	p, _ := sparkTeam(t, false, &reply)
	lead, _ := p.session("leadaaaaaaaa")
	web, _ := p.session("webaaaaaaaaa")
	toWeb := map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": "webaaaaaaaaa"}}
	toLead := map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": "leadaaaaaaaa"}}
	held := func() bool {
		p.mu.Lock()
		p.routed = map[string]string{lead.ID: web.ID}
		p.mu.Unlock()
		_, deny := p.guardRoute(lead, map[string]any{"tool_name": "Edit", "tool_input": map[string]any{}})
		return deny
	}
	if out := p.guardSendMessage(lead, toWeb); out.Stdout != "" || !held() {
		t.Fatalf("not set up: send %s, held %v", out.Stdout, held())
	}
	t.Setenv("SPARK_TEST_KEY", "k-test")
	p.sparkKey = nil
	if out := p.guardSendMessage(lead, toWeb); !strings.Contains(out.Stdout, "deny") || !strings.Contains(out.Stdout, "so triage can record them") {
		t.Fatalf("active, parent to child: %s", out.Stdout)
	}
	if out := p.guardSendMessage(web, toLead); out.Stdout != "" {
		t.Fatalf("active, child to parent: %s", out.Stdout)
	}
	if held() {
		t.Fatal("System One's route hold while Spark is active")
	}
	if who, note := p.routeTurn(lead, "please build the whole cart page now"); who != "" || note != "" {
		t.Fatalf("route question while Spark is active: %q %q", who, note)
	}
}

// A key that did not resolve is tried again after sparkKeyRetry, so one added later starts working
// without saving the tab again.
func TestSparkKeyRetried(t *testing.T) {
	defer func(d time.Duration) { sparkKeyRetry = d }(sparkKeyRetry)
	var reply atomic.Pointer[string]
	reply.Store(ptr(pickWeb))
	p, _ := sparkTeam(t, false, &reply)
	if _, on := p.sparkActive(); on {
		t.Fatal("active with no key")
	}
	t.Setenv("SPARK_TEST_KEY", "k-test")
	if _, on := p.sparkActive(); on {
		t.Fatal("a failed key was looked up again within the retry window")
	}
	sparkKeyRetry = 0
	if _, on := p.sparkActive(); !on {
		t.Fatal("the key added later was not picked up")
	}
}

func TestClipSmallWidths(t *testing.T) {
	for n, want := range map[int]string{-2: "", 0: "", 1: "a…", 5: "abcde"} {
		if got := clip("abcde", n); got != want {
			t.Errorf("clip(abcde, %d) = %q, want %q", n, got, want)
		}
	}
}
