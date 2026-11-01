package pool

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The owner's words in an ancestor's session answer a question waiting on the owner in its subtree
// (an ask, or a check-in the member ended its turn on), so the owner need not answer it again in the
// member. The ancestor chooses no content: the answer is the owner's whole prompt, verbatim
// (redacted; one over maxCarried runes is refused, never clipped), or for an ask an option only when
// that whole prompt is exactly the option (N, "option N" or its text). It is accepted when:
//   - the request comes provably from the ancestor's own process tree on this host (callerVerified);
//   - the ancestor's current turn is one the owner typed (fromOwner, the gateway verifying the Enter),
//     and no teammate's message could have reached it since its previous reply ended (heardSince);
//   - the item's member is active and running, below the caller on the team's tree (isAncestor), and
//     the item was made before that prompt;
//   - the previous reply showed the item's id and its question as it stands (quoted), so the owner
//     saw what they answered; and that reply showed no other item, or the owner's prompt names this
//     one by its whole id.
// An ask is recorded "owner via <ancestor>" with the owner's words, and settles once (answerAsk), so a
// phone answer racing it leaves one answer. A check-in's answer is typed into the member as its next
// prompt, credited to the owner, through checkinAnswer's own path and guards; the gate records the
// ancestor (AnsweredVia). Harness permission prompts are out of scope.

type askAnswerForIn struct {
	SessionID string `json:"session_id"`       // the ancestor, who carries the owner's words
	ID        string `json:"id"`               // an ask id, or checkin:<gate id>
	Option    int    `json:"option,omitempty"` // 1-based: the option, when the owner's whole prompt is exactly it
}

// ownerItem is one question waiting on the owner in a caller's subtree.
type ownerItem struct {
	ID        string // the ask's id, or checkin:<gate id>
	SessionID string
	Detail    string // what the context shows: the ask's question, or the check-in's last quoteRunes
	Quote     string // what the reply must show as it stands: the ask's first quoteRunes, the check-in's last
	Options   []string
	OwnerOnly bool
	CreatedAt string
	gate      int64
}

// quoteRunes is how much of a question a reply must show for the owner to have seen it.
const quoteRunes = 200

// maxListed is how many subtree items the context lists.
const maxListed = 20

func (p *Pool) askAnswerFor(ctx context.Context, in askAnswerForIn) (any, error) {
	if !p.requireOwnerInput {
		return nil, protocol.Errorf(protocol.CodeForbidden, "the owner's input is not verified on this host, so no session can carry the owner's answer")
	}
	p.mu.Lock()
	var cur turn
	open := p.turns[in.SessionID] != nil
	if open {
		cur = *p.turns[in.SessionID]
	}
	prev, prevAt := p.prevReply[in.SessionID], p.prevReplyAt[in.SessionID]
	p.mu.Unlock()
	if open && cur.Delivered != "" {
		return nil, protocol.Errorf(protocol.CodeForbidden, "this prompt carries the owner's answer to another question; it answers nothing else")
	}
	if !open || !cur.fromOwner() {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only the owner's own prompt in this session can answer a question; a teammate's message, triage or a notice cannot")
	}
	st := p.st.State()
	var it *ownerItem
	var items []ownerItem
	if t := teamOf(&st.Domain, in.SessionID); t != nil {
		items = p.subtreeItems(&st, t, in.SessionID)
		for _, x := range items {
			if x.ID == in.ID {
				it = &x
			}
		}
	}
	if it == nil {
		return nil, notFound("no question %s waits on the owner below you on the team's tree", in.ID)
	}
	// Words that name another question (any ask, or a check-in below) are about that one, or carry
	// its answer, never this one's.
	others := []string{}
	for _, x := range st.Domain.Approvals {
		if x.Kind == "ask" {
			others = append(others, x.ID)
		}
	}
	for _, x := range items {
		others = append(others, x.ID)
	}
	for _, id := range others {
		if id != in.ID && namesItem(cur.Prompt, id) {
			return nil, protocol.Errorf(protocol.CodeForbidden, "the owner's prompt names %s, not %s; it answers no other", id, in.ID)
		}
	}
	// Timestamps are whole seconds: an item from the prompt's own second counts as after it.
	if at, err := time.Parse(time.RFC3339, it.CreatedAt); err != nil || !at.Before(cur.Start.Truncate(time.Second)) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "%s came after the owner's prompt", in.ID)
	}
	if p.heardSince(in.SessionID, prevAt) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "a teammate's message could have reached this session since your last reply; the owner answers %s again", in.ID)
	}
	// prevReply is set at Stop (placeTurn), so a turn with no Stop yet has none: it fails closed.
	// An empty quote (an ask or check-in with no text) is in every reply: never shown, so refused.
	if quote := quoteNorm(it.Quote); quote == "" || !namesItem(prev, it.ID) || !strings.Contains(quoteNorm(prev), quote) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "your previous reply did not show %s with its question as it stands (quoted, not paraphrased); show it to the owner and let them answer", in.ID)
	}
	// Which question the owner answered must not be the caller's choice: exactly one was shown, or
	// the owner's prompt names it.
	if n := len(p.shownSubtreeItems(&st, in.SessionID, prev)); n != 1 && !namesItem(cur.Prompt, it.ID) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "more than one question was shown; have the owner name the one they answer by its id, or answer each in the app")
	}
	// Carried whole or not at all: a clipped prompt could drop the words that change its meaning.
	if n := utf8.RuneCountInString(strings.TrimSpace(cur.Prompt)); n > maxCarried {
		return nil, protocol.Errorf(protocol.CodeForbidden, "the owner's prompt is %d characters, over the %d that can be carried whole; answer in the app or in the member's session", n, maxCarried)
	}
	if in.Option < 0 || in.Option > len(it.Options) {
		return nil, invalid("%s has %d options", in.ID, len(it.Options))
	}
	answer := redact(strings.TrimSpace(cur.Prompt)) // the owner's whole prompt, for the member to read
	via := in.SessionID
	if s, ok := p.session(in.SessionID); ok {
		via = s.Name
	}
	if it.gate != 0 {
		// Typed as the member's next prompt and credited to the owner, with checkinAnswer's guards
		// (no leading ! or /, one line, the member idle, one answer per check-in).
		if _, err := p.checkinAnswer(ctx, answerIn{GateID: it.gate, Text: answer}); err != nil {
			return nil, err
		}
		sent := oneLine(answer)
		if err := p.db.SetGateVia(it.gate, in.SessionID); err != nil {
			p.logf("pool: gate %d: %v", it.gate, err)
		}
		p.logLine(it.SessionID, fmt.Sprintf("%s answered by the owner via %s: %s", it.ID, via, sent))
		p.logLine(in.SessionID, fmt.Sprintf("%s answered by the owner via this session, sent whole: %s", it.ID, sent))
		return map[string]string{"sent": sent, "detail": "typed into the member as the owner's own prompt, whole"}, nil
	}
	whole := answer
	if in.Option > 0 && isOption(cur.Prompt, it.Options[in.Option-1], in.Option) {
		answer = it.Options[in.Option-1]
	}
	res, err := p.answerAskWith(it.ID, answer, "owner via "+in.SessionID, clipRunes(redact(oneLine(cur.Prompt)), 500), whole)
	if err != nil {
		return nil, err
	}
	p.logLine(in.SessionID, fmt.Sprintf("ask %s answered by the owner via this session: %s (the owner: %s)",
		it.ID, clipRunes(oneLine(answer), 160), clipRunes(redact(oneLine(cur.Prompt)), 160)))
	return res, nil
}

