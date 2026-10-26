package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Team lifecycle: a task against an ingested work repository gets a versioned proposal (the main
// worker plus only the optional helpers the task needs), one launch approval, then sessions. No
// session starts before approval. Every later change to who is on the team is a formation approval
// (formation.go). A team outlives its task: a new task for the same team starts its next cycle. Only
// the owner completes or cancels a task; nothing is merged automatically.

// proposedHelpers caps how many helpers the formation rules propose; a team itself has no size limit.
const proposedHelpers = 7
const maxCandidates = 20

type taskIn struct {
	Title        string   `json:"title"`
	Objective    string   `json:"objective"`
	RepoID       string   `json:"repo_id"`
	Harness      string   `json:"harness,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func (p *Pool) taskCreate(ctx context.Context, in taskIn) (any, error) {
	in.Title, in.Objective = strings.TrimSpace(in.Title), strings.TrimSpace(in.Objective)
	if in.Title == "" || in.Objective == "" {
		return nil, invalid("a task needs a title and an objective")
	}
	if in.Harness == "" {
		in.Harness = harness.Claude
	}
	if !harness.IsAgent(in.Harness) {
		return nil, invalid("a team runs agents (%s), not %q", agentIDs(), in.Harness)
	}
	d := p.st.State().Domain
	if findRepo(&d, in.RepoID) == nil {
		return nil, notFound("ingest the work repository first")
	}
	t := protocol.Task{
		ID: protocol.NewID(), Title: in.Title, Objective: in.Objective, RepoID: in.RepoID, Harness: in.Harness,
		Capabilities: normTags(in.Capabilities), State: "proposed", CreatedAt: now(), UpdatedAt: now(),
	}
	if err := p.update(func(st *store.State) error {
		st.Domain.Tasks = append(st.Domain.Tasks, t)
		return nil
	}); err != nil {
		return nil, err
	}
	prop, err := p.propose(ctx, t, 1)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": t, "proposal": prop}, nil
}

func (p *Pool) taskRepropose(ctx context.Context, in idIn) (any, error) {
	d := p.st.State().Domain
	t := findTask(&d, in.ID)
	if t == nil {
		return nil, notFound("no task %s", in.ID)
	}
	if t.State != "proposed" {
		return nil, conflict("the task is %s", t.State)
	}
	return p.propose(ctx, *t, latestProposal(&d, t.ID).Version+1)
}

func latestProposal(d *protocol.Domain, taskID string) protocol.Proposal {
	var best protocol.Proposal
	for _, pr := range d.Proposals {
		if pr.TaskID == taskID && pr.Version > best.Version {
			best = pr
		}
	}
	return best
}

type candidate struct {
	skill   protocol.Skill
	covered []string
	score   float64
	harness string // the provider's agent pick for this task, if any
}

// helperAgent picks the agent a skill's helper runs on: the first of pick, the skill's preference,
// the team default and then its required agents that is installed and allowed by `requires`.
// ok is false when no installed agent satisfies `requires`; note says why a choice was passed over.
func helperAgent(sk protocol.Skill, pick, teamDefault string, installed map[string]string) (agent, note string, ok bool) {
	allowed := func(a string) bool {
		_, in := installed[a]
		return a != "" && in && (len(sk.Requires) == 0 || slices.Contains(sk.Requires, a))
	}
	want := firstNonEmpty(pick, sk.Harness)
	for _, a := range append([]string{pick, sk.Harness, teamDefault}, sk.Requires...) {
		if !allowed(a) {
			continue
		}
		if want != "" && a != want {
			note = fmt.Sprintf("%s runs on %s: %s is not installed or not allowed by its requires", sk.Name, a, want)
		}
		return a, note, true
	}
	return "", fmt.Sprintf("%s needs %s, and none is installed here", sk.Name, strings.Join(sk.Requires, " or ")), false
}

// agentOptions lists the agents installed here, as the choice options the provider picks from.
func (p *Pool) agentOptions() map[string]string {
	out := map[string]string{}
	for _, a := range harness.Agents {
		if _, err := harness.Lookup(a.ID, p.env); err != nil {
			continue
		}
		d := a.Label + ": " + a.Hint
		if a.Typed {
			d += "; receives team messages only by paste when idle, so it suits a focused helper more than a coordinator"
		}
		out[a.ID] = d
	}
	return out
}

// propose builds a proposal: deterministic capability checks and ranking first, then the provider
// (if any) answers finite questions about the top candidates. The main worker is always there and
// zero helpers is a valid team.
func (p *Pool) propose(ctx context.Context, t protocol.Task, version int) (protocol.Proposal, error) {
	st := p.st.State()
	d := st.Domain
	repo := findRepo(&d, t.RepoID)
	text := t.Title + " " + t.Objective
	required := t.Capabilities
	if len(required) == 0 {
		required = inferCapabilities(&d, text)
	}
	cands := rankCandidates(&d, t, required)
	prop := protocol.Proposal{ID: protocol.NewID(), TaskID: t.ID, Version: version, Provider: "rules", CreatedAt: now(), Confidence: 1}
	mainName := slug(repo.Name) + "-main"
	members := []protocol.ProposedMember{{Name: mainName, Role: "main", Assignment: t.Objective, Coordinator: true}}

	var helpers []candidate
	prov := p.decider()
	cfg := st.Domain // unused copy guard
	_ = cfg
	if prov != nil && len(cands) > 0 {
		res, qs, err := p.askTeam(ctx, prov, t, cands)
		line := decision.Line("team.form", res, qs, err != nil, err)
		p.logf("pool: %s", line)
		prop.Decision = line
		switch {
		case err != nil && p.st.Config().Decision.RulesFallback:
			prop.Fallback = true
			prop.Warnings = append(prop.Warnings, "provider failed ("+err.Error()+"); rules fallback used")
			helpers = rulesHelpers(cands)
		case err != nil:
			prop.Fallback = true
			prop.Warnings = append(prop.Warnings, "provider failed ("+err.Error()+"); main-only proposal")
		default:
			prop.Provider, prop.Model = res.Provider, res.Model
			size := 1
			if a, ok := res.Answers["team_size"]; ok {
				fmt.Sscanf(a.Choice, "%d", &size)
				prop.Confidence = a.Confidence
			}
			type hit struct {
				c   candidate
				yes float64
			}
			var hits []hit
			for i, c := range cands {
				if a := res.Answers[fmt.Sprintf("helps_%d", i)]; a.Yes >= 0.5 {
					// The provider may move a helper to another installed agent; an unsure answer
					// (calibrated below the threshold) keeps the skill's own preference.
					if ag, ok := res.Answers[fmt.Sprintf("agent_%d", i)]; ok && ag.Choice != "" && (!res.Calibrated || ag.Confidence >= decision.Thresholds["agent"]) {
						c.harness = ag.Choice
					}
					hits = append(hits, hit{c, a.Yes})
				}
			}
			sort.SliceStable(hits, func(a, b int) bool { return hits[a].yes > hits[b].yes })
			for _, h := range hits {
				if len(helpers) < size-1 {
					helpers = append(helpers, h.c)
				}
			}
			if !res.Calibrated {
				prop.Confidence = 0
				prop.Warnings = append(prop.Warnings, "the provider has no calibrated confidence; review the members yourself")
			} else if prop.Confidence < decision.Thresholds["team"] {
				prop.Warnings = append(prop.Warnings, fmt.Sprintf("low confidence %.2f; review the members", prop.Confidence))
			}
		}
		raw, _ := json.Marshal(map[string]string{"provider": res.Provider, "model": res.Model})
		prop.Config = raw
	} else {
		helpers = rulesHelpers(cands)
	}
	covered := map[string]bool{}
	installed := p.agentOptions()
	for _, h := range helpers {
		ag, note, ok := helperAgent(h.skill, h.harness, t.Harness, installed)
		if note != "" {
			prop.Warnings = append(prop.Warnings, note)
		}
		if !ok {
			continue
		}
		members = append(members, protocol.ProposedMember{
			SkillID: h.skill.ID, Name: slug(h.skill.Name), Role: "helper", Harness: ag,
			Assignment: "Assist with " + strings.Join(h.covered, ", ") + " for: " + t.Title,
		})
		for _, c := range h.covered {
			covered[c] = true
		}
	}
	for _, r := range required {
		if !covered[r] {
			prop.Gaps = append(prop.Gaps, r)
		}
	}
	if len(prop.Gaps) > 0 {
		prop.Warnings = append(prop.Warnings, "optional helper coverage is incomplete; the main worker can still start")
	}
	if repo != nil && !repo.Git && len(members) > 1 {
		members = members[:1]
		prop.Warnings = append(prop.Warnings, "helpers need a Git repository for their worktrees; main-only")
	}
	prop.Members = members
	err := p.update(func(st *store.State) error {
		st.Domain.Proposals = append(st.Domain.Proposals, prop)
		// One open launch approval per task, for its latest proposal version.
		for i := range st.Domain.Approvals {
			a := &st.Domain.Approvals[i]
			if a.Kind == "launch" && a.TaskID == t.ID && a.State == "pending" {
				a.State = "expired"
			}
		}
		st.Domain.Approvals = append(st.Domain.Approvals, protocol.Approval{
			ID: protocol.NewID(), Kind: "launch", TaskID: t.ID, Version: version, State: "pending", CreatedAt: now(),
			Detail: fmt.Sprintf("%s: %d member(s)", t.Title, len(members)),
		})
		return nil
	})
	return prop, err
}

// inferCapabilities finds only known whole-word tags or skill names in the task text.
func inferCapabilities(d *protocol.Domain, text string) []string {
	ws := map[string]bool{}
	for _, w := range words(text) {
		ws[w] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, sk := range d.Skills {
		for _, c := range sk.Capabilities {
			if ws[c] && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

func rankCandidates(d *protocol.Domain, t protocol.Task, required []string) []candidate {
	var out []candidate
	for _, sk := range d.Skills {
		if _, err := os.Stat(sk.Path); err != nil {
			continue
		}
		c := candidate{skill: sk}
		for _, r := range required {
			for _, cap := range sk.Capabilities {
				if cap == r {
					c.covered = append(c.covered, r)
				}
			}
		}
		c.score = float64(len(c.covered))*10 + overlap(sk.Description+" "+sk.Method+" "+strings.Join(sk.Capabilities, " "), t.Title+" "+t.Objective)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].skill.Name < out[j].skill.Name
	})
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

func rulesHelpers(cands []candidate) []candidate {
	var out []candidate
	for _, c := range cands {
		if len(c.covered) > 0 && len(out) < proposedHelpers {
			out = append(out, c)
		}
	}
	return out
}

func (p *Pool) askTeam(ctx context.Context, prov decision.Provider, t protocol.Task, cands []candidate) (decision.Response, []decision.Question, error) {
	state := map[string]any{"task": map[string]string{"title": t.Title, "objective": t.Objective}}
	var cs []map[string]any
	qs := []decision.Question{{Name: "team_size", Type: decision.Choice,
		Instructions: "How many members does this task need in total, counting the main worker? Most tasks need only the main worker.",
		Options:      map[string]string{"1": "main worker only", "2": "one helper", "3": "two helpers", "4": "three helpers"}}}
	agents := p.agentOptions()
	for i, c := range cands {
		cs = append(cs, map[string]any{"name": c.skill.Name, "capabilities": c.skill.Capabilities, "description": c.skill.Description,
			"preferred_agent": firstNonEmpty(c.skill.Harness, "none")})
		qs = append(qs, decision.Question{Name: fmt.Sprintf("helps_%d", i), Type: decision.Predicate,
			Instructions: fmt.Sprintf("Would the skill agent `candidates[%d]` materially help the main worker complete `task`?", i)})
		opts := map[string]string{}
		for id, d := range agents {
			if len(c.skill.Requires) == 0 || slices.Contains(c.skill.Requires, id) {
				opts[id] = d
			}
		}
		if len(opts) > 1 {
			qs = append(qs, decision.Question{Name: fmt.Sprintf("agent_%d", i), Type: decision.Choice, Options: opts,
				Instructions: fmt.Sprintf("If `candidates[%d]` joins, which agent should run it for `task`? Keep `candidates[%d].preferred_agent` unless the task clearly suits another one better.", i, i)})
		}
	}
	state["candidates"] = cs
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := prov.Decide(ctx, decision.Request{State: state, Questions: qs})
	return res, qs, err
}

type proposalEditIn struct {
	TaskID  string                    `json:"task_id"`
	Version int                       `json:"version"`
	Members []protocol.ProposedMember `json:"members"`
}

// proposalEdit saves the owner's member edits as a new version; the base version must be current.
func (p *Pool) proposalEdit(_ context.Context, in proposalEditIn) (any, error) {
	var out protocol.Proposal
	err := p.update(func(st *store.State) error {
		t := findTask(&st.Domain, in.TaskID)
		if t == nil || t.State != "proposed" {
			return conflict("the task is not waiting for a launch")
		}
		cur := latestProposal(&st.Domain, in.TaskID)
		if cur.Version != in.Version {
			return conflict("the proposal changed (now version %d); review it again", cur.Version)
		}
		mains := 0
		for _, m := range in.Members {
			if m.Role == "main" {
				mains++
			} else if findSkill(&st.Domain, m.SkillID) == nil {
				return invalid("helper %s is not among the helper agents", m.Name)
			}
			if m.Harness != "" && !harness.IsAgent(m.Harness) {
				return invalid("%s: %q is not an agent (%s)", m.Name, m.Harness, agentIDs())
			}
			if sk := findSkill(&st.Domain, m.SkillID); m.Role == "helper" && sk != nil && len(sk.Requires) > 0 && !slices.Contains(sk.Requires, firstNonEmpty(m.Harness, t.Harness)) {
				return invalid("%s can only run on %s", m.Name, strings.Join(sk.Requires, " or "))
			}
		}
		if mains != 1 {
			return invalid("a team has exactly one main worker")
		}
		out = cur
		out.ID, out.Version, out.Members, out.CreatedAt = protocol.NewID(), cur.Version+1, in.Members, now()
		out.Provider = "owner edit of v" + fmt.Sprint(cur.Version)
		st.Domain.Proposals = append(st.Domain.Proposals, out)
		for i := range st.Domain.Approvals {
			a := &st.Domain.Approvals[i]
			if a.Kind == "launch" && a.TaskID == in.TaskID && a.State == "pending" {
				a.Version = out.Version
				a.Detail = fmt.Sprintf("%s: %d member(s)", t.Title, len(out.Members))
			}
		}
		return nil
	})
	return out, err
}

type decideIn struct {
	ID       string `json:"id"`
	Decision string `json:"decision"` // approve | deny
	Version  int    `json:"version,omitempty"`
	// A member's add request: the agent and model the owner picked while approving it.
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
}

func (p *Pool) approvalDecide(ctx context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in decideIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	d := p.st.State().Domain
	var a *protocol.Approval
	for i := range d.Approvals {
		if d.Approvals[i].ID == in.ID {
			a = &d.Approvals[i]
		}
	}
	if a == nil || a.State != "pending" {
		return nil, conflict("the approval is no longer pending")
	}
	if c.Kind == protocol.KindIOS {
		if err := p.iosMayReach(c, a.SessionID); err != nil {
			return nil, err
		}
		if a.Kind == "launch" || a.Kind == "formation" {
			return nil, protocol.Errorf(protocol.CodeForbidden, "team launches and formation changes are approved on the desktop")
		}
	}
	switch a.Kind {
	case "permission":
		// Permission requests are answered in the terminal now; one a previous daemon left pending just ends.
		p.resolveApproval(a.ID, "expired")
		return nil, nil
	case "formation":
		if in.Decision == "deny" {
			p.resolveApproval(a.ID, "denied")
			p.approvalChose(*a, "deny")
			if a.Change != nil && a.Change.RequestedBy != "" {
				p.notice(a.TeamID, a.Change.RequestedBy, "The owner declined your request: "+a.Detail)
			}
			return nil, nil
		}
		ap := *a
		if ap.Change != nil && ap.Change.Op == "add" && (in.Harness != "" || in.Model != "") {
			ch := *ap.Change
			ch.Harness, ch.Model = firstNonEmpty(in.Harness, ch.Harness), firstNonEmpty(in.Model, ch.Model)
			ap.Change = &ch
		}
		res, err := p.applyFormation(ctx, ap)
		if err == nil {
			p.approvalChose(*a, "approve")
		}
		return res, err
	case "launch":
		if in.Decision == "deny" {
			p.resolveApproval(a.ID, "denied")
			p.approvalChose(*a, "deny")
			return nil, nil
		}
		if in.Version != a.Version {
			return nil, conflict("the proposal is now version %d; review it again", a.Version)
		}
		res, err := p.launchTeam(ctx, *a)
		if err == nil {
			p.approvalChose(*a, "approve")
		}
		return res, err
	}
	return nil, invalid("unknown approval kind")
}

// approvalChose: the owner's decision on a proposal is a choice between approving and denying it. A
// proposal PenguPool drops itself (a failed launch, a stale one) is not.
func (p *Pool) approvalChose(a protocol.Approval, chosen string) {
	p.ownerChose(a.TeamID, map[string]string{"approve": "approved", "deny": "denied"}[chosen]+": "+a.Detail, chosen, "approve", "deny")
}

// launchTeam starts the approved proposal: workspace lock, base commit, main session in the
// repository, helpers in disposable worktrees. A failed launch is rolled back completely.
func (p *Pool) launchTeam(ctx context.Context, a protocol.Approval) (any, error) {
	st := p.st.State()
	d := st.Domain
	t := findTask(&d, a.TaskID)
	if t == nil || t.State != "proposed" {
		return nil, conflict("the task is not waiting for a launch")
	}
	prop := latestProposal(&d, t.ID)
	if prop.Version != a.Version {
		return nil, conflict("stale approval")
	}
	repo := findRepo(&d, t.RepoID)
	if repo == nil {
		return nil, notFound("the work repository is gone")
	}
	for _, pm := range prop.Members {
		if pm.SkillID != "" && findSkill(&d, pm.SkillID) == nil {
			return nil, invalid("the helper agent %s was deleted; propose the team again", pm.Name)
		}
	}
	for _, other := range d.Teams {
		if other.State == "active" && other.RepoID == repo.ID {
			return nil, protocol.Errorf(protocol.CodeWorkspaceBusy, "team %s already works in %s; concurrent writers need distinct worktrees", other.Name, repo.Path)
		}
	}
	// Probe every agent on the roster before starting anyone: a team never launches half-built.
	for _, pm := range append([]protocol.ProposedMember{{}}, prop.Members...) {
		if _, err := harness.Lookup(firstNonEmpty(pm.Harness, t.Harness), p.env); err != nil {
			return nil, protocol.Errorf(protocol.CodeHarnessUnavailable, "%s: %v", firstNonEmpty(pm.Name, "team"), err)
		}
	}
	team := protocol.Team{
		ID: protocol.NewID(), Name: t.Title, TaskID: t.ID, RepoID: repo.ID, Harness: t.Harness, Goal: t.Objective,
		State: "active", IOSAccess: !p.st.Config().Network.LAN, CreatedAt: now(),
	}
	base := headCommit(repo.Path)
	var started []protocol.Member
	rollback := func(cause error) error {
		for _, m := range started {
			_ = p.hub.Kill(context.Background(), m.SessionID)
			if m.Worktree != "" {
				_ = removeWorktree(repo.Path, m.Worktree, m.Branch, true)
			}
		}
		p.resolveApproval(a.ID, "denied")
		_ = p.update(func(st *store.State) error {
			if tk := findTask(&st.Domain, t.ID); tk != nil {
				tk.State, tk.Failure, tk.UpdatedAt = "failed", "launch failed: "+cause.Error(), now()
			}
			return nil
		})
		return cause
	}
	for _, pm := range prop.Members {
		m, err := p.startMember(ctx, &team, *repo, *t, pm, base)
		if err != nil {
			return nil, rollback(fmt.Errorf("%s: %w", pm.Name, err))
		}
		started = append(started, m)
		team.Members = append(team.Members, m)
	}
	err := p.update(func(st *store.State) error {
		st.Domain.Teams = append(st.Domain.Teams, team)
		if tk := findTask(&st.Domain, t.ID); tk != nil {
			tk.State, tk.TeamID, tk.UpdatedAt = "active", team.ID, now()
		}
		for i := range st.Domain.Approvals {
			if st.Domain.Approvals[i].ID == a.ID {
				st.Domain.Approvals[i].State = "approved"
			}
		}
		return nil
	})
	if err != nil {
		return nil, rollback(err)
	}
	for _, m := range team.Members {
		p.logLine(m.SessionID, fmt.Sprintf("team: joined %s as %s (proposal v%d, base %s)", team.Name, m.Role, prop.Version, short(base)))
		if prop.Decision != "" {
			p.logLine(m.SessionID, prop.Decision)
		}
	}
	return team, nil
}

// clip cuts s to at most n bytes on a rune boundary, marking the cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

// startMember launches one member: the main worker in the repository, an SME in a fresh worktree on
// its own branch, a helper in no worktree (it advises, it never edits).
func (p *Pool) startMember(ctx context.Context, team *protocol.Team, repo protocol.Repo, t protocol.Task, pm protocol.ProposedMember, base string) (protocol.Member, error) {
	id := protocol.NewID()
	m := protocol.Member{SessionID: id, Name: pm.Name, Role: pm.Role, SkillID: pm.SkillID, Assignment: pm.Assignment,
		Coordinator: pm.Coordinator, State: "active", BaseCommit: base}
	cwd := repo.Path
	var readDirs []string
	if pm.Role != "main" { // SMEs work in their own worktree, helpers in none
		for _, x := range team.Members {
			if x.Role == "main" {
				m.Parent = x.SessionID
			}
		}
	}
	if pm.SkillID != "" {
		// A helper advises and never edits: no worktree, no branch. It runs in its skill folder when that is a
		// clone PenguPool installed (guardSkill protects it), else in the team's scratch folder, and reads
		// the team's repositories by path.
		d := p.st.State().Domain
		cwd = p.workDir(team.ID)
		if sk := findSkill(&d, pm.SkillID); sk != nil && sk.Path != "" && sk.Source == "git" && within(sk.Path, filepath.Join(p.st.Dir(), "skills")) {
			cwd = sk.Path
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return m, err
		}
		for _, r := range teamRepos(&d, team) {
			readDirs = append(readDirs, r.Path)
		}
	} else if pm.Role != "main" {
		if !repo.Git {
			return m, invalid("SMEs need a Git work repository")
		}
		m.RepoID = repo.ID
		m.Worktree = filepath.Join(p.st.Dir(), "worktrees", team.ID[:8], slug(pm.Name)+"-"+id[:8])
		m.Branch = fmt.Sprintf("pengupool/%s/%s-%s", slug(t.Title), slug(pm.Name), id[:8])
		if err := os.MkdirAll(filepath.Dir(m.Worktree), 0o755); err != nil {
			return m, err
		}
		out, err := exec.CommandContext(ctx, "git", "-C", repo.Path, "worktree", "add", "-b", m.Branch, m.Worktree, base).CombinedOutput()
		if err != nil {
			return m, fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
		}
		cwd = m.Worktree
	}
	_, err := p.spawn(ctx, spawnReq{
		ID: id, Name: pm.Name, Harness: firstNonEmpty(pm.Harness, team.Harness), Model: pm.Model, Cwd: cwd, SkillID: pm.SkillID, ReadDirs: readDirs,
		TrustFrom: map[bool]string{true: repo.Path}[m.Worktree != ""],
		Decorate: func(s *store.Session) {
			s.TeamID, s.Role, s.SkillID = team.ID, pm.Role, pm.SkillID
		},
	})
	if err != nil {
		if m.Worktree != "" {
			_ = removeWorktree(repo.Path, m.Worktree, m.Branch, true)
		}
		return m, err
	}
	return m, nil
}

type placeIn struct {
	TeamID     string `json:"team_id"`
	SkillID    string `json:"skill_id"`
	Assignment string `json:"assignment,omitempty"`
	Harness    string `json:"harness,omitempty"`
	Model      string `json:"model,omitempty"`
	by         string
}

// placeHelperRPC is the Agent pool drop: an owner formation change, approved in the same step.
func (p *Pool) placeHelperRPC(ctx context.Context, in placeIn) (any, error) {
	return p.teamFormation(ctx, formationIn{TeamID: in.TeamID, Change: protocol.FormationChange{Op: "add", SkillID: in.SkillID, Assignment: in.Assignment, Harness: in.Harness, Model: in.Model}})
}

// placeHelper starts a freelancer (a skill helper) in the team, once its formation change is approved.
func (p *Pool) placeHelper(ctx context.Context, in placeIn) (any, error) {
	d := p.st.State().Domain
	team := findTeam(&d, in.TeamID)
	if team == nil || team.State != "active" {
		return nil, conflict("the target team is not active")
	}
	sk := findSkill(&d, in.SkillID)
	if sk == nil {
		return nil, notFound("no active helper agent %s", in.SkillID)
	}
	for _, m := range team.Members {
		if m.State == "active" && m.SkillID == sk.ID {
			return nil, conflict("%s already assists this team", sk.Name)
		}
	}
	anchor := anchorSkill(*sk)
	repo := findRepo(&d, team.RepoID)
	if !anchor && repo == nil {
		return nil, invalid("the team has no work repository (a manual group); helpers need one")
	}
	task := protocol.Task{Title: team.Name, Objective: team.Goal}
	if t := findTask(&d, team.TaskID); t != nil {
		task = *t
	}
	assignment := firstNonEmpty(in.Assignment, "Assist the team with "+firstNonEmpty(strings.Join(sk.Capabilities, ", "), sk.Name)+" for: "+firstNonEmpty(team.Goal, team.Name))
	ag, note, ok := helperAgent(*sk, in.Harness, team.Harness, p.agentOptions())
	if !ok {
		return nil, protocol.Errorf(protocol.CodeHarnessUnavailable, "%s needs %s, and none is installed here", sk.Name, strings.Join(sk.Requires, " or "))
	}
	if in.Harness != "" && ag != in.Harness { // the owner picked it: start it there or not at all
		return nil, invalid("%s", note)
	}
	pm := protocol.ProposedMember{SkillID: sk.ID, Name: slug(sk.Name), Role: "helper", Assignment: assignment, Harness: ag, Model: in.Model}
	var m protocol.Member
	var err error
	if anchor {
		m, err = p.startAnchor(ctx, team, *sk, pm)
	} else {
		m, err = p.startMember(ctx, team, *repo, task, pm, headCommit(repo.Path))
	}
	if err != nil {
		return nil, err
	}
	if err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil || t.State != "active" {
			return conflict("the team closed meanwhile")
		}
		t.Members = append(t.Members, m)
		return nil
	}); err != nil {
		_ = p.hub.Kill(context.Background(), m.SessionID)
		if m.Worktree != "" {
			_ = removeWorktree(repo.Path, m.Worktree, m.Branch, true)
		}
		return nil, err
	}
	p.logLine(m.SessionID, "team: placed as helper in "+team.Name)
	p.forEachMember(team.ID, func(s string) { p.logLine(s, "team: "+m.Name+" joined as helper") })
	if anchor { // approved with the add: an anchor agent anchors the team it joins
		if _, err := p.setAnchor(team.ID, m.SessionID); err != nil {
			p.logf("pool: make %s the anchor: %v", m.Name, err)
		}
	}
	return m, nil
}

// anchorSkill: a skill agent that anchors a team (the marketplace's team-hub-agent), known by its
// team-hub skill. It owns no repository and edits no code.
func anchorSkill(sk protocol.Skill) bool { return slices.Contains(sk.Capabilities, "team-hub") }

// startAnchor starts an anchor agent with no worktree, so it can anchor a group with no work
// repository, or with several of which none should anchor. It works in a scratch folder of its own,
// <store>/work/<team>/<member>, so nothing it or its agent drops in its working directory lands in
// its skill repository; its skills load from sk.Path (the context names it, Claude gets --plugin-dir).
func (p *Pool) startAnchor(ctx context.Context, team *protocol.Team, sk protocol.Skill, pm protocol.ProposedMember) (protocol.Member, error) {
	id := protocol.NewID()
	cwd := filepath.Join(p.workDir(team.ID), slug(pm.Name))
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return protocol.Member{}, err
	}
	m := protocol.Member{SessionID: id, Name: pm.Name, Role: pm.Role, SkillID: pm.SkillID, Assignment: pm.Assignment, State: "active"}
	for _, x := range team.Members {
		if x.Role == "main" && x.State == "active" {
			m.Parent = x.SessionID
		}
	}
	_, err := p.spawn(ctx, spawnReq{
		ID: id, Name: pm.Name, Harness: firstNonEmpty(pm.Harness, team.Harness), Model: pm.Model, Cwd: cwd, SkillID: pm.SkillID,
		Decorate: func(s *store.Session) {
			s.TeamID, s.Role, s.SkillID = team.ID, pm.Role, pm.SkillID
		},
	})
	return m, err
}

// teamRepos: the team's work repositories, the first one first.
func teamRepos(d *protocol.Domain, t *protocol.Team) []protocol.Repo {
	var out []protocol.Repo
	for _, id := range append([]string{t.RepoID}, t.Repos...) {
		if r := findRepo(d, id); r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// logStrayWrite notes a helper's file edit inside a team repository. Log only: the context forbids it, no hook denies it.
func (p *Pool) logStrayWrite(s store.Session, pl map[string]any) {
	path := changedFile(pl)
	if path == "" || s.SkillID == "" || (!filepath.IsAbs(path) && s.Cwd == "") {
		return
	}
	d := p.st.State().Domain
	t := teamOf(&d, s.ID)
	if t == nil {
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.Cwd, path)
	}
	path = filepath.Clean(path)
	for _, r := range teamRepos(&d, t) {
		if within(path, r.Path) {
			p.logLine(s.ID, "helper: wrote in a team repository: "+path)
			return
		}
	}
}

func (p *Pool) forEachMember(teamID string, fn func(sessionID string)) {
	d := p.st.State().Domain
	if t := findTeam(&d, teamID); t != nil {
		for _, m := range t.Members {
			if m.State == "active" {
				fn(m.SessionID)
			}
		}
	}
}

type closeIn struct {
	TeamID       string `json:"team_id"`
	SessionID    string `json:"session_id"`
	KeepWorktree bool   `json:"keep_worktree,omitempty"`
	Reason       string `json:"reason,omitempty"`
	reason, by   string
}

func (p *Pool) member(teamID, sessionID string) (protocol.Team, protocol.Member, protocol.Repo, error) {
	d := p.st.State().Domain
	t := findTeam(&d, teamID)
	if t == nil {
		return protocol.Team{}, protocol.Member{}, protocol.Repo{}, notFound("no team %s", teamID)
	}
	var repo protocol.Repo
	if r := findRepo(&d, t.RepoID); r != nil {
		repo = *r
	}
	for _, m := range t.Members {
		if m.SessionID == sessionID {
			if r := findRepo(&d, m.RepoID); r != nil { // an SME works in its own repository
				repo = *r
			}
			return *t, m, repo, nil
		}
	}
	return *t, protocol.Member{}, repo, notFound("not a member of %s", t.Name)
}

// worktreeState reports whether a helper worktree can be removed without losing work.
func worktreeState(repo, wt, branch, base string) (removable bool, reason string) {
	if wt == "" {
		return true, "no worktree (a grouped session)"
	}
	if _, err := os.Stat(wt); errors.Is(err, fs.ErrNotExist) {
		return true, "already gone"
	}
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return false, "git status failed"
	}
	if strings.TrimSpace(string(out)) != "" {
		return false, "uncommitted or untracked changes"
	}
	if strings.Contains(string(out), "UU ") {
		return false, "unmerged paths"
	}
	head, _ := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	h := strings.TrimSpace(string(head))
	if h == base {
		return true, "unchanged"
	}
	main, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", h, strings.TrimSpace(string(main))).Run() == nil {
		return true, "integrated"
	}
	return false, "commits on " + branch + " are not integrated"
}

// removeWorktree removes a clean worktree without force. rollback also drops the branch a failed
// launch just created (safe -d only: it refuses a branch with commits).
func removeWorktree(repo, wt, branch string, rollback bool) error {
	if out, err := exec.Command("git", "-C", repo, "worktree", "remove", wt).CombinedOutput(); err != nil {
		if _, serr := os.Stat(wt); !errors.Is(serr, fs.ErrNotExist) {
			return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(string(out)))
		}
		_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
	}
	if rollback && branch != "" {
		_ = exec.Command("git", "-C", repo, "branch", "-d", branch).Run()
	}
	return nil
}

// closePreview is the short close review: helper, team and worktree state.
func (p *Pool) closePreview(_ context.Context, in closeIn) (any, error) {
	t, m, repo, err := p.member(in.TeamID, in.SessionID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"team": t.Name, "member": m, "worktree": m.Worktree}
	if m.Role != "main" {
		ok, why := worktreeState(repo.Path, m.Worktree, m.Branch, m.BaseCommit)
		out["worktree_removable"], out["worktree_state"] = ok, why
	}
	d := p.st.State().Domain
	if sk := findSkill(&d, m.SkillID); sk != nil {
		out["skill"] = sk.Name
	}
	return out, nil
}

// closeHelperRPC is the owner closing a member from its close review: a formation change, approved
// in the same step.
func (p *Pool) closeHelperRPC(ctx context.Context, in closeIn) (any, error) {
	a, err := p.newFormation(in.TeamID, protocol.FormationChange{Op: "remove", SessionID: in.SessionID, Reason: in.Reason}, "pending")
	if err != nil {
		return nil, err
	}
	in.reason, in.by = in.Reason, "owner"
	res, err := p.closeHelper(ctx, in)
	if err != nil {
		p.resolveApproval(a.ID, "denied")
		return nil, err
	}
	p.resolveApproval(a.ID, "approved")
	return res, nil
}

// closeHelper stops the member (a helper or an SME) and removes its worktree when that loses
// nothing. The member's own experience stays under its key; the team's roster records that it left. The registered skill source, the main session and
// the work repository stay.
func (p *Pool) closeHelper(ctx context.Context, in closeIn) (any, error) {
	t, m, repo, err := p.member(in.TeamID, in.SessionID)
	if err != nil {
		return nil, err
	}
	if m.Role == "main" {
		return nil, invalid("the main worker closes with its team")
	}
	if m.State != "active" {
		return nil, conflict("already closed")
	}
	removable, why := worktreeState(repo.Path, m.Worktree, m.Branch, m.BaseCommit)
	if !removable && !in.KeepWorktree {
		return nil, conflict("the worktree has %s: preserve or integrate that work, or choose to keep the worktree, then retry", why)
	}
	result := map[string]any{}
	_ = p.hub.Kill(ctx, m.SessionID)
	if removable && m.Worktree != "" {
		if err := removeWorktree(repo.Path, m.Worktree, "", false); err != nil {
			result["worktree"] = "kept: " + err.Error()
		} else {
			result["worktree"] = "removed (" + why + "); branch " + m.Branch + " kept"
		}
	} else if m.Worktree != "" {
		result["worktree"] = "kept at " + m.Worktree + " (" + why + ")"
	}
	err = p.update(func(st *store.State) error {
		tt := findTeam(&st.Domain, t.ID)
		for i := range tt.Members {
			if tt.Members[i].SessionID == m.SessionID {
				tt.Members[i].State = "closed"
			}
		}
		if i := sessionIndex(st, m.SessionID); i >= 0 {
			st.Sessions = append(st.Sessions[:i], st.Sessions[i+1:]...)
		}
		return nil
	})
	p.forEachMember(t.ID, func(s string) { p.logLine(s, "team: helper "+m.Name+" closed") })
	return result, err
}

type teamCloseIn struct {
	TeamID        string `json:"team_id"`
	KeepWorktrees bool   `json:"keep_worktrees,omitempty"`
}

// teamClose closes every helper and dissolves the team. The main session keeps running ungrouped;
// the task stays for the owner to complete.
func (p *Pool) teamClose(ctx context.Context, in teamCloseIn) (any, error) {
	d := p.st.State().Domain
	t := findTeam(&d, in.TeamID)
	if t == nil || t.State != "active" {
		return nil, conflict("the team is not active")
	}
	results := map[string]any{}
	for _, m := range t.Members {
		if m.Role != "main" && m.State == "active" {
			r, err := p.closeHelper(ctx, closeIn{TeamID: t.ID, SessionID: m.SessionID, KeepWorktree: in.KeepWorktrees, reason: "team closed", by: "owner"})
			if err != nil {
				return results, fmt.Errorf("%s: %w", m.Name, err)
			}
			results[m.Name] = r
		}
	}
	err := p.update(func(st *store.State) error {
		tt := findTeam(&st.Domain, t.ID)
		tt.State = "closed"
		for i := range st.Domain.Approvals { // requests for a closed team have nothing left to change
			if a := &st.Domain.Approvals[i]; a.Kind == "formation" && a.TeamID == t.ID && a.State == "pending" {
				a.State = "denied"
			}
		}
		for i := range tt.Members {
			if tt.Members[i].Role == "main" {
				tt.Members[i].State = "closed"
				if j := sessionIndex(st, tt.Members[i].SessionID); j >= 0 {
					st.Sessions[j].TeamID, st.Sessions[j].Role = "", ""
				}
			}
		}
		return nil
	})
	if err == nil {
		p.recordFormation(t.ID, protocol.FormationChange{Op: "remove", Reason: "team closed"}, "close the team")
	}
	p.revokeTeam(t.ID)
	return results, err
}

type teamCreateIn struct {
	Name       string   `json:"name"`
	Goal       string   `json:"goal,omitempty"`
	SessionIDs []string `json:"session_ids"`
}

// teamCreate groups running sessions of one harness into a team; the first is the main worker.
func (p *Pool) teamCreate(_ context.Context, in teamCreateIn) (any, error) {
	if strings.TrimSpace(in.Name) == "" || len(in.SessionIDs) == 0 {
		return nil, invalid("a team needs a name and at least one session")
	}
	var team protocol.Team
	lan := p.st.Config().Network.LAN // read before the update: the store's lock is not reentrant
	err := p.update(func(st *store.State) error {
		team = protocol.Team{ID: protocol.NewID(), Name: strings.TrimSpace(in.Name), Goal: in.Goal, State: "active",
			IOSAccess: !lan, CreatedAt: now()}
		for k, id := range in.SessionIDs {
			i := sessionIndex(st, id)
			if i < 0 {
				return notFound("no session %s", id)
			}
			s := &st.Sessions[i]
			if !harness.IsAgent(s.Harness) {
				return invalid("%s is a plain terminal; only agent sessions join teams", s.Name)
			}
			// team.Harness is the default for helpers the team starts; members may run any agent.
			if team.Harness == "" {
				team.Harness = s.Harness
			}
			if teamOf(&st.Domain, id) != nil {
				return conflict("%s is already in a team", s.Name)
			}
			role := "helper"
			if k == 0 {
				role = "main"
				for _, r := range st.Domain.Repos {
					if r.Path == s.Cwd {
						team.RepoID = r.ID
					}
				}
			}
			s.TeamID, s.Role = team.ID, role
			parent := ""
			if k > 0 {
				parent = in.SessionIDs[0]
			}
			team.Members = append(team.Members, protocol.Member{SessionID: id, Name: s.Name, Role: role, State: "active", Coordinator: k == 0, Parent: parent})
		}
		st.Domain.Teams = append(st.Domain.Teams, team)
		return nil
	})
	if err == nil {
		p.recordFormation(team.ID, protocol.FormationChange{Op: "add", Reason: "assembled from running sessions"}, "assemble from "+fmt.Sprint(len(team.Members))+" sessions")
	}
	return team, err
}

type addSessionIn struct {
	TeamID    string `json:"team_id"`
	SessionID string `json:"session_id"`
}

func (p *Pool) teamAddSession(_ context.Context, in addSessionIn) (any, error) {
	err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil || t.State != "active" {
			return conflict("the team is not active")
		}
		i := sessionIndex(st, in.SessionID)
		if i < 0 {
			return notFound("no session")
		}
		s := &st.Sessions[i]
		if teamOf(&st.Domain, s.ID) != nil {
			return conflict("already in a team")
		}
		s.TeamID, s.Role = t.ID, "helper"
		t.Members = append(t.Members, protocol.Member{SessionID: s.ID, Name: s.Name, Role: "helper", State: "active"})
		return nil
	})
	if err == nil {
		s, _ := p.session(in.SessionID)
		p.recordFormation(in.TeamID, protocol.FormationChange{Op: "add", SessionID: in.SessionID}, "add "+s.Name)
	}
	return nil, err
}

func (p *Pool) setTaskState(id, state, reason string) error {
	return p.update(func(st *store.State) error {
		t := findTask(&st.Domain, id)
		if t == nil {
			return notFound("no task %s", id)
		}
		if t.State == "completed" || t.State == "cancelled" {
			return conflict("the task is already %s", t.State)
		}
		t.State, t.Failure, t.UpdatedAt = state, reason, now()
		for i := range st.Domain.Approvals {
			a := &st.Domain.Approvals[i]
			if a.TaskID == id && a.State == "pending" {
				a.State = "expired"
			}
		}
		return nil
	})
}

type budgetIn struct {
	TeamID    string  `json:"team_id"`
	BudgetUSD float64 `json:"budget_usd"` // 0 clears it
}

// teamBudget sets the owner's budget for a team's job.
func (p *Pool) teamBudget(_ context.Context, in budgetIn) (any, error) {
	if in.BudgetUSD < 0 || in.BudgetUSD > 1e6 {
		return nil, invalid("a budget is between $0 and $1,000,000")
	}
	return nil, p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil {
			return notFound("no team %s", in.TeamID)
		}
		t.BudgetUSD = in.BudgetUSD
		return nil
	})
}

type goalIn struct {
	TeamID string `json:"team_id"`
	Goal   string `json:"goal"`
}

// teamGoal changes what the team is working on. The team is the unit and its goal evolves: the
// edit is a direction change on the record (revision, record entry); the members see the new goal
// in their Current task section, with no notice.
func (p *Pool) teamGoal(_ context.Context, in goalIn) (any, error) {
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		return nil, invalid("a team needs a goal")
	}
	if len(goal) > 4<<10 {
		return nil, invalid("a goal is limited to 4 KiB")
	}
	if err := p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil || t.State != "active" {
			return conflict("the team is not active")
		}
		t.Goal = goal
		if task := findTask(&st.Domain, t.TaskID); task != nil { // a team launched from a task
			task.Objective, task.UpdatedAt = goal, now()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (p *Pool) taskComplete(_ context.Context, in idIn) (any, error) {
	return nil, p.setTaskState(in.ID, "completed", "")
}

func (p *Pool) taskCancel(_ context.Context, in idIn) (any, error) {
	return nil, p.setTaskState(in.ID, "cancelled", "")
}

type failIn struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func (p *Pool) taskFail(_ context.Context, in failIn) (any, error) {
	return nil, p.setTaskState(in.ID, "failed", firstNonEmpty(in.Reason, "marked failed by the owner"))
}

type teamIDIn struct {
	TeamID string `json:"team_id"`
}

// teamDelete removes a closed team and what is recorded for it: its helpers' notes and turns. Member sessions, skill scopes,
// repositories and branches stay.
func (p *Pool) teamDelete(_ context.Context, in teamIDIn) (any, error) {
	db, err := p.agentDB()
	if err != nil {
		return nil, err
	}
	d0 := p.st.State().Domain
	t := findTeam(&d0, in.TeamID)
	if t == nil {
		return nil, notFound("no such team")
	}
	if t.State == "active" {
		return nil, conflict("close the team first")
	}
	if err := db.DeleteTeamRecord(in.TeamID); err != nil {
		return nil, err
	}
	if err := p.update(func(st *store.State) error {
		d := &st.Domain
		d.Teams = slices.DeleteFunc(d.Teams, func(x protocol.Team) bool { return x.ID == in.TeamID })
		d.Messages = slices.DeleteFunc(d.Messages, func(x protocol.TeamMessage) bool { return x.TeamID == in.TeamID })
		d.Approvals = slices.DeleteFunc(d.Approvals, func(x protocol.Approval) bool { return x.TeamID == in.TeamID })
		return nil
	}); err != nil {
		return nil, err
	}
	// Its anchor's scratch folders (t.ID is the id the store gave it, never a path).
	return nil, os.RemoveAll(p.workDir(t.ID))
}

// workDir is the scratch folder of a team's members that work outside any repository (its anchor):
// <store>/work/<team id>.
func (p *Pool) workDir(teamID string) string { return filepath.Join(p.st.Dir(), "work", teamID) }
