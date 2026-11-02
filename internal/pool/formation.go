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
// Each member's label is derived from the team's tree, never stored or chosen (teamLabel): the LEAD
// is the root, who composes the results; it is a hub, not a boss, and cannot approve, merge or change
// the direction.

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
	Member     string `json:"member,omitempty"` // remove: a member by name or id
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
				return protocol.Approval{}, notFound("no active specialist %s", c.SkillID)
			}
			detail = "add " + sk.Name + " (specialist)"
		case c.RepoID != "" && c.SkillID == "":
			r := findRepo(&d, c.RepoID)
			if r == nil {
				return protocol.Approval{}, notFound("ingest the repository first")
			}
			detail = "add an SME for " + r.Name
		default:
			return protocol.Approval{}, invalid("add names a specialist or a repository (an SME)")
		}
	case "remove":
		m, err := findMember(t, c.SessionID)
		if err != nil {
			return protocol.Approval{}, err
		}
		c.SessionID = m.SessionID
		detail = c.Op + " " + m.Name
	default:
		return protocol.Approval{}, invalid("a formation change is add or remove")
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
	} // an older record's request to choose who leads changes nothing: the lead is the tree's root
	if err == nil {
		p.resolveApproval(a.ID, "approved")
	}
	return res, err
}

// addMember starts a freelancer (a skill helper) or an SME (an agent for one more repository) in its
// own worktree.
func (p *Pool) addMember(ctx context.Context, teamID string, c protocol.FormationChange, by string) (protocol.Member, error) {
	if c.SkillID != "" {
		r, err := p.placeHelper(ctx, placeIn{TeamID: teamID, SkillID: c.SkillID, Assignment: c.Assignment, Harness: c.Harness, Model: c.Model, Parent: c.Parent, by: by})
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

// leadOf is the session id of the team's LEAD: the root of its tree, the main worker, else the first
// active member with no active parent. "" for a team with no active member.
func leadOf(t *protocol.Team) string {
	for _, m := range t.Members {
		if m.State == "active" && m.Role == "main" {
			return m.SessionID
		}
	}
	for _, m := range t.Members {
		if m.State == "active" && treeParent(t, m.SessionID) == "" {
			return m.SessionID
		}
	}
	return ""
}

// teamLabel is a member's label, derived, never stored: LEAD for the root of the team's tree (with
// a repository or without), SPECIALIST for an agent taken from the specialist pool, SME for a member the
// owner added that has a repository behind it (its worktree, or a session working in a known
// repository), "" for any other member. web/src/views/Formation.tsx mirrors it.
func teamLabel(st *store.State, t *protocol.Team, m protocol.Member) string {
	switch {
	case m.SessionID == leadOf(t):
		return "LEAD"
	case m.SkillID != "":
		return "SPECIALIST"
	case m.RepoID != "" || m.Worktree != "":
		return "SME"
	}
	if i := sessionIndex(st, m.SessionID); i >= 0 && st.Sessions[i].Cwd != "" {
		for _, r := range st.Domain.Repos {
			if r.Path != "" && within(st.Sessions[i].Cwd, r.Path) {
				return "SME"
			}
		}
	}
	return ""
}

func assignmentSuffix(a string) string {
	if a == "" {
		return ""
	}
	return ": " + clip(a, 200)
}
