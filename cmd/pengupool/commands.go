package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/node"
	"github.com/nduwork/pengupool-ai/internal/pool"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
	"github.com/nduwork/pengupool-ai/internal/webapp"
)

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pengupool: "+format+"\n", args...)
}

// hostVersionFile, under the store directory, is the version line the running daemon wrote at
// startup: which build serves the store, for the owner and `pengupool doctor` to read.
const hostVersionFile = "run/host.version"

// writeHostVersion records the running host's build for the desktop app.
func writeHostVersion(dir, line string) error {
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, hostVersionFile), []byte(line+"\n"), 0o600)
}

// handoverFile marks sessions an app-owned host left running for the next host: the app quit to
// install an update. handoverTTL bounds it: an update that failed and was never reopened leaves
// workers the next run ends and offers to restore, as after a crash.
const (
	handoverFile = "run/handover"
	handoverTTL  = 10 * time.Minute
)

// tookOver reports, and consumes, a recent hand-over to this host.
func tookOver(dir string) bool {
	path := filepath.Join(dir, handoverFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_ = os.Remove(path)
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	return err == nil && time.Since(at) < handoverTTL
}

// runningIDs are the sessions the record says are running.
func runningIDs(st *store.Store) []string {
	var ids []string
	for _, s := range st.State().Sessions {
		if s.State == "running" {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// hubSessions adapts *hub.Hub to the gateway's session interface. The adapter lives here, in the
// one package that is allowed to know about both, which keeps the two packages independent.
type hubSessions struct {
	h *hub.Hub
}

func (a hubSessions) Dial(ctx context.Context, id string) (net.Conn, error) {
	return a.h.Dial(ctx, id)
}

func (a hubSessions) Create(ctx context.Context, req gateway.CreateRequest) (protocol.SessionView, error) {
	s, err := a.h.Spawn(ctx, hub.SpawnSpec{
		Name:    req.Name,
		Cwd:     req.Cwd,
		Harness: req.Harness,
		Command: req.Command,
		Cols:    req.Cols,
		Rows:    req.Rows,
	})
	if err != nil {
		return protocol.SessionView{}, err
	}
	return s.View(), nil
}

func (a hubSessions) Kill(ctx context.Context, id string) error {
	return a.h.Kill(ctx, id)
}

func readConfig(dir string) (store.Config, error) {
	var c store.Config
	b, err := os.ReadFile(filepath.Join(dir, store.ConfigFile))
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// cmdDaemon runs the host: state ownership, worker supervision and the browser app.
func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	httpAddr := fs.String("http", "127.0.0.1:7787", "HTTP address for the browser app")
	lan := fs.Bool("lan", false, "listen on the LAN, not only loopback")
	ownerPID := fs.Int("owner-pid", 0, "the desktop app run this host belongs to: it ends, with its sessions, when that process does")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr := *httpAddr
	dir := storeDir(*storeFlag)
	if cfg, err := readConfig(dir); err == nil && cfg.Network.LAN {
		*lan = true
		if cfg.Network.LANAddr != "" {
			addr = cfg.Network.LANAddr
		}
	}
	if *lan {
		if host, port, err := net.SplitHostPort(addr); err == nil && (host == "127.0.0.1" || host == "localhost" || host == "") {
			addr = net.JoinHostPort("0.0.0.0", port)
		}
	}

	// The default port may belong to another store's host (or anything else): fall back to a free
	// loopback port. The bound address is published in run/hub.addr, so clients still find it.
	if !explicitHTTP(fs) && !*lan {
		if l, err := net.Listen("tcp", addr); err != nil {
			logf("%s is in use; using a free port", addr)
			addr = "127.0.0.1:0"
		} else {
			l.Close()
		}
	}

	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	// One daemon per store: a second one would fight for state.json, the socket and the token.
	lock, err := store.Acquire(dir)
	if err != nil {
		return err
	}
	defer lock.Release()
	// Which build serves the store, next to the address and the token.
	if err := writeHostVersion(dir, versionLine()); err != nil {
		logf("host version: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// An app-owned host lives as long as the app's run: when the app quits, crashes or is killed,
	// the host ends and takes its sessions with it, so no host outlives the build that started it.
	if *ownerPID > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			for ctx.Err() == nil {
				if syscall.Kill(*ownerPID, 0) == syscall.ESRCH {
					logf("the app (pid %d) is gone; ending this run", *ownerPID)
					cancel()
					return
				}
				time.Sleep(time.Second)
			}
		}()
	}
	// The sessions recorded running when this host starts are from a run that ended without its
	// shutdown (a crash, power loss): an app-owned host offers them to the owner to restore.
	wasRunning := runningIDs(st)
	// A restart to apply listener settings (pool.Restarter), or a host the app handed its sessions to
	// before installing an update (handOver), continues the same run: adopt its sessions.
	// A host started by hand (or before 0.9.5) meant its sessions to outlive it: the first app run
	// after it adopts them. run/owned says the last host was an app run's.
	ownedMark := filepath.Join(dir, "run", "owned")
	_, err = os.Stat(ownedMark)
	afterStandalone := os.IsNotExist(err)
	if *ownerPID > 0 {
		_ = os.WriteFile(ownedMark, nil, 0o600)
	} else {
		_ = os.Remove(ownedMark)
	}
	tookOver := tookOver(dir) // consumes the marker
	sameRun := tookOver || os.Getenv("PENGUPOOL_SAME_RUN") == "1" || afterStandalone
	os.Unsetenv("PENGUPOOL_SAME_RUN")
	var handover atomic.Bool
	if *ownerPID > 0 {
		usr1 := make(chan os.Signal, 1)
		signal.Notify(usr1, syscall.SIGUSR1)
		go func() {
			if _, ok := <-usr1; ok {
				logf("handing the sessions over to the next host (an update); they keep running")
				if err := os.WriteFile(filepath.Join(dir, handoverFile), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
					logf("hand over: %v; the sessions end with this run", err)
				} else {
					handover.Store(true)
				}
				stop()
			}
		}()
	}

	h, err := hub.New(hub.Options{Store: st, Executable: selfPath(), Logf: logf})
	if err != nil {
		return err
	}
	if err := h.Start(ctx); err != nil {
		return err
	}
	defer h.Close()

	nodes := node.New(st, selfPath(), logf)
	nodes.Version = versionLine()
	pl := pool.New(pool.Options{Store: st, Hub: h, Exe: selfPath(), Logf: logf, Nodes: nodes, Version: Version, RequireOwnerInput: true})
	if *ownerPID > 0 && !sameRun {
		pl.MarkRestore(wasRunning)
		pl.EndRun(ctx) // workers a killed host left behind belong to no run now
	}
	if *ownerPID > 0 {
		defer func() {
			if handover.Load() {
				return // the workers keep running for the next host to adopt
			}
			// The run is over: end its sessions, recorded for the next run's question.
			ectx, ecancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ecancel()
			pl.EndRun(ectx)
		}()
	}
	go pl.Run(ctx)
	srv, err := gateway.New(gateway.Options{
		SocketPath: hubSocket(dir),
		HTTPAddr:   addr,
		Store:      st,
		Sessions:   hubSessions{h: h},
		Static:     staticFS(),
		Logf:       logf,
		RPC:        pl.RPC,
		Filter:     pl.Filter,
		OnClose:    pl.ConnectionClosed,
		Authorize:  pl.Authorize,
		OwnerInput: pl.OwnerInput,
		InSession:  pl.InAgentSession,
		PhoneHosts: func() []string {
			ts := st.Config().Network.Tailscale
			ts = strings.TrimPrefix(strings.TrimPrefix(ts, "https://"), "http://")
			return []string{strings.TrimSuffix(ts, "/")}
		},
	})
	if err != nil {
		return err
	}
	pl.SetRevoker(srv.Revoke)
	pool.HTTPAddr = srv.HTTPAddr
	go pl.HealTailscaleServe() // re-serve after a port change; owner's home only
	pool.Restarter = func() {
		logf("restarting to apply listener settings; sessions keep running")
		lock.Release()
		if err := syscall.Exec(selfPath(), os.Args, append(os.Environ(), "PENGUPOOL_SAME_RUN=1")); err != nil {
			logf("restart failed: %v", err)
			os.Exit(1)
		}
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	go func() {
		for range 200 {
			if bound := srv.HTTPAddr(); bound != "" {
				os.WriteFile(filepath.Join(dir, "run", "hub.addr"), []byte(bound+"\n"), 0o600)
				logf("host listening on %s", bound)
				logf("unix socket %s", srv.SocketPath())
				logf("browser app: %s", browserURL(dir, bound))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		logf("warning: HTTP listener did not report a bound address")
	}()

	select {
	case <-ctx.Done():
		logf("shutting down; sessions keep running")
		return nil
	case err := <-serveErr:
		return err
	}
}

// browserURL builds the app URL with the bootstrap token. The app takes the token from the URL
// fragment and sends it on the WebSocket; the fragment is never sent to a server as a header.
func browserURL(dir, addr string) string {
	tok, err := os.ReadFile(tokenPath(dir))
	if err != nil {
		return "http://" + addr + "/"
	}
	return "http://" + addr + "/#token=" + string(trimSpace(tok))
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// staticFS is the built browser app rooted at its bundle directory, so the gateway can serve it at
// "/" instead of exposing the embed's dist/ prefix.
func staticFS() fs.FS {
	dist, err := webapp.Dist()
	if err != nil {
		logf("warning: the embedded browser app is missing: %v", err)
		return nil
	}
	return dist
}

// cmdWorker runs one PTY worker. It is started by the hub with the launch spec on fd 3.
func cmdWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "session id (the spec on fd 3 wins)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := session.WorkerConfig{SpecFD: 3, Logf: logf}
	cfg.Spec.SessionID = *sessionID
	return session.RunWorker(ctx, cfg)
}

// cmdNew starts a session and prints its id.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	name := fs.String("name", "", "session name (default: <folder>-<harness>)")
	cwd := fs.String("cwd", "", "working directory (default: the current directory)")
	harnessFlag := fs.String("harness", "claude", "agent runtime: claude, muse, omp, pi, codex or copilot")
	model := fs.String("model", "", "model (default: the harness default)")
	raw := fs.Bool("raw", false, "tests only: run the command after -- as a plain, non-agent session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *raw {
		return rawNew(*storeFlag, *name, *cwd, fs.Args())
	}
	// One folder argument is the working directory; anything else is a command, which v2 refuses.
	rest := fs.Args()
	if len(rest) == 1 && *cwd == "" {
		if fi, err := os.Stat(rest[0]); err == nil && fi.IsDir() {
			*cwd, _ = filepath.Abs(rest[0])
			rest = nil
		}
	}
	if len(rest) > 0 {
		return fmt.Errorf("a PenguPool session runs an agent (--harness claude|muse|omp|pi|codex|copilot), not a command")
	}
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		*cwd = wd
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := rpc(ctx, storeDir(*storeFlag), "session.launch", map[string]any{
		"name": *name, "harness": *harnessFlag, "model": *model,
		"source": map[string]string{"kind": "local", "path": *cwd},
	})
	if err != nil {
		return err
	}
	var s protocol.SessionView
	if err := json.Unmarshal(res, &s); err != nil {
		return err
	}
	fmt.Printf("%s\t%s\t%s\n", s.ID, s.Name, s.Cwd)
	return nil
}

// rawNew starts a plain command through session.create; the smoke test needs a deterministic
// process. The app never offers it: PenguPool sessions are agents.
func rawNew(store, name, cwd string, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("--raw needs a command after --")
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := dialDaemon(ctx, storeDir(store), protocol.KindCLI, "pengupool new")
	if err != nil {
		return err
	}
	defer conn.Close()
	reply, err := conn.Call(ctx, &protocol.Message{Type: protocol.TypeSessionCreate, Name: name, Cwd: cwd, Command: command, Cols: 80, Rows: 24})
	if err != nil {
		return err
	}
	if reply.Type == protocol.TypeError || len(reply.Sessions) != 1 {
		return fmt.Errorf("create refused: %s: %s", reply.Code, reply.Message)
	}
	s := reply.Sessions[0]
	fmt.Printf("%s\t%s\t%s\n", s.ID, s.Name, s.Cwd)
	return nil
}

// cmdList prints the sessions the host knows, newest last.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	asJSON := fs.Bool("json", false, "print the protocol session records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dialDaemon(ctx, storeDir(*storeFlag), protocol.KindCLI, "pengupool ls")
	if err != nil {
		return err
	}
	defer c.Close()
	all, err := listSessions(ctx, c)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(all)
	}
	if len(all) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	fmt.Printf("%-8s %-12s %-10s %-9s %5s %7s  %s\n", "ID", "NAME", "STATE", "ACTIVITY", "PID", "SIZE", "CWD")
	for _, s := range all {
		fmt.Println(describe(s))
	}
	return nil
}

// cmdKill stops a session and its worker.
func cmdKill(args []string) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := fs.Arg(0)
	if target == "" {
		return errors.New("usage: pengupool kill <session>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := dialDaemon(ctx, storeDir(*storeFlag), protocol.KindCLI, "pengupool kill")
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := resolveSession(ctx, c, target)
	if err != nil {
		return err
	}
	if _, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionKill, SessionID: s.ID}); err != nil {
		return err
	}
	fmt.Printf("stopped %s (%s)\n", shortID(s.ID), s.Name)
	return nil
}

// cmdWeb prints the browser app URL of the running host.
func cmdWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := storeDir(*storeFlag)
	addr, err := os.ReadFile(filepath.Join(dir, "run", "hub.addr"))
	if err != nil {
		return fmt.Errorf("the host has not published an address (%v); start it with: pengupool daemon", err)
	}
	fmt.Println(browserURL(dir, string(trimSpace(addr))))
	return nil
}

// explicitHTTP reports whether --http was given on the command line.
func explicitHTTP(fs *flag.FlagSet) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "http" })
	return set
}
