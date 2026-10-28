package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// nextStepTimeout bounds the per-turn route question: it runs inside the prompt hook.
const nextStepTimeout = 1500 * time.Millisecond

// nextStepNote is triage before the anchor's next step (research-002): one typed question, who should
// do the work this turn asks for, the anchor or a member whose routing description covers it. A firm
// answer (calibrated, p >= the delegate threshold, a member) is a note plus a binding hold: guardRoute
// refuses the anchor's working tools for the whole turn until it messages that member. Any other
// answer, or none, binds nothing; a hedged one only names the likely member. ponytail: one Choice per
// anchor turn, no cache or budget of its own; add size/parallel questions when the record shows the
// note routing quick or blocking steps.
func (p *Pool) nextStepNote(s store.Session, prompt string) string {
	who, note := p.routeTurn(s, prompt)
	p.mu.Lock()
	if p.routed == nil {
		p.routed = map[string]string{}
	}
	if who == "" {
		delete(p.routed, s.ID)
	} else {
		p.routed[s.ID] = who // the member's session id
	}
	p.mu.Unlock()
	return note
}

// routeTurn is the member session triage binds the anchor's turn to ("" for none) and the note ("" for none).
func (p *Pool) routeTurn(s store.Session, prompt string) (who, note string) {
	// A notice or triage's own words is not a step, and a few words are an answer (a check-in's "go
	// ahead"), not a task: neither waits on the model. A teammate's message is a step.
	// ponytail: a word count, not the open gate; read the gate when long answers get notes.
	if len(strings.Fields(prompt)) < 4 || strings.HasPrefix(prompt, "[PenguPool notice") || strings.HasPrefix(prompt, "[PenguPool triage]") {
		return "", ""
	}
	d := p.st.State().Domain
	t := teamOf(&d, s.ID)
	if t == nil || anchorOf(&d, t) != s.ID {
		return "", ""
	}
	options := map[string]string{"self": "the anchor itself: coordinating, answering the owner, or work no member's description covers"}
	ids := map[string]string{} // member name → session id
	for _, m := range t.Members {
		if m.State != "active" || m.SessionID == s.ID || !p.mayTalk(s.ID, m.SessionID) {
			continue
		}
		desc := p.routingDesc(&d, m)
		if desc == "" {
			continue
		}
		busy := ""
		if ms, ok := p.session(m.SessionID); !ok || ms.Status == "ended" {
			continue // not running: a message could not reach it
		} else if ms.Status == "working" {
			busy = " (busy: working on something now)"
		} else if ms.Status != "idle" && ms.Status != "" {
			continue // waiting on the owner
		}
		options[m.Name], ids[m.Name] = redactSecrets(desc)+busy, m.SessionID
	}
	if len(ids) == 0 {
		return "", ""
	}
	if ownerChose(prompt, ids) {
		return "", "" // the owner's own words say who does it
	}
	prompt = redactSecrets(clipRunes(prompt, 1500))
	dec := p.decider()
	if dec == nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), nextStepTimeout)
	res, err := dec.Decide(ctx, decision.Request{
		State: map[string]any{"task": redactSecrets(t.Goal), "turn_prompt": prompt, "anchor": s.Name},
		Questions: []decision.Question{{Name: "route", Type: decision.Choice, Options: options,
			Instructions: "Who should do the work this turn asks for next? If the owner's message itself says who should do it (the anchor itself, or a member by name), pick that. Otherwise pick a member when its description covers the work, the work is more than one quick command, and the member is not busy with something else; otherwise self."}},
	})
	cancel()
	a, ok := res.Answers["route"]
	if err != nil || !ok || a.Choice == "" {
		if err != nil {
			p.logLine(s.ID, "triage: next step: "+err.Error())
		}
		return "", ""
	}
	pr := a.Probabilities[a.Choice]
	if res.Calibrated && pr < 0.5 { // a calibrated guess under even odds says nothing
		return "", ""
	}
	id, member := ids[a.Choice]
	if !member {
		return "", "" // self, or an option the model made up
	}
	if res.Calibrated && pr >= decision.Thresholds["delegate"] {
		p.logLine(s.ID, fmt.Sprintf("triage: route=%s p=%.2f band=firm", a.Choice, pr))
		return id, fmt.Sprintf("\n[PenguPool triage] Next step: %s's description covers this (p=%.2f, the owner's decision model). Hand the work to %s with SendMessage (or `%s send` on a shell harness); PenguPool holds your working tools until you do. The owner can override in their next prompt.\n", a.Choice, pr, a.Choice, p.exe)
	}
	p.logLine(s.ID, fmt.Sprintf("triage: route=%s p=%.2f band=hedged", a.Choice, pr))
	return "", fmt.Sprintf("\n[PenguPool triage] Next step: this may fit %s (unsure). You decide: hand it over if it fits, otherwise do it; ask the owner if you cannot tell.\n", a.Choice)
}

// ownerChose reports an owner's prompt that names a member or says "yourself". ponytail: a word
// match, so a member named like an ordinary word ("web") skips the question for prompts that use it.
func ownerChose(prompt string, ids map[string]string) bool {
	if !(turn{Prompt: prompt}).fromOwner() {
		return false // a teammate's message (which names its sender) or a notice is not the owner's choice
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	}) {
		if w == "yourself" {
			return true
		}
		for name := range ids {
			if w == strings.ToLower(name) {
				return true
			}
		}
	}
	return false
}

