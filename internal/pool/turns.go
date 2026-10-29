package pool

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Turns: the record cannot depend on an agent remembering to log. The host follows each agent turn
// through its hooks (the prompt, the tools it ran, its final reply) and, when the turn ends, triage
// places it:
//
//   - the team's goal: an owner's instruction becomes it;
//   - the Compass: the owner's own words are classified (an instruction, a preference, a choice or a
//     correction); all but an instruction are signals that move Compass lines (compass.go match).
//
// Triage is the decision provider (typed choices); without one, simple rules decide and the Compass
// is left alone.

type turn struct {
	Prompt    string
	Tools     int
	PrevReply string // the session's previous turn's reply (set when placed): what a choice picks from
	Start     time.Time
	Seq       uint64 // the session's turn count at its end (gates.go)
	GateDone  bool   // its check-in was settled in the Stop hook (gates.go)
	// Unverified: the prompt looks like the owner's (no PenguPool prefix) but the owner pressed no
	// Enter in this session to send it (ownerinput.go): an agent can post a user turn into its own
	// session. It is treated as a teammate's words, not the owner's.
	Unverified bool
}

// turnStart is the owner's (or a teammate's) prompt opening a turn.
func (p *Pool) turnStart(id, prompt string) { p.turnStartAs(id, prompt, false) }

// turnStartAs is turnStart for a prompt the hook found not to be the owner's after all (unverified).
func (p *Pool) turnStartAs(id, prompt string, unverified bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turns == nil {
		p.turns = map[string]*turn{}
	}
	p.turns[id] = &turn{Prompt: prompt, Start: time.Now(), Unverified: unverified}
}

// turnOpen opens a turn for a session that has none open, with an empty prompt (origin unknown):
// the first tool hook of a turn nothing opened, or an extension's prompt-less signal, which must not
// erase the prompt of a turn a delivered message opened.
func (p *Pool) turnOpen(id, prompt string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turns[id] != nil {
		return
	}
	if p.turns == nil {
		p.turns = map[string]*turn{}
	}
	p.turns[id] = &turn{Prompt: prompt, Start: time.Now()}
}

// turnTool counts a tool run.
func (p *Pool) turnTool(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.turns[id]; t != nil {
		t.Tools++
	}
}

// writesFiles: the tool's name says it changes files (Claude Edit, Codex apply_patch, Gemini write_file).
func writesFiles(tool string) bool {
	tool = strings.ToLower(tool)
	return strings.Contains(tool, "edit") || strings.Contains(tool, "write") || strings.Contains(tool, "patch") || strings.Contains(tool, "replace")
}

// changedFile is the path a file-changing tool (name has edit, write, patch or replace, any case:
// Claude Edit, Codex apply_patch, Gemini write_file) acts on, else "".
func changedFile(pl map[string]any) string {
	if name, _ := pl["tool_name"].(string); !writesFiles(name) {
		return ""
	}
	in, _ := pl["tool_input"].(map[string]any)
	for _, k := range []string{"file_path", "filePath", "notebook_path", "path", "file"} {
		if v, _ := in[k].(string); v != "" {
			return v
		}
	}
	return ""
}

// turnEnd closes the turn and places it, in the background so the hook returns at once. On a team
// in "on" mode whose harness can continue a turn from its Stop hook, the check-in is decided first,
// inline, and the hook's output (triage's go-ahead, or nothing) is returned.
func (p *Pool) turnEnd(s store.Session, reply, transcript string, stopActive bool, deadline time.Time) HookOut {
	p.mu.Lock()
	t := p.turns[s.ID]
	delete(p.turns, s.ID)
	p.mu.Unlock()
	if p.db == nil {
		return HookOut{}
	}
	// A turn no hook opened still did work: recorded, origin unknown. Not a Stop that another hook
	// continued: its turn was recorded at the first Stop.
	if t == nil && !stopActive && strings.TrimSpace(reply) != "" {
		t = &turn{Start: time.Now()}
	}
	seq, done := p.turnEnded(s, t)
	if t == nil {
		return HookOut{}
	}
	tt := *t
	tt.Seq = seq
	var out HookOut
	if stopContinues(s.Harness) {
		out, tt.GateDone = p.stopGate(s, tt, reply, transcript, stopActive, deadline)
	}
	go func() {
		defer close(done) // an owner's reply waiting for this turn's gate goes on
		if reply == "" {
			reply = lastReply(firstNonEmpty(transcript, s.Transcript))
		}
		p.placeTurn(context.Background(), s, tt, reply)
	}()
	return out
}

