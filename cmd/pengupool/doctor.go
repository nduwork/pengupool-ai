package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/client"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// cmdDoctor: `doctor` checks the machine PenguPool runs on and says what to fix. It only reads.
// The checks come from PenguPool 2 (v1 leftovers, the keychain, the agent CLIs), plus what breaks
// this app quietly: a leftover PenguPool MCP server, a Muse plugin that is off or points elsewhere,
// and a host that is doubled, runs from a disk image, or does not answer.
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	home := fs.String("home", "", "the home directory to check (default: yours)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h := *home
	if h == "" {
		var err error
		if h, err = os.UserHomeDir(); err != nil {
			return err
		}
	}
	dir := storeDir(*storeFlag)
	var cs []doctorCheck
	cs = append(cs, v1Checks(h)...)
	cs = append(cs, keychainCheck(os.Getenv))
	cs = append(cs, agentCLIChecks()...)
	cs = append(cs, mcpCheck(h))
	cs = append(cs, museCheck(dir))
	cs = append(cs, hostChecks(dir)...)
	for _, c := range cs {
		mark := "ok  "
		if c.Warn {
			mark = "warn"
		}
		fmt.Printf("%s  %s: %s\n", mark, c.Name, c.Detail)
	}
	return nil
}

type doctorCheck struct {
	Name, Detail string
	Warn         bool
}

// isV1Command: a command of the PenguPool v1 python package (python -m pengupool.context, .routing,
// .statusline, or its oldest pengupool/session_start.sh), which v2 does not use.
func isV1Command(cmd string) bool {
	return strings.Contains(cmd, "pengupool/session_start.sh") || strings.Contains(cmd, "-m pengupool.") ||
		strings.Contains(cmd, "pengupool.context") || strings.Contains(cmd, "pengupool.routing") || strings.Contains(cmd, "pengupool.statusline")
}

// v1Checks: v1 installed, and v1 hooks or status line left in Claude's settings (they run in every
// Claude session).
func v1Checks(home string) []doctorCheck {
	var cs []doctorCheck
	if out, err := runShort("uv", "tool", "list"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "pengupool ") {
				cs = append(cs, doctorCheck{"PenguPool v1", "installed (" + strings.TrimSpace(l) + "); remove it with `uv tool uninstall pengupool`", true})
			}
		}
	}
	clean := true
	for _, name := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(home, ".claude", name)
		if raw, err := os.ReadFile(path); err == nil && isV1Command(string(raw)) {
			clean = false
			cs = append(cs, doctorCheck{"Claude settings", path + " holds PenguPool v1 hooks or status line (they run in every Claude session); remove them", true})
		}
	}
	if clean {
		cs = append(cs, doctorCheck{"Claude settings", "no PenguPool v1 entries", false})
	}
	return cs
}

func keychainCheck(getenv func(string) string) doctorCheck {
	if getenv("SSH_CONNECTION") != "" {
		return doctorCheck{"Keychain", "this shell is an SSH login: a daemon started from it cannot read the decision model's key from the macOS keychain (security exit 36). Start PenguPool from the desktop app", true}
	}
	return doctorCheck{"Keychain", "this shell is not an SSH login (the running daemon's own environment is not checked)", false}
}

func agentCLIChecks() []doctorCheck {
	var cs []doctorCheck
	for _, c := range []string{"claude", "muse", "codex", "pi", "omp", "ollama"} {
		path, err := exec.LookPath(c)
		if err != nil {
			cs = append(cs, doctorCheck{c, "not installed (only needed to run agents on it)", false})
			continue
		}
		v, err := runShort(path, "--version")
		if err != nil {
			cs = append(cs, doctorCheck{c, path + ": --version failed: " + err.Error(), true})
			continue
		}
		cs = append(cs, doctorCheck{c, strings.TrimSpace(strings.SplitN(v, "\n", 2)[0]) + " (" + path + ")", false})
	}
	return cs
}

