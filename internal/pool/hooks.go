package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Harness hooks call `pengupool hook <harness> <event>`, which forwards here. Status hooks never
// block agent work: the CLI gives up quickly and exits 0 when the daemon is not answering. Checked
// operations (a SendMessage to a peer) fail closed only on an explicit refusal.

type hookIn struct {
	SessionID string            `json:"session_id"`
	Harness   string            `json:"harness"`
	Event     string            `json:"event"`
	Payload   json.RawMessage   `json:"payload,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// HookOut is what the CLI prints and how it exits.
type HookOut struct {
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Exit   int    `json:"exit"`
}

// resolveMuseSession maps a Muse hook to its pool session. Muse scrubs the hook environment, so the
// hook names no session. When the calling process is known, the session is the live Muse session whose
// process it runs under: nothing else, so an agent in another session cannot speak as it, and two Muse
// sessions in one folder (or the owner's own Muse, outside PenguPool) never take each other's identity.
// Without a process to go by (a platform with no readable process tree, tests), a live Muse session
// whose ConvID is the payload's session id wins, else the newest live, unbound Muse session in the same
// folder is bound. Anything else resolves to nothing: the hook is a silent, fast no-op.
func (p *Pool) resolveMuseSession(c gateway.Caller, pl map[string]any) (store.Session, bool) {
	msid, _ := pl["session_id"].(string)
	st := p.st.State()
	if c.PeerPID > 0 && procTreeReadable {
		for _, s := range st.Sessions {
			if s.Harness != harness.Muse || !museLive(s) || s.Pid <= 0 || !p.onThisHost(s.NodeID) {
				continue
			}
			if yes, _ := descendsFrom(c.PeerPID, s.Pid); yes {
				if msid != "" && s.ConvID != msid {
					p.setSession(s.ID, func(x *store.Session) { x.ConvID = msid })
					s, _ = p.session(s.ID)
				}
				return s, true
			}
		}
		return store.Session{}, false
	}
	cwd, _ := pl["cwd"].(string)
	if msid == "" {
		return store.Session{}, false
	}
	for _, s := range st.Sessions {
		if s.Harness == harness.Muse && s.ConvID == msid && museLive(s) {
			return s, true
		}
	}
	if cwd == "" {
		return store.Session{}, false
	}
	var best *store.Session
	for i, s := range st.Sessions {
		if s.Harness != harness.Muse || s.ConvID != "" || !museLive(s) || !sameDir(s.Cwd, cwd) {
			continue
		}
		if best == nil || s.StartedAt.After(best.StartedAt) {
			best = &st.Sessions[i]
		}
	}
	if best == nil {
		return store.Session{}, false
	}
	p.setSession(best.ID, func(x *store.Session) { x.ConvID = msid })
	if s, ok := p.session(best.ID); ok {
		return s, true
	}
	return store.Session{}, false
}

// museLive reports whether a pool session may take a Muse hook: still
// running and not marked ended or unsupported. An exited session never
// binds again.
func museLive(s store.Session) bool {
	return s.State != "exited" && s.Status != "ended" && s.Status != "harness no longer supported"
}

// sameDir reports whether two working directories are the same folder,
// sameDir reports whether two working directories are the same folder,
// through symlinks (/tmp vs /private/tmp) as well as literally.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// hookEvent is a hook with no known calling process (tests): see hookEventFrom.
func (p *Pool) hookEvent(ctx context.Context, in hookIn) (any, error) {
	return p.hookEventFrom(ctx, gateway.Caller{}, in)
}

// hookEventFrom is hook.event, with the calling process that resolves a Muse hook's session.
func (p *Pool) hookEventFrom(ctx context.Context, c gateway.Caller, in hookIn) (any, error) {
	entered := time.Now() // a Stop's whole answer is due by entered + StopDeadline
	var pl map[string]any
	_ = json.Unmarshal(in.Payload, &pl)
	if in.Harness == harness.Muse {
		// Muse scrubs the hook environment, so the session is resolved for every event, in this
		// one place.
		s, ok := p.resolveMuseSession(c, pl)
		if !ok {
			return HookOut{}, nil // outside PenguPool, or the daemon just started: untouched
		}
		in.SessionID = s.ID
	}
	s, ok := p.session(in.SessionID)
	if !ok {
		return HookOut{}, nil // not a pool session: say nothing
	}
	p.hookArrived(s, in.Event)
	if pl != nil {
		copilotTool(pl)
	}
	str := func(k string) string { v, _ := pl[k].(string); return v }
	// Activity time drives the map's Stale state; written at most every 30 s.
	if time.Since(s.ActiveAt) > 30*time.Second && in.Event != "Status" {
		p.setSession(s.ID, func(x *store.Session) { x.ActiveAt = time.Now().UTC() })
	}
	if pct, ok := pl["context_pct"].(float64); ok && int(pct+0.5) != s.ContextPct {
		p.setSession(s.ID, func(x *store.Session) { x.ContextPct = int(pct + 0.5) })
	}
	if cost, ok := pl["cost_usd"].(float64); ok {
		p.addCost(s, cost)
	}
	if m, ok := pl["model"].(string); ok && m != "" && m != s.Model {
		p.setSession(s.ID, func(x *store.Session) { x.Model = m })
	}

	switch in.Event {
	case "Stop", "TurnFailed", "SessionStart", "SessionEnd": // Interrupted clears them below, past its guard
		// A question or plan tool that was denied or interrupted never reports PostToolUse: its
		// flag must not outlive the turn or the session (ownerinput.go).
		p.mu.Lock()
		delete(p.askingOwner, s.ID)
		delete(p.routed, s.ID)
		p.mu.Unlock()
	}
	// The pending question lasts until the agent got its answer: a hook for that same tool call, or
	// any hook when the question names no call. Another tool running alongside (parallel calls, a
	// subagent) leaves it; a turn's or session's end always clears it.
	switch in.Event {
	case "PermissionRequest", "Notification", "Status", "Delivered":
	case "PreToolUse", "PostToolUse":
		p.mu.Lock()
		use := p.questionUse[s.ID]
		p.mu.Unlock()
		if use == "" || use == str("tool_use_id") {
			p.setQuestion(s.ID, "", "")
		}
	default:
		p.setQuestion(s.ID, "", "")
	}
	switch in.Event {
	case "SessionStart":
		conv, transcript := firstNonEmpty(str("session_id"), str("sessionId")), firstNonEmpty(str("transcript_path"), str("transcriptPath"))
		a, _ := harness.Find(s.Harness)
		if sock := in.Env["CLAUDE_CODE_MESSAGING_SOCKET"]; sock != "" {
			p.setInbox(s.ID, claudeInbox{Socket: sock, Token: in.Env["CLAUDE_CODE_MESSAGING_TOKEN"]})
		}
		p.setSession(s.ID, func(x *store.Session) {
			if !a.EarlyStart || x.Status != "starting" {
				x.Status = "idle"
			}
			if conv != "" {
				x.ConvID = conv
			}
			if transcript != "" {
				x.Transcript = transcript
			}
		})
		p.logLine(s.ID, fmt.Sprintf("session: %s started (%s)", s.Harness, firstNonEmpty(str("source"), "startup")))
		p.mu.Lock()
		delete(p.injected, s.ID)
		p.mu.Unlock()
		return p.contextOut(s.ID, "SessionStart", p.journalAtStart(s)), nil
	case "UserPromptSubmit":
		if out, blocked := p.blockOutside(s, str("prompt")); blocked {
			return out, nil
		}
		t := turn{Prompt: str("prompt")}
		if from := p.outsideSender(t.Prompt); from != "" {
			p.nativeArrived(s.ID, from, entered)
		}
		if t.fromOwner() && !p.ownerSent(s.ID, t.Prompt) {
			t.Unverified = true // looks like the owner's, but the owner sent nothing: not the owner's
			p.logLine(s.ID, "prompt: a user turn the owner did not send; treated as a teammate's words ("+p.ownerInputSeen(s.ID)+")")
		}
		owner := t.fromOwner()
		wait := p.promptArrived(s, owner)
		if owner {
			p.ownerMentions(s, t.Prompt)
			go p.ownerReplied(s, t.Prompt, wait) // the owner may be answering a gate: off the hook, it becomes a signal
		}
		p.setStatus(s.ID, "working")
		if t.Prompt == "" { // an extension's status-only or prompt-less signal: a delivered message's turn keeps its prompt
			p.turnOpen(s.ID, "")
		} else {
			p.turnStartAs(s.ID, t.Prompt, t.Unverified)
		}
		if s.JobStart.IsZero() { // the first prompt since its last log entry starts a job
			p.setSession(s.ID, func(x *store.Session) { x.JobStart = time.Now().UTC() })
		}
		if _, statusOnly := pl["status_only"]; statusOnly {
			return HookOut{}, nil
		}
		return p.contextOut(s.ID, "UserPromptSubmit", p.journalForPrompt(s, t.Prompt), p.nextStepNote(s, t.Prompt), p.subagentPlan(s, t.Prompt), p.wordingNote(s.ID)), nil
	case "PreToolUse":
		if in.Harness == harness.Muse {
			if _, ok := pl["tool_name"].(string); !ok {
				// A Muse tool call without a name is a shape this version does not know: never
				// block on it, allow and note it once. Named calls enforce below, with the
				// harness read set (Muse names its tools snake_case: read_file, search, ...).
				p.mu.Lock()
				warned := p.museToolWarned
				p.museToolWarned = true
				p.mu.Unlock()
				if !warned {
					p.logf("pool: muse PreToolUse without tool_name; allowed")
				}
				return HookOut{}, nil
			}
		}
		// Muse asks its permission before PreToolUse: the call waits on the owner until it runs or fails.
		if in.Harness != harness.Muse || s.Status != "approval" {
			p.setStatus(s.ID, "working")
		}
		p.turnOpen(s.ID, "")
		p.asking(s.ID, str("tool_name"), true)
		if t := str("tool_name"); t == "AskUserQuestion" || t == "ExitPlanMode" {
			p.setQuestion(s.ID, askedSummary(t, pl["tool_input"]), str("tool_use_id"))
		}
		if out, deny := p.guardRoute(s, pl); deny {
			return out, nil
		}
		if str("tool_name") == "SendMessage" {
			return p.guardSendMessage(s, pl), nil
		}
		if out, deny := p.guardStore(in.Payload); deny {
			return out, nil
		}
		if out, deny := p.guardSkill(s, pl); deny {
			return out, nil
		}
		p.logStrayWrite(s, pl)
		// No floor check here: PenguPool never adds a prompt to what the agent's own permissions allow.
		// The floor only limits what it answers for the owner (autoAnswer).
	case "PostToolUse":
		p.setStatus(s.ID, "working")
		p.turnOpen(s.ID, "")
		p.asking(s.ID, str("tool_name"), false)
		p.turnTool(s.ID)
		p.nativeArrived(s.ID, "", entered)
		if str("tool_name") == "SendMessage" {
			p.sendMessageRan(s, pl)
		}
		p.permissionRan(s, str("tool_name"), pl["tool_input"], str("tool_use_id"))
		if str("tool_name") == "AskUserQuestion" && s.Harness == harness.Claude {
			p.askedPicks(s, pl)
		}
		// The owner's words may be waiting to be worded (an answer to a question or a check-in, asked
		// while the turn runs): offered at the next tool round, whichever turn that is.
		if s.Harness == harness.Claude {
			if note := p.wordingNow(s.ID, "at the next tool round"); note != "" {
				return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PostToolUse", "additionalContext": note}}), nil
			}
		}
	case "ToolFailed": // Muse: a tool failed or was denied; the turn goes on. Which one is unknown, so
		// a permission prompt it answered stays open until the turn ends and expires it.
		p.setStatus(s.ID, "working")
		p.asking(s.ID, str("tool_name"), false)
		p.turnTool(s.ID)
	case "TurnFailed", "Interrupted": // Muse: the turn ended in an error or an interrupt, with no Stop
		// Interrupt is async: one landing after the owner's next prompt must not close that new turn.
		// ponytail: it still reads idle until that turn's next hook, and meanwhile a message may be
		// typed into the busy session; the window is the hook's spawn delay. A turn interrupted in
		// its first second reads working until its next hook.
		if in.Event == "Interrupted" {
			if p.turnSince(s.ID, entered.Add(-time.Second)) {
				break
			}
			p.mu.Lock()
			delete(p.askingOwner, s.ID)
			delete(p.routed, s.ID)
			p.mu.Unlock()
		}
		p.setStatus(s.ID, "idle")
		p.turnBroke(s)
		if in.Event == "TurnFailed" {
			p.flushQueue(s.ID) // an unattended agent's queue goes on; after an interrupt the owner is at the prompt
		}
	case "PermissionRequest":
		// A shell command the owner answered before is answered the same way for them on an autonomous
		// team (autoAnswer). Otherwise no decision: the harness shows its own dialog in the terminal;
		// the marker clears on the next hook. On an autonomous team the prompt is recorded as a gate.
		// (Its Notification twin is not: it names no tool.)
		if s.Harness == harness.Claude {
			switch decision, why := p.autoAnswer(s, str("tool_name"), pl["tool_input"]); decision {
			case "allow":
				p.logLine(s.ID, "permission: allowed for the owner, who allowed "+why+" before")
				return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PermissionRequest",
					"decision":      map[string]any{"behavior": "allow"},
				}}), nil
			case "deny":
				p.logLine(s.ID, "permission: declined for the owner, who declined "+why+" before")
				return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PermissionRequest",
					"decision":      map[string]any{"behavior": "deny", "message": "PenguPool: the owner declined " + why + " before. Ask them if it is needed now."},
				}}), nil
			}
		}
		p.setStatus(s.ID, "approval")
		p.setQuestion(s.ID, askedSummary(str("tool_name"), pl["tool_input"]), str("tool_use_id"))
		p.permissionAsked(s, str("tool_name"), pl["tool_input"], str("tool_use_id"))
		return HookOut{}, nil
	case "Notification":
		switch str("notification_type") {
		case "permission_prompt":
			p.setStatus(s.ID, "approval")
			// Its message only names the tool: the PermissionRequest before it says more.
			if cur, ok := p.session(s.ID); ok && cur.Question == "" {
				p.setQuestion(s.ID, clipRunes(redact(oneLine(str("message"))), 300), "")
			}
		case "idle_prompt":
			p.setStatus(s.ID, "idle")
		}
	case "Stop":
		// The turn ends before the session goes idle: triage may continue it from here with the
		// owner's go-ahead (then it stays working, and its queue waits for the next Stop).
		active, _ := pl["stop_hook_active"].(bool)
		p.nativeArrived(s.ID, "", entered) // a turn with no tool round: what it was sent is in it too
		out := p.turnEnd(s, firstNonEmpty(str("last_assistant_message"), str("prompt_response")),
			firstNonEmpty(str("transcript_path"), str("transcriptPath")), active, entered.Add(StopDeadline))
		if out.Stdout != "" {
			p.journalTurnEnd(s.ID) // continued: working again
			return out, nil
		}
		// A wording asked during the turn and not yet offered goes on the turn once, before it ends: a
		// harness that can continue a turn words the owner's words while they are fresh. At most once
		// per wording (wordingNow), and never on a turn this hook already continued. One asked after the
		// Stop (a preference triage reads with the reply) comes as a notice instead (wordingAtIdle).
		if stopContinues(s.Harness) && !active {
			if note := strings.TrimSpace(p.wordingNow(s.ID, "at the turn's end (one continuation)")); note != "" {
				p.turnStart(s.ID, note)
				p.journalTurnEnd(s.ID)
				return continueTurn(s.Harness, note), nil
			}
		}
		p.setStatus(s.ID, "idle")
		p.journalTurnEnd(s.ID) // before the queue: a delivered message starts the next turn
		p.flushQueue(s.ID)     // after the turn's end: a message it delivers starts the next turn
		return out, nil
	case "Status":
	case "Delivered":
		var ids []string
		if raw, ok := pl["ids"].([]any); ok {
			for _, v := range raw {
				if id, ok := v.(string); ok {
					ids = append(ids, id)
				}
			}
		}
		p.markDelivered(ids)
	case "SessionEnd":
		p.wordingEnded(s.ID)
		p.setStatus(s.ID, "ended")
		p.logLine(s.ID, "session: harness session ended ("+firstNonEmpty(str("reason"), "exit")+")")
	}
	return HookOut{}, nil
}

func (p *Pool) setSession(id string, fn func(*store.Session)) {
	_ = p.update(func(st *store.State) error {
		if i := sessionIndex(st, id); i >= 0 {
			fn(&st.Sessions[i])
		}
		return nil
	})
}

// setQuestion records what the session asks the owner now, and the tool call it is about ("" when
// unknown: then any tool hook ends it).
func (p *Pool) setQuestion(id, q, use string) {
	p.mu.Lock()
	if p.questionUse == nil {
		p.questionUse = map[string]string{}
	}
	if q == "" {
		delete(p.questionUse, id)
	} else if use != "" || p.questionUse[id] == "" {
		p.questionUse[id] = use // a Notification (no id) never loses the id its PermissionRequest gave
	}
	p.mu.Unlock()
	if s, ok := p.session(id); !ok || s.Question == q {
		return
	}
	p.setSession(id, func(x *store.Session) { x.Question = q })
}

// askedSummary is what a tool call asks the owner, in one line: the questions and their choices, the
// plan's first line, or the tool and what it acts on.
func askedSummary(tool string, input any) string {
	in, _ := input.(map[string]any)
	switch tool {
	case "AskUserQuestion":
		var parts []string
		qs, _ := in["questions"].([]any)
		for _, q := range qs {
			m, _ := q.(map[string]any)
			text, _ := m["question"].(string)
			var labels []string
			opts, _ := m["options"].([]any)
			for _, o := range opts {
				if om, _ := o.(map[string]any); om != nil {
					if l, _ := om["label"].(string); l != "" {
						labels = append(labels, l)
					}
				}
			}
			if len(labels) > 0 {
				text += " (" + strings.Join(labels, " / ") + ")"
			}
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return clipRunes(redact(oneLine(strings.Join(parts, "; "))), 300)
		}
	case "ExitPlanMode":
		plan, _ := in["plan"].(string)
		for _, line := range strings.Split(plan, "\n") {
			if line = strings.TrimSpace(strings.TrimLeft(line, "# ")); line != "" {
				return clipRunes(redact(oneLine("Approve the plan: "+line)), 300)
			}
		}
		return "Approve the plan"
	}
	return toolSummary(tool, input)
}

func (p *Pool) setStatus(id, status string) {
	s, ok := p.session(id)
	if !ok || s.Status == status {
		return
	}
	p.setSession(id, func(x *store.Session) { x.Status = status })
	if status == "idle" || status == "approval" {
		p.logLine(id, "status: "+status)
	}
	if status == "idle" {
		p.pendingRename(id)
	}
}

// resolveApproval sets a pending approval's state and prunes old resolved permission approvals.
func (p *Pool) resolveApproval(id, state string) {
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Approvals {
			if st.Domain.Approvals[i].ID == id && st.Domain.Approvals[i].State == "pending" {
				st.Domain.Approvals[i].State = state
			}
		}
		// Keep the list short: resolved permission approvals older than a day are dropped.
		cut := time.Now().Add(-24 * time.Hour)
		out := st.Domain.Approvals[:0]
		for _, a := range st.Domain.Approvals {
			t, _ := time.Parse(time.RFC3339, a.CreatedAt)
			if a.State == "pending" || a.Kind != "permission" || t.After(cut) {
				out = append(out, a)
			}
		}
		st.Domain.Approvals = out
		return nil
	})
}

// expireStaleApprovals ends permission approvals a previous daemon left pending: their hooks are
// gone, so nobody waits for them.
func (p *Pool) expireStaleApprovals() {
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Approvals {
			a := &st.Domain.Approvals[i]
			if a.Kind == "permission" && a.State == "pending" {
				a.State = "expired"
			}
		}
		return nil
	})
}

// copilotTool gives a Copilot tool hook's camelCase payload (toolName, toolArgs as a JSON string)
// the names the other harnesses use (tool_name, tool_input).
func copilotTool(pl map[string]any) {
	if n, _ := pl["toolName"].(string); n != "" && pl["tool_name"] == nil {
		pl["tool_name"] = n
	}
	if pl["tool_input"] != nil {
		return
	}
	switch a := pl["toolArgs"].(type) {
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(a), &m) == nil {
			pl["tool_input"] = m
		}
	case map[string]any:
		pl["tool_input"] = a
	}
}

// guardSendMessage checks a Claude SendMessage to another session: only its parent or a child on
// the team's tree, or a session the owner opened a channel to with @. An address (a socket, a
// bridge or cloud session) that is not such a session is refused; a plain name that is no
// PenguPool session is an in-process teammate.
func (p *Pool) guardSendMessage(s store.Session, pl map[string]any) HookOut {
	target, ok := p.sendTarget(pl)
	if ok && p.mayTalk(s.ID, target) {
		p.mu.Lock()
		if p.nativeTo == nil {
			p.nativeTo = map[string]time.Time{}
		}
		p.nativeTo[target] = time.Now() // it may be in target's inbox before its record exists (askAnswerFor)
		p.mu.Unlock()
	}
	if !ok || p.mayTalk(s.ID, target) {
		return HookOut{}
	}
	return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": "PenguPool: agents talk only to their parent and children on their team, and that session is neither, or the name is shared by more than one session. Message your parent; it passes on what others need. To reach a teammate whatever its name, run `" + p.exe + " send --to <session id>` with the id from your Team members list. Only the owner can open a channel to another session, by typing @<session> in your prompt.",
	}})
}

// sendTarget is the PenguPool session a SendMessage names, if it names one. Claude delivers a
// SendMessage by name to the Claude session of that name, so that session is the target, whatever
// team holds a session of the same name; a name two Claude sessions share is ("", true), which no
// one may send to (the guard says to use the session id).
func (p *Pool) sendTarget(pl map[string]any) (string, bool) {
	in, _ := pl["tool_input"].(map[string]any)
	to, _ := in["to"].(string)
	to, _, _ = strings.Cut(to, " [") // a ListAgents row's name may carry its [ref]
	if strings.Contains(to, "/") || strings.Contains(to, ":") {
		return p.sessionBySocket(strings.TrimPrefix(to, "uds:")), true
	}
	st := p.st.State()
	var claude []string
	for _, s := range st.Sessions {
		if s.State == "running" && s.Harness == harness.Claude && strings.EqualFold(s.Name, to) {
			claude = append(claude, s.ID)
		}
	}
	switch {
	case len(claude) == 1:
		return claude[0], true
	case len(claude) > 1:
		return "", true
	}
	if x, ok := sessionNamed(st, to); ok {
		return x.ID, true
	}
	return "", false
}

// sendMessageRan puts a SendMessage that went through (PostToolUse: no hook or prompt refused it)
// on the team's record, so the map and the message log show it, and keeps it when it is a done report.
// It is queued until it is in the recipient's turn (nativeArrived); its type is its first line's.
func (p *Pool) sendMessageRan(s store.Session, pl map[string]any) {
	target, ok := p.sendTarget(pl)
	if !ok || target == "" || !p.mayTalk(s.ID, target) {
		return
	}
	d := p.st.State().Domain
	t := teamOf(&d, s.ID)
	if t == nil {
		return
	}
	in, _ := pl["tool_input"].(map[string]any)
	text, _ := in["message"].(string)
	text = cleanText(text)
	if detectSecret(text) { // checked whole, so a token cut by the clip cannot leave a fragment
		text = leftOut
	}
	text = clip(text, maxMessage)
	typ, _ := msgType("", text)
	m := protocol.TeamMessage{ID: protocol.NewID(), TeamID: t.ID, From: s.ID, To: target, Text: text, State: "queued", At: now(), Via: viaNative, Type: typ}
	if err := p.update(func(st *store.State) error { addMessage(st, m); return nil }); err != nil {
		p.logf("pool: message record for %s: %v", s.ID, err)
	}
	p.doneReport(m)
}

// addCost takes a harness's reported spend for its conversation and adds what is new to the
// session's team. A drop means a new conversation, which starts from zero.
func (p *Pool) addCost(s store.Session, cost float64) {
	delta := cost - s.CostUSD
	if cost < s.CostUSD {
		delta = cost
	}
	if delta < 0.005 && cost >= s.CostUSD { // ponytail: cent-ish granularity keeps state.json writes rare
		return
	}
	_ = p.update(func(st *store.State) error {
		if i := sessionIndex(st, s.ID); i >= 0 {
			st.Sessions[i].CostUSD = cost
		}
		if t := teamOf(&st.Domain, s.ID); t != nil {
			if t.CostBy == nil {
				t.CostBy = map[string]float64{}
			}
			t.CostBy[s.ID] += delta
		}
		return nil
	})
}

// guardStore refuses an agent's tool call that names the host's own files: agents.db (the Compass
// and logs), the run logs, state.json (teams and approvals) and the host token. Those change only
// through the host, which checks the owner's switches. ponytail: a name match; a command that
// builds the path at run time gets past it. Running agents as another OS user is the hard boundary.
func (p *Pool) guardStore(payload []byte) (HookOut, bool) {
	text := string(payload)
	dir := p.st.Dir()
	for _, f := range []string{"agents.db", gateway.TokenFile, filepath.Join(dir, store.StateFile), filepath.Join(dir, store.LogsDir) + string(filepath.Separator)} {
		if strings.Contains(text, f) {
			return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": "PenguPool: the host's own files (" + f + ") change only through PenguPool. The record is PenguPool's: search it with `" + p.exe + " search`.",
			}}), true
		}
	}
	return HookOut{}, false
}

// guardSkill refuses a helper's file edit (Write, Edit, MultiEdit, NotebookEdit and the other agents'
// write tools) of an installed skill file: one Git tracks in a clone PenguPool installed under
// <store>/skills. Git decides, not the file's mode, which the helper can change and the library sync
// lifts while it runs. A new file or folder there is fine, and a checkout the owner works in is never
// guarded. A shell command is left to the read-only files and the context: matching commands would be guesswork.
func (p *Pool) guardSkill(s store.Session, pl map[string]any) (HookOut, bool) {
	path := changedFile(pl)
	if path == "" || s.SkillID == "" || (!filepath.IsAbs(path) && s.Cwd == "") {
		return HookOut{}, false
	}
	d := p.st.State().Domain
	sk := findSkill(&d, s.SkillID)
	if sk == nil || sk.Path == "" || sk.Source != "git" || !within(sk.Path, filepath.Join(p.st.Dir(), "skills")) {
		return HookOut{}, false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.Cwd, path)
	}
	path = filepath.Clean(path)
	if !within(path, sk.Path) {
		return HookOut{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.gitIn(ctx, sk.Path, "ls-files", "--error-unmatch", "--", path); err != nil {
		return HookOut{}, false // untracked: the helper's own
	}
	return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":      "PreToolUse",
		"permissionDecision": "deny",
		"permissionDecisionReason": fmt.Sprintf("PenguPool: %s's installed skill files are read-only; temp files and folders of your own are fine. The record is PenguPool's: search it with `%s search`.",
			sk.Name, firstNonEmpty(p.exe, "pengupool")),
	}}), true
}

// contextOut returns the context sections that changed since the last injection into this session
// (all of them the first time, and after SessionStart, which a /compact or /clear fires), and the
// notices queued for it. Triage and teammates change the task on most turns;
// re-sending only what changed keeps that from costing the agent the whole context each time.
func (p *Pool) contextOut(id, event string, extra ...string) HookOut {
	now := map[string]string{}
	var changed []string
	p.mu.Lock()
	seen := p.injected[id]
	for _, sec := range contextSections(p.renderContext(id)) {
		sum := sha256.Sum256([]byte(sec))
		k := hex.EncodeToString(sum[:])
		now[k] = sectionHeading(sec)
		if _, ok := seen[k]; !ok {
			changed = append(changed, sec)
		}
	}
	p.injected[id] = now
	p.mu.Unlock()
	text := strings.Join(changed, "")
	if seen == nil && event != "SessionStart" && text != "" {
		// a session that ran across an upgrade: its earlier context may tell it to log, plan or restate
		text = "# PenguPool context (replaces all earlier PenguPool context; earlier instructions to log, plan or restate no longer apply)\n" + text
	}
	if seen != nil {
		gone := map[string]bool{}
		for _, h := range seen {
			gone[h] = true
		}
		for _, h := range now {
			delete(gone, h)
		}
		for h := range gone {
			text += "\n" + h + ": no longer applies\n"
		}
		if text != "" {
			text = "# PenguPool context update: only these sections changed; the rest stands as given before\n" + text
		}
	}
	text += decision.SecretRe.ReplaceAllString(p.takeNotices(id), "[left out: looked like a secret]")
	text += strings.Join(extra, "") // what this task adds: the journal entries that fit it
	if limit := p.hookOutCap(id); limit > 0 && len(text) > limit {
		text = p.fitContext(id, extra, limit)
	}
	if text == "" {
		return HookOut{}
	}
	if s, _ := p.session(id); s.Harness == harness.Copilot {
		return hookJSON(map[string]any{"additionalContext": text})
	}
	return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": event, "additionalContext": text}})
}

// contextSections splits rendered context at its "## " headings; the text before the first one is
// a section of its own.
func contextSections(text string) []string {
	var out []string
	for {
		i := strings.Index(text[min(1, len(text)):], "\n## ")
		if i < 0 {
			break
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	if strings.TrimSpace(text) != "" {
		out = append(out, text)
	}
	return out
}

// sectionHeading is a section's first non-empty line.
func sectionHeading(sec string) string {
	for _, l := range strings.Split(sec, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func hookJSON(v any) HookOut {
	b, _ := json.Marshal(v)
	return HookOut{Stdout: string(b)}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Claude inbox sockets are kept in run/inbox/<session>.claude (0600) so a restarted daemon can
// still deliver.
func (p *Pool) setInbox(id string, ib claudeInbox) {
	p.mu.Lock()
	p.msgSock[id] = ib
	p.mu.Unlock()
	dir := filepath.Join(p.st.Dir(), store.RunDir, "inbox")
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(ib)
	_ = os.WriteFile(filepath.Join(dir, id+".claude"), b, 0o600)
}

func (p *Pool) loadInboxes() {
	dir := filepath.Join(p.st.Dir(), store.RunDir, "inbox")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".claude")
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		var ib claudeInbox
		if err == nil && json.Unmarshal(b, &ib) == nil {
			p.msgSock[id] = ib
		}
	}
}

// hookOutCap is the hook answer budget for id's harness in bytes, 0 for uncapped.
func (p *Pool) hookOutCap(id string) int {
	s, ok := p.session(id)
	if !ok {
		return 0
	}
	if a, ok := harness.Find(s.Harness); ok {
		return a.HookOutCap
	}
	return 0
}
