// Package session implements the PenguPool worker: one process that owns one PTY, the agent
// process inside it, a 4 MiB output ring, and the terminal emulator used for snapshots.
//
// A worker survives its daemon. It listens on its own unix socket under the store's run/ directory
// and speaks the same versioned frame protocol as every other hop, so a restarted daemon simply
// reconnects. The hub (internal/hub) spawns workers and relays client frames to them.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultRingBytes is the screen-replay ring size (8 MiB): what a client that detached (another
// session selected, a dropped connection) can catch up on from its own offset, scrollback included.
// The buffer is only touched as output arrives, so a quiet session holds little of it.
const DefaultRingBytes = 8 << 20

// DefaultCols and DefaultRows are the PTY size a worker starts with when the launcher does not
// specify one.
const (
	DefaultCols = 80
	DefaultRows = 24
)

// Handshake is written by a worker after its PTY is live, and read by a daemon that wants to adopt
// a worker it did not start (daemon restart, or a worker started before an upgrade).
type Handshake struct {
	SessionID string    `json:"session_id"`
	Pid       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"` // when the PTY child started, not the worker
	// WorkerStartedAt is when the worker process itself started. A daemon adopting or killing a
	// pid it did not start must compare it: a reused pid would otherwise be mistaken for ours.
	WorkerStartedAt time.Time `json:"worker_started_at,omitempty"`
	Protocol        int       `json:"protocol"`
	Cols            int       `json:"cols"`
	Rows            int       `json:"rows"`
	Socket          string    `json:"socket"`
	Cwd             string    `json:"cwd,omitempty"`
	Command         []string  `json:"command,omitempty"`
	Name            string    `json:"name,omitempty"`
	// ExitCode and EndedAt are written by a worker whose child exited while no daemon was
	// supervising it, so reconcile records "exited(code)" instead of "lost".
	ExitCode *int      `json:"exit_code,omitempty"`
	EndedAt  time.Time `json:"ended_at,omitempty"`
}

// LaunchSpec is the worker's start-up contract. The hub marshals it into a pipe and passes the read
// end as file descriptor 3, so the command line never carries the spec.
type LaunchSpec struct {
	SessionID  string   `json:"session_id"`
	Name       string   `json:"name,omitempty"`
	Dir        string   `json:"dir"`
	Command    []string `json:"command"`
	Env        []string `json:"env,omitempty"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	RingBytes  int      `json:"ring_bytes,omitempty"`
	SocketPath string   `json:"socket_path"`
}

// Normalize fills in defaults and rejects a spec a worker cannot run.
func (s LaunchSpec) Normalize() (LaunchSpec, error) {
	if s.SessionID == "" {
		return s, fmt.Errorf("launch spec has no session id")
	}
	if s.SocketPath == "" {
		return s, fmt.Errorf("launch spec has no socket path")
	}
	if len(s.Command) == 0 {
		return s, fmt.Errorf("launch spec has no command")
	}
	if s.Cols <= 0 {
		s.Cols = DefaultCols
	}
	if s.Rows <= 0 {
		s.Rows = DefaultRows
	}
	if s.RingBytes <= 0 {
		s.RingBytes = DefaultRingBytes
	}
	return s, nil
}

// maxSocketPath is the usable length of a unix socket path. macOS caps sun_path at 104 bytes
// including the terminating NUL and Linux at 108, so 100 leaves headroom for both.
const maxSocketPath = 100

// RunDir is where workers keep their sockets and handshakes.
//
// Unix sockets cannot be longer than maxSocketPath, and a store directory is the user's choice —
// a temporary directory used by a test can already be 60 bytes. When the preferred path does not
// fit, the whole run directory moves to a short per-store directory under the system temporary
// directory, derived from the store path so every process computes the same location.
func RunDir(storeDir string) string {
	preferred := filepath.Join(storeDir, "run")
	if len(filepath.Join(preferred, socketFileName("0000000000000000"))) <= maxSocketPath {
		return preferred
	}
	sum := sha256.Sum256([]byte(storeDir))
	return filepath.Join(os.TempDir(), "pengupool-"+hex.EncodeToString(sum[:4]))
}

// SocketPath is a session's worker socket. The file name uses the random tail of the session id:
// a UUIDv7 starts with a timestamp, so two sessions started in the same millisecond would share a
// prefix. The run directory is created if it does not exist.
func SocketPath(storeDir, sessionID string) string {
	dir := RunDir(storeDir)
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, socketFileName(sessionID))
}

// HandshakePath is a session's handshake file, next to its socket so the worker can publish it
// without knowing the store directory.
func HandshakePath(storeDir, sessionID string) string {
	dir := RunDir(storeDir)
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, sessionID+".json")
}

// socketFileName is the socket's base name: the last 16 hex digits of the session id.
func socketFileName(sessionID string) string {
	compact := strings.NewReplacer("-", "").Replace(sessionID)
	if len(compact) > 16 {
		compact = compact[len(compact)-16:]
	}
	if compact == "" {
		compact = "session"
	}
	return compact + ".sock"
}

// WriteHandshake writes the handshake atomically.
func WriteHandshake(path string, h Handshake) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadHandshake reads a session handshake.
func ReadHandshake(path string) (Handshake, error) {
	var h Handshake
	raw, err := os.ReadFile(path)
	if err != nil {
		return h, err
	}
	return h, json.Unmarshal(raw, &h)
}
