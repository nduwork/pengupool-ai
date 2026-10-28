package node

// Remote setup. Switching to a host needs PenguPool there at this version with its daemon
// running. Connect installs (or updates) the executable from the app bundle — the macOS (Apple
// Silicon) binary is this executable, and the bundle carries Linux builds next to it — and
// restarts or starts the daemon.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// startScript starts the node's daemon unless one answers, through the login shell so agents it
// launches get the PATH the owner has in a terminal, and waits for it to come up. $1 is the
// executable.
const startScript = `"$1" ls >/dev/null 2>&1 && exit 0
logs="${PENGUPOOL_HOME:-$HOME/.pengupool-ai}/v2/logs"; mkdir -p "$logs"
nohup "${SHELL:-/bin/sh}" -lc "exec '$1' daemon" </dev/null >>"$logs/daemon.log" 2>&1 &
i=0; while [ $i -lt 30 ]; do "$1" ls >/dev/null 2>&1 && exit 0; sleep 0.5; i=$((i+1)); done
echo "the PenguPool host did not start; see $logs/daemon.log"; exit 1`

// startDaemon makes sure the node's daemon is running.
func (m *Manager) startDaemon(ctx context.Context, n store.Node) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", m.args(n, "sh", "-c", shellQuote(startScript), "pengupool", cmdOf(n))...).CombinedOutput()
	if err != nil {
		return errors.New(firstLine(string(out), err))
	}
	return nil
}

// installScript reads an executable on stdin and puts it at ~/.local/bin/pengupool-ai, replacing
// any older one only once the copy is complete, then prints its path.
const installScript = `set -e; d="$HOME/.local/bin"; mkdir -p "$d"; t="$d/.pengupool-ai.$$"
cat > "$t"; chmod 755 "$t"; mv -f "$t" "$d/pengupool-ai"; echo "$d/pengupool-ai"`

// install copies the PenguPool executable for the node's OS and CPU to ~/.local/bin/pengupool-ai
// over the node's connection and records it as the node's command.
func (m *Manager) install(ctx context.Context, n *store.Node) error {
	out, err := exec.CommandContext(ctx, "ssh", m.args(*n, "uname", "-sm")...).CombinedOutput()
	if err != nil {
		return errors.New(firstLine(string(out), err))
	}
	src, err := m.binaryFor(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "ssh", m.args(*n, "sh", "-c", shellQuote(installScript))...)
	cmd.Stdin = f
	out, err = cmd.CombinedOutput()
	path := strings.TrimSpace(string(out))
	if err != nil || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("copying PenguPool to %s: %s", n.Address, firstLine(path, err))
	}
	n.Command = path
	m.setCommand(n.ID, path)
	return nil
}

// stopScript stops the daemon of the host's store — the pid in its lock file, if that process is
// still a PenguPool daemon — and waits for it to go. Its sessions keep running and the next
// daemon adopts them.
const stopScript = `pid=$(head -n1 "${PENGUPOOL_HOME:-$HOME/.pengupool-ai}/v2/daemon.lock" 2>/dev/null)
[ -n "$pid" ] && ps -p "$pid" -o command= 2>/dev/null | grep -q ' daemon' || exit 0
kill "$pid" 2>/dev/null
i=0; while [ $i -lt 20 ]; do kill -0 "$pid" 2>/dev/null || exit 0; sleep 0.5; i=$((i+1)); done
echo "the PenguPool host did not stop"; exit 1`

// restartDaemon replaces the host's running daemon with the node's command.
func (m *Manager) restartDaemon(ctx context.Context, n store.Node) error {
	out, err := exec.CommandContext(ctx, "ssh", m.args(n, "sh", "-c", shellQuote(stopScript))...).CombinedOutput()
	if err != nil {
		return errors.New(firstLine(string(out), err))
	}
	return m.startDaemon(ctx, n)
}

// binaryFor picks the bundled executable for `uname -sm` output.
func (m *Manager) binaryFor(uname string) (string, error) {
	f := strings.Fields(uname)
	if len(f) != 2 {
		return "", fmt.Errorf("unrecognised host system %q", uname)
	}
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[f[1]]
	switch {
	case f[0] == "Darwin" && arch == "amd64":
		return "", errors.New("PenguPool's macOS host runs on Apple Silicon only")
	case f[0] == "Darwin" && arch != "":
		return m.bin, nil // the app's host binary, arm64
	case f[0] == "Linux" && arch != "":
		p := filepath.Join(filepath.Dir(m.bin), "linux", "pengupool-"+arch)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("this build has no Linux %s executable; install the app with make install", arch)
		}
		return p, nil
	}
	return "", fmt.Errorf("PenguPool does not run on %s", uname)
}
