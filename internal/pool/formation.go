package pool

import (
	"context"
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Formation: who is on a team changes only through an approval. A change the owner makes in the
// app is approved in the same step (team.formation records it as approved); a change a member asks
// for (team.request) waits as a pending approval. Members join and leave at any time: a member that
// leaves keeps its own memory and experience (its scope), while the team's record keeps who joined
// and who left (each a direction revision, with the reason) and every entry the member wrote while
// it was there.
//
// Each team has one anchor (Team.AnchorID): the member every message reaches, through a digest, and
// who composes the results and writes the reflection. It is a hub, not a boss: it cannot approve,
// merge or change the direction. Moving the anchor is a formation change too.

type formationIn struct {
	TeamID string                   `json:"team_id"`
	Change protocol.FormationChange `json:"change"`
}

// teamFormation is the owner's change, approved inline: it is executed and recorded as an approved
// formation approval.
func (p *Pool) teamFormation(ctx context.Context, in formationIn) (any, error) {
	in.Change.RequestedBy = ""
	a, err := p.newFormation(in.TeamID, in.Change, "pending") // approved by this call, below
	if err != nil {
		return nil, err
	}
	res, err := p.applyFormation(ctx, a)
	if err != nil {
		p.resolveApproval(a.ID, "denied")
		return nil, err
	}
	return res, nil
}

type requestIn struct {
	SessionID  string `json:"session_id"` // the requesting member
	Op         string `json:"op"`
	SkillID    string `json:"skill_id,omitempty"`
	RepoID     string `json:"repo_id,omitempty"`
	Member     string `json:"member,omitempty"` // remove, anchor: a member by name or id
	Assignment string `json:"assignment,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// teamRequest is a member asking for a formation change; it waits for the owner.
func (p *Pool) teamRequest(_ context.Context, in requestIn) (any, error) {
	d := p.st.State().Domain
	t := teamOf(&d, in.SessionID)
	if t == nil {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only an active team member can request a formation change")
	}
	c := protocol.FormationChange{Op: in.Op, SkillID: in.SkillID, RepoID: in.RepoID, SessionID: in.Member,
		Assignment: in.Assignment, Reason: in.Reason, RequestedBy: in.SessionID}
	if c.SessionID != "" { // a member named by name or id prefix
		m, err := findMember(t, c.SessionID)
		if err != nil {
			return nil, err
		}
		c.SessionID = m.SessionID
	}
	if c.SkillID != "" {
		if sk := skillByNameOrID(&d, c.SkillID); sk != nil {
			c.SkillID = sk.ID
		}
	}
	if c.RepoID != "" {
		if r := repoByNameOrID(&d, c.RepoID); r != nil {
			c.RepoID = r.ID
		}
	}
	a, err := p.newFormation(t.ID, c, "pending")
	if err != nil {
		return nil, err
	}
	p.logLine(in.SessionID, "team: requested "+a.Detail+" (waiting for the owner)")
	return map[string]string{"approval": a.ID, "state": "pending", "detail": a.Detail}, nil
}

func findMember(t *protocol.Team, ref string) (protocol.Member, error) {
	var hits []protocol.Member
	for _, m := range t.Members {
		if m.State == "active" && (m.SessionID == ref || strings.EqualFold(m.Name, ref) || len(ref) >= 8 && strings.HasPrefix(m.SessionID, ref)) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 0:
		return protocol.Member{}, notFound("%q is not an active member of %s", ref, t.Name)
	case 1:
		return hits[0], nil
	}
	return protocol.Member{}, invalid("%q names more than one member; use the session id", ref)
}

func skillByNameOrID(d *protocol.Domain, ref string) *protocol.Skill {
	for i := range d.Skills {
		if d.Skills[i].ID == ref || strings.EqualFold(d.Skills[i].Name, ref) {
			return &d.Skills[i]
		}
	}
	return nil
}

func repoByNameOrID(d *protocol.Domain, ref string) *protocol.Repo {
	for i := range d.Repos {
		if d.Repos[i].ID == ref || strings.EqualFold(d.Repos[i].Name, ref) || d.Repos[i].Path == ref {
			return &d.Repos[i]
		}
	}
	return nil
}

// newFormation validates a change and stores it as an approval in the given state.
func (p *Pool) newFormation(teamID string, c protocol.FormationChange, state string) (protocol.Approval, error) {
	d := p.st.State().Domain
	t := findTeam(&d, teamID)
	if t == nil || t.State != "active" {
		return protocol.Approval{}, conflict("the team is not active")
	}
	var detail string
	switch c.Op {
	case "add":
		switch {
		case c.SkillID != "" && c.RepoID == "":
			sk := findSkill(&d, c.SkillID)
			if sk == nil {
				return protocol.Approval{}, notFound("no active helper agent %s", c.SkillID)
			}
			detail = "add " + sk.Name + " (helper agent)"
			if anchorSkill(*sk) {
				detail = "add " + sk.Name + " as the team's anchor"
			}
		case c.RepoID != "" && c.SkillID == "":
			r := findRepo(&d, c.RepoID)
			if r == nil {
				return protocol.Approval{}, notFound("ingest the repository first")
			}
			detail = "add an SME for " + r.Name
		default:
			return protocol.Approval{}, invalid("add names a helper agent or a repository (an SME)")
		}
	case "remove", "anchor":
		m, err := findMember(t, c.SessionID)
		if err != nil {
			return protocol.Approval{}, err
		}
		c.SessionID = m.SessionID
		detail = c.Op + " " + m.Name
		if c.Op == "anchor" {
			detail = "make " + m.Name + " the anchor"
		}
	default:
		return protocol.Approval{}, invalid("a formation change is add, remove or anchor")
	}
	if c.Assignment != "" {
		detail += ": " + c.Assignment
	}
	if c.Reason != "" {
		detail += " (" + c.Reason + ")"
	}
	a := protocol.Approval{ID: protocol.NewID(), Kind: "formation", TeamID: t.ID, TaskID: t.TaskID, Change: &c,
		Detail: t.Name + ": " + detail, State: state, CreatedAt: now()}
	return a, p.update(func(st *store.State) error {
		st.Domain.Approvals = append(st.Domain.Approvals, a)
		return nil
	})
}

// recordFormation stores a formation change the owner made directly (a map drag, grouping sessions)
// as an approved formation approval, so every change to a team is on file.
func (p *Pool) recordFormation(teamID string, c protocol.FormationChange, detail string) {
	name := teamID
	d := p.st.State().Domain
	if t := findTeam(&d, teamID); t != nil {
		name = t.Name
	}
	_ = p.update(func(st *store.State) error {
		st.Domain.Approvals = append(st.Domain.Approvals, protocol.Approval{ID: protocol.NewID(), Kind: "formation", TeamID: teamID,
			Change: &c, Detail: name + ": " + detail, State: "approved", CreatedAt: now()})
		return nil
	})
}

// applyFormation executes an approved formation change, once: a second decision on the same
// approval finds it no longer pending.
func (p *Pool) applyFormation(ctx context.Context, a protocol.Approval) (any, error) {
	p.formMu.Lock() // ponytail: one formation change at a time; they are rare and owner-paced
	defer p.formMu.Unlock()
	d := p.st.State().Domain
	if i := slices.IndexFunc(d.Approvals, func(x protocol.Approval) bool { return x.ID == a.ID }); i < 0 || d.Approvals[i].State != "pending" {
		return nil, conflict("the approval is no longer pending")
	}
	c := *a.Change
	by := "owner"
	if c.RequestedBy != "" {
		if s, ok := p.session(c.RequestedBy); ok {
			by = "owner, at " + s.Name + "'s request"
		}
	}
	var res any
	var err error
	switch c.Op {
	case "add":
		res, err = p.addMember(ctx, a.TeamID, c, by)
	case "remove":
		res, err = p.closeHelper(ctx, closeIn{TeamID: a.TeamID, SessionID: c.SessionID, KeepWorktree: true, reason: c.Reason, by: by})
	case "anchor":
		res, err = p.setAnchor(a.TeamID, c.SessionID)
	}
	if err == nil {
		p.resolveApproval(a.ID, "approved")
	}
	return res, err
}

// addMember starts a freelancer (a skill helper) or an SME (an agent for one more repository) in its
// own worktree.
func (p *Pool) addMember(ctx context.Context, teamID string, c protocol.FormationChange, by string) (protocol.Member, error) {
	if c.SkillID != "" {
		r, err := p.placeHelper(ctx, placeIn{TeamID: teamID, SkillID: c.SkillID, Assignment: c.Assignment, Harness: c.Harness, Model: c.Model, by: by})
		if err != nil {
			return protocol.Member{}, err
		}
		return r.(protocol.Member), nil
	}
	d := p.st.State().Domain
	team := findTeam(&d, teamID)
	if team == nil || team.State != "active" {
		return protocol.Member{}, conflict("the team is not active")
	}
	repo := findRepo(&d, c.RepoID)
	if repo == nil {
		return protocol.Member{}, notFound("no repository %s", c.RepoID)
	}
	if !repo.Git {
		return protocol.Member{}, invalid("an SME works in a worktree, so its repository must use Git")
	}
	if n := activeMembers(team); n >= maxMembers {
		return protocol.Member{}, conflict("a team has at most %d members", maxMembers)
	}
	task := protocol.Task{Title: team.Name, Objective: team.Goal}
	if t := findTask(&d, team.TaskID); t != nil {
		task = *t
	}
	assignment := firstNonEmpty(c.Assignment, "Own the "+repo.Name+" side of: "+firstNonEmpty(task.Objective, team.Name))
	m, err := p.startMember(ctx, team, *repo, task, protocol.ProposedMember{Name: slug(repo.Name), Role: "sme", Assignment: assignment, Harness: c.Harness, Model: c.Model}, headCommit(repo.Path))
	if err != nil {
		return m, err
	}
	m.RepoID = repo.ID
	if err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, teamID)
		if t == nil || t.State != "active" {
			return conflict("the team closed meanwhile")
		}
		t.Members = append(t.Members, m)
		if repo.ID != t.RepoID && !slices.Contains(t.Repos, repo.ID) {
			t.Repos = append(t.Repos, repo.ID)
		}
		return nil
	}); err != nil {
		_ = p.hub.Kill(context.Background(), m.SessionID)
		_ = removeWorktree(repo.Path, m.Worktree, m.Branch, true)
		return m, err
	}
	return m, nil
}

// anchorOf is the session id of the team's anchor: its current task's anchor while that member is
// active, else its coordinator, else its main worker.
func anchorOf(d *protocol.Domain, t *protocol.Team) string {
	if t.AnchorID != "" {
		for _, m := range t.Members {
			if m.SessionID == t.AnchorID && m.State == "active" {
				return m.SessionID
			}
		}
	}
	for _, role := range []string{"coordinator", "main"} {
		for _, m := range t.Members {
			if m.State == "active" && (role == "coordinator" && m.Coordinator || role == "main" && m.Role == "main") {
				return m.SessionID
			}
		}
	}
	return ""
}

func (p *Pool) setAnchor(teamID, sessionID string) (any, error) {
	err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, teamID)
		if t == nil {
			return notFound("no team %s", teamID)
		}
		t.AnchorID = sessionID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{"anchor": sessionID}, nil
}

func assignmentSuffix(a string) string {
	if a == "" {
		return ""
	}
	return ": " + clip(a, 200)
}
