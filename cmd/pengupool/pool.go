package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/client"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/session"
)

// rpc sends one pool request to the local daemon and returns its result.
func rpc(ctx context.Context, dir, typ string, params any) (json.RawMessage, error) {
	c, err := client.Dial(ctx, hubSocket(dir), protocol.KindCLI, "pengupool "+typ)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err := c.Call(ctx, c.HelloMessage()); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reply, err := c.Call(ctx, &protocol.Message{Type: typ, Params: raw})
	if err != nil {
		return nil, err
	}
	if reply.Type == protocol.TypeError {
		return nil, &rpcError{reply.Code, reply.Message}
	}
	return reply.Result, nil
}

// rpcError is the daemon's error reply: the message is what a person reads, the code what the
// exit status is made from.
type rpcError struct{ code, msg string }

func (e *rpcError) Error() string { return e.msg }

// exitCode is the CLI's exit status for a failed command: 3 when no decision model is set (the
// caller of `decide` falls back to its own rules), 1 otherwise.
func exitCode(err error) int {
	var re *rpcError
	if errors.As(err, &re) && re.code == protocol.CodeNoDecider {
		return 3
	}
	return 1
}

// cmdDecide: `decide [--file F]` sends a question pack (JSON {state, questions}, from F or stdin)
// to the owner's decision model for this session and prints the answers as JSON.
func cmdDecide(args []string) error {
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	file := fs.String("file", "", "the question pack (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var b []byte
	var err error
	if *file == "" || *file == "-" {
		b, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	} else {
		b, err = os.ReadFile(*file)
	}
	if err != nil {
		return err
	}
	sid := os.Getenv("PENGUPOOL_SESSION_ID")
	if sid == "" {
		return errors.New("decide works inside a PenguPool session")
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(b, &req); err != nil {
		return fmt.Errorf("decide reads a JSON object {\"state\": …, \"questions\": […]}: %v", err)
	}
	req["session_id"], _ = json.Marshal(sid)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	res, err := rpc(ctx, poolStore(), "decision.ask", req)
	if err != nil {
		return err
	}
	fmt.Println(string(res))
	return nil
}

// poolStore is the store a hook or agent command talks to: the one its session was started from.
func poolStore() string {
	if s := os.Getenv("PENGUPOOL_STORE"); s != "" {
		return s
	}
	return storeDir("")
}

// hookWait is how long the CLI waits for the daemon's answer to a hook. A Stop may carry triage's
// go-ahead, decided within 10 s (pool.StopDeadline), and a prompt its route or subagent plan, asked
// within 8 s; every harness gives the hook 15 s. Every other event stays quick.
func hookWait(event string) time.Duration {
	switch event {
	case "Stop":
		return 12 * time.Second
	case "UserPromptSubmit":
		return 11 * time.Second
	}
	return 3 * time.Second
}

// hookUnjudged notes on stderr that a PreToolUse the daemon could not judge (no answer, an error, a
// payload too big to read whole) goes to the harness's own permission system unchecked, as every
// status hook does: PenguPool adds no check of its own beyond the harness's, and the Compass is what
// reduces the checks the owner makes. While the daemon is down its deny guards do not apply either.
func hookUnjudged(event, why string) {
	if event == "PreToolUse" {
		fmt.Fprintf(os.Stderr, "pengupool: a tool call was not checked by PenguPool (%s); the harness's own permissions apply\n", why)
	}
}

// tooLarge is the PreToolUse answer for a payload too big to judge: the owner is asked (Claude) or
// the call refused (the harnesses that cannot ask).
func tooLarge(h string) string {
	decision := "deny"
	if h == "claude" {
		decision = "ask"
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": decision,
		"permissionDecisionReason": "PenguPool could not check this call against the floor (the call is too large): the owner decides it."}})
	return string(b)
}

