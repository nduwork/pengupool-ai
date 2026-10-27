// Command pengu is the PenguPool AI host: the Compass CLI and daemon.
//
// Port of pengu/__main__.py. Store directory: $PENGUPOOL_AI_HOME, default
// ~/.pengupool-ai (a legacy ~/.pengupool-muse compass.jsonl migrates once).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/ingest"
	"github.com/nduwork/pengupool-ai/internal/judge"
	"github.com/nduwork/pengupool-ai/internal/mcp"
	"github.com/nduwork/pengupool-ai/internal/owner"
	"github.com/nduwork/pengupool-ai/internal/rules"
)

const Version = "0.1.1"

// Build identifies the exact build (scripts/build-app.sh sets it from the Git commit).
var Build = ""

const help = `usage: pengu <command> [options]

  rule "words"              propose a Compass line (you confirm at a terminal)
  list                      confirmed Compass lines
  forget <id>               drop a Compass line
  check '<tool JSON>'       which line allows this call, if any
  triage --text "..."       route an owner-bound message
  new --agent A [--task T]   start a Claude/Muse session in tmux
  ls | stop <id>            sessions
  mcp                       serve the Compass MCP server on stdio
  send/inbox                chat along the tree
  ask/owner-serve/proposals owner questions and proposals
  learned/recall            helper lessons
  ingest/confirm            propose/apply Compass changes
  voice --clip F            transcribe, then propose
  review [REV]              Muse Code reviews the diff
  experiment                with-vs-without on ~5 tasks
  version | help            print the version / this help`

func init() {
	// Proposals live in the owner package; ingest only proposes.
	ingest.Propose = owner.SaveProposalSimple
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, help)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version", "--version", "-V":
		if Build != "" {
			fmt.Printf("%s (%s)\n", Version, Build)
		} else {
			fmt.Println(Version)
		}
	case "help", "--help", "-h":
		fmt.Println(help)
	case "list":
		err = runList()
	case "rule":
		err = runRule(os.Args[2:])
	case "forget":
		err = runForget(os.Args[2:])
	case "check":
		err = runCheck(os.Args[2:])
	case "triage":
		err = runTriage(os.Args[2:])
	case "new":
		err = runNew(os.Args[2:])
	case "ls":
		err = runLs()
	case "stop":
		err = runStop(os.Args[2:])
	case "mcp":
		mcp.ServeStdio()
	case "send":
		err = runSend(os.Args[2:])
	case "inbox":
		err = runInbox(os.Args[2:])
	case "ask":
		err = runAsk(os.Args[2:])
	case "owner-serve":
		err = runOwnerServe(os.Args[2:])
	case "proposals":
		err = runProposals()
	case "learned":
		err = runLearned(os.Args[2:])
	case "recall":
		err = runRecall(os.Args[2:])
	case "ingest":
		err = runIngest(os.Args[2:])
	case "confirm":
		err = runConfirm(os.Args[2:])
	case "voice":
		err = runVoice(os.Args[2:])
	case "review":
		err = runReview(os.Args[2:])
	case "experiment":
		err = runExperiment(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pengu:", err)
		os.Exit(1)
	}
}

func runList() error {
	lines, err := compass.Load()
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Printf("%s  %s  (%s, %s)\n", l.ID, l.Rule, l.Source, l.Created)
	}
	return nil
}

func runForget(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pengu forget <id>")
	}
	ok, err := compass.Forget(args[0])
	if err != nil {
		return err
	}
	fmt.Println(map[bool]string{true: "forgotten", false: "no such line"}[ok])
	return nil
}

func runCheck(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pengu check '<tool-call JSON>'")
	}
	var call any
	if err := json.Unmarshal([]byte(args[0]), &call); err != nil {
		return err
	}
	lines, err := compass.Load()
	if err != nil {
		return err
	}
	id, err := rules.AllowedBy(call, lines)
	if err != nil {
		return err
	}
	if id == "" {
		fmt.Println("no line allows it")
		return nil
	}
	fmt.Println(id)
	return nil
}

func runTriage(args []string) error {
	var text string
	for i := 0; i < len(args); i++ {
		if args[i] == "--text" && i+1 < len(args) {
			text = args[i+1]
			i++
		}
	}
	if text == "" {
		return fmt.Errorf("usage: pengu triage --text \"...\"")
	}
	r, err := judge.TriageRoute(text, "")
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s  %.2f\n", r.Route, r.Kind, r.Confidence)
	return nil
}
