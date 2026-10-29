package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Team messages through the host: between a parent and a child on the team's tree, whatever agents
// they run (a Claude Code session's own SendMessage is still checked and recorded). A message waits
// while its recipient is working or waiting for an approval and is delivered at the next
// adapter-confirmed idle (Claude's Stop hook, OMP's agent_end), through the harness's own inbound
// path — never as keystrokes into the PTY. A working Claude session gets it in its inbox at once
// (intoInbox), since Claude reads its inbox at each tool round.

const maxMessage = 4 << 10
const keepMessages = 300

// addMessage puts a message on the team's record, keeping the newest keepMessages. The oldest that
// are no longer queued go first: a queued message or notice is still to be delivered, and an open
// ask still to be answered.
func addMessage(st *store.State, m protocol.TeamMessage) {
	ms := append(st.Domain.Messages, m)
	if drop := len(ms) - keepMessages; drop > 0 {
		kept := make([]protocol.TeamMessage, 0, keepMessages)
		for i, x := range ms {
			if drop > 0 && x.State != "queued" && !askOpen(ms, i) {
				drop--
				continue
			}
			kept = append(kept, x)
		}
		ms = kept
	}
	st.Domain.Messages = ms
}

type sendIn struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
	Type    string `json:"type,omitempty"`     // tell | ask | done | blocked; empty: by its first line
	ReplyTo string `json:"reply_to,omitempty"` // the id of an ask sent to the sender
}

