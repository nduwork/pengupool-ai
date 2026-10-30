package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// A paired device manages the owner's agent teams as the desktop app does: it sees who is on a team
// and where, settles a member's ask the team left to the owner, stops, interrupts or resumes a
// member, and asks for a change to who is on a team (approved like any other, on /next). The same
// visibility holds: teams with iOS access, and sessions on no team.

// museTeamAsks are the members' asks nobody answered that triage left to the owner (the app's Needs you).
func (p *Pool) museTeamAsks() []museAsk {
	st := p.st.State()
	ms := st.Domain.Messages
	var out []museAsk
	for i, m := range ms {
		if m.Type != "ask" || m.EscalatedAt == "" || !askOpen(ms, i) {
			continue
		}
		from, ok := p.session(m.From)
		if !ok || from.State != "running" {
			continue
		}
		to, _ := p.session(m.To)
		text := fmt.Sprintf("%s asked %s: %s", from.Name, firstNonEmpty(to.Name, m.To), m.Text)
		out = append(out, museAsk{id: askID("teamask", m.ID), kind: "team-ask", sessionID: from.ID, teamID: m.TeamID,
			text: text, at: parseMuseTime(m.At), payload: "team-ask\x00" + m.ID + "\x00" + m.Text})
	}
	return out
}

// museAnswerTeamAsk settles one: go on without the answer, close it, or the owner's own reply to
// the asker. Each tells the asker as the app's buttons do.
func (p *Pool) museAnswerTeamAsk(ctx context.Context, id string, in museAnswerIn) (any, error) {
	var m protocol.TeamMessage
	open := false
	ms := p.st.State().Domain.Messages
	for i, x := range ms {
		if x.ID == id && x.Type == "ask" {
			m, open = x, x.EscalatedAt != "" && askOpen(ms, i)
		}
	}
	if m.ID == "" || !p.museTeamVisible(m.TeamID) || !p.museVisible(m.From) {
		return nil, notFound("no ask teamask:%s", id)
	}
	if !open {
		return nil, conflict("the ask was answered or closed")
	}
	if payloadHash("team-ask\x00"+m.ID+"\x00"+m.Text) != in.PayloadHash {
		return nil, conflict("the ask changed or expired")
	}
	switch in.Action {
	case "go_on", "dismiss":
		return p.askSettle(ctx, settleIn{ID: m.ID, Action: in.Action})
	case "reply":
		text := strings.TrimSpace(in.Text)
		if text == "" {
			return nil, invalid("a reply needs its text")
		}
		to, _ := p.session(m.To)
		p.goOnAsk(m.ID, fmt.Sprintf("The owner answered your ask to %s: %s", firstNonEmpty(to.Name, m.To), clipRunes(text, 4000)))
		return nil, nil
	}
	return nil, invalid("action is go_on, dismiss or reply")
}

type museActionIn struct {
	Session string `json:"session"`
	Action  string `json:"action"` // stop | interrupt | resume
}

// museAction stops, interrupts or resumes one visible agent session. One already in the state asked
// for is no error: the owner's tap had its result.
func (p *Pool) museAction(ctx context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in museActionIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	s, ok := p.session(in.Session)
	if !ok || !harness.IsAgent(s.Harness) || !p.museVisible(s.ID) {
		return nil, notFound("no session %s", in.Session)
	}
	running := s.State == "running"
	switch in.Action {
	case "stop":
		if running {
			if err := p.hub.Kill(ctx, s.ID); err != nil {
				return nil, protocol.Errorf(protocol.CodeUnavailable, "%v", err)
			}
		}
	case "interrupt": // Esc, the agent's own interrupt key
		if running {
			if err := p.typeInto(s.ID, "\x1b"); err != nil {
				return nil, protocol.Errorf(protocol.CodeUnavailable, "%v", err)
			}
		}
	case "resume":
		if !running {
			if _, err := p.resume(ctx, sessionIn{SessionID: s.ID}); err != nil {
				return nil, err
			}
		}
	default:
		return nil, invalid("action is stop, interrupt or resume")
	}
	p.logf("owner via %s: %s %s", c.Device, in.Action, s.Name)
	s, _ = p.session(s.ID)
	return map[string]string{"session": s.ID, "action": in.Action, "state": string(s.State)}, nil
}

type museMemberIn struct {
	Team       string `json:"team"`
	Op         string `json:"op"`              // add (default) | remove
	Skill      string `json:"skill,omitempty"` // add: a helper agent, by name or id
	Repo       string `json:"repo,omitempty"`  // add: an SME for this repository, by name or id
	Member     string `json:"member,omitempty"`
	Assignment string `json:"assignment,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// museMember files a change to who is on a team, the request a LEAD makes with `team request`.
// It waits on /next for the owner's allow like any other: nothing launches before that.
func (p *Pool) museMember(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in museMemberIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	d := p.st.State().Domain
	t := findTeam(&d, in.Team)
	if t == nil || !p.museTeamVisible(t.ID) {
		return nil, notFound("no team %s", in.Team)
	}
	ch := protocol.FormationChange{Op: firstNonEmpty(in.Op, "add"), SkillID: in.Skill, RepoID: in.Repo, SessionID: in.Member,
		Assignment: in.Assignment, Reason: in.Reason}
	if ch.SkillID != "" {
		if sk := skillByNameOrID(&d, ch.SkillID); sk != nil {
			ch.SkillID = sk.ID
		}
	}
	if ch.RepoID != "" {
		if r := repoByNameOrID(&d, ch.RepoID); r != nil {
			ch.RepoID = r.ID
		}
	}
	a, err := p.newFormation(t.ID, ch, "pending")
	if err != nil {
		return nil, err
	}
	p.logf("owner via %s: requested %s on %s", c.Device, a.Detail, t.Name)
	return map[string]string{"ask_id": askID("approval", a.ID), "state": "pending", "detail": a.Detail}, nil
}
