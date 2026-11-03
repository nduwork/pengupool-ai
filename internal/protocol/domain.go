package protocol

import "encoding/json"

// Domain is the pool state every desktop client sees through state.subscribe: work repositories,
// the Skill library, tasks with their proposals, teams, approvals and recent team messages. It is
// small and sent whole whenever it changes; memory, run logs and skill logs are never part of it
// and are read through bounded requests instead.
type Domain struct {
	Repos     []Repo        `json:"repos"`
	Skills    []Skill       `json:"skills"`
	Tasks     []Task        `json:"tasks"`
	Proposals []Proposal    `json:"proposals"`
	Teams     []Team        `json:"teams"`
	Approvals []Approval    `json:"approvals"`
	Messages  []TeamMessage `json:"messages"`
	Nodes     []NodeView    `json:"nodes"`
	// Update is a newer PenguPool release the host's update check found; nil when there is none.
	Update *Update `json:"update,omitempty"`
}

// Update is the answer of the host's update check (update.check): the newest release of PenguPool
// on GitHub and whether it is newer than the running host. Error says why the check could not run.
type Update struct {
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"` // version, without the tag's v
	Tag     string `json:"tag,omitempty"`
	Newer   bool   `json:"newer"`
	URL     string `json:"url,omitempty"`
	Notes   string `json:"notes,omitempty"` // the release notes, at most 2 KB
	Asset   string `json:"asset,omitempty"` // the disk image's file name
	Error   string `json:"error,omitempty"`
}

// Repo is a work repository or directory: the material a main session changes.
type Repo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"` // "local" | "clone"
	URL       string `json:"url,omitempty"`
	Git       bool   `json:"git"`
	NodeID    string `json:"node_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// SkillFile is one canonical skills/<name>/SKILL.md inside a skill repository.
type SkillFile struct {
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Digest        string            `json:"digest"`
	Problem       string            `json:"problem,omitempty"`
}

// Credit is a source a helper was adapted from: "owner/repo" and its link.
type Credit struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Skill is a Skill library entry: a reusable method with capabilities, memory and history.
// Registration starts no session.
type Skill struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Path         string      `json:"path"`
	Method       string      `json:"method,omitempty"`
	Description  string      `json:"description,omitempty"`
	Capabilities []string    `json:"capabilities,omitempty"`
	Harness      string      `json:"harness,omitempty"`  // preferred agent
	Requires     []string    `json:"requires,omitempty"` // agents it can only run on (empty: any)
	Source       string      `json:"source"`             // "local" | "git"
	Commit       string      `json:"commit,omitempty"`
	Files        []SkillFile `json:"files,omitempty"`
	Profiles     []string    `json:"profiles,omitempty"` // agents/*.md seeds
	Group        string      `json:"group,omitempty"`    // the library's section: the catalog's category, or the owner's own
	Category     string      `json:"category,omitempty"` // the catalog's category it last had: a Group equal to it came from the catalog
	OnMap        bool        `json:"on_map,omitempty"`   // listed in the map's Specialist pool
	// Credits name the public repositories a helper was adapted from. Display only: never in an
	// agent's context.
	Credits []Credit `json:"credits,omitempty"`
	// Key is the agent's identity beyond this machine: the normalised Git remote of its repository,
	// or its folder name. One agent per key per environment.
	Key       string `json:"key,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Task is a user objective against one work repository.