// turnBroke closes a turn that ended with no Stop (an error or an interrupt): recorded and its open
// prompts expired like a Stop's, with no check-in.
func (p *Pool) turnBroke(s store.Session) {
	p.mu.Lock()
	t := p.turns[s.ID]
	delete(p.turns, s.ID)
	p.mu.Unlock()
	if p.db == nil {
		return
	}
	seq, done := p.turnEnded(s, t)
	if t == nil {
		return
	}
	tt := *t
	tt.Seq, tt.GateDone = seq, true
	go func() {
		defer close(done)
		p.placeTurn(context.Background(), s, tt, "") // no complete reply: none to choose from next
	}()
}

// turnSince reports whether the session's open turn started after at.
func (p *Pool) turnSince(id string, at time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.turns[id]
	return t != nil && t.Start.After(at)
}

// fromOwner: a turn opened by the owner's prompt, not by a delivered team message or notice, nor a
// message from a session outside the team.
func (t turn) fromOwner() bool {
	return t.Prompt != "" && !strings.HasPrefix(t.Prompt, "[PenguPool") && !t.fromOutside() && !t.Unverified
}

// fromOutside: a turn opened by a session outside the team. Like a /btw it is information: it is
// recorded in the open file but never starts a job, sets the task or feeds memory.
// The host labels a message it delivers over such a channel outsideLabel.
func (t turn) fromOutside() bool {
	_, _, ok := outsideMessage(t.Prompt)
	return ok || strings.HasPrefix(t.Prompt, outsideLabel)
}

// fromWording: a turn PenguPool opened to ask for the owner's words to be worded (a notice or a Stop
// continuation). It settles nothing the owner was asked, so their pending check-ins stay open.
func (t turn) fromWording() bool { return strings.HasPrefix(t.Prompt, "[PenguPool compass]") }

// fromNotice: a PenguPool notice or a message from outside the team, information rather than a request.
func (t turn) fromNotice() bool {
	return t.fromOutside() || strings.HasPrefix(t.Prompt, "[PenguPool notice")
}

type placement struct {
	Record      bool
	Signal      string // the owner's message: instruction | preference | choice | correction; "" when not classified
	ChangesPref bool   // a correction that changes how the owner wants things in general
	StatesPref  bool   // the message, whatever else it is (a task), states how the owner wants things in general
}

const leftOut = "[left out: looked like a secret]"

func (p *Pool) placeTurn(ctx context.Context, s store.Session, t turn, reply string) {
	if strings.TrimSpace(t.Prompt) == "" && strings.TrimSpace(reply) == "" {
		return
	}
	p.mu.Lock()
	t.PrevReply = p.prevReply[s.ID]
	if p.prevReply == nil {
		p.prevReply = map[string]string{}
	}
	p.prevReply[s.ID] = tailRunes(reply, 1500) // all a choice reads of it
	p.mu.Unlock()
	d := p.st.State().Domain
	team := teamOf(&d, s.ID)
	var gate map[string]any // triage also asks whether the turn ends waiting on the owner
	if team != nil && !t.GateDone && strings.TrimSpace(reply) != "" {
		gate = p.gateState(s, team, reply)
	}
	pl, res := p.triage(ctx, s, t, reply, gate)
	if gate != nil && res != nil {
		var deliver func(string) error // only the harnesses that take it as a user message get it after the turn
		if sendsGoAhead(s.Harness) {
			deliver = func(text string) error {
				if cur, _ := p.session(s.ID); cur.Status != "idle" {
					return errors.New("the session is working again")
				}
				return p.sendGoAhead(s.ID, text)
			}
		}
		p.gateTurn(s, team, reply, t.Seq, *res, deliver)
	}
	if team != nil && p.helperOf(s.ID) == nil && pl.Record && isInstruction(t) { // the team's goal follows the owner's latest instruction
		p.setTask(team.ID, t.Prompt)
	}
	if t.fromOwner() {
		p.ownerSaid(s, team, t, pl)
	}
}

// standingRE is a sentence that states a standing preference whatever triage made of it: a small
// model reads "if the risk is low, just approve for me" as a task, or times out. It asks for a rule's
// shape (a time span, an order to the agents, a condition with a decision handed over), not a word:
// "fix it so it never returns nil" is a task. ponytail: English phrases only; triage finds the rest.
var standingRE = regexp.MustCompile(`(?i)(\b(from now on|going forward|from here on|in (the )?future)\b|^\s*(please\s+)?(always|never)\s+(ask|check|approve|use|run|merge|commit|push|wait|stop|touch|edit|skip)\b|\b(you|agents?|helpers?)\s+(should\s+)?(always|never)\b|\b(do not|don't|no need to)\s+(ask|check with)\s+me\b|\b(whenever|if)\b[^.!?]{0,80}\b(approve|decide|choose|accept|merge|go ahead)\b[^.!?]{0,20}\bfor me\b|\bremember (my|this) (choice|preference)\b|\b(if|when|whenever)\b[^.!?]{0,80}\b(agrees?|approves?|passes|is green)\b[^.!?]{0,40}\b(you can|go ahead|feel free)\b)`)

