package pool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// deviceTeam is gateTeam with every team visible to a paired device.
func deviceTeam(t *testing.T) (*Pool, *sends) {
	t.Helper()
	p, _, sent := gateTeam(t)
	_ = p.st.Update(func(s *store.State) error {
		for i := range s.Domain.Teams {
			s.Domain.Teams[i].IOSAccess = true
		}
		return nil
	})
	return p, sent
}

func museTry(p *Pool, c gateway.Caller, typ string, params any) (any, error) {
	raw, _ := json.Marshal(params)
	return p.RPC(context.Background(), c, typ, raw)
}

// A device sees who is on a team, and where.
func TestMuseSessionsTeam(t *testing.T) {
	p, _ := deviceTeam(t)
	_ = p.st.Update(func(s *store.State) error {
		s.Domain.Teams[0].Members[1].Parent = "leadaaaaaaaa"
		s.Domain.Teams[0].Members[1].Assignment = "the login page"
		return nil
	})
	var sessions []museSessionView
	raw, _ := json.Marshal(museCall(t, p, "muse.sessions", map[string]any{}))
	if err := json.Unmarshal(raw, &sessions); err != nil {
		t.Fatal(err)
	}
	got := map[string]museSessionView{}
	for _, v := range sessions {
		got[v.Session] = v
	}
	if v := got["leadaaaaaaaa"]; v.Team != "app" || v.TeamID != "team1" || v.Role != "LEAD" || v.Status != "idle" || v.Name != "lead" {
		t.Errorf("lead = %+v", v)
	}
	if v := got["webaaaaaaaaa"]; v.Parent != "leadaaaaaaaa" || v.Assignment != "the login page" {
		t.Errorf("web = %+v", v)
	}
	if v := got["planaaaaaaaa"]; v.Role != "SPECIALIST" {
		t.Errorf("plan = %+v", v)
	}
}

// A member's ask the team left to the owner waits on /next, and a device settles it as the app does.
func TestMuseTeamAsk(t *testing.T) {
	p, sent := deviceTeam(t)
	escalated := func(id string) {
		_ = p.st.Update(func(s *store.State) error {
			addMessage(s, protocol.TeamMessage{ID: id, TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa",
				Text: "Which port?", State: "delivered", At: now(), Type: "ask", EscalatedAt: now()})
			return nil
		})
	}
	card := func(id string) museCard {
		t.Helper()
		for _, a := range p.museAsks() {
			if a.id == "teamask:"+id {
				return *p.museCard(a)
			}
		}
		t.Fatalf("no card for %s", id)
		return museCard{}
	}
	answer := func(c museCard, action, text string) error {
		_, err := museTry(p, museCaller(), "muse.answer", map[string]any{"ask_id": c.AskID, "action": action, "text": text,
			"payload_hash": c.PayloadHash, "idempotency_key": action + c.AskID})
		return err
	}

	escalated("ta1")
	c := card("ta1")
	if ids := []string{c.Actions[0].ID, c.Actions[1].ID, c.Actions[2].ID}; strings.Join(ids, ",") != "go_on,reply,dismiss" {
		t.Fatalf("actions = %+v", c.Actions)
	}
	if !strings.Contains(c.Description, "Which port?") && !strings.Contains(c.Title, "Which port?") {
		t.Errorf("card lacks the ask: %+v", c)
	}
	bad := c
	bad.PayloadHash = "x"
	if err := answer(bad, "go_on", ""); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("changed ask: %v", err)
	}
	if err := answer(c, "reply", " "); protocol.CodeOf(err) != protocol.CodeInvalidArgument {
		t.Fatalf("empty reply: %v", err)
	}
	if err := answer(c, "reply", "Use 8080."); err != nil {
		t.Fatal(err)
	}
	noticed := false
	for _, m := range p.st.State().Domain.Messages {
		noticed = noticed || m.To == "webaaaaaaaaa" && m.ReplyTo == "ta1" && strings.Contains(m.Text, "Use 8080.")
	}
	if !noticed {
		t.Fatalf("no notice to the asker; sent %v", sent.got)
	}
	if err := answer(c, "go_on", ""); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("second answer: %v", err)
	}

	escalated("ta2")
	if err := answer(card("ta2"), "dismiss", ""); err != nil {
		t.Fatal(err)
	}
	if m := message(p, "ta2"); m.SettledAt == "" {
		t.Fatal("dismiss left the ask open")
	}

	// A team without iOS access keeps its asks off the device.
	_ = p.st.Update(func(s *store.State) error {
		s.Domain.Teams[1].IOSAccess = false
		addMessage(s, protocol.TeamMessage{ID: "tax", TeamID: "team2", From: "webaaaaaaaaa", To: "otheraaaaaaa",
			Text: "Hidden?", State: "delivered", At: now(), Type: "ask", EscalatedAt: now()})
		return nil
	})
	for _, a := range p.museAsks() {
		if a.id == "teamask:tax" {
			t.Fatal("an ask on a hidden team is on /next")
		}
	}
	escalated("ta3")
	c3 := card("ta3")
	_ = p.st.Update(func(s *store.State) error { s.Domain.Teams[0].IOSAccess = false; return nil })
	if err := answer(c3, "go_on", ""); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("invisible team: %v", err)
	}
	for _, a := range p.museAsks() {
		if a.id == "teamask:ta3" {
			t.Fatal("a hidden team's ask is on /next")
		}
	}
}

