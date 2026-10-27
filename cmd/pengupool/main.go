// Command pengupool is the PenguPool 2.0 host: the daemon that owns state and the browser app, the
// attach client for a classic terminal, and the worker that owns one PTY.
//
// This is the M1 prototype. See docs/protocol/v1.md for the wire protocol and
// docs/design/m1-contract.md for what exists and what does not.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/node"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Version of the prototype. It is not the protocol version; that is protocol.Version.
const Version = "0.3.0"

const help = `usage: pengupool <command> [options]

  daemon [--store DIR] [--http ADDR] [--lan]
        run the host: state, sessions, browser app. Loopback only unless --lan.
  new [--name N] [--cwd DIR] [--harness claude|muse|omp|pi|codex] [--model M]
        start an agent session (Claude Code by default) in that directory.
  ls [--json]
        list sessions.
  attach <session> [--no-claim] [--detach-key ctrl-\\|none]
        attach this terminal to a session, raw, no status bar.
  kill <session>
        stop a session and its worker.
  web
        print the browser app URL for the running daemon.
  send --to MEMBER TEXT
        message another member of this session's team (from inside a team session).
  search WORDS
        (inside an agent session) search the owner's compass and a helper's own journal entries.
        "memory search" is the same, under its old name.
  experience search WORDS | experience show ID
        (inside a helper session) list its own journal entries that match words (id, date, title,
        a snippet), or print one in full. An entry is kept when the helper tells its parent
        "Done: <title>".
  team request add --skill S|--repo R [--assignment A] | remove MEMBER | anchor MEMBER [--reason R]
        (inside a team session) ask the owner to change who is on the team; it waits for approval.
  compass word [--supports UID]... [--contradicts UID]... [--line "preference|restriction :: BODY :: WHEN"]... | compass word --none
        (inside a session asked to) say which Compass lines the owner's last words support or go
        against and word up to 3 new lasting preferences, or say they hold none. Only the session
        PenguPool asked, once.
  experience import FILE
        (the owner's terminal only) add journal entries from a JSON file (- for stdin), an array of
        {"helper": NAME, "at": RFC3339, "title": "...", "body": "..."}. An entry already there
        (same helper, at and title), or deleted before, is skipped; nothing is written if any is refused.
  decide [--file PACK.json]
        (inside a helper session) ask the owner's decision model typed questions: JSON
        {state, questions: [{name, type: predicate|choice|score, instructions, options|levels}]}
        on stdin or in the file, the answers as JSON on stdout. Exit 3: no decision model is set.
  eval (--transcripts DIR [--since YYYY-MM-DD] [--limit N] | --sample) [--home DIR] [--json]
        replay owner exchanges through triage, the Compass and the check-in decision on a
        temporary database; prints aggregates against the plan's bars for autonomy by default.
  rpc TYPE ['JSON']
        send one pool request to the daemon and print the result.
  bridge --stdio [--session ID] | --claim HUB
        on an SSH node: pipe stdio to this host's daemon or one worker.
  hook HARNESS EVENT | statusline
        internal: called by harness hooks and Claude's status line.
  worker --session-id ID
        internal: one PTY worker; the launch spec arrives on file descriptor 3.
  review [REV]
        Muse Code reviews the diff (commit, stdin, or worktree).
  experiment [--tasks F] [--out D] [--only ID...] [--judge-model M] [--judge-base-url U] [--max-steps N]
        run the with-vs-without benchmark.
  version | help

Store directory: $PENGUPOOL_HOME/v2, default ~/.pengupool/v2 (override with --store or
PENGUPOOL_HOME).`

// Build identifies the exact build (scripts/build-app.sh sets it from the Git commit), so an SSH
// host running another build of the same version is updated when the app switches to it.
var Build = ""

func versionLine() string {
	v := Version
	if Build != "" {
		v += "+" + Build
	}
	return fmt.Sprintf("pengupool %s (protocol v1)", v)
}