// sentenceRE ends a sentence; unlike sentenceEnd it keeps "From now on: …" whole.
var sentenceRE = regexp.MustCompile(`[.!?]+(\s+|$)`)

// standingClause is the owner's sentence that states a standing preference, "" when none does.
func standingClause(prompt string) string {
	s := strings.Join(strings.Fields(strings.NewReplacer("’", "'", "‘", "'").Replace(prompt)), " ")
	start := 0
	for _, end := range append(sentenceRE.FindAllStringIndex(s, -1), []int{len(s), len(s)}) {
		part, question := s[start:end[0]], strings.Contains(s[end[0]:end[1]], "?")
		start = end[1]
		if !question && standingRE.MatchString(part) { // a question is no rule
			return strings.TrimSpace(part)
		}
	}
	return ""
}

// ownerSaid acts on what the owner's prompt is: a correction, when it changes a preference, becomes a signal; a preference or a choice is a signal, and so
// is an instruction worded as a standing one (standingRE). A choice that answered a recorded
// check-in is a signal already (learnDecision).
func (p *Pool) ownerSaid(s store.Session, team *protocol.Team, t turn, pl placement) {
	sig := agentdb.Signal{Words: clip(oneLine(redact(t.Prompt)), 240), SessionID: s.ID}
	if team != nil {
		sig.TeamID = team.ID
	}
	if p.decider() == nil { // no triage: only a standing sentence is worth the session's agent wording
		if !p.answeredCheckin(s, t.Prompt) && p.standing(s, &sig, t, "no triage") {
			p.match(sig, "", "")
		}
		return
	}
	offered := ""
	switch pl.Signal {
	case "preference":
		sig.Kind = "said"
	case "choice":
		if p.answeredCheckin(s, t.Prompt) {
			return
		}
		sig.Kind, offered = "chose", tailRunes(redact(t.PrevReply), 800)
	case "correction":
		// Right after a go-ahead nobody else answered, it corrects that go-ahead.
		if g, err := p.db.LastGate(s.ID, "checkin"); err == nil && g.Outcome == "auto_proceeded" && (g.OwnerAnswer == "" || g.OwnerAnswer == gateText(t.Prompt, 1500)) {
			p.wrongGoAhead(g)
		}
		if !pl.ChangesPref {
			if !p.standing(s, &sig, t, "a one-off correction") {
				return
			}
			break
		}
		sig.Kind = "correction"
	default: // an instruction, or triage gave no answer
		if !p.standing(s, &sig, t, cmp.Or(pl.Signal, "no answer")) {
			if !pl.StatesPref { // a task that also states a rule: the agent that words it finds the rule in the whole message
				return
			}
			sig.Kind = "said"
			p.logLine(s.ID, "compass: the owner's task also reads as stating a preference")
		}
	}
	p.match(sig, offered, "")
}

// standing makes sig a stated signal of the prompt's standing-preference sentence, whatever triage
// read: its words are that sentence, so a long task before it cannot push it out of the clip.
func (p *Pool) standing(s store.Session, sig *agentdb.Signal, t turn, triage string) bool {
	c := standingClause(t.Prompt)
	if c == "" {
		return false
	}
	sig.Kind, sig.Words = "said", clip(oneLine(redact(c)), 240)
	p.logLine(s.ID, "compass: the owner's words read as a standing preference (triage: "+triage+")")
	return true
}

// answeredCheckin: the prompt is the owner's answer to the session's newest check-in, which
// ownerReplied has claimed and, once settled, made a signal of. A go-ahead's claim counts only
// when its line is set, so a new instruction after it still counts.
// ponytail: a go-ahead answered before its line is set is counted twice; sync them if it shows.
func (p *Pool) answeredCheckin(s store.Session, prompt string) bool {
	g, err := p.db.LastGate(s.ID, "checkin")
	return err == nil && g.OwnerAnswer == gateText(prompt, 1500) && (g.Outcome == "owner" || g.Outcome == "auto_proceeded" && g.LineUID != "")
}

// setTask makes the owner's latest instruction the team's current task: a group's work evolves
// with what the owner asks, so there is nothing to create or close.
func (p *Pool) setTask(teamID, instruction string) {
	task := clip(oneLine(instruction), 600)
	_ = p.update(func(st *store.State) error {
		if t := findTeam(&st.Domain, teamID); t != nil && t.State == "active" {
			t.Goal = task
		}
		return nil
	})
}

