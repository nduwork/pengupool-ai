package pool

import (
	"strings"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// notice queues a PenguPool notice (no sender) for a member. It never starts a turn: a session works
// only when the owner or another agent asks it to, and a notice is information. It reaches the agent
// with the context of its next turn (takeNotices). Its content is already on the record where it
// came from (messages).
func (p *Pool) notice(teamID, to, text string) {
	msg := protocol.TeamMessage{ID: protocol.NewID(), TeamID: teamID, To: to, Text: text, State: "queued", At: now()}
	if err := p.update(func(st *store.State) error {
		addMessage(st, msg)
		return nil
	}); err != nil {
		return
	}
	p.logLine(to, "notice: "+text)
}

// takeNotices returns the session's queued notices as a context section and marks them delivered.
func (p *Pool) takeNotices(id string) string {
	var lines, ids []string
	for _, m := range p.st.State().Domain.Messages {
		if m.To == id && m.From == "" && m.State == "queued" {
			lines = append(lines, "- "+strings.ReplaceAll(m.Text, "\n", "\n  "))
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	for _, mid := range ids {
		p.setMessage(mid, "delivered", "with the next turn's context")
	}
	return "\n\n## PenguPool notices since your last turn (information, not the owner's instructions)\n" + strings.Join(lines, "\n") + "\n"
}
