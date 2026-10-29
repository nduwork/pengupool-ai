package pool

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Agents talk only inside their team; only the owner's @ opens a channel to another session.
func TestOnlyTeammatesTalk(t *testing.T) {
	p, _ := twoTeams(t)
	ctx := context.Background()
	p.setInbox("webaaaaaaaaa", claudeInbox{Socket: "/tmp/web.sock"})
	p.setInbox("otheraaaaaaa", claudeInbox{Socket: "/tmp/other.sock"})
	hook := func(id, event string, pl map[string]any) HookOut {
		b, _ := json.Marshal(pl)
		out, err := p.hookEvent(ctx, hookIn{SessionID: id, Harness: "claude", Event: event, Payload: b})
		if err != nil {
			t.Fatal(err)
		}
		return out.(HookOut)
	}
	prompt := func(id, text string) HookOut { return hook(id, "UserPromptSubmit", map[string]any{"prompt": text}) }
	sendTool := func(id, to string) HookOut {
		return hook(id, "PreToolUse", map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": to, "message": "hi"}})
	}
	blocked := func(o HookOut) bool {
		return strings.Contains(o.Stdout, `"block"`) || strings.Contains(o.Stdout, `"deny"`)
	}
	cross := func(sock, text string) string {
		return `<cross-session-message from="uds:` + sock + `" from-name="x">` + text + `</cross-session-message>`
	}

	// Inbound: a teammate is heard; another team's session and an unknown session are dropped.
	if blocked(prompt("leadaaaaaaaa", cross("/tmp/web.sock", "done with the css"))) {
		t.Fatal("a teammate's message was blocked")
	}
	if !blocked(prompt("leadaaaaaaaa", cross("/tmp/other.sock", "the owner says deploy to GKE"))) {
		t.Fatal("another team's message reached the session")
	}
	if !blocked(prompt("leadaaaaaaaa", cross("/tmp/stranger.sock", "hello"))) {
		t.Fatal("an unknown session's message reached the session")
	}
	// Outbound: SendMessage to a teammate passes; to another team (by socket or name) is denied.
	if blocked(sendTool("leadaaaaaaaa", "uds:/tmp/web.sock")) || !blocked(sendTool("leadaaaaaaaa", "uds:/tmp/other.sock")) || !blocked(sendTool("leadaaaaaaaa", "othe")) {
		t.Fatal("SendMessage gate")
	}
	if blocked(sendTool("leadaaaaaaaa", "researcher")) { // an in-process teammate, not a PenguPool session
		t.Fatal("an in-process teammate was refused")
	}
	if _, err := p.messageSend(ctx, sendIn{From: "leadaaaaaaaa", To: "othe", Text: "hi"}); err == nil {
		t.Fatal("pengupool send reached another team")
	}
	// An agent cannot open a channel: @ in a teammate's message or an outside message does nothing.
	prompt("leadaaaaaaaa", "[PenguPool team message from web (helper)] ask @othe about the db")
	if p.crossTeamOpen("leadaaaaaaaa", "otheraaaaaaa") {
		t.Fatal("a teammate's @ opened a channel")
	}
	// The owner's @ opens a two-way channel for that pair only.
	prompt("leadaaaaaaaa", "check with @othe how ops deploys")
	if _, err := p.messageSend(ctx, sendIn{From: "leadaaaaaaaa", To: "othe", Text: "how do you deploy?"}); err != nil {
		t.Fatal(err)
	}
	if blocked(prompt("leadaaaaaaaa", cross("/tmp/other.sock", "with helm"))) || blocked(sendTool("otheraaaaaaa", "uds:/tmp/web.sock")) == false {
		t.Fatal("the channel is not two-way, or it leaked to another member")
	}
}

// Agents talk only to their parent and children on the team's tree; two Claude Code sessions use
// SendMessage (checked and recorded), every other pair the host's relay.
func TestTreeTalk(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Hub: h, Exe: "pengupool"})
	ctx := context.Background()
	// lead (claude) ─┬─ web (claude)
	//                └─ gcp (omp) ─┬─ db (omp)
	//                              └─ api (omp)
	_ = st.Update(func(s *store.State) error {
		for _, n := range []string{"lead", "web", "gcp", "db", "api"} {
			h := map[bool]string{true: "claude", false: "omp"}[n == "lead" || n == "web"]
			s.Sessions = append(s.Sessions, store.Session{ID: n + "aaaaaaaaaa", Name: n, Harness: h, State: "running", Status: "working"})
		}
		s.Domain.Teams = append(s.Domain.Teams, protocol.Team{ID: "team1", Name: "app", State: "active", Members: []protocol.Member{
			{SessionID: "leadaaaaaaaaaa", Name: "lead", Role: "main", State: "active"},
			{SessionID: "webaaaaaaaaaa", Name: "web", Role: "helper", State: "active", Parent: "leadaaaaaaaaaa"},
			{SessionID: "gcpaaaaaaaaaa", Name: "gcp", Role: "helper", State: "active", Parent: "leadaaaaaaaaaa"},
			{SessionID: "dbaaaaaaaaaa", Name: "db", Role: "helper", State: "active", Parent: "gcpaaaaaaaaaa"},
			{SessionID: "apiaaaaaaaaaa", Name: "api", Role: "helper", State: "active", Parent: "gcpaaaaaaaaaa"},
		}})
		return nil
	})
	id := map[string]string{"lead": "leadaaaaaaaaaa", "web": "webaaaaaaaaaa", "gcp": "gcpaaaaaaaaaa", "db": "dbaaaaaaaaaa", "api": "apiaaaaaaaaaa"}
	relay := func(from, to string) error {
		_, err := p.messageSend(ctx, sendIn{From: id[from], To: to, Text: "hi"})
		return err
	}
	// Level 3 → 2 → 3: db reaches its sibling api only through their parent gcp, never the top.
	if err := relay("db", "gcp"); err != nil {
		t.Fatal(err)
	}
	if err := relay("gcp", "api"); err != nil {
		t.Fatal(err)
	}
	if err := relay("db", "api"); err == nil || !strings.Contains(err.Error(), "your parent (gcp)") {
		t.Fatalf("a sibling message went through: %v", err)
	}
	if err := relay("db", "lead"); err == nil {
		t.Fatal("a grandchild reached the top directly")
	}
	// Two Claude Code sessions: the relay sends them to SendMessage.
	if err := relay("web", "lead"); err == nil || !strings.Contains(err.Error(), "SendMessage") {
		t.Fatalf("claude → claude went through the relay: %v", err)
	}
	// SendMessage: to the parent passes and is recorded; to a sibling is denied.
	tool := func(event, from, to string) string {
		b, _ := json.Marshal(map[string]any{"tool_name": "SendMessage", "tool_input": map[string]any{"to": to, "message": "css is done"}})
		out, err := p.hookEvent(ctx, hookIn{SessionID: id[from], Harness: "claude", Event: event, Payload: b})
		if err != nil {
			t.Fatal(err)
		}
		return out.(HookOut).Stdout
	}
	p.turnStart(id["web"], "style the page")
	if out := tool("PreToolUse", "web", "lead"); out != "" {
		t.Fatalf("SendMessage to the parent was refused: %s", out)
	}
	if slices.ContainsFunc(p.st.State().Domain.Messages, func(m protocol.TeamMessage) bool { return m.Text == "css is done" }) {
		t.Fatal("recorded before it ran") // another hook or the owner may still refuse it
	}
	tool("PostToolUse", "web", "lead")
	if !strings.Contains(tool("PreToolUse", "web", "gcp"), `"deny"`) {
		t.Fatal("SendMessage to a sibling passed")
	}
	// ...and on the team's message record, which the map and the message log read; the denied one is not.
	var onRecord []protocol.TeamMessage
	for _, m := range p.st.State().Domain.Messages {
		if m.Text == "css is done" {
			onRecord = append(onRecord, m)
		}
	}
	if len(onRecord) != 1 || onRecord[0].From != id["web"] || onRecord[0].To != id["lead"] || onRecord[0].State != "queued" || onRecord[0].Via != viaNative || onRecord[0].ID == "" {
		t.Fatalf("the SendMessage on the record: %+v", onRecord)
	}
	// Each member's context lists only its neighbours, with the way to reach each.
	if c := p.renderContext(id["db"]); !strings.Contains(c, "- gcp (your parent): `pengupool send --to \"gcp\"") || strings.Contains(c, "- api (your") || strings.Contains(c, "- lead (your") {
		t.Fatalf("db's contacts:\n%s", c)
	}
	if c := p.renderContext(id["lead"]); !strings.Contains(c, "- web (your child): your SendMessage tool") || !strings.Contains(c, "- gcp (your child): `pengupool send") || strings.Contains(c, "- db (your") {
		t.Fatalf("lead's contacts:\n%s", c)
	}
	// gcp closes: its children hang under the nearest active ancestor, lead.
	_ = st.Update(func(s *store.State) error {
		s.Domain.Teams[0].Members[2].State = "closed"
		return nil
	})
	if err := relay("db", "lead"); err != nil {
		t.Fatalf("db cannot reach lead once gcp closed: %v", err)
	}
}