// triage decides where a turn goes. With a provider it asks typed choices (record and, for the owner's prompt, its kind);
// without one, or when it fails, the rules record turns that ran tools or
// answered at length and leave the Compass alone.
// With gate (state for an autonomous team's turn), the pack also asks checkInQuestions, and the
// response is returned for gateTurn; it is nil when triage had no answers.
func (p *Pool) triage(ctx context.Context, s store.Session, t turn, reply string, gate map[string]any) (placement, *decision.Response) {
	rule := placement{Record: t.Tools > 0 || len(reply) >= 400}
	if t.fromNotice() && t.Tools == 0 { // information that led to no work: not on the record
		rule.Record = false
	}
	prov := p.decider()
	if prov == nil {
		return rule, nil
	}
	qs := []decision.Question{
		{Name: "record", Type: decision.Choice, Instructions: "Does this agent turn belong in the work log?", Options: map[string]string{
			"record": "The turn did, decided or discovered real work worth keeping: changes, findings, results, decisions, discarded attempts.",
			"skip":   "Small talk, a short answer with no work done, or a trivial step.",
		}},
	}
	owner := ""
	if t.fromOwner() {
		owner = clip(redact(t.Prompt), 2000)
		qs = append(qs, ownerSignalQuestions...)
	}
	state := map[string]any{ // redacted before the clips, so a clip cannot leave a fragment the provider no longer recognises
		"agent": s.Name, "owner_message": owner, "teammate_message": map[bool]string{true: clip(redact(request(t.Prompt)), 2000)}[t.Prompt != "" && !t.fromOwner()],
		"agent_reply": clip(redact(reply), 3000), "tools_run": t.Tools,
	}
	if t.fromOwner() {
		state["options_offered_before"] = tailRunes(redact(t.PrevReply), 1500)
	}
	if gate != nil {
		maps.Copy(state, gate)
		qs = append(qs, checkInQuestionsFor(gate)...)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := prov.Decide(ctx, decision.Request{State: state, Questions: qs})
	if err != nil {
		p.logf("pool: triage of %s's turn: %v (rules used)", s.Name, err)
		return rule, nil
	}
	sure := func(a decision.Answer) bool { return !res.Calibrated || a.Confidence >= 0.6 }
	out := rule
	if a, ok := res.Answers["record"]; ok && sure(a) {
		out.Record = a.Choice == "record"
	}
	if a, ok := res.Answers["owner_signal"]; ok && sure(a) && ownerSignalQuestions[0].Options[a.Choice] != "" {
		out.Signal = a.Choice
		if c, ok := res.Answers["changes_preference"]; ok && sure(c) {
			out.ChangesPref = c.Yes >= 0.5
		}
	}
	if a, ok := res.Answers["states_preference"]; ok && sure(a) { // a task can state a rule too
		out.StatesPref = a.Yes >= 0.5
	}
	if t.fromNotice() && t.Tools == 0 {
		out.Record = false
	}
	return out, &res
}

// ownerSignalQuestions classify the owner's own message (C2); the agent's reasoning is never evidence.
var ownerSignalQuestions = []decision.Question{
	{Name: "owner_signal", Type: decision.Choice, Instructions: "Read only the owner's message, and for a choice the options in options_offered_before (the agent's previous reply); never the agent's reasoning as evidence. What is the owner's message?", Options: map[string]string{
		"instruction": "A description or a task only.",
		"preference":  "States how the owner wants things in general.",
		"choice":      "Picks between options the agent offered.",
		"correction":  "Says something the agents did, or decided for the owner, was off.",
	}},
	{Name: "changes_preference", Type: decision.Predicate, Instructions: "Read only the owner's message. Does this correction change how the owner wants things in general, rather than only fix this one case?"},
	// Asked of a rule written as a command, the earlier wording ("how the owner wants things in general")
	// read "when X, do Y" as a task; this one separates rules from one-off tasks on the owner's model.
	{Name: "states_preference", Type: decision.Predicate, Instructions: `Read only the owner's message. Does it set a rule for a kind of situation that will come up again, so agents should act this way every time, not only now? A rule written as a command counts: "when X, do Y", "whenever…", "if X, then Y", "always/never…", "from now on…". A task about this one piece of work (even one with a condition, like "when the tests pass, merge this PR") does not.`},
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// lastReply is the text of the agent's last reply in a Claude transcript (JSONL), read from its end.
func lastReply(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 1<<20 {
		_, _ = f.Seek(st.Size()-1<<20, io.SeekStart)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	var last string
	for sc.Scan() {
		var l struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		// Claude: {"type":"assistant",...}; the omp/pi session file: {"type":"message","message":{"role":"assistant",...}}.
		if json.Unmarshal(sc.Bytes(), &l) != nil || !(l.Type == "assistant" || l.Type == "message" && l.Message.Role == "assistant") {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		var plain string
		if json.Unmarshal(l.Message.Content, &plain) == nil { // content may be a plain string
			blocks = append(blocks, struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{"text", plain})
		} else if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		var text []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				text = append(text, b.Text)
			}
		}
		if len(text) > 0 {
			last = strings.Join(text, "\n")
		}
	}
	return last
}