// mcpCheck finds PenguPool MCP servers registered with Claude or Muse. PenguPool AI uses none, but
// PenguPool 2 may use its own, so only a dead one (its command is gone) warns: it fails at every
// session start.
func mcpCheck(home string) doctorCheck {
	var dead, live []string
	scan := func(file string, servers map[string]json.RawMessage, where string) {
		for name, def := range servers {
			if !strings.Contains(strings.ToLower(name+string(def)), "pengu") {
				continue
			}
			var d struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(def, &d)
			entry := fmt.Sprintf("%q in %s%s", name, file, where)
			if _, err := exec.LookPath(d.Command); d.Command != "" && err != nil {
				dead = append(dead, entry+" calls "+d.Command+", which is gone")
			} else {
				live = append(live, entry)
			}
		}
	}
	var claude struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	f := filepath.Join(home, ".claude.json")
	if raw, err := os.ReadFile(f); err == nil && json.Unmarshal(raw, &claude) == nil {
		scan(f, claude.MCPServers, "")
		for p, v := range claude.Projects {
			scan(f, v.MCPServers, " (project "+p+")")
		}
	}
	var muse struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	f = filepath.Join(home, ".config", "muse", "settings.json")
	if raw, err := os.ReadFile(f); err == nil && json.Unmarshal(raw, &muse) == nil {
		scan(f, muse.MCPServers, "")
	}
	slices.Sort(dead)
	slices.Sort(live)
	switch {
	case len(dead) > 0:
		return doctorCheck{"MCP", "dead PenguPool MCP servers, remove them: " + strings.Join(dead, "; "), true}
	case len(live) > 0:
		return doctorCheck{"MCP", "PenguPool AI uses no MCP server; these are registered (PenguPool 2 may use them): " + strings.Join(live, "; "), false}
	}
	return doctorCheck{"MCP", "no PenguPool MCP server registered", false}
}

