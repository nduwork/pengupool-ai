package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errProcessGone reports that a pid names no process any more.
var errProcessGone = errors.New("process is gone")

// procStatus is the verdict of an identity check on one pid.
type procStatus int

const (
	// procOurs: the pid is alive, its whole command line is this daemon's worker invocation for the
	// session, and it started when the handshake says it did.
	procOurs procStatus = iota
	// procGone: the pid is gone.
	procGone
	// procForeign: the pid is alive but is not this daemon's worker for the session. The worker died
	// and the pid was recycled, or the record never described a worker of ours at all.
	procForeign
	// procUnknown: the process table could not be read, so nothing about the pid is known. This is an
	// error and never "not ours": a failed look must not turn a live worker into a lost session, and
	// it must not let a kill go to a stranger.
	procUnknown
)

func (s procStatus) String() string {
	switch s {
	case procOurs:
		return "ours"
	case procGone:
		return "gone"
	case procForeign:
		return "foreign"
	default:
		return "unknown"
	}
}

// procIdentity is a pid's verdict plus the evidence behind it, for the log.
type procIdentity struct {
	status procStatus
	detail string
	// start is the start time the process table reported, when it reported one.
	start time.Time
}

// startSlack is how far a handshake's start time may differ from the process table's. ps reports
// seconds in local time, and the worker records the time its own main began, a little after the
// fork, so the two are never exactly equal.
const startSlack = 2 * time.Second