// cmdHook forwards a harness hook to the daemon. A status hook never blocks agent work: when the
// daemon does not answer it exits 0 and prints nothing, and so does a PreToolUse (hookUnjudged).
func cmdHook(args []string) error {
	// usage: pengupool hook [--store DIR] <harness> <event>. Muse scrubs the hook environment, so
	// its hook command bakes --store in and the pool resolves the session from the payload.
	dir := poolStore()
	if len(args) >= 3 && args[0] == "--store" {
		dir, args = args[1], args[2:]
	}
	if len(args) < 2 {
		return errors.New("usage: pengupool hook [--store DIR] <harness> <event>")
	}
	h, event := args[0], args[1]
	silent := h == "muse" // unknown sessions and a down daemon are silent no-ops
	sid := os.Getenv("PENGUPOOL_SESSION_ID")
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
	if sid == "" && !silent {
		return nil
	}
	if len(payload) > 1<<20 {
		// The size is the agent's to choose, so this is not "the daemon is down": fail closed, or an
		// agent could pad a call past every guard.
		if event == "PreToolUse" && !silent {
			fmt.Println(tooLarge(h))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookWait(event))
	defer cancel()
	if !json.Valid(payload) {
		if event == "PreToolUse" {
			if !silent {
				hookUnjudged(event, "the call could not be read")
			}
			return nil
		}
		payload = []byte("{}")
	}
	env := map[string]string{}
	for _, k := range []string{"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	res, err := rpc(ctx, dir, "hook.event", map[string]any{
		"session_id": sid, "harness": h, "event": event, "payload": json.RawMessage(payload), "env": env,
	})
	if err != nil {
		if !silent {
			hookUnjudged(event, "the host did not answer")
		}
		return nil
	}
	var out struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
		Exit   int    `json:"exit"`
	}
	_ = json.Unmarshal(res, &out)
	if out.Stdout != "" {
		fmt.Println(out.Stdout)
	}
	if out.Stderr != "" {
		fmt.Fprintln(os.Stderr, out.Stderr)
	}
	if out.Exit != 0 {
		os.Exit(out.Exit)
	}
	return nil
}

// cmdStatusline is Claude's status line: it reports the context percentage, then runs the user's
// own status line command (from ~/.claude/settings.json) on the same input, unchanged.
func cmdStatusline(_ []string) error {
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if sid := os.Getenv("PENGUPOOL_SESSION_ID"); sid != "" {
		payload := map[string]any{}
		if pct, ok := contextPercent(raw); ok {
			payload["context_pct"] = pct
		}
		var c struct {
			Cost struct {
				USD *float64 `json:"total_cost_usd"`
			} `json:"cost"`
			Model struct {
				Name string `json:"display_name"`
			} `json:"model"`
		}
		if json.Unmarshal(raw, &c) == nil {
			if c.Cost.USD != nil {
				payload["cost_usd"] = *c.Cost.USD
			}
			if c.Model.Name != "" {
				payload["model"] = c.Model.Name
			}
		}
		if len(payload) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			_, _ = rpc(ctx, poolStore(), "hook.event", map[string]any{
				"session_id": sid, "harness": "claude", "event": "Status", "payload": payload,
			})
			cancel()
		}
	}
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return nil
	}
	var s struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(b, &s) != nil || s.StatusLine.Command == "" || strings.Contains(s.StatusLine.Command, selfPath()) {
		return nil
	}
	cmd := exec.Command("/bin/sh", "-c", s.StatusLine.Command)
	cmd.Stdin = strings.NewReader(string(raw))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	_ = cmd.Run()
	return nil
}