// museCheck: the PenguPool plugin Muse holds must be on and call a pengupool that exists, with this
// store. A test install elsewhere replaces it, and the app's own install skips when its marker
// matches, so the two can disagree.
func museCheck(dir string) doctorCheck {
	bin, err := exec.LookPath("muse")
	if err != nil {
		return doctorCheck{"Muse plugin", "muse is not installed", false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "plugins", "list", "--json").Output() // stdout only: the JSON
	if err != nil {
		return doctorCheck{"Muse plugin", "`muse plugins list` failed: " + err.Error(), true}
	}
	return museVerdict(out, dir, store.IsHomeDefault(dir))
}

func museVerdict(listJSON []byte, dir string, homeStore bool) doctorCheck {
	var list struct {
		Plugins []struct {
			Record struct {
				ID      string `json:"id"`
				Enabled bool   `json:"enabled"`
			} `json:"record"`
			Active bool `json:"active"`
			Plugin struct {
				Capabilities struct {
					Hooks []struct {
						Command []string `json:"command"`
					} `json:"hooks"`
				} `json:"capabilities"`
			} `json:"plugin"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(listJSON, &list); err != nil {
		return doctorCheck{"Muse plugin", "cannot read `muse plugins list --json`: " + err.Error(), true}
	}
	const fix = "; open PenguPool AI from Applications, which reinstalls it"
	for _, p := range list.Plugins {
		if p.Record.ID != "pengupool" {
			continue
		}
		if !p.Record.Enabled || !p.Active {
			return doctorCheck{"Muse plugin", "installed but not active: Muse sessions get no team context or Compass" + fix, true}
		}
		for _, h := range p.Plugin.Capabilities.Hooks {
			if len(h.Command) == 0 {
				continue
			}
			if _, err := exec.LookPath(h.Command[0]); err != nil {
				return doctorCheck{"Muse plugin", "its hooks call " + h.Command[0] + ", which is gone" + fix, true}
			}
			if i := slices.Index(h.Command, "--store"); homeStore && slices.Contains(h.Command, "hook") && (i < 0 || i+1 >= len(h.Command) || filepath.Clean(h.Command[i+1]) != filepath.Clean(dir)) {
				return doctorCheck{"Muse plugin", "its hooks report to another store (" + strings.Join(h.Command, " ") + ")" + fix, true}
			}
		}
		return doctorCheck{"Muse plugin", "installed and active", false}
	}
	if homeStore {
		return doctorCheck{"Muse plugin", "not installed: Muse sessions get no team context or Compass" + fix, true}
	}
	return doctorCheck{"Muse plugin", "not installed (only the app's own store installs it)", false}
}

// hostChecks: one host on this store, run from an installed app, answering. A host left running
// from an ejected disk image holds the store and answers nothing.
func hostChecks(dir string) []doctorCheck {
	out, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	lockPid := 0
	if b, err := os.ReadFile(filepath.Join(dir, store.LockFile)); err == nil {
		lockPid, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]))
	}
	return hostVerdict(string(out), dir, store.DefaultDir(), lockPid, answers(dir))
}

func hostVerdict(ps, dir, defaultDir string, lockPid int, answered bool) []doctorCheck {
	type proc struct {
		pid int
		cmd string
	}
	var hosts []proc
	for _, l := range strings.Split(ps, "\n") {
		pidText, cmd, ok := strings.Cut(strings.TrimSpace(l), " ")
		cmd = strings.TrimSpace(cmd)
		bin, rest, _ := strings.Cut(cmd, " daemon")
		pid, err := strconv.Atoi(pidText)
		// The binary itself, not a shell or editor quoting it (as desktop/src/hostversion.ts isStoreDaemon).
		if !ok || err != nil || filepath.Base(bin) != "pengupool" || strings.ContainsAny(bin, `'"`) || (rest != "" && rest[0] != ' ') {
			continue
		}
		s := defaultDir // a host started with PENGUPOOL_HOME and no --store is taken to use doctor's own default
		if _, v, has := strings.Cut(strings.Replace(rest, " --store=", " --store ", 1), " --store "); has {
			s, _, _ = strings.Cut(v, " --") // the path may hold spaces; a later flag ends it
			s = strings.TrimSpace(s)
		}
		if filepath.Clean(s) == filepath.Clean(dir) {
			hosts = append(hosts, proc{pid, cmd})
		}
	}
	if len(hosts) == 0 {
		return []doctorCheck{{"Host", "not running on " + dir + " (the app starts it)", false}}
	}
	// The pid to name is the lock's only when it is one of these hosts: the lock file can be missing
	// (pid 0) or stale, and a reused pid is someone else's process.
	holder := 0
	if slices.ContainsFunc(hosts, func(h proc) bool { return h.pid == lockPid }) {
		holder = lockPid
	} else if len(hosts) == 1 {
		holder = hosts[0].pid
	}
	var cs []doctorCheck
	if len(hosts) > 1 {
		var pids []string
		for _, h := range hosts {
			pids = append(pids, strconv.Itoa(h.pid))
		}
		held := "none of them holds the lock"
		if holder != 0 {
			held = fmt.Sprintf("only pid %d holds it", holder)
		}
		cs = append(cs, doctorCheck{"Host", fmt.Sprintf("%d hosts on %s (pids %s); %s. Quit PenguPool AI, stop them, and open it again", len(hosts), dir, strings.Join(pids, ", "), held), true})
	}
	for _, h := range hosts {
		if strings.HasPrefix(h.cmd, "/Volumes/") && strings.Contains(h.cmd, ".app/Contents/") {
			cs = append(cs, doctorCheck{"Host", fmt.Sprintf("pid %d runs from a disk image (%s); install the app into Applications and open it from there", h.pid, h.cmd), true})
		}
	}
	if !answered && holder != 0 {
		cs = append(cs, doctorCheck{"Host", fmt.Sprintf("pid %d holds %s but does not answer; `kill -9 %d`, then open PenguPool AI", holder, dir, holder), true})
	} else if !answered {
		cs = append(cs, doctorCheck{"Host", "the host on " + dir + " does not answer; quit PenguPool AI, stop the hosts above, and open it again", true})
	}
	if len(cs) == 0 {
		cs = append(cs, doctorCheck{"Host", fmt.Sprintf("pid %d on %s answers", hosts[0].pid, dir), false})
	}
	return cs
}

// answers reports whether the store's host completes a hello within 3 s.
func answers(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, hubSocket(dir), protocol.KindCLI, "pengupool doctor")
	if err != nil {
		return false
	}
	defer c.Close()
	_, err = c.Call(ctx, c.HelloMessage())
	return err == nil
}

func runShort(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}
