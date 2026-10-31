// Command pengupool is the PenguPool AI host: the daemon that owns state and the browser app, the
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
)

// Version of the prototype. It is not the protocol version; that is protocol.Version.
const Version = "0.10.10"

const help = `usage: pengupool <command> [options]

  daemon [--store DIR] [--http ADDR]
        run the host: state, sessions, browser app, on loopback only (--http takes a
        loopback address; ":PORT" means 127.0.0.1:PORT). Other devices reach the device
        API through Tailscale Serve.
  new [--name N] [--cwd DIR] [--harness claude|muse|codex|pi|omp] [--model M]
        start an agent session (Claude Code by default) in that directory.
  ls [--json]
        list sessions.
  attach <session> [--no-claim] [--detach-key ctrl-\\|none]
        attach this terminal to a session, raw, no status bar.
  kill <session>
        stop a session and its worker.
  web
        print the browser app URL for the running daemon.
  send --to MEMBER [--type tell|ask|done|blocked] [--reply-to ASK_ID] TEXT
        message another member of this session's team (from inside a team session). An ask stays
        open until the member answers; without --type, a first line starting Done:, Ask: or
        Blocked: gives the type.
  search WORDS
        (inside an agent session) search the owner's compass and a helper's own journal entries.
        "memory search" is the same, under its old name.
  experience search WORDS | experience show ID
        (inside a helper session) list its own journal entries that match words (id, date, title,
        a snippet), or print one in full. An entry is kept when the helper tells its parent
        "Done: <title>".
  team request add --skill S|--repo R [--assignment A] | remove MEMBER [--reason R]
        (inside a team session) ask the owner to change who is on the team; it waits for approval.
  ask [--owner-only] [--option O]... QUESTION | ask --wait ID
        (inside an agent session) put a question to the owner, answered in the app or from a paired
        Muse device; waits up to 2 minutes and prints {"answer": …, "by": …} as JSON, else
        {"pending": ID}: run "ask --wait ID" to go on waiting. The owner may pick an option or reply
        in their own words. Unanswered for 15 minutes, it expires: {"expired": …}; go on with a
        safe default. --owner-only marks a question only the owner may answer (a rule of yours only
        the owner can lift).
  answer ITEM_ID [--option N | --per-compass]   (also: ask answer)
        (inside a session with members below it) carry the owner's answer, typed in this session, to
        a question waiting on the owner below you on the team's tree: an ask (its id), or a check-in
        (checkin:<id>). The owner's whole prompt goes, word for word, or option N of an ask when the
        prompt is exactly it; a check-in's answer is typed into the member as the owner's own prompt.
        Only in a turn the owner's own prompt opened, with no teammate's message to you since your
        previous reply, for a question that reply showed by id and quoted as it stands (the only one
        it showed, or one the owner names by id); a prompt over 2000 characters is refused, not clipped.
  compass word [--line "preference|restriction :: BODY :: WHEN"]... | compass word --none
        (inside a session asked to) word up to 3 new lasting preferences from the owner's last
        words, or say they hold none. Only the session PenguPool asked, once.
  experience import FILE
        (the owner's terminal only) add journal entries from a JSON file (- for stdin), an array of
        {"helper": NAME, "at": RFC3339, "title": "...", "body": "..."}. An entry already there
        (same helper, at and title), or deleted before, is skipped; nothing is written if any is refused.
        The file may instead be {"entries": [...], "drop": [{"id": N, "reason": "..."}]}: the dropped
        entries are deleted first, in the same run.
  experience export FILE
        (the owner's terminal only) write every helper's journal entries, bodies and ids included,
        to FILE in the import format: the source for a rebuild.
  decide [--file PACK.json]
        (inside a helper session) ask the owner's decision model typed questions: JSON
        {state, questions: [{name, type: predicate|choice|score, instructions, options|levels}]}
        on stdin or in the file, the answers as JSON on stdout. Exit 3: no decision model is set.
  eval (--transcripts DIR [--since YYYY-MM-DD] [--limit N] | --sample) [--home DIR] [--json]
        replay owner exchanges through triage, the Compass and the check-in decision on a
        temporary database; prints aggregates against the plan's bars for autonomy by default.
  doctor [--fix [--yes]] [--restore [ID]]
        check this machine: PenguPool v1 left in Claude's settings (--fix removes it, after asking, and
        keeps what it removed in PenguPool's record; --restore lists those and puts one back), the
        keychain, the agent CLIs, what reaches the Compass. Changes nothing without --fix or --restore.
  rpc TYPE ['JSON']
        send one pool request to the daemon and print the result.
  bridge --stdio [--session ID] | --claim HUB
        on an SSH node: pipe stdio to this host's daemon or one worker.
  hook HARNESS EVENT | statusline
        internal: called by harness hooks and Claude's status line.
  worker --session-id ID
        internal: one PTY worker; the launch spec arrives on file descriptor 3.
  version | help

Store directory: $PENGUPOOL_HOME/v2, default ~/.pengupool-ai/v2 (override with --store or
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
	case "ask":
		err = cmdAsk(args[1:])
	case "answer":
		err = cmdAskAnswer(args[1:])
	case "compass":
		err = cmdCompass(args[1:])
	case "experience":
		err = cmdExperience(args[1:])
	case "decide":
		err = cmdDecide(args[1:])
	case "eval":
		err = cmdEval(args[1:])
	case "doctor":
		err = cmdDoctor(args[1:])
	case "rpc":
		err = cmdRPC(args[1:])
	case "bridge":
		err = cmdBridge(args[1:])
	case "worker":
		err = cmdWorker(args[1:])
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
	if home := os.Getenv("PENGUPOOL_HOME"); home != "" {
		return filepath.Join(home, "v2")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".pengupool-ai", "v2")
	}
	return filepath.Join(home, ".pengupool-ai", "v2")
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
