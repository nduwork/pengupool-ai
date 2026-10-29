package protocol

import (
	"encoding/json"

	"github.com/google/uuid"
)

// Control message types.
const (
	TypeHello           = "hello"
	TypeHelloOK         = "hello.ok"
	TypePing            = "ping"
	TypePong            = "pong"
	TypeError           = "error"
	TypeStateSubscribe  = "state.subscribe"
	TypeStateSnapshot   = "state.snapshot"
	TypeStateDelta      = "state.delta"
	TypeSessionAttach   = "session.attach"
	TypeSessionAttached = "session.attached"
	TypeSessionDetach   = "session.detach"
	TypeSessionResize   = "session.resize"
	TypeSessionExit     = "session.exit"
	TypeSessionCreate   = "session.create"
	TypeSessionList     = "session.list"
	TypeSessionKill     = "session.kill"
	TypeSnapshotBegin   = "snapshot.begin"
	TypeSnapshotEnd     = "snapshot.end"
	TypeLeaseChanged    = "lease.changed"
)

// Attach modes reported in session.attached.
const (
	ModeReplay   = "replay"
	ModeSnapshot = "snapshot"
)

// Capabilities advertised by the daemon in hello.ok.
var Capabilities = []string{
	"state.subscribe",
	"session.attach",
	"session.input",
	"session.resize",
	"session.snapshot",
	"session.create",
	"session.list",
	"session.kill",
	"session.lease",
}

// ClientKind identifies the connecting client for logging and capability checks.
type ClientKind string

// Known client kinds.
const (
	KindCLI     ClientKind = "cli"
	KindBrowser ClientKind = "browser"
	KindIOS     ClientKind = "ios"
	KindBridge  ClientKind = "bridge"
)

// ClientInfo describes the connecting client in a hello message.
type ClientInfo struct {
	Kind    ClientKind `json:"kind"`
	Name    string     `json:"name,omitempty"`
	Version string     `json:"version,omitempty"`
	// Token: the daemon's token, sent by the owner's terminal CLI on the unix socket. It allows
	// session.create and session.kill there, never pool requests (those are the app's).
	Token string `json:"token,omitempty"`
}

// RetainedState tells a server which client state the caller still holds, so it can decide between
// replaying missing bytes and sending a fresh snapshot.
type RetainedState struct {
	Generation uint64 `json:"generation"`
	Offset     uint64 `json:"offset"`
}

// SessionView is the state.subscribe representation of one session.
type SessionView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	NodeID    string   `json:"node_id"`
	Cwd       string   `json:"cwd"`
	Command   []string `json:"command,omitempty"`
	Harness   string   `json:"harness,omitempty"`
	Via       string   `json:"via,omitempty"` // the launcher it runs through: "ollama" for Claude Code on Ollama
	State     string   `json:"state"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	Pid       int      `json:"pid,omitempty"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
	StartedAt string   `json:"started_at,omitempty"`

	// Pool fields: harness status from hooks, team membership and context use.
	Status     string `json:"status,omitempty"`  // working | waiting | approval | idle
	Waiting    string `json:"waiting,omitempty"` // a first-run dialog the agent is stuck on, while starting
	TeamID     string `json:"team_id,omitempty"`
	Role       string `json:"role,omitempty"` // main | helper
	SkillID    string `json:"skill_id,omitempty"`
	ContextPct int    `json:"context_pct,omitempty"`
	Branch     string `json:"branch,omitempty"`
	RunLog     string `json:"run_log,omitempty"`
	ActiveAt   string `json:"active_at,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

// Message is one control message. Every message carries ID and Type; the remaining fields are
// per-type and omitempty, which keeps one machine-readable schema for all control traffic.
type Message struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	V            int         `json:"v,omitempty"`
	Client       *ClientInfo `json:"client,omitempty"`
	NodeID       string      `json:"node_id,omitempty"`
	ConnectionID string      `json:"connection_id,omitempty"`
	Capabilities []string    `json:"capabilities,omitempty"`

	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`

	// state.subscribe / state.snapshot / state.delta
	SinceRev uint64        `json:"since_rev,omitempty"`
	Rev      uint64        `json:"rev,omitempty"`
	Epoch    string        `json:"epoch,omitempty"`
	Sessions []SessionView `json:"sessions,omitempty"`
	Upserted []SessionView `json:"upserted,omitempty"`
	Removed  []string      `json:"removed,omitempty"`

	// session.attach / session.attached / session.resize
	SessionID  string         `json:"session_id,omitempty"`
	Channel    uint32         `json:"channel,omitempty"`
	Generation uint64         `json:"generation,omitempty"`
	Retained   *RetainedState `json:"retained,omitempty"`
	ClaimLease bool           `json:"claim_lease,omitempty"`
	Cols       int            `json:"cols,omitempty"`
	Rows       int            `json:"rows,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	Offset     uint64         `json:"offset,omitempty"`

	// session.create
	Name    string   `json:"name,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Command []string `json:"command,omitempty"`
	Harness string   `json:"harness,omitempty"`

	// kind of the client behind a relayed request (session.attach): protocol.ClientKind. The worker
	// refuses the size lease and input from KindIOS, whose screen is view-only.
	Kind string `json:"kind,omitempty"`

	// lease.changed: the worker tells every attached client who holds the size lease. Owner is set
	// for the holder itself; ConnectionID names the holder in every copy.
	Owner bool `json:"owner,omitempty"`

	// session.exit
	ExitCode *int   `json:"exit_code,omitempty"`
	At       string `json:"at,omitempty"`

	// Pool state in state.snapshot and state.delta (whole, only when it changed).
	Domain *Domain `json:"domain,omitempty"`

	// Pool requests: every other request type carries its arguments in Params and is answered
	// with the same type and the outcome in Result.
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`

	// snapshot.begin / snapshot.end
	SnapshotID string `json:"snapshot_id,omitempty"`
	Parts      int    `json:"parts,omitempty"`
}

// ErrorMessage builds an error response echoing the request id.
func ErrorMessage(requestID string, err error) *Message {
	return &Message{ID: requestID, Type: TypeError, Code: CodeOf(err), Message: err.Error()}
}

// NewID returns a UUIDv7, the record id and request id format used across PenguPool AI.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the entropy source fails; fall back to a random UUID.
		return uuid.NewString()
	}
	return id.String()
}