func main() {
	args := os.Args[1:]
	// Run by ssh as SSH_ASKPASS for a password node: the prompt is the only argument.
	if fifo := os.Getenv(node.AskpassEnv); fifo != "" {
		os.Exit(node.Askpass(fifo, args))
	}
	if len(args) == 0 {
		fmt.Println(help)
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "daemon":
		err = cmdDaemon(args[1:])
	case "new":
		err = cmdNew(args[1:])
	case "ls", "list":
		err = cmdList(args[1:])
	case "attach":
		err = cmdAttach(args[1:])
	case "kill":
		err = cmdKill(args[1:])
	case "web":
		err = cmdWeb(args[1:])
	case "hook":
		err = cmdHook(args[1:])
	case "statusline":
		err = cmdStatusline(args[1:])
	case "send":
		err = cmdSend(args[1:])
	case "search":
		err = cmdSearch(args[1:])
	case "memory":
		err = cmdMemory(args[1:])
	case "team":
		err = cmdTeam(args[1:])
	case "compass":
		err = cmdCompass(args[1:])
	case "experience":
		err = cmdExperience(args[1:])
	case "decide":
		err = cmdDecide(args[1:])
	case "eval":
		err = cmdEval(args[1:])
	case "rpc":
		err = cmdRPC(args[1:])
	case "bridge":
		err = cmdBridge(args[1:])
	case "worker":
		err = cmdWorker(args[1:])
	case "review":
		err = runReview(args[1:])
	case "experiment":
		err = runExperiment(args[1:])
	case "version", "-V", "--version":
		fmt.Println(versionLine())
	case "help", "-h", "--help":
		fmt.Println(help)
	default:
		fmt.Fprintf(os.Stderr, "pengupool: unknown command %q\n\n%s\n", args[0], help)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pengupool: %v\n", err)
		os.Exit(exitCode(err))
	}
}

// storeDir resolves the v2 store directory: --store wins, then $PENGUPOOL_HOME, then the default.
func storeDir(override string) string {
	if override != "" {
		return override
	}
	return store.DefaultDir()
}

// hubSocket is the daemon's unix socket, derived from the store directory.
func hubSocket(dir string) string { return filepath.Join(dir, "run", "hub.sock") }

// tokenPath is the daemon's browser bootstrap token.
func tokenPath(dir string) string { return filepath.Join(dir, "run", "hub.token") }

func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "pengupool"
	}
	return exe
}

// describe renders a session the way the CLI shows it to a person.
func describe(s sessionRow) string {
	state := s.State
	if s.ExitCode != nil {
		state = fmt.Sprintf("%s(%d)", s.State, *s.ExitCode)
	}
	pid := "-"
	if s.Pid > 0 {
		pid = fmt.Sprintf("%d", s.Pid)
	}
	row := fmt.Sprintf("%s  %-12s %-10s %-9s %5s %4dx%d  %s",
		shortID(s.ID), s.Name, state, activity(s, time.Now()), pid, s.Cols, s.Rows, s.Cwd)
	if s.Waiting != "" {
		row += "  [waiting on a dialog: " + s.Waiting + "; open its terminal]"
	}
	return row
}

// staleAfter is how long a waiting agent stays quiet before it reads as stale (v1's rule; the
// desktop map's STALE_MS in web/src/app/ctx.tsx uses the same value).
const staleAfter = 10 * time.Minute

// activity is what the agent is doing, in the map's words: a running process can still be a stale
// agent, so `ls` shows both instead of leaving "running" to contradict the map's "Stale".
func activity(s sessionRow, now time.Time) string {
	if s.State != "running" {
		return "-"
	}
	if _, ok := harness.Find(s.Harness); !ok {
		return "terminal"
	}
	switch s.Status {
	case "working":
		return "active"
	case "approval":
		return "approval"
	case "starting":
		if s.Waiting != "" {
			return "dialog"
		}
		return "starting"
	}
	// A live team's helper waits for its coordinator by design, so it never reads as stale (as on the map).
	if t, err := time.Parse(time.RFC3339, s.ActiveAt); err == nil && now.Sub(t) > staleAfter && s.Role != "helper" {
		return "stale"
	}
	return "waiting"
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// isPrefix reports whether target names the session s either by id prefix or by name.
func isPrefix(s sessionRow, target string) bool {
	return strings.HasPrefix(s.ID, target) || s.Name == target
}