// contextPercent is Claude's own percentage, else used tokens over an explicit window size.
func contextPercent(raw []byte) (float64, bool) {
	var d struct {
		ContextWindow struct {
			Used  *float64        `json:"used_percentage"`
			Size  float64         `json:"context_window_size"`
			Usage json.RawMessage `json:"current_usage"`
		} `json:"context_window"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return 0, false
	}
	if d.ContextWindow.Used != nil {
		return math.Min(100, math.Max(0, *d.ContextWindow.Used)), true
	}
	if d.ContextWindow.Size <= 0 {
		return 0, false
	}
	var u map[string]float64
	if json.Unmarshal(d.ContextWindow.Usage, &u) != nil {
		return 0, false
	}
	used := u["input_tokens"] + u["output_tokens"] + u["cache_creation_input_tokens"] + u["cache_read_input_tokens"]
	return math.Min(100, used/d.ContextWindow.Size*100), true
}

// cmdSend is how a team member messages another member.
func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	to := fs.String("to", "", "member name or session id")
	typ := fs.String("type", "", "tell, ask, done or blocked (default: by the first line, Done:/Ask:/Blocked:, else tell)")
	replyTo := fs.String("reply-to", "", "the id of the ask this answers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if text == "-" || text == "" {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 8<<10))
		text = string(b)
	}
	from := os.Getenv("PENGUPOOL_SESSION_ID")
	if from == "" {
		return errors.New("send works from inside a PenguPool team session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := rpc(ctx, poolStore(), "message.send", map[string]string{"from": from, "to": *to, "text": text, "type": *typ, "reply_to": *replyTo})
	if err != nil {
		return err
	}
	var out map[string]string
	_ = json.Unmarshal(res, &out)
	fmt.Printf("sent to %s (delivered when it is idle)\n", out["to"])
	return nil
}

// cmdTeam: `team request add --skill S|--repo R [--assignment A] [--reason R]` |
// `team request remove MEMBER [--reason R]`: a formation change the owner approves.
func cmdTeam(args []string) error {
	usage := errors.New("usage: pengupool team request add --skill S|--repo R [--assignment A] [--reason R] | team request remove MEMBER [--reason R]")
	if len(args) < 2 || args[0] != "request" {
		return usage
	}
	op := args[1]
	fs := flag.NewFlagSet("team request", flag.ContinueOnError)
	skill := fs.String("skill", "", "a helper, by name")
	repo := fs.String("repo", "", "a work repository (a builder), by name")
	assignment := fs.String("assignment", "", "what the new member should do")
	reason := fs.String("reason", "", "why")
	rest := args[2:]
	member := ""
	if op == "remove" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return usage
		}
		member, rest = rest[0], rest[1:]
	} else if op != "add" {
		return usage
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := rpc(ctx, poolStore(), "team.request", map[string]any{
		"session_id": os.Getenv("PENGUPOOL_SESSION_ID"), "op": op, "skill_id": *skill, "repo_id": *repo,
		"member": member, "assignment": *assignment, "reason": *reason,
	})
	if err != nil {
		return err
	}
	var out map[string]string
	_ = json.Unmarshal(res, &out)
	fmt.Printf("requested: %s; it waits for the owner's approval\n", out["detail"])
	return nil
}

// askPatience is how long one `pengupool ask` waits for the answer: under the 2 minutes an agent's
// shell tool gives a command by default. The agent waits again with --wait ID.
var askPatience = 110 * time.Second

// cmdAsk puts a question to the owner from inside an agent session and waits for the answer.
func cmdAsk(args []string) error {
	if len(args) > 0 && args[0] == "answer" {
		return cmdAskAnswer(args[1:])
	}
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	waitID := fs.String("wait", "", "go on waiting for the answer to this ask")
	ownerOnly := fs.Bool("owner-only", false, "only the owner may answer it (a rule of yours only the owner can lift); never decided from the Compass")
	var opts []string
	fs.Func("option", "a choice to offer; repeatable", func(v string) error { opts = append(opts, v); return nil })
	words, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	session := os.Getenv("PENGUPOOL_SESSION_ID")
	if session == "" {
		return errors.New("pengupool ask works inside an agent session PenguPool runs")
	}
	id := *waitID
	if id == "" {
		q := strings.TrimSpace(strings.Join(words, " "))
		if q == "" {
			return errors.New(`usage: pengupool ask [--owner-only] [--option O]... QUESTION | pengupool ask --wait ID`)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res, err := rpc(ctx, poolStore(), "ask.create", map[string]any{"session_id": session, "question": q, "options": opts, "owner_only": *ownerOnly})
		cancel()
		if err != nil {
			return err
		}
		var out map[string]string
		_ = json.Unmarshal(res, &out)
		id = out["id"]
		// First, so an agent whose shell tool stops the wait still has the id to wait again with.
		fmt.Fprintf(os.Stderr, "asked the owner (ask %s); waiting for the answer\n", id)
	}
	deadline := time.Now().Add(askPatience)
	for {
		wait := min(time.Until(deadline), 60*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), wait+15*time.Second)
		res, err := rpc(ctx, poolStore(), "ask.wait", map[string]any{"session_id": session, "id": id, "wait": int(wait.Seconds())})
		cancel()
		if err != nil {
			return err
		}
		var out map[string]string
		_ = json.Unmarshal(res, &out)
		if out["pending"] == "" || time.Until(deadline) <= time.Second {
			fmt.Println(string(res))
			return nil
		}
	}
}

// cmdAskAnswer carries the owner's answer, typed in this session, to a question waiting on the owner
// below it: an ask, or a check-in (checkin:<id>). `pengupool answer` and `pengupool ask answer`.
func cmdAskAnswer(args []string) error {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	option := fs.Int("option", 0, "the option (1-based) of an ask the owner's whole prompt is; otherwise the prompt itself is the answer")
	perCompass := fs.Bool("per-compass", false, "ask PenguPool to decide it from the owner's Compass instead; it decides or refuses")
	usage := errors.New(`usage: pengupool answer ITEM_ID [--option N | --per-compass]`)
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usage
	}
	id := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	session := os.Getenv("PENGUPOOL_SESSION_ID")
	if session == "" {
		return errors.New("pengupool answer works inside an agent session PenguPool runs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if *perCompass {
		res, err := rpc(ctx, poolStore(), "ask.per_compass", map[string]any{"session_id": session, "id": id})
		if err != nil {
			return err
		}
		var out map[string]string
		_ = json.Unmarshal(res, &out)
		fmt.Println(out["detail"])
		return nil
	}
	res, err := rpc(ctx, poolStore(), "ask.answer_for_owner", map[string]any{"session_id": session, "id": id, "option": *option})
	if err != nil {
		return err
	}
	fmt.Println(string(res))
	return nil
}

// parseInterspersed parses fs's flags wherever they stand among args (`ask "Q" --option A`: the flag
// package alone stops at "Q" and folds the options into the question), returning the other args.
// ponytail: an arg after "--" that looks like a flag is still parsed as one.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if args = fs.Args(); len(args) == 0 {
			return rest, nil
		}
		rest, args = append(rest, args[0]), args[1:]
	}
}

// cmdCompass: `compass word …`, the answer to a [PenguPool compass] note.
func cmdCompass(args []string) error {
	usage := errors.New(`usage: pengupool compass word [--line "preference|restriction :: BODY :: WHEN"]... | compass word --none (or --kind K --body TEXT [--condition WHEN] for one line)`)
	if len(args) == 0 || args[0] != "word" {
		return usage
	}
	fs := flag.NewFlagSet("compass word", flag.ContinueOnError)
	kind := fs.String("kind", "preference", "preference or restriction")
	body := fs.String("body", "", "the preference, in the owner's terms")
	cond := fs.String("condition", "", "when it applies (empty: always)")
	forID := fs.String("for", "", "the id in the compass note being answered")
	none := fs.Bool("none", false, "the owner's words hold no lasting preference")
	var lines []string
	add := func(to *[]string) func(string) error {
		return func(v string) error { *to = append(*to, v); return nil }
	}
	fs.Func("line", `a new line, "preference|restriction :: BODY :: WHEN"; repeatable`, add(&lines))
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !*none && *body == "" && len(lines) == 0 {
		return usage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := rpc(ctx, poolStore(), "compass.word", map[string]any{
		"session_id": os.Getenv("PENGUPOOL_SESSION_ID"), "for": *forID, "none": *none, "kind": *kind, "body": *body, "condition": *cond,
		"lines": lines,
	})
	if err != nil {
		return err
	}
	if *none {
		fmt.Println("noted: no lasting preference")
	} else {
		fmt.Println("thanks; the owner's Compass has it")
	}
	return nil
}

// cmdSearch: `search WORDS`.
func cmdSearch(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return search(ctx, strings.Join(args, " "))
}

// cmdMemory: `memory search WORDS`, the name of `search` before 0.11, kept one release.
func cmdMemory(args []string) error {
	if len(args) == 0 || args[0] != "search" {
		return errors.New("usage: pengupool search WORDS")
	}
	return cmdSearch(args[1:])
}

// search prints what this session can see that matches words: the owner's compass, a helper's
// journal entries.
func search(ctx context.Context, words string) error {
	if strings.TrimSpace(words) == "" {
		return errors.New("give some words to search for")
	}
	res, err := rpc(ctx, poolStore(), "search", map[string]any{"session_id": os.Getenv("PENGUPOOL_SESSION_ID"), "query": words})
	if err != nil {
		return err
	}
	var out struct {
		Compass []struct {
			Kind, Body, Condition string
		} `json:"compass"`
		Experience []journalEntry `json:"experience"`
		Total      int            `json:"experience_total"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return err
	}
	fmt.Printf("Compass (%d):\n", len(out.Compass))
	for _, l := range out.Compass {
		t := l.Body
		if l.Condition != "" {
			t += " (when " + l.Condition + ")"
		}
		fmt.Printf("- [%s] %s\n", l.Kind, t)
	}
	if len(out.Experience) > 0 {
		fmt.Printf("Your past work (%d of %d):\n", len(out.Experience), out.Total)
		printEntries(out.Experience)
	}
	return nil
}

