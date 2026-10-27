package node

// Password login. OpenSSH reads passwords only from a terminal or an askpass program, so
// PenguPool is its own askpass: ssh runs this executable with PENGUPOOL_ASKPASS naming a FIFO, and
// the daemon writes the password into that FIFO once. The password never touches disk or argv,
// and only the master connection ever asks for it: every probe, bridge and attach multiplexes over
// that master (ControlMaster), which outlives the daemon like the workers do.
//
// A saved password lives in the macOS Keychain; an unsaved one is held in memory until the
// daemon exits. When a master is gone and no password is at hand, the node is locked until the
// owner unlocks it, never retried.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// AskpassEnv names the FIFO an askpass child reads the password from.
const AskpassEnv = "PENGUPOOL_ASKPASS"

var errLocked = errors.New("password needed")

// controlPath is the node's master socket. ssh appends 17 bytes for a temporary name while it
// sets the master up, and the result must fit a Unix socket path.
func (m *Manager) controlPath(id string) string {
	sum := sha256.Sum256([]byte(m.st.Dir() + id))
	name := "ssh-" + hex.EncodeToString(sum[:6])
	dir := session.RunDir(m.st.Dir())
	if len(filepath.Join(dir, name))+17 > 100 {
		dir = os.TempDir()
	}
	return filepath.Join(dir, name)
}

func (m *Manager) masterUp(ctx context.Context, n store.Node) bool {
	return exec.CommandContext(ctx, "ssh", "-oControlPath="+m.controlPath(n.ID), "-O", "check", "--", n.Address).Run() == nil
}

// ensureMaster makes sure a password node's master connection is up, logging in with the
// password from memory or the Keychain.
func (m *Manager) ensureMaster(ctx context.Context, n store.Node) error {
	if m.masterUp(ctx, n) {
		return nil
	}
	m.mu.Lock()
	pw := m.pw[n.ID]
	m.mu.Unlock()
	if pw == "" && n.SavedPassword {
		var err error
		if pw, err = loadPassword(n.ID); err != nil {
			return fmt.Errorf("%w: %v", errLocked, err)
		}
	}
	if pw == "" {
		return errLocked
	}
	err := m.openMaster(ctx, n, pw, false)
	if errors.Is(err, errRejected) {
		// Never retry a rejected password: hosts lock accounts out for less.
		m.mu.Lock()
		delete(m.pw, n.ID)
		m.mu.Unlock()
		return fmt.Errorf("%w: the password was rejected", errLocked)
	}
	return err
}

var errRejected = errors.New("permission denied")