type Task struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Objective    string   `json:"objective"`
	RepoID       string   `json:"repo_id"`
	Harness      string   `json:"harness"`
	Capabilities []string `json:"capabilities,omitempty"`
	State        string   `json:"state"` // proposed | active | completed | cancelled | failed
	TeamID       string   `json:"team_id,omitempty"`
	Failure      string   `json:"failure,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// ProposedMember is one row of a team proposal; SkillID is empty for the main worker.
type ProposedMember struct {
	SkillID     string `json:"skill_id,omitempty"`
	Name        string `json:"name"`
	Role        string `json:"role"` // main | helper
	Assignment  string `json:"assignment"`
	Coordinator bool   `json:"coordinator,omitempty"`
	Model       string `json:"model,omitempty"`
	Harness     string `json:"harness,omitempty"` // empty: the team's default
	Copy        int    `json:"copy,omitempty"`    // the Member's Copy (placeHelper sets it)
}

// Proposal is a versioned team proposal waiting for the launch approval.
type Proposal struct {
	ID         string           `json:"id"`
	TaskID     string           `json:"task_id"`
	Version    int              `json:"version"`
	Members    []ProposedMember `json:"members"`
	Provider   string           `json:"provider"`
	Model      string           `json:"model,omitempty"`
	Confidence float64          `json:"confidence"`
	Fallback   bool             `json:"fallback,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	Gaps       []string         `json:"gaps,omitempty"`
	Config     json.RawMessage  `json:"config,omitempty"`   // non-secret provider snapshot
	Decision   string           `json:"decision,omitempty"` // the decision line, copied into run logs at launch
	CreatedAt  string           `json:"created_at"`
}

