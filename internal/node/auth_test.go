package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The test binary doubles as the askpass program, as the daemon's executable does.
func TestMain(m *testing.M) {
	if fifo := os.Getenv(AskpassEnv); fifo != "" {
		os.Exit(Askpass(fifo, os.Args[1:]))
	}
	os.Exit(m.Run())
}

const testPassword = `p"a ss\wörd`

// fakePenguPool answers version and the claim; ls succeeds once daemon has run, so every probe
// goes through starting the daemon.
const fakePenguPool = "#!/bin/sh\ncase \"$1\" in version) echo 'pengupool test (protocol v1)';; bridge) [ \"$2\" = --claim ] || exit 1;; ls) [ -f \"$HOME/.daemon\" ];; daemon) touch \"$HOME/.daemon\";; web) cat \"$HOME/.webaddr\";; workspace) printf '200\\n{\"route\":\"%s\",\"cwd\":\"%s\",\"q\":\"%s\"}' \"$2\" \"$4\" \"$5\";; *) echo \"pengupool: unknown command \\\"$1\\\"\" >&2; exit 2;; esac\n"

// sshHost runs an SSH server that takes testPassword or a key in its home's authorized_keys, and
// runs commands with sh in that home, where a fake pengupool-ai answers version and the claim. The
// real ssh client reaches it as "testhost" through a wrapper that adds a private ssh config, so
// the user's own config and known_hosts are never read or written.
func sshHost(t *testing.T) (home string) {
	return sshHostWith(t, true)
}

func sshHostWith(t *testing.T, installed bool) (home string) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client")
	}
	dir := t.TempDir()
	home = filepath.Join(dir, "home")
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{home, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Installed where make install puts it, which a non-login shell's PATH does not include.
	if installed {
		if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(home, ".local", "bin", "pengupool-ai"), fakePenguPool)
	}

	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == testPassword {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			b, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
			for len(b) > 0 {
				k, _, _, rest, err := ssh.ParseAuthorizedKey(b)
				if err != nil {
					break
				}
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return nil, nil
				}
				b = rest
			}
			return nil, errors.New("unknown key")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(c, cfg, home, bin)
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	conf := filepath.Join(dir, "ssh_config")
	write(conf, fmt.Sprintf("Host testhost\n  HostName 127.0.0.1\n  Port %d\n  User tester\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n  IdentityAgent none\n  IdentitiesOnly yes\n", port, filepath.Join(dir, "known_hosts")))
	real, _ := exec.LookPath("ssh")
	wrap := filepath.Join(dir, "client")
	if err := os.MkdirAll(wrap, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(wrap, "ssh"), fmt.Sprintf("#!/bin/sh\nexec %s -F %s \"$@\"\n", real, conf))
	t.Setenv("PATH", wrap+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}

func serveSSH(c net.Conn, cfg *ssh.ServerConfig, home, bin string) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() == "direct-tcpip" {
			go forwardTCP(nc)
			continue
		}
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, in, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range in {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var p struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &p)
				_ = req.Reply(true, nil)
				cmd := exec.Command("sh", "-c", p.Command)
				cmd.Dir = home
				cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
				cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
				code := 0
				if err := cmd.Run(); err != nil {
					code = 1
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						code = ee.ExitCode()
					}
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}

