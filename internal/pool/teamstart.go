package pool

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// teamStartLead is the specialist that leads a team started from Welcome: the team-hub agent of the
// Specialist hub, which owns no repository.
const teamStartLead = "team-lead-agent"

type teamStartIn struct {
	SkillIDs []string `json:"skill_ids"`
	Folder   string   `json:"folder,omitempty"`  // the work repository; needed when a picked specialist works in one
	Harness  string   `json:"harness,omitempty"` // "" the lead's own choice; the whole team runs on it
}

type teamStartMember struct {
	SkillID   string `json:"skill_id"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// teamStart is Welcome's "Start with a specialist": a new team led by team-lead-agent, with the
// picked specialists as members. Everything is checked before anything starts. If the lead does not
// start, the team goes again and nothing is left behind; a specialist that does not start is reported
// and the team keeps its lead. Specialists join through team.formation, under the same rules as on the map.
func (p *Pool) teamStart(ctx context.Context, in teamStartIn) (any, error) {
	d := p.st.State().Domain
	var lead *protocol.Skill
	for i := range d.Skills {
		if d.Skills[i].Name == teamStartLead && teamHubSkill(d.Skills[i]) {
			lead = &d.Skills[i]
		}
	}
	if lead == nil {
		return nil, notFound("%s isn't installed, so a team can't be led: install it from Specialists", teamStartLead)
	}
	opts := p.agentOptions()
	def := "" // with no agent picked, the first installed one in the app's order (Claude Code first)
	for _, a := range harness.Agents {
		if _, ok := opts[a.ID]; ok && def == "" {
			def = a.ID
		}
	}
	if def == "" {
		return nil, protocol.Errorf(protocol.CodeHarnessUnavailable, "none of the agents is installed here")
	}
	agent, note, ok := helperAgent(*lead, in.Harness, def, opts)
	if !ok {
		return nil, protocol.Errorf(protocol.CodeHarnessUnavailable, "%s", note)
	}
	if in.Harness != "" && agent != in.Harness {
		return nil, invalid("%s", note)
	}
	var picks []protocol.Skill
	needRepo := false
	for _, id := range in.SkillIDs {
		sk := findSkill(&d, id)
		switch {
		case sk == nil:
			return nil, notFound("no specialist %s", id)
		case sk.ID == lead.ID:
			return nil, invalid("%s leads the team; pick other specialists", teamStartLead)
		case slices.ContainsFunc(picks, func(x protocol.Skill) bool { return x.ID == sk.ID }):
			continue
		}
		if a, n, ok := helperAgent(*sk, agent, agent, opts); !ok || a != agent {
			return nil, invalid("%s", firstNonEmpty(n, sk.Name+" can't run on "+agent))
		}
		needRepo = needRepo || !teamHubSkill(*sk)
		picks = append(picks, *sk)
	}
	var repo protocol.Repo
	if strings.TrimSpace(in.Folder) != "" {
		r, err := p.resolveSource(ctx, sourceIn{Kind: "local", Path: in.Folder})
		if err != nil {
			return nil, err
		}
		repo = r
	} else if needRepo {
		return nil, invalid("choose a folder: the specialists you picked work in a repository")
	}

	names := []string{teamStartLead}
	for _, sk := range picks {
		names = append(names, sk.Name)
	}
	team := protocol.Team{ID: protocol.NewID(), Name: firstNonEmpty(repo.Name, "team") + ": " + strings.Join(names, ", "), State: "active",
		Harness: agent, RepoID: repo.ID, IOSAccess: true, CreatedAt: now()}
	if err := p.update(func(st *store.State) error { st.Domain.Teams = append(st.Domain.Teams, team); return nil }); err != nil {
		return nil, err
	}
	drop := func() { // the lead did not start: no team stays behind
		_ = p.update(func(st *store.State) error {
			st.Domain.Teams = slices.DeleteFunc(st.Domain.Teams, func(t protocol.Team) bool { return t.ID == team.ID })
			return nil
		})
		_ = os.RemoveAll(p.workDir(team.ID))
	}
	m, err := p.startHub(ctx, &team, *lead, protocol.ProposedMember{SkillID: lead.ID, Name: slug(lead.Name), Role: "main",
		Assignment: "Lead the team: " + strings.Join(names[1:], ", "), Harness: agent})
	if err != nil {
		drop()
		return nil, fmt.Errorf("%s did not start: %v; no team was created", teamStartLead, err)
	}
	m.Coordinator = true
	if err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, team.ID)
		if t == nil {
			return conflict("the team closed meanwhile")
		}
		t.Members = append(t.Members, m)
		return nil
	}); err != nil {
		if p.hub != nil {
			_ = p.hub.Kill(context.Background(), m.SessionID)
		}
		drop()
		return nil, err
	}
	p.recordFormation(team.ID, protocol.FormationChange{Op: "add", SessionID: m.SessionID, Reason: "started with " + teamStartLead}, "start with "+strings.Join(names, ", "))

	members := []teamStartMember{}
	for _, sk := range picks {
		r := teamStartMember{SkillID: sk.ID, Name: sk.Name}
		res, err := p.teamFormation(ctx, formationIn{TeamID: team.ID, Change: protocol.FormationChange{Op: "add", SkillID: sk.ID, Harness: agent}})
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			if mm, ok := res.(protocol.Member); ok {
				r.SessionID = mm.SessionID
			}
		}
		members = append(members, r)
	}
	return map[string]any{"team_id": team.ID, "name": team.Name, "lead": m.SessionID, "members": members}, nil
}
