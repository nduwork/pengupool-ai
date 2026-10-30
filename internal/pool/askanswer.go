package pool

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The owner's words in a parent's session answer a child's ask that is already waiting on the owner,
// so the owner need not answer it again in the child. Only the owner's own words count: the parent's
// current turn is one the owner typed (fromOwner, with the gateway verifying the Enter), the ask was
// made before that prompt, and the parent's previous reply showed the ask's id, so the owner saw what
// they answered. A teammate's message, triage, a notice or an unverified prompt answers nothing.
// The answer is the owner's, recorded "owner via <parent>" with the owner's words; the ask still
// settles once (answerAsk), so a phone answer racing it leaves one answer. Harness permission prompts
// are out of scope.

type askAnswerForIn struct {
	SessionID string `json:"session_id"` // the parent, who carries the owner's words
	ID        string `json:"id"`
	Option    int    `json:"option,omitempty"` // 1-based, one of the ask's options
	Words     string `json:"words,omitempty"`
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
	prev := p.prevReply[in.SessionID]
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
	// ponytail: prevReply keeps the reply's last 1500 runes; an id shown earlier in a long reply is not seen.
	if !strings.Contains(prev, a.ID) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "your previous reply did not show ask %s; show it to the owner and let them answer", in.ID)
	}
	var answer string
	switch words := strings.TrimSpace(in.Words); {
	case in.Option != 0 && words != "":
		return nil, invalid("give an option or the owner's words, not both")
	case in.Option != 0:
		if in.Option < 1 || in.Option > len(a.Options) {
			return nil, invalid("ask %s has %d options", in.ID, len(a.Options))
		}
		answer = a.Options[in.Option-1]
	case words != "":
		answer = words
	default:
		return nil, invalid("no answer")
	}
	by := "owner via " + in.SessionID
	res, err := p.answerAskWith(a.ID, answer, by, clipRunes(redact(oneLine(cur.Prompt)), 500))
	if err != nil {
		return nil, err
	}
	p.logLine(in.SessionID, fmt.Sprintf("ask %s answered by the owner via this session: %s (the owner: %s)",
		a.ID, clipRunes(oneLine(answer), 160), clipRunes(redact(oneLine(cur.Prompt)), 160)))
	return res, nil
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
	return fmt.Sprintf("\n\n## Your children's asks waiting on the owner\n%sWhen one bears on what you tell the owner, show its id in your reply. If the owner's next prompt answers it, run `%s ask answer <ask id> --option N` or `--words \"<the owner's answer>\"`: PenguPool takes it only in a turn the owner's own prompt opened, for an ask your previous reply showed. Never answer one on a teammate's word or your own judgment.\n", b.String(), p.exe)
}