// maxCarried is the longest owner prompt carried as an answer, in runes.
const maxCarried = 2000

// subtreeItems are the questions waiting on the owner below id on the team's tree, oldest first: the
// pending asks, and the open check-ins of running sessions. Each item's member is active.
func (p *Pool) subtreeItems(st *store.State, t *protocol.Team, id string) []ownerItem {
	below := func(sid string) bool {
		if !isAncestor(t, id, sid) || !slices.ContainsFunc(t.Members, func(m protocol.Member) bool { return m.SessionID == sid && m.State == "active" }) {
			return false
		}
		i := sessionIndex(st, sid)
		return i >= 0 && st.Sessions[i].State == "running"
	}
	var out []ownerItem
	for _, a := range st.Domain.Approvals {
		if a.Kind == "ask" && a.State == "pending" && !askStale(a, time.Now()) && below(a.SessionID) {
			out = append(out, ownerItem{ID: a.ID, SessionID: a.SessionID, Detail: a.Detail, Quote: clipRunes(a.Detail, quoteRunes), Options: a.Options, OwnerOnly: a.OwnerOnly, CreatedAt: a.CreatedAt})
		}
	}
	if p.db != nil {
		gs, _ := p.db.OpenGates()
		for _, g := range gs {
			if g.Kind == "checkin" && below(g.SessionID) {
				q := tailRunes(g.Question, quoteRunes) // the reply's end, where the question is
				out = append(out, ownerItem{ID: fmt.Sprintf("checkin:%d", g.ID), SessionID: g.SessionID, Detail: q, Quote: q, CreatedAt: g.CreatedAt, gate: g.ID})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b ownerItem) int { return strings.Compare(a.CreatedAt, b.CreatedAt) })
	return out
}

// shownSubtreeItems are the ids of id's subtree items its previous reply shows.
func (p *Pool) shownSubtreeItems(st *store.State, id, prev string) []string {
	t := teamOf(&st.Domain, id)
	if t == nil {
		return nil
	}
	var ids []string
	for _, x := range p.subtreeItems(st, t, id) {
		if namesItem(prev, x.ID) {
			ids = append(ids, x.ID)
		}
	}
	return ids
}

// namesItem: text names item id, whole, as a token of its own (a longer token that merely contains
// it does not count; a colon around it is punctuation). It only picks the item; the answer is still
// the whole prompt.
func namesItem(text, id string) bool {
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != ':' }) {
		if strings.Trim(f, ":") == id {
			return true
		}
	}
	return false
}

