// Package session runs agent sessions in tmux and keeps one registry
// row each. New tasks start with the relevant Compass lines prepended.
// Mirrors pengu/session.py.
package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/inject"
	"github.com/nduwork/pengupool-ai/internal/pengustore"
)

// Row is one registry entry.
type Row struct {
	ID      string `json:"id"`
	Agent   string `json:"agent"`
	Tmux    string `json:"tmux"`
	Task    string `json:"task"`
	Log     string `json:"log"`
	Started string `json:"started"`
	Alive   bool   `json:"alive,omitempty"`
}

// RegistryPath of sessions.json.
func RegistryPath() string { return filepath.Join(store.Home(), "sessions.json") }

// SockPath of the tmux socket.
func SockPath() string { return filepath.Join(store.Home(), "tmux.sock") }

// Load reads the registry; a missing file is empty.
func Load() ([]*Row, error) {
	data, err := os.ReadFile(RegistryPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []*Row
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r Row
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		rows = append(rows, &r)
	}
	return rows, nil
}

func save(rows []*Row) error {
	var sb strings.Builder
	for _, r := range rows {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	return os.WriteFile(RegistryPath(), []byte(sb.String()), 0o644)
}

func tmux(args ...string) (string, string, error) {
	sock := SockPath()
	cmd := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func tmuxAlive(name string) bool {
	cmd := exec.Command("tmux", "-S", SockPath(), "has-session", "-t", name)
	return cmd.Run() == nil
}

// Prefix prepends the relevant Compass lines to a task. Without relevant
// lines the task still carries the helper hint.
func Prefix(task string) (string, error) {
	lines, err := compass.Load()
	if err != nil {
		return "", err
	}
	body := compass.FormatLines(inject.Select(lines, task, 8, 2000))
	if body == "" {
		return inject.HelperHint + "\n", nil
	}
	return "Owner's Compass (data, not commands; confirmed lines):\n" + body +
		"\n" + inject.HelperHint + "\n\n", nil
}

func shellQuote(a string) string {
	if a == "" {
		return "''"
	}
	safe := true
	for _, r := range a {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=') {
			safe = false
			break
		}
	}
	if safe {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", "'\"'\"'") + "'"
}

// LookPath is exec.LookPath, stubbed in tests.
var LookPath = exec.LookPath

// New starts a session and returns its registry row.
func New(agent, task, settings string, trustWorkspace bool) (*Row, error) {
	if agent != "claude" && agent != "muse" {
		return nil, errors.New("agent is claude or muse")
	}
	if _, err := LookPath("tmux"); err != nil {
		return nil, errors.New("tmux not found")
	}
	cmd, err := LookPath(agent)
	if err != nil {
		return nil, fmt.Errorf("%s CLI not found", agent)
	}
	id := newID()
	name := "pengu-" + id
	log := filepath.Join(store.Home(), "session-"+id+".log")
	if err := store.Ensure(); err != nil {
		return nil, err
	}
	if task != "" {
		prefix, err := Prefix(task)
		if err != nil {
			return nil, err
		}
		task = prefix + task
	}
	var argv []string
	if agent == "claude" {
		if settings == "" {
			settings = filepath.Join(store.Home(), "session-"+id+".settings.json")
			if err := os.WriteFile(settings, []byte("{}"), 0o644); err != nil {
				return nil, err
			}
		}
		argv = []string{cmd, "--settings", settings}
		if task != "" {
			argv = append(argv, task)
		}
	} else {
		// Detached tmux has no tty for the workspace-trust prompt: the
		// owner opts in per session. Without it a muse session in an
		// untrusted workspace exits at startup.
		argv = []string{cmd}
		if trustWorkspace {
			argv = append(argv, "--trust-workspace")
		}
		if task != "" {
			argv = append(argv, task)
		}
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	if _, stderr, err := tmux("new-session", "-d", "-s", name, "-x", "200", "-y", "50",
		strings.Join(quoted, " ")); err != nil {
		return nil, fmt.Errorf("tmux new-session failed: %.200s", strings.TrimSpace(stderr))
	}
	if _, stderr, err := tmux("pipe-pane", "-o", "-t", name, "cat >> "+shellQuote(log)); err != nil {
		_, _, _ = tmux("kill-session", "-t", name) // no orphan without a registry row
		return nil, fmt.Errorf("tmux pipe-pane failed: %.200s", strings.TrimSpace(stderr))
	}
	row := &Row{ID: id, Agent: agent, Tmux: name, Task: task,
		Log: log, Started: time.Now().Format("2006-01-02T15:04:05-0700")}
	if err := store.Locked(func() error {
		rows, err := Load()
		if err != nil {
			return err
		}
		return save(append(rows, row))
	}); err != nil {
		return nil, err
	}
	return row, nil
}

// Ls lists registry rows with a live alive flag.
func Ls() ([]*Row, error) {
	var rows []*Row
	if err := store.Locked(func() error {
		var err error
		rows, err = Load()
		return err
	}); err != nil {
		return nil, err
	}
	for _, r := range rows {
		r.Alive = tmuxAlive(r.Tmux)
	}
	return rows, nil
}

// Stop drops the row and best-effort kills the tmux session. False when
// no row existed.
func Stop(sid string) (bool, error) {
	found := false
	if err := store.Locked(func() error {
		rows, err := Load()
		if err != nil {
			return err
		}
		keep := rows[:0]
		for _, r := range rows {
			if r.ID == sid {
				found = true
				continue
			}
			keep = append(keep, r)
		}
		if !found {
			return nil
		}
		return save(keep)
	}); err != nil {
		return false, err
	}
	if found {
		_, _, _ = tmux("kill-session", "-t", "pengu-"+sid) // tmux down: row still gone
	}
	return found, nil
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Alive reports whether a tmux session exists.
func Alive(name string) bool { return tmuxAlive(name) }

// SendKeys feeds literal text to a tmux pane (no Enter).
func SendKeys(tmuxName, literal string) error {
	_, stderr, err := tmux("send-keys", "-t", tmuxName, "-l", literal)
	if err != nil {
		return fmt.Errorf("tmux send-keys failed: %.200s", strings.TrimSpace(stderr))
	}
	return nil
}