// routingDesc is what a member is for, not what it was assigned: a helper's skill description, a
// repository agent's name and the first paragraph of its README; the assignment only when neither exists.
func (p *Pool) routingDesc(d *protocol.Domain, m protocol.Member) string {
	if sk := findSkill(d, m.SkillID); sk != nil && sk.Description != "" {
		return sk.Description
	}
	if r := findRepo(d, m.RepoID); r != nil && m.SkillID == "" {
		// ponytail: read at question time, no cache; cache at ingest if repos get large.
		if b, err := os.ReadFile(filepath.Join(r.Path, "README.md")); err == nil {
			var para []string
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "#") {
					if len(para) > 0 {
						break
					}
					continue
				}
				para = append(para, l)
			}
			if len(para) > 0 {
				return clipRunes("repository "+r.Name+": "+strings.Join(para, " "), 300)
			}
		}
		return "repository " + r.Name
	}
	return m.Assignment
}

func redactSecrets(s string) string { return decision.SecretRe.ReplaceAllString(s, "[left out]") }

// lookTools only read or talk: they never do the routed work, so the hold lets them through.
var lookTools = map[string]bool{"Read": true, "Grep": true, "Glob": true, "LS": true, "ToolSearch": true, "ListAgents": true,
	"TodoWrite": true, "AskUserQuestion": true, "WebFetch": true, "WebSearch": true, "SendMessage": true, "read": true, "grep": true, "find": true, "ls": true}

// readOnlyCmd is a shell command that only reads (Codex and pi read through the shell). ponytail: a
// conservative first-word allowlist with no redirects, chaining or substitution, and no flag that
// writes a file or runs a program (rg --pre, find -fprint, git --output); sed is left out, since its
// script can write (w) or run (e). Git runs drivers the repository's config names; widen on need.
func readOnlyCmd(cmd string) bool {
	if cmd == "" || strings.ContainsAny(cmd, ">;&`\n") || strings.Contains(cmd, "$(") {
		return false
	}
	for _, seg := range strings.Split(cmd, "|") {
		f := strings.Fields(seg)
		if len(f) == 0 {
			return false
		}
		for _, a := range f[1:] {
			a = strings.Trim(a, `"'`)
			if strings.HasPrefix(a, "--pre") || strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--ext-diff") ||
				strings.HasPrefix(a, "-exec") || strings.HasPrefix(a, "-ok") || strings.HasPrefix(a, "-fprint") || a == "-fls" || a == "-delete" {
				return false
			}
		}
		switch f[0] {
		case "cat", "rg", "grep", "ls", "find", "head", "tail", "wc":
		case "git":
			if len(f) < 2 || !map[string]bool{"log": true, "show": true, "diff": true, "status": true, "blame": true}[f[1]] {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// sentTo reports a shell command that runs `pengupool send --to <member>`: the --to value only, so a
// message to someone else that mentions the member does not count.
func (p *Pool) sentTo(cmd, name string) bool {
	if !strings.Contains(cmd, "pengupool send") && !strings.Contains(cmd, p.exe+" send") {
		return false
	}
	f := strings.Fields(cmd)
	for i, w := range f {
		to, ok := strings.CutPrefix(w, "--to=")
		if !ok && w == "--to" && i+1 < len(f) {
			to, ok = f[i+1], true
		}
		if ok && strings.EqualFold(strings.Trim(to, `"'`), name) {
			return true
		}
	}
	return false
}

// guardRoute is triage's hold (research-002): in a turn triage firmly routed to a member, the anchor's
// working tools are refused for the whole turn. Only a message to that member releases it (SendMessage
// to its session, or `pengupool send` naming it); reads and read-only shell commands pass, and the
// owner can override in words (that skips the route question). State resets at the next prompt and Stop.
func (p *Pool) guardRoute(s store.Session, pl map[string]any) (HookOut, bool) {
	tool, _ := pl["tool_name"].(string)
	in, _ := pl["tool_input"].(map[string]any)
	cmd, _ := in["command"].(string)
	p.mu.Lock()
	who := p.routed[s.ID]
	p.mu.Unlock()
	if who == "" {
		return HookOut{}, false
	}
	name := who
	if m, _ := p.session(who); m.Name != "" {
		name = m.Name
	}
	if tool == "SendMessage" {
		if to, ok := p.sendTarget(pl); ok && to == who {
			p.release(s.ID, name)
		}
		return HookOut{}, false
	}
	if cmd != "" && p.sentTo(cmd, name) {
		p.release(s.ID, name)
		return HookOut{}, false
	}
	if lookTools[tool] || harness.ReadTool(s.Harness, tool) || readOnlyCmd(cmd) {
		return HookOut{}, false
	}
	p.logLine(s.ID, "triage: held "+tool)
	return hookJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":      "PreToolUse",
		"permissionDecision": "deny",
		"permissionDecisionReason": fmt.Sprintf("PenguPool triage routed this turn's work to %s. Hand it over with SendMessage to %s (or `%s send --to %s \"...\"` in a shell); a brief file holds long context. Reading is allowed meanwhile. The owner can override in their next prompt by telling you to do it yourself.",
			name, name, p.exe, name),
	}}), true
}

func (p *Pool) release(id, name string) {
	p.mu.Lock()
	delete(p.routed, id)
	p.mu.Unlock()
	p.logLine(id, "triage: handed to "+name)
}
