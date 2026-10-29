// Package store owns PenguPool's durable state: plain JSON files under $PENGUPOOL_HOME/v2.
//
// There is no database. state.json has exactly one writer (the daemon), is replaced atomically, and
// keeps the previous revision as state.json.bak. A corrupt state.json stops all changes and reports
// its path; it is never silently replaced with an empty file.
package store

import (
	"encoding/json"
	"maps"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// File names inside the store directory.
const (
	StateFile  = "state.json"
	BackupFile = "state.json.bak"
	TempSuffix = ".tmp"
	ConfigFile = "config.json"
	RunDir     = "run"
	LogsDir    = "logs"
	BackupsDir = "backups"
)

// Version is the on-disk schema version of state.json and config.json.
const Version = 1

// Node is a host that can run workers: the local host, or a host reached over system OpenSSH.
type Node struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"` // "local" | "ssh"
	Address   string    `json:"address,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Command is the remote pengupool executable for an ssh node.
	Command string `json:"command,omitempty"`
	// Auth is how an ssh node logs in: "" uses keys and the agent as ssh would, "password" logs in
	// once with a password and multiplexes everything over that connection.
	Auth string `json:"auth,omitempty"`
	// IdentityFile is the private key PenguPool set up for this node, if any.
	IdentityFile string `json:"identity_file,omitempty"`
	// SavedPassword means the password is in the OS vault; otherwise it is held in memory only.
	SavedPassword bool `json:"saved_password,omitempty"`
}

// Session is one worker: one PTY, one agent process, one work directory.
type Session struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	NodeID     string    `json:"node_id"`
	Cwd        string    `json:"cwd"`
	Command    []string  `json:"command,omitempty"`
	Harness    string    `json:"harness,omitempty"`
	State      string    `json:"state"` // "running" | "exited" | "lost"
	ExitCode   *int      `json:"exit_code,omitempty"`
	Pid        int       `json:"pid,omitempty"`
	Cols       int       `json:"cols"`
	Rows       int       `json:"rows"`
	SocketPath string    `json:"socket_path,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at,omitempty"`

	Status string `json:"status,omitempty"`
	// Waiting is the first-run dialog (folder trust, sign-in...) the agent's screen shows while it is
	// still starting: it needs the owner, not a message.
	Waiting    string `json:"waiting,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
	Role       string `json:"role,omitempty"`
	SkillID    string `json:"skill_id,omitempty"`
	ContextPct int    `json:"context_pct,omitempty"`
	// CostUSD is the harness's last reported spend for its current conversation (Claude's status
	// line); a drop means a new conversation. Spend is added to the team's task as it grows.
	CostUSD float64 `json:"cost_usd,omitempty"`
	// Model is the model it last reported; JobStart is when its current piece of work began (the
	// first prompt after its last log entry), recorded on that entry.
	Model string `json:"model,omitempty"`
	// Via is the launcher it runs through: "ollama" for Claude Code on Ollama; a resume uses it again.
	Via        string    `json:"via,omitempty"`
	JobStart   time.Time `json:"job_start,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	RunLog     string    `json:"run_log,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
	ConvID     string    `json:"conv_id,omitempty"`
	Env        []string  `json:"env,omitempty"`
	// ActiveAt is the last harness activity (hook event); Summary is the owner\'s role description.
	ActiveAt time.Time `json:"active_at,omitempty"`
	Summary  string    `json:"summary,omitempty"`
}

// State is the whole of state.json.
type State struct {
	Version   int       `json:"version"`
	Rev       uint64    `json:"rev"`
	UpdatedAt time.Time `json:"updated_at"`
	NodeID    string    `json:"node_id"`
	Nodes     []Node    `json:"nodes"`
	Sessions  []Session `json:"sessions"`

	// Pool records; Secrets holds password verifiers keyed by team id and never leaves the host.
	Domain  protocol.Domain   `json:"domain"`
	Secrets map[string]string `json:"secrets,omitempty"`
	// Offsets are transcript offsets already consumed, by session id.
	Offsets map[string]int64 `json:"offsets,omitempty"`
	// Restore lists the sessions a reboot, crash or killed worker ended while they were running; the
	// app asks the owner whether to restore them, then clears it.
	Restore []string `json:"restore,omitempty"`
}

// Listener describes where the daemon accepts connections.
type Listener struct {
	Kind string `json:"kind"` // "loopback" | "lan"
	Addr string `json:"addr"` // host:port for HTTP/WS; the unix socket path is derived from the store dir
}

// Config is the whole of config.json. Secrets are never stored here, only references.
type Config struct {
	Version   int            `json:"version"`
	UpdatedAt time.Time      `json:"updated_at"`
	Listener  Listener       `json:"listener"`
	Decision  DecisionConfig `json:"decision"`
	Network   Network        `json:"network"`
	// AgentModels is the old Claude-on-Ollama setting: read once (pool.MigrateAgentModels), then dropped.
	AgentModels *AgentModels `json:"agent_models,omitempty"`
	// OllamaDefault: New session starts on Claude Code (Ollama), moved from the old "every Claude
	// Code session on Ollama". No setting shows it.
	OllamaDefault bool `json:"ollama_default,omitempty"`
	// Sync shares the Compass and the helpers' experience through a private GitHub repository.
	Sync Sync `json:"sync"`
	// MuseDevices are paired Muse app devices: name, token hash, created and last seen. Tokens
	// themselves are shown once and never stored.
	MuseDevices []MuseDevice `json:"muse_devices,omitempty"`
	// Backup is Sync's old name: read when there is no "sync", and written beside it for one release so a downgrade keeps the settings.
	Backup *Sync `json:"backup,omitempty"`
	// CatalogRepo is the Helper agent hub's Git URL; empty means the default hub.
	CatalogRepo string `json:"catalog_repo,omitempty"`
}

