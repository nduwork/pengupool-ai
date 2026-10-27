// Package node manages the SSH hosts the app can switch to. The app works in one environment at
// a time, like a remote editor: this machine, or one SSH host whose own PenguPool host serves the
// whole UI (its sessions, teams, skills, memories and settings), so nothing is mirrored or synced.
//
// Every connection goes through the system's OpenSSH, so the user's ssh config, ProxyJump, agents
// and host-key checks all apply, and no remote listener is opened: Connect logs in (keys, or a
// password through PenguPool's askpass, see auth.go), makes sure the host runs this version of
// PenguPool (install.go), and forwards a local port to its host over the master connection.
package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// DefaultCommand is the remote executable name.
const DefaultCommand = "pengupool2"

// Manager keeps the SSH hosts' login state and their forwarded connections.
type Manager struct {
	st   *store.Store
	logf func(string, ...any)
	hub  string // this host's node id
	exe  string // this executable, ssh's askpass program for password nodes
	bin  string // the macOS executable Install copies to a node: this one
	// Version is this build's `version` line; Connect brings a node to the same one.
	Version string

	mu     sync.Mutex
	status map[string]protocol.NodeView
	pw     map[string]string // passwords held in memory, by node id
}

// New loads the SSH hosts. A remote host's sessions are seen by switching to it.
func New(st *store.Store, exe string, logf func(string, ...any)) *Manager {
	return &Manager{st: st, logf: logf, hub: st.NodeID(), exe: exe, bin: exe, status: map[string]protocol.NodeView{}, pw: map[string]string{}}
}

var addrRe = regexp.MustCompile(`^[A-Za-z0-9_.@:\[\]-]+$`)

// args is the ssh command line for one remote command. A password node rides its master
// connection; nothing here ever prompts.
func (m *Manager) args(n store.Node, remote ...string) []string {
	a := []string{"-oBatchMode=yes", "-oConnectTimeout=10", "-oServerAliveInterval=15", "-T"}
	if n.Auth == "password" {
		a = append(a, "-oControlMaster=no", "-oControlPath="+m.controlPath(n.ID))
	}
	if n.IdentityFile != "" {
		a = append(a, "-oIdentityFile="+n.IdentityFile)
	}
	// "--" ends ssh's options, so an address can never be read as a flag.
	return append(append(a, "--", n.Address), remote...)
}

// view is a node's status line with its login details.
func view(n store.Node, status, reason, hub string) protocol.NodeView {
	auth := "key"
	if n.Auth == "password" {
		auth = "password"
	}
	return protocol.NodeView{ID: n.ID, Name: n.Name, Kind: "ssh", Address: n.Address, Status: status, Reason: reason, Hub: hub,
		Auth: auth, IdentityFile: n.IdentityFile, SavedPassword: n.SavedPassword}
}

// Status lists every node, this host first, with each SSH host's last check.
func (m *Manager) Status() []protocol.NodeView {
	st := m.st.State()
	var out []protocol.NodeView
	for _, n := range st.Nodes {
		if n.Kind != "ssh" {
			out = append(out, protocol.NodeView{ID: n.ID, Name: n.Name, Kind: n.Kind, Status: "up", Hub: m.hub})
			continue
		}
		m.mu.Lock()
		v, ok := m.status[n.ID]
		m.mu.Unlock()
		if !ok {
			v = view(n, "unknown", "", m.hub)
		}
		// Login details come from the store, so a change shows before the next check.
		v.Name, v.Address, v.IdentityFile, v.SavedPassword = n.Name, n.Address, n.IdentityFile, n.SavedPassword
		v.Auth = view(n, "", "", "").Auth
		out = append(out, v)
	}
	return out
}