// openMaster logs in and leaves the master connection running in the background: with a
// password through the askpass FIFO, or with "" as ssh would on its own (keys and agent).
func (m *Manager) openMaster(ctx context.Context, n store.Node, password string, trustHostKey bool) error {
	login := []string{"-oBatchMode=yes"}
	var env []string
	if password != "" {
		fifo, done, err := servePassword(m.st.Dir(), password)
		if err != nil {
			return err
		}
		defer done()
		login = []string{"-oBatchMode=no", "-oNumberOfPasswordPrompts=1", "-oPreferredAuthentications=keyboard-interactive,password"}
		env = []string{"SSH_ASKPASS=" + m.exe, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0", AskpassEnv + "=" + fifo}
	}
	if n.IdentityFile != "" {
		login = append(login, "-oIdentityFile="+n.IdentityFile)
	}
	// ssh -f forks the master after login; it keeps any pipe open, so its output goes to a file.
	out, err := os.CreateTemp(session.RunDir(m.st.Dir()), "ssh-login-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	hostKey := "-oStrictHostKeyChecking=yes"
	if trustHostKey {
		hostKey = "-oStrictHostKeyChecking=accept-new"
	}
	// A leftover socket would make ssh give up on being the master and log in unmultiplexed.
	_ = os.Remove(m.controlPath(n.ID))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append([]string{"-f", "-N", "-T", "-oControlMaster=yes", "-oControlPersist=yes", "-oControlPath=" + m.controlPath(n.ID),
		"-oConnectTimeout=10", "-oServerAliveInterval=15", hostKey}, login...)
	cmd := exec.CommandContext(ctx, "ssh", append(args, "--", n.Address)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, out, out
	// No controlling terminal: an older ssh without SSH_ASKPASS_REQUIRE would read /dev/tty.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Run(); err != nil {
		b, _ := os.ReadFile(out.Name())
		msg := firstLine(string(b), err)
		switch {
		case strings.Contains(msg, "Permission denied"):
			return fmt.Errorf("%w: %s", errRejected, msg)
		case strings.Contains(msg, "Host key verification failed"), strings.Contains(msg, "No ED25519 host key"), strings.Contains(msg, "host key"):
			return fmt.Errorf("%s: the host key is not known to this machine; allow trusting it on first connect, or connect once with ssh", n.Address)
		}
		return errors.New(msg)
	}
	return nil
}

func (m *Manager) closeMaster(n store.Node) {
	_ = exec.Command("ssh", "-oControlPath="+m.controlPath(n.ID), "-O", "exit", "--", n.Address).Run()
}

// forget drops everything a node held for login: master, password in memory and in the Keychain.
func (m *Manager) forget(n store.Node) {
	if n.Auth == "password" {
		m.closeMaster(n)
	}
	m.mu.Lock()
	delete(m.pw, n.ID)
	m.mu.Unlock()
	if n.SavedPassword {
		_ = deletePassword(n.ID)
	}
}

// Unlock logs a node in with a password, switching a key node to password login. save keeps
// the password in the Keychain; otherwise any saved one is removed and this one lives in memory.
func (m *Manager) Unlock(ctx context.Context, id, password string, save bool) (protocol.NodeView, error) {
	n, ok := m.node(id)
	if !ok || n.Kind != "ssh" {
		return protocol.NodeView{}, protocol.Errorf(protocol.CodeNotFound, "no ssh node %s", id)
	}
	if password == "" {
		return protocol.NodeView{}, protocol.Errorf(protocol.CodeInvalidArgument, "enter the password for %s", n.Address)
	}
	n.Auth = "password"
	m.closeMaster(n) // a stale master would skip the password check
	if err := m.openMaster(ctx, n, password, false); err != nil {
		return view(n, "locked", err.Error(), m.hub), protocol.Errorf(protocol.CodeUnavailable, "%v", err)
	}
	if save {
		if err := savePassword(n.ID, password); err != nil {
			return view(n, "down", err.Error(), m.hub), err
		}
	} else if n.SavedPassword {
		_ = deletePassword(n.ID)
	}
	m.mu.Lock()
	m.pw[n.ID] = password
	m.mu.Unlock()
	if err := m.setLogin(id, "password", n.IdentityFile, save); err != nil {
		return protocol.NodeView{}, err
	}
	return m.Check(ctx, id), nil
}

// ForgetPassword removes a node's saved password; the running connection is kept.
func (m *Manager) ForgetPassword(id string) error {
	n, ok := m.node(id)
	if !ok {
		return protocol.Errorf(protocol.CodeNotFound, "no ssh node %s", id)
	}
	if n.SavedPassword {
		if err := deletePassword(id); err != nil {
			return err
		}
	}
	return m.setLogin(id, n.Auth, n.IdentityFile, false)
}

func (m *Manager) setLogin(id, auth, identity string, saved bool) error {
	return m.st.Update(func(st *store.State) error {
		for i := range st.Nodes {
			if st.Nodes[i].ID == id {
				st.Nodes[i].Auth, st.Nodes[i].IdentityFile, st.Nodes[i].SavedPassword = auth, identity, saved
			}
		}
		return nil
	})
}

// Keys lists the key pairs in ~/.ssh that key setup can use.
func (m *Manager) Keys() []protocol.SSHKey {
	home, _ := os.UserHomeDir()
	pubs, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
	pubs = append(pubs, m.ownKey()+".pub")
	out := []protocol.SSHKey{}
	for _, pub := range pubs {
		k, ok := readKey(pub)
		if ok {
			out = append(out, k)
		}
	}
	return out
}

func (m *Manager) ownKey() string { return filepath.Join(m.st.Dir(), "ssh", "id_ed25519") }

func readKey(pub string) (protocol.SSHKey, bool) {
	priv := strings.TrimSuffix(pub, ".pub")
	if _, err := os.Stat(priv); err != nil {
		return protocol.SSHKey{}, false
	}
	b, err := os.ReadFile(pub)
	if err != nil {
		return protocol.SSHKey{}, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 || !strings.HasPrefix(f[0], "ssh-") && !strings.HasPrefix(f[0], "ecdsa-") && !strings.HasPrefix(f[0], "sk-") {
		return protocol.SSHKey{}, false
	}
	k := protocol.SSHKey{Path: priv, Type: strings.TrimPrefix(f[0], "ssh-")}
	if len(f) > 2 {
		k.Comment = strings.Join(f[2:], " ")
	}
	if fp, err := exec.Command("ssh-keygen", "-l", "-f", pub).Output(); err == nil {
		if g := strings.Fields(string(fp)); len(g) > 1 {
			k.Fingerprint = g[1]
		}
	}
	return k, true
}

// KeySetup moves a password node to key login, doing every step the owner would do by hand: make
// or pick the key, append it to the host's authorized_keys over the logged-in connection, prove
// that the key alone logs in, then switch the node and drop its password. key is the private key
// path, or "" to create PenguPool's own key. Each step reports what it did; the first failure
// stops the run and leaves the node on its password.
func (m *Manager) KeySetup(ctx context.Context, id, key string) ([]protocol.KeySetupStep, error) {
	n, ok := m.node(id)
	if !ok || n.Kind != "ssh" {
		return nil, protocol.Errorf(protocol.CodeNotFound, "no ssh node %s", id)
	}
	var steps []protocol.KeySetupStep
	step := func(name string, err error, detail string) bool {
		s := protocol.KeySetupStep{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			s.Detail = err.Error()
		}
		steps = append(steps, s)
		return err == nil
	}

	// 1. The key.
	var pub []byte
	if key == "" {
		key = m.ownKey()
		detail := "using PenguPool's key " + key
		if _, err := os.Stat(key); err != nil {
			err = createKey(key)
			if !step("Key", err, "created "+key+" (ed25519, used only by PenguPool)") {
				return steps, nil
			}
		} else {
			step("Key", nil, detail)
		}
	} else if _, ok := readKey(key + ".pub"); !ok {
		step("Key", fmt.Errorf("%s has no matching .pub file", key), "")
		return steps, nil
	} else {
		step("Key", nil, "using "+key)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		step("Install", err, "")
		return steps, nil
	}
	line := strings.TrimSpace(string(pub))

	// 2. Install over the password connection.
	if n.Auth == "password" {
		if err := m.ensureMaster(ctx, n); err != nil {
			step("Install", fmt.Errorf("log in with the password first: %v", err), "")
			return steps, nil
		}
	}
	const script = `umask 077; mkdir -p ~/.ssh && touch ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys && IFS= read -r k && if grep -qxF "$k" ~/.ssh/authorized_keys; then echo present; else printf '%s\n' "$k" >> ~/.ssh/authorized_keys && echo added; fi`
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cmd := exec.CommandContext(ictx, "ssh", m.args(n, "sh", "-c", shellQuote(script))...)
	cmd.Stdin = strings.NewReader(line + "\n")
	out, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		step("Install", errors.New(firstLine(string(out), err)), "")
		return steps, nil
	}
	if strings.Contains(string(out), "present") {
		step("Install", nil, "the key was already in ~/.ssh/authorized_keys on "+n.Address)
	} else {
		step("Install", nil, "added the key to ~/.ssh/authorized_keys on "+n.Address)
	}

	// 3. Verify with the key alone: no master, no password.
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	out, err = exec.CommandContext(vctx, "ssh", "-oBatchMode=yes", "-oConnectTimeout=10", "-oControlPath=none",
		"-oPreferredAuthentications=publickey", "-oIdentityFile="+key, "-T", "--", n.Address, "true").CombinedOutput()
	cancel()
	if err != nil {
		msg := firstLine(string(out), err)
		if strings.Contains(msg, "Permission denied") {
			msg += ". If the key has a passphrase, add it to the agent (ssh-add --apple-use-keychain " + key + ") or let PenguPool create its own key"
		}
		step("Verify", errors.New(msg), "")
		return steps, nil
	}
	step("Verify", nil, "logged in with the key alone, no password")

	// 4. Switch the node to the key and drop the password.
	m.forget(n)
	step("Switch", m.setLogin(id, "", key, false), "now logs in with the key; the password is no longer kept")
	return steps, nil
}

func createKey(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	host, _ := os.Hostname()
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "pengupool@"+host, "-f", path).CombinedOutput()
	if err != nil {
		return errors.New(firstLine(string(out), err))
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// servePassword makes a FIFO that hands the password to one reader. done removes it and
// releases a writer still waiting for a reader.
func servePassword(storeDir, password string) (string, func(), error) {
	dir := session.RunDir(storeDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	var r [8]byte
	_, _ = rand.Read(r[:])
	path := filepath.Join(dir, "askpass-"+hex.EncodeToString(r[:]))
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		return "", nil, err
	}
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0) // blocks until a reader opens it
		if err != nil {
			return
		}
		_, _ = io.WriteString(f, password)
		f.Close()
	}()
	done := func() {
		// Release a writer whose reader never came, then remove the name.
		if f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		_ = os.Remove(path)
	}
	return path, done, nil
}

// Askpass is the askpass program: it answers a password prompt from the FIFO and refuses
// anything else (host key questions, one-time codes), so ssh stops instead of guessing.
func Askpass(fifo string, prompt []string) int {
	if !strings.Contains(strings.ToLower(strings.Join(prompt, " ")), "password") {
		fmt.Fprintln(os.Stderr, "pengupool: refusing ssh prompt:", strings.Join(prompt, " "))
		return 1
	}
	got := make(chan []byte, 1)
	go func() {
		b, _ := os.ReadFile(fifo)
		got <- b
	}()
	select {
	case b := <-got:
		if len(b) == 0 {
			return 1
		}
		os.Stdout.Write(append(b, '\n'))
		return 0
	case <-time.After(10 * time.Second):
		return 1
	}
}

// The Keychain holds a password hex-encoded: security(1) prints a non-ASCII password as hex, so a
// plain one could not be told apart on the way back.
const keychainService = "PenguPool SSH"

func savePassword(id, password string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("saving a password needs the macOS Keychain; leave Save off")
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s \"%s\" -a %s -w %s\n", keychainService, id, hex.EncodeToString([]byte(password))))
	if out, err := cmd.CombinedOutput(); err != nil || bytes.Contains(out, []byte("error")) {
		return fmt.Errorf("the Keychain refused the password: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func loadPassword(id string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-a", id, "-w").Output()
	if err != nil {
		return "", errors.New("the saved password is not in the Keychain")
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return "", errors.New("the saved password in the Keychain is unreadable")
	}
	return string(b), nil
}

func deletePassword(id string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	_ = exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", id).Run()
	return nil
}