// forwardTCP serves a client's -L forward: dial the target and pipe both ways.
func forwardTCP(nc ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if ssh.Unmarshal(nc.ExtraData(), &p) != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	c, err := net.Dial("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)))
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { _, _ = io.Copy(ch, c); ch.Close() }()
	_, _ = io.Copy(c, ch)
	c.Close()
}

func testManager(t *testing.T, ctx context.Context) *Manager {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A short store path, so the master socket stays under the Unix socket limit.
	dir, err := os.MkdirTemp("", "ppn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, exe, func(string, ...any) {})
	t.Cleanup(func() {
		for _, n := range st.State().Nodes {
			if n.Kind == "ssh" {
				m.closeMaster(n)
			}
		}
	})
	return m
}

func TestPasswordLoginAndKeySetup(t *testing.T) {
	home := sshHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := testManager(t, ctx)

	if _, err := m.Add(ctx, protocol.NodeAdd{Address: "testhost", Auth: "password", Password: "nope", TrustHostKey: true}); err == nil {
		t.Fatal("a wrong password added the host")
	}
	if len(m.st.State().Nodes) != 1 {
		t.Fatal("a failed add left a node behind")
	}

	v, err := m.Add(ctx, protocol.NodeAdd{Name: "box", Address: "testhost", Auth: "password", Password: testPassword, TrustHostKey: true})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if v.Status != "up" || v.Auth != "password" || v.SavedPassword {
		t.Fatalf("add view = %+v", v)
	}
	n, _ := m.node(v.ID)
	if n.Command != filepath.Join(home, ".local", "bin", "pengupool-ai") {
		t.Fatalf("remote command = %q", n.Command)
	}
	if !m.masterUp(ctx, n) {
		t.Fatal("no master connection after a password login")
	}

	// A fresh daemon with the master gone and nothing saved is locked, not retrying.
	m.closeMaster(n)
	m2 := New(m.st, m.exe, func(string, ...any) {})
	if got := m2.Check(ctx, v.ID); got.Status != "locked" {
		t.Fatalf("after restart without a password: %+v", got)
	}
	if _, err := m2.Unlock(ctx, v.ID, "nope", false); err == nil {
		t.Fatal("unlock took a wrong password")
	}
	if got, err := m2.Unlock(ctx, v.ID, testPassword, false); err != nil || got.Status != "up" {
		t.Fatalf("unlock: %+v %v", got, err)
	}

	steps, err := m2.KeySetup(ctx, v.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if !s.OK {
			t.Fatalf("key setup step %s failed: %s", s.Name, s.Detail)
		}
	}
	if len(steps) != 4 {
		t.Fatalf("steps = %+v", steps)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if !strings.Contains(string(b), "pengupool@") {
		t.Fatalf("authorized_keys = %q", b)
	}
	n, _ = m2.node(v.ID)
	if n.Auth != "" || n.IdentityFile != m2.ownKey() || n.SavedPassword {
		t.Fatalf("node after key setup = %+v", n)
	}
	if got := m2.Check(ctx, v.ID); got.Status != "up" || got.Auth != "key" {
		t.Fatalf("key login: %+v", got)
	}
	// Running it again finds the key already installed.
	steps, _ = m2.KeySetup(ctx, v.ID, "")
	if len(steps) < 2 || !strings.Contains(steps[1].Detail, "already") {
		t.Fatalf("second key setup = %+v", steps)
	}
}

func TestAskpassRefusesOtherPrompts(t *testing.T) {
	if Askpass("/nonexistent", []string{"Are you sure you want to continue connecting (yes/no)?"}) == 0 {
		t.Fatal("answered a host key question")
	}
}

func TestServePasswordOnce(t *testing.T) {
	fifo, done, err := servePassword(t.TempDir(), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	f, err := os.Open(fifo)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != testPassword {
		t.Fatalf("read %q", b)
	}
}

// Uses the real Keychain, so it runs only when asked: PENGUPOOL_KEYCHAIN_TEST=1.
func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("PENGUPOOL_KEYCHAIN_TEST") == "" {
		t.Skip("set PENGUPOOL_KEYCHAIN_TEST=1 to use the Keychain")
	}
	id := "test-" + protocol.NewID()
	if err := savePassword(id, testPassword); err != nil {
		t.Fatal(err)
	}
	defer deletePassword(id)
	if got, err := loadPassword(id); err != nil || got != testPassword {
		t.Fatalf("load = %q %v", got, err)
	}
	_ = deletePassword(id)
	if _, err := loadPassword(id); err == nil {
		t.Fatal("deleted password still loads")
	}
}

// remoteHost serves the host's HTTP side for Connect: healthz answers only the token.
// writeBin is the host binary the manager installs on the fake remote host. The fake host runs this
// machine's own uname, so on Linux (CI) it is the bundled Linux executable that gets installed.
func writeBin(t *testing.T, m *Manager, content string) {
	t.Helper()
	dir := t.TempDir()
	m.bin = filepath.Join(dir, "pengupool")
	paths := []string{m.bin}
	if runtime.GOOS == "linux" {
		arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
		paths = append(paths, filepath.Join(dir, "linux", "pengupool-"+arch))
		if err := os.MkdirAll(filepath.Join(dir, "linux"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func remoteHost(t *testing.T, home string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/healthz" || r.Header.Get("X-PenguPool-Token") != "tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"owner":true}`)
	}))
	t.Cleanup(srv.Close)
	if err := os.WriteFile(filepath.Join(home, ".webaddr"), []byte(srv.URL+"/#token=tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConnectInstallsStartsAndForwards(t *testing.T) {
	home := sshHostWith(t, false)
	remoteHost(t, home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := testManager(t, ctx)
	m.Version = "pengupool test (protocol v1)"
	writeBin(t, m, fakePenguPool)
	v, err := m.Add(ctx, protocol.NodeAdd{Address: "testhost", Auth: "password", Password: testPassword, TrustHostKey: true})
	if err != nil || v.Status != "not_installed" {
		t.Fatalf("add without PenguPool: %+v %v", v, err)
	}
	c, err := m.Connect(ctx, v.ID)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if c.Token != "tok" || !strings.HasPrefix(c.URL, "http://127.0.0.1:") {
		t.Fatalf("connection = %+v", c)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".local", "bin", "pengupool-ai")); err != nil || string(b) != fakePenguPool {
		t.Fatalf("installed = %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".daemon")); err != nil {
		t.Fatal("connect did not start the host's daemon")
	}
	// The forward reaches the host's HTTP side.
	req, _ := http.NewRequest(http.MethodGet, c.URL+"/api/healthz", nil)
	req.Header.Set("X-PenguPool-Token", c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("through the forward: %v %v", res, err)
	}
	res.Body.Close()
	// Connecting again reuses the same forward.
	if c2, err := m.Connect(ctx, v.ID); err != nil || c2.URL != c.URL {
		t.Fatalf("reconnect = %+v %v", c2, err)
	}
}

func TestConnectUpdatesOutdatedHost(t *testing.T) {
	home := sshHost(t)
	remoteHost(t, home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := testManager(t, ctx)
	m.Version = "pengupool newer (protocol v1)"
	newer := strings.Replace(fakePenguPool, "pengupool test", "pengupool newer", 1)
	writeBin(t, m, newer)
	// A key host: its master connection is opened for the forward.
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "id")
	if err := createKey(key); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(key + ".pub")
	if err := os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	// Added with a password, which records the host key, then moved to the key.
	v, err := m.Add(ctx, protocol.NodeAdd{Address: "testhost", Auth: "password", Password: testPassword, TrustHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := m.node(v.ID)
	m.forget(n)
	if err := m.setLogin(v.ID, "", key, false); err != nil {
		t.Fatal(err)
	}
	if got := m.Check(ctx, v.ID); !got.Outdated {
		t.Fatalf("check = %+v", got)
	}
	if _, err := m.Connect(ctx, v.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".local", "bin", "pengupool-ai")); string(b) != newer {
		t.Fatal("the outdated PenguPool was not replaced")
	}
	if got := m.Check(ctx, v.ID); got.Outdated || got.Status != "up" {
		t.Fatalf("after connect = %+v", got)
	}
}

// An older app never downgrades a host that runs a newer release; it is told to update itself.
func TestOlderAppNeverDowngradesHost(t *testing.T) {
	for _, c := range []struct {
		host, app string
		outdated  bool
		status    string
	}{
		{"pengupool 0.3.0+5a6ed69 (protocol v1)", "pengupool 0.6.1+e623aa6 (protocol v1)", true, "up"},
		{"pengupool 0.6.1+e623aa6 (protocol v1)", "pengupool 0.3.0+5a6ed69 (protocol v1)", false, "newer"},
		{"pengupool 0.6.1+aaaaaaa (protocol v1)", "pengupool 0.6.1+bbbbbbb (protocol v1)", false, "up"},
		{"pengupool test (protocol v1)", "pengupool newer (protocol v1)", true, "up"},
	} {
		outdated, newer := versusApp(c.host, c.app)
		status := "up"
		if newer {
			status = "newer"
		}
		if outdated != c.outdated || status != c.status {
			t.Errorf("host %q, app %q: outdated %v status %s", c.host, c.app, outdated, status)
		}
	}
}