// Add registers an SSH host and checks it. A password host must log in before it is saved; a
// key host, or one without PenguPool, is saved anyway and set up when the owner switches to it.
func (m *Manager) Add(ctx context.Context, in protocol.NodeAdd) (protocol.NodeView, error) {
	address := strings.TrimSpace(in.Address)
	if address == "" || strings.HasPrefix(address, "-") || !addrRe.MatchString(address) {
		return protocol.NodeView{}, protocol.Errorf(protocol.CodeInvalidArgument, "use an ssh destination such as user@host or a Host alias from ~/.ssh/config")
	}
	n := store.Node{ID: protocol.NewID(), Name: strings.TrimSpace(in.Name), Kind: "ssh", Address: address, Command: DefaultCommand, CreatedAt: time.Now().UTC()}
	if n.Name == "" {
		n.Name = address
	}
	for _, o := range m.st.State().Nodes {
		if o.Address == address {
			return protocol.NodeView{}, protocol.Errorf(protocol.CodeConflict, "%s is already a host", address)
		}
	}
	switch in.Auth {
	case "", "key":
	case "password":
		if in.Password == "" {
			return protocol.NodeView{}, protocol.Errorf(protocol.CodeInvalidArgument, "enter the password for %s", address)
		}
		n.Auth = "password"
		if err := m.openMaster(ctx, n, in.Password, in.TrustHostKey); err != nil {
			return view(n, "down", err.Error(), m.hub), protocol.Errorf(protocol.CodeUnavailable, "%v", err)
		}
		if in.Save {
			if err := savePassword(n.ID, in.Password); err != nil {
				m.closeMaster(n)
				return view(n, "down", err.Error(), m.hub), err
			}
			n.SavedPassword = true
		}
		m.mu.Lock()
		m.pw[n.ID] = in.Password
		m.mu.Unlock()
	default:
		return protocol.NodeView{}, protocol.Errorf(protocol.CodeInvalidArgument, "auth must be key or password")
	}
	v := m.probe(ctx, &n)
	if err := m.st.Update(func(st *store.State) error {
		for _, o := range st.Nodes {
			if o.Address == address {
				return protocol.Errorf(protocol.CodeConflict, "%s is already a host", address)
			}
		}
		st.Nodes = append(st.Nodes, n)
		return nil
	}); err != nil {
		m.forget(n)
		return v, err
	}
	m.setStatus(v)
	return v, nil
}

// Remove forgets a host: its login and connection here. PenguPool and its sessions keep
// running on the host.
func (m *Manager) Remove(id string) error {
	n, ok := m.node(id)
	m.mu.Lock()
	delete(m.status, id)
	m.mu.Unlock()
	if ok && n.Kind == "ssh" {
		m.forget(n)
	}
	return m.st.Update(func(st *store.State) error {
		out := st.Nodes[:0]
		for _, n := range st.Nodes {
			if n.ID != id || n.Kind != "ssh" {
				out = append(out, n)
			}
		}
		st.Nodes = out
		return nil
	})
}

// Check probes one node now.
func (m *Manager) Check(ctx context.Context, id string) protocol.NodeView {
	n, ok := m.node(id)
	if !ok {
		return protocol.NodeView{ID: id, Status: "down", Reason: "unknown host"}
	}
	if n.Kind != "ssh" {
		return protocol.NodeView{ID: n.ID, Name: n.Name, Kind: n.Kind, Status: "up"}
	}
	v := m.probe(ctx, &n)
	m.setStatus(v)
	return v
}