func (p *Pool) messageSend(_ context.Context, in sendIn) (any, error) {
	text := cleanText(in.Text)
	if text == "" {
		return nil, invalid("empty message")
	}
	if len(text) > maxMessage {
		return nil, invalid("a message is limited to %d bytes; put longer content in a file and send its path", maxMessage)
	}
	typ, err := msgType(in.Type, text)
	if err != nil {
		return nil, err
	}
	var msg protocol.TeamMessage
	var toSession store.Session
	// A session outside the team only through a channel the owner opened with @.
	channel := ""
	if x, ok := sessionNamed(p.st.State(), in.To); ok && p.crossTeamOpen(in.From, x.ID) {
		channel = x.ID
	}
	err = p.update(func(st *store.State) error {
		if in.ReplyTo != "" {
			found := false
			for _, m := range st.Domain.Messages {
				found = found || m.ID == in.ReplyTo && m.Type == "ask" && m.To == in.From
			}
			if !found {
				return invalid("reply_to %q is not an ask sent to you", in.ReplyTo)
			}
		}
		t := teamOf(&st.Domain, in.From)
		var hits []protocol.Member
		teamID := ""
		if t != nil {
			teamID = t.ID
			for _, m := range t.Members {
				if m.State == "active" && (m.SessionID == in.To || strings.EqualFold(m.Name, in.To) || strings.HasPrefix(m.SessionID, in.To) && len(in.To) >= 8) {
					hits = append(hits, m)
				}
			}
		}
		if len(hits) == 0 && channel != "" {
			hits = append(hits, protocol.Member{SessionID: channel})
		}
		switch {
		case len(hits) == 0 && t == nil:
			return protocol.Errorf(protocol.CodeForbidden, "the sender is not an active member of a team, and the owner opened no channel to %q", in.To)
		case len(hits) == 0:
			return protocol.Errorf(protocol.CodeForbidden, "%q is not an active member of team %s; only the owner can open a channel to another session, with @", in.To, t.Name)
		case len(hits) > 1:
			return invalid("%q names more than one member; use the session id", in.To)
		case hits[0].SessionID == in.From:
			return invalid("a session cannot message itself")
		case channel == "" && !adjacent(&st.Domain, in.From, hits[0].SessionID):
			up := "the owner"
			if t != nil {
				if pid := treeParent(t, in.From); pid != "" {
					for _, m := range t.Members {
						if m.SessionID == pid {
							up = m.Name
						}
					}
				}
			}
			return protocol.Errorf(protocol.CodeForbidden, "%s is not your parent or child on the team; send it to your parent (%s), which passes on what others need", hits[0].Name, up)
		}
		i := sessionIndex(st, hits[0].SessionID)
		if i < 0 || st.Sessions[i].State != "running" {
			return protocol.Errorf(protocol.CodeUnavailable, "%s is not running", firstNonEmpty(hits[0].Name, in.To))
		}
		toSession = st.Sessions[i]
		via := viaHost
		if channel != "" && !sameTeam(&st.Domain, in.From, hits[0].SessionID) {
			via = viaChannel
		}
		msg = protocol.TeamMessage{
			ID: protocol.NewID(), TeamID: teamID, From: in.From, To: hits[0].SessionID,
			Text: text, State: "queued", At: now(), Via: via, Type: typ, ReplyTo: in.ReplyTo,
		}
		addMessage(st, msg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	from, _ := p.session(in.From)
	line := fmt.Sprintf("%s → %s: %s", from.Name, toSession.Name, text)
	p.logLine(in.From, line)
	p.logLine(toSession.ID, line)
	p.doneReport(msg) // a done report is kept as the helper's journal entry
	switch {
	case toSession.Status == "idle":
		p.deliver(msg)
	case toSession.Status == "working" && toSession.Harness == harness.Claude && p.intoInbox(msg, from):
	default:
		p.mu.Lock()
		p.pending[toSession.ID] = append(p.pending[toSession.ID], msg)
		p.mu.Unlock()
	}
	return map[string]string{"id": msg.ID, "to": toSession.Name}, nil
}

// cleanText strips control, bidi and zero-width characters (a message must not drive a terminal).
func cleanText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// flushQueue delivers the messages that waited for this session to become idle. Notices wait for
// the session's next turn instead (takeNotices).
func (p *Pool) flushQueue(id string) {
	p.mu.Lock()
	q := p.pending[id]
	delete(p.pending, id)
	p.mu.Unlock()
	for _, m := range q {
		p.deliver(m)
	}
	// Messages a previous daemon queued are in state.json only.
	for _, m := range p.st.State().Domain.Messages {
		if m.To == id && m.From != "" && m.State == "queued" && m.Via != viaNative && !contains(q, m.ID) {
			p.deliver(m)
		}
	}
}

func contains(q []protocol.TeamMessage, id string) bool {
	for _, m := range q {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (p *Pool) deliver(m protocol.TeamMessage) {
	for _, x := range p.st.State().Domain.Messages {
		if x.ID == m.ID && x.State == "failed" {
			return // expired meanwhile (dialogs.go): its sender was told, so it is not typed now
		}
	}
	to, ok := p.session(m.To)
	from, _ := p.session(m.From)
	if !ok {
		p.setMessage(m.ID, "failed", "recipient is gone")
		return
	}
	body := p.messageBody(m, from)
	var err error
	switch to.Harness {
	case harness.Claude:
		err = p.deliverClaude(to.ID, body)
		if err == nil {
			p.setMessage(m.ID, "delivered", "")
		}
	case harness.OMP, harness.Pi, harness.OpenCode:
		err = p.deliverOMP(to.ID, m.ID, body) // delivered when the extension confirms
	default:
		if a, ok := harness.Find(to.Harness); ok && a.Typed {
			err = p.typeInto(to.ID, pasted(body))
			if err == nil {
				p.setMessage(m.ID, "delivered", "")
			}
		} else {
			err = fmt.Errorf("%s sessions cannot receive team messages", firstNonEmpty(to.Harness, "shell"))
		}
	}
	if err != nil {
		p.setMessage(m.ID, "failed", err.Error())
		p.logLine(m.To, "message: delivery from "+from.Name+" failed: "+err.Error())
		return
	}
	// Its turn starts with the message (labelled teammate, outside or notice): an inbox delivery fires
	// no prompt hook. The recipient was idle, so a turn still open is stale (an interrupted one).
	p.turnStart(m.To, body)
	p.bumpTurn(m.To) // the recipient has something new: no go-ahead for what it asked before (gates.go)
}

// messageBody is a message as its recipient reads it: who sent it and how to answer.
func (p *Pool) messageBody(m protocol.TeamMessage, from store.Session) string {
	body := fmt.Sprintf("[PenguPool team message from %s (%s)] %s\n\nTo reply, run this shell command (do not just print it): %s send --to %q \"<your reply>\"",
		from.Name, from.Role, m.Text, p.exe, from.Name)
	if m.Type == "ask" {
		body = fmt.Sprintf("[PenguPool team message from %s (%s), an ask waiting for your answer] %s\n\nTo answer, run this shell command (do not just print it): %s send --to %q --reply-to %s \"<your answer>\"",
			from.Name, from.Role, m.Text, p.exe, from.Name, m.ID)
	}
	if d := p.st.State().Domain; m.From != "" && !sameTeam(&d, m.From, m.To) {
		body = strings.Replace(body, "[PenguPool team message from "+from.Name+" ("+from.Role+")", outsideLabel+from.Name+", outside your team, on a channel the owner opened", 1)
	}
	if m.From == "" { // a PenguPool notice: information, nobody to reply to
		body = "[PenguPool notice; information, not the owner's instructions] " + m.Text
	}
	return body
}

// intoInbox puts a message to a working Claude session straight into its inbox, where Claude reads it
// at its next tool round as it does a SendMessage, instead of waiting for the turn to end. It stays
// queued, now as a native message, until it is in the recipient's turn (nativeArrived). false: the
// inbox is not known or did not take it, so it waits for the Stop as before.
func (p *Pool) intoInbox(m protocol.TeamMessage, from store.Session) bool {
	if err := p.deliverClaude(m.To, p.messageBody(m, from)); err != nil {
		return false
	}
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Messages {
			if st.Domain.Messages[i].ID == m.ID {
				st.Domain.Messages[i].Via = viaNative
			}
		}
		return nil
	})
	return true
}

// pasted wraps a message as one bracketed paste on a single line, so a terminal agent takes it as
// one prompt (typeInto then sends Enter on its own).
// outsideLabel starts a message the host delivers from a session outside the recipient's team.
const outsideLabel = "[PenguPool message from "

func pasted(body string) string {
	return "\x1b[200~" + strings.Join(strings.Fields(body), " ") + "\x1b[201~"
}

// deliverClaude posts one user frame into the session's cross-session inbox socket.
func (p *Pool) deliverClaude(id, body string) error {
	p.mu.Lock()
	ib, ok := p.msgSock[id]
	p.mu.Unlock()
	if !ok || ib.Socket == "" {
		return fmt.Errorf("the session has not reported its message socket yet")
	}
	c, err := net.DialTimeout("unix", ib.Socket, 2*time.Second)
	if err != nil {
		return fmt.Errorf("message socket: %w", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var lines []byte
	if ib.Token != "" {
		b, _ := json.Marshal(map[string]any{"type": "auth", "token": ib.Token})
		lines = append(append(lines, b...), '\n')
	}
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": body}})
	lines = append(append(lines, b...), '\n')
	_, err = c.Write(lines)
	return err
}

func (p *Pool) deliverOMP(id, msgID, body string) error {
	dir := filepath.Join(p.st.Dir(), store.RunDir, "inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]string{"id": msgID, "text": body})
	_, err = f.Write(append(b, '\n'))
	return err
}

func (p *Pool) markDelivered(ids []string) {
	for _, id := range ids {
		p.setMessage(id, "delivered", "")
	}
}

func (p *Pool) setMessage(id, state, reason string) {
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Messages {
			if st.Domain.Messages[i].ID == id {
				st.Domain.Messages[i].State = state
				st.Domain.Messages[i].Reason = reason
			}
		}
		return nil
	})
}