// Member is one session of a team.
type Member struct {
	SessionID   string `json:"session_id"`
	Name        string `json:"name"`
	Role        string `json:"role"` // main | helper
	SkillID     string `json:"skill_id,omitempty"`
	RepoID      string `json:"repo_id,omitempty"` // the repository its worktree is in (empty: the team's)
	Assignment  string `json:"assignment,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	BaseCommit  string `json:"base_commit,omitempty"`
	Coordinator bool   `json:"coordinator,omitempty"`
	// Copy numbers a skill's copies on the team from 2, fixed when it is added (0: the first). Its
	// scratch folder comes from it, never from the name, which the owner may change.
	Copy  int    `json:"copy,omitempty"`
	State string `json:"state"` // active | closed
	// Parent is the member this one hangs under on the map (empty: the team's root).
	Parent string `json:"parent,omitempty"`
}

// Team is a running work team; it doubles as the iOS access group.
type Team struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	TaskID  string   `json:"task_id,omitempty"`
	RepoID  string   `json:"repo_id,omitempty"`
	Repos   []string `json:"repos,omitempty"` // further repositories it works in (SMEs); RepoID is the first
	Harness string   `json:"harness"`
	// Goal is the team's job. The team is the unit and its goal evolves: the owner edits it in
	// place, and each edit is a direction change on the record.
	Goal string `json:"goal,omitempty"`
	// CostBy is what each member session spent while on the team, in USD, as its harness reports it.
	CostBy map[string]float64 `json:"cost_by,omitempty"`
	// BudgetUSD is the owner's budget for the team's job; 0: none set (the app shows an estimate).
	BudgetUSD float64  `json:"budget_usd,omitempty"`
	Members   []Member `json:"members"`
	State     string   `json:"state"` // active | closed
	IOSAccess bool     `json:"ios_access"`
	// NoLeadCompass: the owner turned off "Lead decides from your Compass" for this team, so
	// `pengupool answer --per-compass` is refused (on by default).
	NoLeadCompass bool   `json:"no_lead_compass,omitempty"`
	CreatedAt     string `json:"created_at"`
}

// Approval is an urgent owner decision: a team launch, a change to who is on a team (formation),
// or a harness permission request.
type Approval struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // launch | formation | permission | ask
	TeamID string `json:"team_id,omitempty"`
	// Change is a formation change: what to do once approved.
	Change    *FormationChange `json:"change,omitempty"`
	TaskID    string           `json:"task_id,omitempty"`
	Version   int              `json:"version,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
	Tool      string           `json:"tool,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	// An agent's ask (kind ask): the choices it offers, and the owner's answer, one of them or their
	// own words; AnsweredBy is "owner" or the paired device's name.
	Options    []string `json:"options,omitempty"`
	Answer     string   `json:"answer,omitempty"`
	AnsweredBy string   `json:"answered_by,omitempty"`
	// OwnerWords: for an ask the owner answered through a parent's session ("owner via <id>"), the
	// owner's own prompt there, clipped and redacted: the provenance of that answer.
	OwnerWords string `json:"owner_words,omitempty"`
	// OwnerOnly: the asker marked it for the owner alone (a rule of its own that only the owner may
	// lift), so it is never decided from the Compass.
	OwnerOnly bool `json:"owner_only,omitempty"`
	// LineUID: an ask the host decided from the Compass at a lead's request (AnsweredBy "lead per
	// Compass line <uid>"): the line that decided it. Wrong: the owner called that decision wrong.
	LineUID   string `json:"line_uid,omitempty"`
	Wrong     bool   `json:"wrong,omitempty"`
	State     string `json:"state"` // pending | approved | denied | expired | answered
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// FormationChange is one change to who is on a team: add a member (a skill as a freelancer, or a
// repository as an SME) or remove one. Who leads is not a change: the LEAD is the tree's root.
type FormationChange struct {
	Op          string `json:"op"` // add | remove
	SkillID     string `json:"skill_id,omitempty"`
	RepoID      string `json:"repo_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"` // remove
	Assignment  string `json:"assignment,omitempty"`
	Reason      string `json:"reason,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"` // a member session id; empty: the owner
	Harness     string `json:"harness,omitempty"`      // add: the agent to run it on; empty: the helper's default
	Model       string `json:"model,omitempty"`        // add: the model; empty: the agent's default
	Parent      string `json:"parent,omitempty"`       // add (specialist): the member it goes under; empty: the main member
}

// TeamMessage is one routed message between members of one team.
type TeamMessage struct {
	ID     string `json:"id"`
	TeamID string `json:"team_id"`
	From   string `json:"from"` // session id; empty: a PenguPool notice
	To     string `json:"to"`   // session id
	Text   string `json:"text"`
	State  string `json:"state"` // queued | delivered | failed
	Reason string `json:"reason,omitempty"`
	At     string `json:"at"`
	// Via is how it travels: host (PenguPool delivers it), channel (PenguPool delivers it outside the
	// team, on a channel the owner opened) or native (Claude's own SendMessage). Empty: host.
	Via string `json:"via,omitempty"`
	// Type is tell, ask, done or blocked; empty: tell.
	Type    string `json:"type,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"` // the ask this answers
	// An ask waiting too long is escalated (to triage, then the owner's Needs you); one the owner
	// or triage closed without the recipient's answer is settled.
	EscalatedAt string `json:"escalated_at,omitempty"`
	SettledAt   string `json:"settled_at,omitempty"`

	DeliveredAt string `json:"delivered_at,omitempty"` // when it reached the recipient; older records have none
}

// NodeView is a worker host as clients see it.
type NodeView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // local | ssh
	Address string `json:"address,omitempty"`
	Status  string `json:"status"` // up | down | locked (needs a password) | not_installed | incompatible
	Reason  string `json:"reason,omitempty"`
	Hub     string `json:"hub,omitempty"`
	// Auth is "key" or "password" for an ssh node.
	Auth          string `json:"auth,omitempty"`
	IdentityFile  string `json:"identity_file,omitempty"`
	SavedPassword bool   `json:"saved_password,omitempty"`
	// Version is the node's `pengupool version` line; Outdated means it is an older release than
	// this hub's (a newer host is reported as status "newer" and never downgraded).
	Version  string `json:"version,omitempty"`
	Outdated bool   `json:"outdated,omitempty"`
}

// HostConnection is where the app reaches a PenguPool host: its address on this machine and token.
type HostConnection struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// NodeAdd registers an SSH host. With Auth "password" the password logs in once; Save keeps it
// in the OS vault, and TrustHostKey accepts the key of a host this machine has never seen (a changed
// key is still refused).
type NodeAdd struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	Auth         string `json:"auth,omitempty"`
	Password     string `json:"password,omitempty"`
	Save         bool   `json:"save,omitempty"`
	TrustHostKey bool   `json:"trust_host_key,omitempty"`
}

// KeySetupStep is one step of setting up key login for a password node.
type KeySetupStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// SSHKey is a key pair on this machine that key setup can install.
type SSHKey struct {
	Path        string `json:"path"` // the private key
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