// probe logs in and reads the host's PenguPool version. A password node logs in first, unless
// its master connection is still up; with no password to hand it is locked.
func (m *Manager) probe(ctx context.Context, np *store.Node) protocol.NodeView {
	n := *np
	v := view(n, "", "", m.hub)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if n.Auth == "password" {
		if err := m.ensureMaster(ctx, n); err != nil {
			v.Status, v.Reason = "down", err.Error()
			if errors.Is(err, errLocked) {
				v.Status = "locked"
			}
			return v
		}
	}
	out, err := exec.CommandContext(ctx, "ssh", m.args(n, cmdOf(n), "version")...).CombinedOutput()
	if err != nil && cmdOf(n) == DefaultCommand && notFound(string(out)) {
		// ssh runs commands in a non-login shell, whose PATH rarely has ~/.local/bin.
		path, rerr := m.locate(ctx, n)
		if rerr != nil {
			v.Status, v.Reason = "not_installed", rerr.Error()
			return v
		}
		n.Command, np.Command = path, path
		m.setCommand(n.ID, path)
		out, err = exec.CommandContext(ctx, "ssh", m.args(n, cmdOf(n), "version")...).CombinedOutput()
	}
	if err != nil {
		v.Status, v.Reason = "down", firstLine(string(out), err)
		return v
	}
	v.Version = strings.TrimSpace(string(out))
	// Only an older host is brought to this build: a host that runs a newer release (its own app
	// was updated) is never downgraded by an older app connecting to it. Builds whose version lines
	// do not parse are replaced whenever they differ, as before.
	var newer bool
	v.Outdated, newer = versusApp(v.Version, m.Version)
	v.Status = "up"
	if newer {
		v.Status, v.Reason = "newer", "it runs "+v.Version+", newer than this app ("+m.Version+"); update this app to open it"
	}
	return v
}

// versusApp places a host's version line against this app's: outdated (bring it to this build) or
// newer (leave it alone).
func versusApp(host, app string) (outdated, newer bool) {
	if app == "" || host == app {
		return false, false
	}
	c, ok := compareRelease(host, app)
	return !ok || c < 0, ok && c > 0
}