// Sync: GitHub adds the sync of the record through a private repository in the signed-in account,
// created on the first push. (The daily snapshots on this machine, BackupsDir, are always kept.)
type Sync struct {
	GitHub      bool   `json:"github"`
	IntervalMin int    `json:"interval_min,omitempty"` // minutes between pushes; 0: every 5
	Repo        string `json:"repo,omitempty"`         // owner/name, recorded once created
}

// AgentModels: the agents listed in Ollama ran on OllamaModel at OllamaURL instead of their own
// account. Only read, to migrate it.
type AgentModels struct {
	OllamaURL   string   `json:"ollama_url,omitempty"`
	OllamaModel string   `json:"ollama_model,omitempty"`
	Ollama      []string `json:"ollama,omitempty"`
}

// ProviderConfig is one saved decision provider. KeyRef names where the key lives:
// "keychain:<account>" or "env:<VAR>"; the key itself is never in this file.
type ProviderConfig struct {
	Type    string `json:"type"`   // ollama | jev (decision model); ollama | other (writer)
	Format  string `json:"format"` // systemone | ollama-systemone | openai-chat
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
	Model   string `json:"model,omitempty"`
	KeyRef  string `json:"key_ref,omitempty"`
}

// DecisionConfig selects the triage provider. Active is a key of Providers, or "" for rules only.
type DecisionConfig struct {
	Active        string                    `json:"active,omitempty"`
	Providers     map[string]ProviderConfig `json:"providers,omitempty"`
	RulesFallback bool                      `json:"rules_fallback"`
}

// Network holds the phone connection endpoints the owner saved.
type Network struct {
	LAN       bool   `json:"lan"`
	LANAddr   string `json:"lan_addr,omitempty"`
	Tailscale string `json:"tailscale,omitempty"`
}

// Clone returns a deep copy, so callers can never mutate store state by holding a pointer.
func (s State) Clone() State {
	c := s
	c.Nodes = append([]Node(nil), s.Nodes...)
	c.Sessions = make([]Session, len(s.Sessions))
	for i, sess := range s.Sessions {
		c.Sessions[i] = sess.clone()
	}
	// The pool records are plain data; a JSON round trip is the simplest correct deep copy.
	if b, err := json.Marshal(s.Domain); err == nil {
		c.Domain = protocol.Domain{}
		_ = json.Unmarshal(b, &c.Domain)
	}
	c.Secrets = maps.Clone(s.Secrets)
	c.Offsets = maps.Clone(s.Offsets)
	c.Restore = append([]string(nil), s.Restore...)
	return c
}

func (s Session) clone() Session {
	c := s
	c.Command = append([]string(nil), s.Command...)
	c.Env = append([]string(nil), s.Env...)
	if s.ExitCode != nil {
		code := *s.ExitCode
		c.ExitCode = &code
	}
	return c
}

// View converts a session record into its protocol representation.
func (s Session) View() protocol.SessionView {
	v := protocol.SessionView{
		ID:        s.ID,
		Name:      s.Name,
		NodeID:    s.NodeID,
		Cwd:       s.Cwd,
		Command:   append([]string(nil), s.Command...),
		Harness:   s.Harness,
		Via:       s.Via,
		State:     s.State,
		ExitCode:  s.ExitCode,
		Pid:       s.Pid,
		Cols:      s.Cols,
		Rows:      s.Rows,
		StartedAt: RFC3339(s.StartedAt),

		Status:     s.Status,
		Waiting:    s.Waiting,
		TeamID:     s.TeamID,
		Role:       s.Role,
		SkillID:    s.SkillID,
		ContextPct: s.ContextPct,
		Branch:     s.Branch,
		RunLog:     s.RunLog,
		ActiveAt:   RFC3339(s.ActiveAt),
		Summary:    s.Summary,
	}
	return v
}

// RFC3339 formats a timestamp in the UTC RFC 3339 form used across PenguPool AI.
func RFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// DefaultState is the state of a fresh store directory.
func DefaultState(nodeID string, now time.Time) State {
	return State{
		Version:   Version,
		Rev:       1,
		UpdatedAt: now.UTC(),
		NodeID:    nodeID,
		Nodes: []Node{{
			ID:        nodeID,
			Name:      "local",
			Kind:      "local",
			CreatedAt: now.UTC(),
		}},
		Sessions: []Session{},
	}
}

// DefaultConfig is the configuration of a fresh store directory: loopback only.
func DefaultConfig(now time.Time) Config {
	return Config{
		Version:   Version,
		UpdatedAt: now.UTC(),
		Listener:  Listener{Kind: "loopback", Addr: "127.0.0.1:0"},
	}
}

// MuseDevice is one paired Muse app device: the token lives only as a hash.
type MuseDevice struct {
	Name      string `json:"name"`
	TokenHash string `json:"token_hash"`
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen,omitempty"`
}