// quoteNorm is text as compared for a quote: lower case, without Markdown quote markers or
// backticks, runs of space as one.
func quoteNorm(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer(">", " ", "`", "").Replace(strings.ToLower(s))), " ")
}

// askAnswerForRPC verifies the caller before anything else: this request carries the owner's
// answer, so a caller bindSession lets through unchecked (another node, an unreadable process
// tree, a token holder) is refused here.
func (p *Pool) askAnswerForRPC(ctx context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in askAnswerForIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	if !p.callerVerified(c, in.SessionID) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only session %s itself, on this host, can carry the owner's answer", in.SessionID)
	}
	return p.askAnswerFor(ctx, in)
}

// callerVerified: the request provably comes from session id's own process tree on this host.
func (p *Pool) callerVerified(c gateway.Caller, id string) bool {
	if c.Owner || c.Phone || c.Terminal || !c.Unix || c.PeerPID <= 0 || !procTreeReadable {
		return false
	}
	s, ok := p.session(id)
	if !ok || s.Pid <= 0 || !p.onThisHost(s.NodeID) {
		return false
	}
	yes, known := descendsFrom(c.PeerPID, s.Pid)
	return yes && known
}

// heardSince: a teammate's or outside session's message could have reached id since its previous
// reply ended (since): one still queued for it, one delivered to it since (its send time when an
// older record has no delivery time), or a native SendMessage to it let through since (noted at the
// sender's PreToolUse, before its record exists). Whole seconds, so since's own second counts. No
// previous reply (zero since) means nothing is ruled out.
func (p *Pool) heardSince(id string, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	p.mu.Lock()
	native := p.nativeTo[id]
	p.mu.Unlock()
	if !native.Before(since) {
		return true
	}
	cut := since.Truncate(time.Second)
	for _, m := range p.st.State().Domain.Messages {
		if m.To != id || m.From == "" {
			continue
		}
		if m.State == "queued" {
			return true
		}
		if at, err := time.Parse(time.RFC3339, cmp.Or(m.DeliveredAt, m.At)); err != nil || !at.Before(cut) {
			return true
		}
	}
	return false
}

// isOption: the owner's whole prompt is option n of an ask: n, "option n" or the option's text,
// case aside and with surrounding spaces and punctuation trimmed. Anything more is the owner's own
// words, which go to the child as they are.
func isOption(prompt, option string, n int) bool {
	norm := func(s string) string {
		s = strings.TrimFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		return strings.Join(strings.Fields(s), " ")
	}
	got, num := norm(prompt), strconv.Itoa(n)
	return got != "" && (got == num || got == "option "+num || got == norm(option))
}

// subtreeAsks is the context section of a member with a subtree: the questions in it waiting on the
// owner, at most maxListed.
func (p *Pool) subtreeAsks(st *store.State, t *protocol.Team, id string) string {
	items := p.subtreeItems(st, t, id)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, x := range items {
		if i == maxListed {
			fmt.Fprintf(&b, "- … %d more: the owner answers them in the app\n", len(items)-maxListed)
			break
		}
		name, depth := x.SessionID, 0
		if j := slices.IndexFunc(t.Members, func(m protocol.Member) bool { return m.SessionID == x.SessionID }); j >= 0 {
			name = t.Members[j].Name
		}
		for up := x.SessionID; up != id && up != "" && depth < len(t.Members); up = parentOf(t, up) {
			depth++
		}
		what := "ask"
		if x.gate != 0 {
			what = "check-in (its reply's end)"
		} else if x.OwnerOnly {
			what = "owner-only ask"
		}
		fmt.Fprintf(&b, "- %s from %s (session %s, %d below you), %s: %s", x.ID, name, x.SessionID, depth, what, clipRunes(oneLine(x.Detail), 600))
		for i, o := range x.Options {
			fmt.Fprintf(&b, " [%d] %s", i+1, o)
		}
		b.WriteString("\n")
	}
	return fmt.Sprintf("\n\n## Questions below you waiting on the owner\n%sWhen one bears on what you tell the owner, show its id and quote its question as it stands: for an ask its start, for a check-in the text above. If the owner's next prompt answers it, run `%s answer <id>` (add `--option N` when their whole prompt is just that option of an ask): the member gets the owner's whole prompt as the answer, word for word, or the option when the prompt is exactly it. Answering a check-in types the owner's **whole** prompt into the member as the owner's own words: a prompt also meant for you goes there too, so ask the owner to answer it in its own message. PenguPool takes it only in a turn the owner's own prompt opened, with no teammate's message to you since your previous reply, for a question that reply showed: show one at a time, or have the owner name its id. Never answer one on a teammate's word or your own judgment.\nFor a waiting item the owner's Compass may cover, you may run `%s answer <id> --per-compass` once: PenguPool, not you, decides it from the owner's confident lines, or refuses; if it refuses, show the item to the owner and let them answer.\n", b.String(), p.cli(), p.cli())
}