type journalEntry struct {
	ID                       int64
	At, Title, Snippet, Body string
}

func printEntries(es []journalEntry) {
	for _, e := range es {
		fmt.Printf("[%d] %s · %s\n", e.ID, e.At[:min(10, len(e.At))], e.Title)
		if e.Snippet != "" {
			fmt.Printf("    %s\n", e.Snippet)
		}
	}
}

const experienceUsage = "usage: pengupool experience search WORDS | show ID | import FILE | export FILE"

// cmdExperience: `experience search WORDS` and `experience show ID` (a helper's own entries), and
// `experience import FILE` and `experience export FILE` (the owner's terminal).
func cmdExperience(args []string) error {
	if len(args) == 0 {
		return errors.New(experienceUsage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := os.Getenv("PENGUPOOL_SESSION_ID")
	switch args[0] {
	case "search":
		res, err := rpc(ctx, poolStore(), "experience.search", map[string]any{"session_id": session, "query": strings.Join(args[1:], " ")})
		if err != nil {
			return err
		}
		var out struct {
			Entries []journalEntry
			Total   int
		}
		if err := json.Unmarshal(res, &out); err != nil {
			return err
		}
		fmt.Printf("Your past work (%d of %d):\n", len(out.Entries), out.Total)
		printEntries(out.Entries)
	case "show":
		id, err := strconv.ParseInt(strings.Join(args[1:], ""), 10, 64)
		if err != nil {
			return errors.New("usage: pengupool experience show ID")
		}
		res, err := rpc(ctx, poolStore(), "experience.show", map[string]any{"session_id": session, "id": id})
		if err != nil {
			return err
		}
		var e journalEntry
		if err := json.Unmarshal(res, &e); err != nil {
			return err
		}
		fmt.Printf("[%d] %s · %s\n\n%s\n", e.ID, e.At, e.Title, e.Body)
	case "import":
		fs := flag.NewFlagSet("experience import", flag.ContinueOnError)
		storeFlag := fs.String("store", "", "store directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: pengupool experience import FILE")
		}
		var raw []byte
		var err error
		if fs.Arg(0) == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(fs.Arg(0))
		}
		if err != nil {
			return err
		}
		// An array of entries, or {"entries": [...], "drop": [{"id": N, "reason": "..."}]}.
		var in struct {
			Entries []json.RawMessage `json:"entries"`
			Drop    []json.RawMessage `json:"drop"`
		}
		if err := json.Unmarshal(raw, &in.Entries); err != nil {
			if err := json.Unmarshal(raw, &in); err != nil {
				return fmt.Errorf("the file is neither a JSON array of entries nor {\"entries\", \"drop\"}: %w", err)
			}
		}
		res, err := rpc(ctx, storeDir(*storeFlag), "experience.import", map[string]any{"entries": in.Entries, "drop": in.Drop})
		if err != nil {
			return err
		}
		var out struct{ Added, Skipped, Dropped int }
		if err := json.Unmarshal(res, &out); err != nil {
			return err
		}
		fmt.Printf("%d added, %d skipped (already there), %d dropped\n", out.Added, out.Skipped, out.Dropped)
	case "export":
		fs := flag.NewFlagSet("experience export", flag.ContinueOnError)
		storeFlag := fs.String("store", "", "store directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: pengupool experience export FILE")
		}
		res, err := rpc(ctx, storeDir(*storeFlag), "experience.export", map[string]any{})
		if err != nil {
			return err
		}
		var es []json.RawMessage
		if err := json.Unmarshal(res, &es); err != nil {
			return err
		}
		out, _ := json.MarshalIndent(es, "", "  ")
		if err := os.WriteFile(fs.Arg(0), append(out, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Printf("%d entries written to %s\n", len(es), fs.Arg(0))
	default:
		return errors.New(experienceUsage)
	}
	return nil
}

// cmdRPC sends any pool request: `pengupool rpc <type> '<json params>'`.
func cmdRPC(args []string) error {
	fs := flag.NewFlagSet("rpc", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pengupool rpc <type> ['<json params>']")
	}
	params := json.RawMessage("{}")
	if fs.NArg() > 1 {
		params = json.RawMessage(fs.Arg(1))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := rpc(ctx, storeDir(*storeFlag), fs.Arg(0), params)
	if err != nil {
		return err
	}
	fmt.Println(string(res))
	return nil
}

// cmdBridge runs on an SSH node. --stdio pipes stdin/stdout to this host's daemon socket (or with
// --session, to one worker); --claim records the owning hub and refuses a second one.
func cmdBridge(args []string) error {
	fs := flag.NewFlagSet("bridge", flag.ContinueOnError)
	storeFlag := fs.String("store", "", "store directory")
	stdio := fs.Bool("stdio", false, "pipe stdin/stdout to the daemon")
	sess := fs.String("session", "", "pipe to this session's worker instead")
	claim := fs.String("claim", "", "record this hub as the node's owner")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := storeDir(*storeFlag)
	if *claim != "" {
		path := filepath.Join(dir, "run", "hub.owner")
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) != "" && strings.TrimSpace(string(b)) != *claim {
			return fmt.Errorf("this node is owned by hub %s; transfer it there first (remove %s on this host to release it)", strings.TrimSpace(string(b)), path)
		}
		return os.WriteFile(path, []byte(*claim+"\n"), 0o600)
	}
	if !*stdio {
		return errors.New("usage: pengupool bridge --stdio [--session ID] | --claim HUB")
	}
	target := hubSocket(dir)
	if *sess != "" {
		target = session.SocketPath(dir, *sess)
	}
	c, err := net.Dial("unix", target)
	if err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(c, os.Stdin); done <- struct{}{} }()
	go func() { _, _ = io.Copy(os.Stdout, c); done <- struct{}{} }()
	<-done
	return c.Close()
}

// readEntry reads an entry file, or stdin for "" and "-".
func readEntry(file string) ([]byte, error) {
	if file == "-" || file == "" {
		return io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	}
	return os.ReadFile(file)
}