// A device stops, interrupts or resumes a member; one already so is no error.
func TestMuseAction(t *testing.T) {
	p, _ := deviceTeam(t)
	var keys []string
	p.testKeys = func(id, k string) error { keys = append(keys, id+":"+k); return nil }
	act := func(c gateway.Caller, id, action string) (map[string]string, error) {
		res, err := museTry(p, c, "muse.action", map[string]string{"session": id, "action": action})
		m, _ := res.(map[string]string)
		return m, err
	}

	if r, err := act(museCaller(), "webaaaaaaaaa", "interrupt"); err != nil || r["state"] != "running" {
		t.Fatalf("interrupt = %v %v", r, err)
	}
	if len(keys) != 1 || keys[0] != "webaaaaaaaaa:\x1b" {
		t.Fatalf("keys = %q", keys)
	}
	if r, err := act(museCaller(), "webaaaaaaaaa", "resume"); err != nil || r["state"] != "running" {
		t.Fatalf("resume running = %v %v", r, err)
	}
	_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, "webaaaaaaaaa")].State = "exited"; return nil })
	for _, a := range []string{"stop", "interrupt"} {
		if r, err := act(museCaller(), "webaaaaaaaaa", a); err != nil || r["state"] != "exited" {
			t.Fatalf("%s stopped = %v %v", a, r, err)
		}
	}
	if len(keys) != 1 {
		t.Fatalf("interrupted a stopped session: %q", keys)
	}

	if _, err := act(museCaller(), "webaaaaaaaaa", "restart"); protocol.CodeOf(err) != protocol.CodeInvalidArgument {
		t.Fatalf("unknown action: %v", err)
	}
	if _, err := act(museCaller(), "nosuchsessio", "stop"); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("no session: %v", err)
	}
	_ = p.st.Update(func(s *store.State) error { s.Domain.Teams[0].IOSAccess = false; return nil })
	if _, err := act(museCaller(), "leadaaaaaaaa", "interrupt"); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("invisible team: %v", err)
	}
	for _, c := range []gateway.Caller{{Unix: true}, {Phone: true}} {
		if _, err := act(c, "planaaaaaaaa", "stop"); err == nil {
			t.Fatalf("%+v acted", c)
		}
	}
}

// A device asks for a change to who is on a team; it waits on /next for the owner's allow.
func TestMuseMember(t *testing.T) {
	p, _ := deviceTeam(t)
	res, err := museTry(p, museCaller(), "muse.member", map[string]string{"team": "team1", "skill": "planning-agent", "assignment": "plan v2"})
	if err != nil {
		t.Fatal(err)
	}
	askID := res.(map[string]string)["ask_id"]
	if !strings.HasPrefix(askID, "approval:") {
		t.Fatalf("result = %v", res)
	}
	found := false
	for _, a := range p.museAsks() {
		found = found || a.id == askID
	}
	if !found {
		t.Fatalf("%s not on /next", askID)
	}
	for _, s := range p.st.State().Sessions {
		if s.ID != "leadaaaaaaaa" && s.ID != "webaaaaaaaaa" && s.ID != "planaaaaaaaa" && s.ID != "otheraaaaaaa" {
			t.Fatalf("launched before the owner allowed: %s", s.ID)
		}
	}

	_ = p.st.Update(func(s *store.State) error { s.Domain.Teams[1].IOSAccess = false; return nil })
	if _, err := museTry(p, museCaller(), "muse.member", map[string]string{"team": "team2", "skill": "planning-agent"}); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("invisible team: %v", err)
	}
	for _, c := range []gateway.Caller{{Unix: true}, {Phone: true}} {
		if _, err := museTry(p, c, "muse.member", map[string]string{"team": "team1", "skill": "planning-agent"}); err == nil {
			t.Fatalf("%+v requested", c)
		}
	}
}