// pidAlive reports whether pid names a process this user can signal. A process that has already
// been reaped is gone; a zombie still counts as present, which is why the spawn path reaps workers
// in the background.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// signalProcess sends SIGTERM (term true) or SIGKILL (term false). A pid that is already gone is
// not an error.
func signalProcess(pid int, term bool) error {
	sig := syscall.SIGKILL
	if term {
		sig = syscall.SIGTERM
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// waitForExit waits for pid to disappear, giving up after d or as soon as ctx is done.
func waitForExit(ctx context.Context, pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !pidAlive(pid) {
			return true
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// psField runs ps for one pid and field, wide enough that a long command line is not truncated. It
// returns an error whenever ps said nothing usable: callers must treat that as "unknown", never as
// "the process is not ours".
func psField(pid int, field string) (string, error) {
	if pid <= 0 {
		return "", errProcessGone
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-ww", "-o", field+"=")
	cmd.Env = append(os.Environ(), "COLUMNS=4096")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ps -p %d -o %s: %w", pid, field, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// procCommandLine reads a pid's whole command line, arguments joined the way ps prints them. A
// command line is everything that identifies a worker: the executable alone is not, because a
// recycled pid usually belongs to a program that looks nothing like ours.
func procCommandLine(pid int) (string, error) {
	if !pidAlive(pid) {
		return "", errProcessGone
	}
	line, err := psField(pid, "command")
	if err != nil {
		return "", err
	}
	if line == "" {
		// ps refused to name a process that exists: the pid went away between the two probes.
		if !pidAlive(pid) {
			return "", errProcessGone
		}
		return "", fmt.Errorf("ps knows no command line for live pid %d", pid)
	}
	return line, nil
}

// procStartTime reads when a pid started. ps prints the second in local time, which is as precise as
// the process table gets; a pid that was recycled started at a visibly different time.
func procStartTime(pid int) (time.Time, error) {
	raw, err := psField(pid, "lstart")
	if err != nil {
		if !pidAlive(pid) {
			return time.Time{}, errProcessGone
		}
		return time.Time{}, err
	}
	started, ok := parseStartTime(raw)
	if !ok {
		return time.Time{}, fmt.Errorf("ps printed %q as the start time of pid %d", raw, pid)
	}
	return started, nil
}

// parseStartTime parses ps's `lstart` field, "Mon Jan  2 15:04:05 2006" in local time. ps pads the
// day of the month with a space, so the fields are rejoined before parsing.
func parseStartTime(raw string) (time.Time, bool) {
	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields, " "), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// sameStartTime reports whether the process table's start time and the handshake's agree, allowing
// for ps's one-second resolution.
func sameStartTime(table, recorded time.Time) bool {
	d := table.Sub(recorded)
	if d < 0 {
		d = -d
	}
	return d <= startSlack
}

// workerArgvPrefix is the beginning of the command line of this daemon's worker for a session.
func workerArgvPrefix(exe, id string) string {
	return exe + " worker --session-id " + id
}

// isWorkerCommand reports whether a command line is a worker invocation for id, matching the whole
// joined argv: it must start with `<exe> worker --session-id <id>`, so a path that contains spaces
// is matched as one path and never split into fields. Anything after the session id must be another
// argument, which keeps a longer session id or a stale pid from matching by prefix.
func isWorkerCommand(cmdline string, exes []string, id string) bool {
	for _, exe := range exes {
		if exe == "" {
			continue
		}
		prefix := workerArgvPrefix(exe, id)
		if cmdline == prefix {
			return true
		}
		if rest, ok := strings.CutPrefix(cmdline, prefix); ok && strings.HasPrefix(rest, " ") {
			return true
		}
	}
	return false
}

// isUpgradedWorker accepts a worker started by another pengupool binary (an older build, or the
// same build at another install path): workers outlive upgrades and must be adopted. Only used
// when the handshake carries the worker's start time, so the pid's start time still proves it.
func isUpgradedWorker(cmdline, id string) bool {
	i := strings.Index(cmdline, " worker --session-id "+id)
	if i <= 0 {
		return false
	}
	rest := cmdline[i+len(" worker --session-id "+id):]
	return filepath.Base(cmdline[:i]) == "pengupool" && (rest == "" || strings.HasPrefix(rest, " "))
}

// workerExes lists the spellings of this daemon's binary a worker's command line may start with: the
// path the hub was given (a bare name when the daemon was started through PATH) and the same path
// with symlinks resolved, because a daemon that restarted through another spelling must still
// recognize its own workers.
func (h *Hub) workerExes() []string {
	if h.exePath != "" && h.exePath != h.executable {
		return []string{h.executable, h.exePath}
	}
	return []string{h.executable}
}

// checkWorker decides what pid proves about session id. A pid is ours only when it is alive, its
// whole command line is this daemon's worker invocation for the session, and it started when the
// handshake says it did: a recycled pid fails the start time even when it runs the same binary.
func (h *Hub) checkWorker(pid int, id string, startedAt time.Time) procIdentity {
	cmdline, err := procCommandLine(pid)
	switch {
	case errors.Is(err, errProcessGone):
		return procIdentity{status: procGone, detail: fmt.Sprintf("pid %d is gone", pid)}
	case err != nil:
		return procIdentity{status: procUnknown, detail: err.Error()}
	}
	if !isWorkerCommand(cmdline, h.workerExes(), id) && (startedAt.IsZero() || !isUpgradedWorker(cmdline, id)) {
		return procIdentity{status: procForeign,
			detail: fmt.Sprintf("pid %d runs %q, not this daemon's worker for %s", pid, cmdline, id)}
	}
	if startedAt.IsZero() {
		// An older handshake carries no worker start time: the command line is all the proof there is.
		return procIdentity{status: procOurs, detail: fmt.Sprintf("pid %d runs this daemon's worker for %s", pid, id)}
	}
	started, err := procStartTime(pid)
	switch {
	case errors.Is(err, errProcessGone):
		return procIdentity{status: procGone, detail: fmt.Sprintf("pid %d is gone", pid)}
	case err != nil:
		return procIdentity{status: procUnknown, detail: err.Error()}
	}
	if !sameStartTime(started, startedAt) {
		return procIdentity{status: procForeign,
			detail: fmt.Sprintf("pid %d started at %s, the handshake says %s: the pid was reused",
				pid, started.Format(time.RFC3339), startedAt.Format(time.RFC3339))}
	}
	return procIdentity{status: procOurs, start: started,
		detail: fmt.Sprintf("pid %d is the worker of %s, started %s", pid, id, started.Format(time.RFC3339))}
}

// resolveExecutable resolves a name from a command line or the process table to an absolute path
// with symlinks resolved, so that two spellings of the same binary compare equal.
func resolveExecutable(name string) string {
	if name == "" {
		return ""
	}
	if !strings.ContainsRune(name, os.PathSeparator) {
		if p, err := exec.LookPath(name); err == nil {
			name = p
		}
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return name
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