// compareRelease compares the releases (X.Y.Z) of two version lines ("pengupool X.Y.Z+build …"):
// -1, 0 or 1, and whether both parsed.
func compareRelease(a, b string) (int, bool) {
	pa, oka := releaseOf(a)
	pb, okb := releaseOf(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func releaseOf(line string) ([3]int, bool) {
	var r [3]int
	f := strings.Fields(line)
	if len(f) < 2 {
		return r, false
	}
	ver, _, _ := strings.Cut(f[1], "+")
	parts := strings.Split(ver, ".")
	if len(parts) != 3 {
		return r, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil {
			return r, false
		}
		r[i] = n
	}
	return r, true
}

func notFound(out string) bool {
	return strings.Contains(out, "not found") || strings.Contains(out, "No such file")
}

// locateScript prints the first PenguPool executable on the host: the login shell's PATH, then
// where `make install` and the app put it.
const locateScript = `for p in "$($SHELL -lc 'command -v pengupool2' 2>/dev/null)" "$HOME/.local/bin/pengupool2" ` +
	`"$HOME/Applications/PenguPool.app/Contents/Resources/bin/pengupool" "/Applications/PenguPool.app/Contents/Resources/bin/pengupool" ` +
	`/opt/homebrew/bin/pengupool2 /usr/local/bin/pengupool2; do [ -n "$p" ] && [ -x "$p" ] && { echo "$p"; exit 0; }; done; exit 1`

func (m *Manager) locate(ctx context.Context, n store.Node) (string, error) {
	out, err := exec.CommandContext(ctx, "ssh", m.args(n, "sh", "-c", shellQuote(locateScript))...).Output()
	path := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(path, '\n'); i >= 0 {
		path = path[i+1:]
	}
	if err != nil || !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("PenguPool is not installed on %s", n.Address)
	}
	return path, nil
}

// setCommand records the remote executable a probe found.
func (m *Manager) setCommand(id, path string) {
	_ = m.st.Update(func(st *store.State) error {
		for i := range st.Nodes {
			if st.Nodes[i].ID == id {
				st.Nodes[i].Command = path
			}
		}
		return nil
	})
}

func cmdOf(n store.Node) string {
	if n.Command != "" {
		return n.Command
	}
	return DefaultCommand
}

func firstLine(out string, err error) string {
	s := strings.TrimSpace(out)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if s == "" {
		s = err.Error()
	}
	return s
}

func (m *Manager) setStatus(v protocol.NodeView) {
	m.mu.Lock()
	m.status[v.ID] = v
	m.mu.Unlock()
}

func (m *Manager) node(id string) (store.Node, bool) {
	for _, n := range m.st.State().Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return store.Node{}, false
}

// Connect readies an SSH host as the app's environment: log in, bring PenguPool there to this
// version and start its host, then forward a port on this machine's loopback to it over the master
// connection. The forward lives as long as the master, which outlives this daemon.
func (m *Manager) Connect(ctx context.Context, id string) (protocol.HostConnection, error) {
	n, ok := m.node(id)
	if !ok || n.Kind != "ssh" {
		return protocol.HostConnection{}, protocol.Errorf(protocol.CodeNotFound, "no ssh host %s", id)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	unavailable := func(format string, a ...any) (protocol.HostConnection, error) {
		return protocol.HostConnection{}, protocol.Errorf(protocol.CodeUnavailable, "%s: "+format, append([]any{n.Name}, a...)...)
	}
	// A key host gets a master connection too, to carry the forward.
	if n.Auth != "password" && !m.masterUp(ctx, n) {
		if err := m.openMaster(ctx, n, "", false); err != nil {
			return unavailable("%v", err)
		}
	}
	v := m.probe(ctx, &n)
	m.setStatus(v)
	switch {
	case v.Status == "locked":
		return unavailable("enter its password in Settings → SSH hosts (%s)", v.Reason)
	case v.Status == "not_installed" || v.Outdated:
		if err := m.install(ctx, &n); err != nil {
			return unavailable("%v", err)
		}
		if err := m.restartDaemon(ctx, n); err != nil {
			return unavailable("%v", err)
		}
	case v.Status != "up":
		return unavailable("%s", v.Reason)
	}
	if err := m.startDaemon(ctx, n); err != nil {
		return unavailable("%v", err)
	}
	out, err := exec.CommandContext(ctx, "ssh", m.args(n, cmdOf(n), "web")...).Output()
	if err != nil {
		return unavailable("reading its host address: %v", err)
	}
	remote, err := url.Parse(strings.TrimSpace(string(out)))
	if err != nil || remote.Port() == "" {
		return unavailable("unexpected host address %q", strings.TrimSpace(string(out)))
	}
	port, err := m.forward(ctx, n, remote.Port())
	if err != nil {
		return unavailable("%v", err)
	}
	c := protocol.HostConnection{URL: "http://127.0.0.1:" + port, Token: strings.TrimPrefix(remote.Fragment, "token=")}
	if err := healthy(ctx, c); err != nil {
		return unavailable("its host does not answer through the forward: %v", err)
	}
	v.Status, v.Reason, v.Version, v.Outdated = "up", "", m.Version, false
	m.setStatus(v)
	return c, nil
}

// forward forwards a loopback port here to the host's port there. The local port is stable per
// host, so reconnecting reuses the forward the master already has; if another program holds it,
// any free port does.
func (m *Manager) forward(ctx context.Context, n store.Node, remotePort string) (string, error) {
	try := func(local string) error {
		spec := "127.0.0.1:" + local + ":127.0.0.1:" + remotePort
		out, err := exec.CommandContext(ctx, "ssh", "-oControlPath="+m.controlPath(n.ID), "-O", "forward", "-L", spec, "--", n.Address).CombinedOutput()
		if err != nil {
			return errors.New(firstLine(string(out), err))
		}
		return nil
	}
	h := uint32(2166136261)
	for _, b := range []byte(n.ID) {
		h = (h ^ uint32(b)) * 16777619
	}
	stable := fmt.Sprint(27100 + h%800)
	if try(stable) == nil {
		return stable, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	_, free, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	if err := try(free); err != nil {
		return "", fmt.Errorf("forwarding a port: %v", err)
	}
	return free, nil
}

// healthy asks the forwarded host for its health with the token, as the app's own probe does.
func healthy(ctx context.Context, c protocol.HostConnection) error {
	var last error
	for range 20 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/healthz", nil)
		req.Header.Set("X-PenguPool-Token", c.Token)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %d", res.StatusCode)
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return last
}
