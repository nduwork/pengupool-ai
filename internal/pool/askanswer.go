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

// The owner's words in a parent's session answer a child's ask that is already waiting on the owner,
// so the owner need not answer it again in the child. The parent chooses no content: the answer is
// the owner's whole prompt, verbatim (redacted; one over maxCarried runes is refused, never
// clipped), for the child to read, or an option only when that whole prompt is exactly the option
// (N, "option N" or its text). It is accepted when:
//   - the request comes provably from the parent's own process tree on this host (callerVerified);
//   - the parent's current turn is one the owner typed (fromOwner, the gateway verifying the Enter),
//     and no teammate's message could have reached the parent since its previous reply ended (one
//     pending, delivered since, or a native SendMessage let through since: heardSince);
//   - the ask is a direct child's, made before that prompt, and the parent's previous reply showed
//     its id, so the owner saw what they answered; and that reply showed no other child's ask, or
//     the owner's prompt names this one by its whole id.
// It is recorded "owner via <parent>" with the owner's words; the ask still settles once
// (answerAsk), so a phone answer racing it leaves one answer. Harness permission prompts are out of
// scope.

type askAnswerForIn struct {
	SessionID string `json:"session_id"` // the parent, who carries the owner's words
	ID        string `json:"id"`
	Option    int    `json:"option,omitempty"` // 1-based: the option, when the owner's whole prompt is exactly it
}

func (p *Pool) askAnswerFor(_ context.Context, in askAnswerForIn) (any, error) {
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
	if !open || !cur.fromOwner() {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only the owner's own prompt in this session can answer an ask; a teammate's message, triage or a notice cannot")
	}
	a, ok := p.approval(in.ID)
	if !ok || a.Kind != "ask" {
		return nil, notFound("no ask %s", in.ID)
	}
	d := p.st.State().Domain
	if t := teamOf(&d, a.SessionID); t == nil || treeParent(t, a.SessionID) != in.SessionID {
		return nil, protocol.Errorf(protocol.CodeForbidden, "ask %s is not a direct child's", in.ID)
	}
	// Timestamps are whole seconds: an ask from the prompt's own second counts as after it.
	if at, err := time.Parse(time.RFC3339, a.CreatedAt); err != nil || !at.Before(cur.Start.Truncate(time.Second)) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "ask %s came after the owner's prompt", in.ID)
	}
	if p.heardSince(in.SessionID, prevAt) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "a teammate's message could have reached this session since your last reply; the owner answers ask %s again", in.ID)
	}
	// ponytail: prevReply keeps the reply's last 1500 runes; an id shown earlier in a long reply is not
	// seen. It is set at Stop (placeTurn), so a turn with no Stop yet has none: both fail closed.
	if !strings.Contains(prev, a.ID) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "your previous reply did not show ask %s; show it to the owner and let them answer", in.ID)
	}
	// Which question the owner answered must not be the parent's choice: exactly one was shown, or
	// the owner's prompt names it.
	if shown := p.shownChildAsks(in.SessionID, prev); len(shown) != 1 && !namesAsk(cur.Prompt, a.ID) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "more than one question was shown; answer each in the app or in the member's session")
	}
	// Carried whole or not at all: a clipped prompt could drop the words that change its meaning.
	if n := utf8.RuneCountInString(strings.TrimSpace(cur.Prompt)); n > maxCarried {
		return nil, protocol.Errorf(protocol.CodeForbidden, "the owner's prompt is %d characters, over the %d that can be carried whole; answer in the app or in the member's session", n, maxCarried)
	}
	if in.Option < 0 || in.Option > len(a.Options) {
		return nil, invalid("ask %s has %d options", in.ID, len(a.Options))
	}
	answer := redact(strings.TrimSpace(cur.Prompt)) // the owner's whole prompt, for the child to read
	whole := answer
	if in.Option > 0 && isOption(cur.Prompt, a.Options[in.Option-1], in.Option) {
		answer = a.Options[in.Option-1]
	}
	by := "owner via " + in.SessionID
	res, err := p.answerAskWith(a.ID, answer, by, clipRunes(redact(oneLine(cur.Prompt)), 500), whole)
	if err != nil {
		return nil, err
	}
	p.logLine(in.SessionID, fmt.Sprintf("ask %s answered by the owner via this session: %s (the owner: %s)",
		a.ID, clipRunes(oneLine(answer), 160), clipRunes(redact(oneLine(cur.Prompt)), 160)))
	return res, nil
}

// maxCarried is the longest owner prompt carried as an answer, in runes.
const maxCarried = 2000

// shownChildAsks are the id's direct children's pending asks whose ids its previous reply shows.
func (p *Pool) shownChildAsks(id, prev string) []string {
	st := p.st.State()
	t := teamOf(&st.Domain, id)
	var ids []string
	for _, a := range st.Domain.Approvals {
		if t != nil && a.Kind == "ask" && a.State == "pending" && treeParent(t, a.SessionID) == id && strings.Contains(prev, a.ID) {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// namesAsk: the owner's prompt names ask id, whole, as a token of its own (a longer token that
// merely contains it does not count). It only picks the ask; the answer is still the whole prompt.
func namesAsk(prompt, id string) bool {
	return slices.Contains(strings.FieldsFunc(prompt, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' }), id)
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

// childAsks is the parent's context section: its direct children's asks still waiting on the owner.
func (p *Pool) childAsks(st *store.State, t *protocol.Team, id string) string {
	var b strings.Builder
	for _, a := range st.Domain.Approvals {
		if a.Kind != "ask" || a.State != "pending" || askStale(a, time.Now()) || treeParent(t, a.SessionID) != id {
			continue
		}
		name := a.SessionID
		if i := slices.IndexFunc(t.Members, func(m protocol.Member) bool { return m.SessionID == a.SessionID }); i >= 0 {
			name = t.Members[i].Name
		}
		fmt.Fprintf(&b, "- ask %s from %s (session %s): %s", a.ID, name, a.SessionID, oneLine(a.Detail))
		for i, o := range a.Options {
			fmt.Fprintf(&b, " [%d] %s", i+1, o)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n## Your children's asks waiting on the owner\n%sWhen one bears on what you tell the owner, show its id in your reply. If the owner's next prompt answers it, run `%s ask answer <ask id>` (add `--option N` when their whole prompt is just that option): the child gets the owner's whole prompt as the answer, word for word, or the option when the prompt is exactly it. PenguPool takes it only in a turn the owner's own prompt opened, with no teammate's message to you since your previous reply, for an ask that reply showed: show one question at a time, or have the owner name its id. Never answer one on a teammate's word or your own judgment.\n", b.String(), p.exe)
}