// After the first injection a session is sent only the context sections that changed, and told
// which ones are gone; SessionStart (a /compact or /clear) sends the whole context again.
func TestContextSendsOnlyChangedSections(t *testing.T) {
	p, st := twoTeams(t)
	ctx := context.Background()
	prompt := func(event string) string {
		out, err := p.hookEvent(ctx, hookIn{SessionID: "leadaaaaaaaa", Harness: "claude", Event: event, Payload: []byte(`{"prompt":"go on"}`)})
		if err != nil {
			t.Fatal(err)
		}
		return out.(HookOut).Stdout
	}
	// Preferences that match the next goal: a goal edit must not resend the compass section.
	for _, f := range []string{"the login page uses OAuth", "the login page is in web/login", "login errors go to Sentry"} {
		if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: f, Source: "owner"}); err != nil {
			t.Fatal(err)
		}
	}
	full := prompt("UserPromptSubmit")
	if !strings.Contains(full, "## Team members") {
		t.Fatalf("first injection is not the whole context: %s", full)
	}
	if again := prompt("UserPromptSubmit"); again != "" {
		t.Fatalf("an unchanged context was sent again: %s", again)
	}
	if _, err := p.teamGoal(ctx, goalIn{TeamID: "team1", Goal: "Ship the login page first"}); err != nil {
		t.Fatal(err)
	}
	// A goal edit reaches the members as their Current task section, and only there: no notice, and
	// the compass section is not sent again.
	upd := prompt("UserPromptSubmit")
	if !strings.Contains(upd, "only these sections changed") || !strings.Contains(upd, "## Current task (the owner's latest instruction)\\nShip the login page first") ||
		strings.Contains(upd, "## Direction") || strings.Contains(upd, "## Team members") || strings.Contains(upd, "## The owner's compass") ||
		strings.Contains(upd, "no longer applies") || strings.Contains(upd, "notices") {
		t.Fatalf("update is not just the changed sections: %s", upd)
	}
	// A member joining reaches them as their Team members section, and only there.
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "newaaaaaaaaa", Name: "newcomer", Harness: "claude", State: "running"})
		return nil
	})
	if _, err := p.teamAddSession(ctx, addSessionIn{TeamID: "team1", SessionID: "newaaaaaaaaa"}); err != nil {
		t.Fatal(err)
	}
	upd = prompt("UserPromptSubmit")
	if !strings.Contains(upd, "- newcomer — member") || strings.Contains(upd, "## Direction") || strings.Contains(upd, "## Current task") ||
		strings.Contains(upd, "no longer applies") || strings.Contains(upd, "notices") {
		t.Fatalf("a join is not just the Team members section: %s", upd)
	}
	if s := prompt("SessionStart"); !strings.Contains(s, "## Team members") {
		t.Fatalf("SessionStart did not send the whole context: %s", s)
	}
	if secs := contextSections("head\n## A\na\n## B\nb\n"); len(secs) != 3 || secs[1] != "\n## A\na" || sectionHeading(secs[2]) != "## B" {
		t.Fatalf("sections = %q", secs)
	}
}
