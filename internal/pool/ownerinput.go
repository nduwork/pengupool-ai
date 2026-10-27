package pool

import (
	"strings"
	"time"
	"unicode"

	"github.com/nduwork/pengupool-ai/internal/harness"
)

// Who typed a prompt. A user turn reaches an agent two ways PenguPool cannot tell apart by its text: the
// owner pressing Enter in the session's terminal, and any process of the account posting a user frame
// to the agent's own messaging socket (the socket and its token are in the agent's shell environment).
// So an unprefixed prompt is the owner's only when the owner typed that very text and pressed Enter in
// that session through the app or the owner's terminal (the gateway knows: only they present the
// host's token, and not from inside an agent), shortly before. Each submission pays for one prompt of
// the same text, so an agent can post nothing the owner did not type.
// ponytail: a prompt typed outside PenguPool (Claude's own remote control, the phone), recalled with
// the arrow keys, completed from a menu or edited with cursor keys pays nothing and is treated as a
// teammate's words: it teaches the Compass nothing until it is typed in the app.

const (
	ownerEnterTTL = 30 * time.Minute // a prompt can wait that long behind a running turn
	maxCredits    = 20
)

// credit is one prompt the owner submitted in a session: when, and the text they typed.
type credit struct {
	at   time.Time
	text string
}

// squash makes two spellings of a prompt comparable: lower case, no whitespace.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// OwnerInput is the gateway's word that the owner submitted a prompt in a session, with the text they
// typed or pasted (gateway/typed.go: an Enter with nothing typed, inside a paste, or after ESC or a
// backslash is none). A prompt answering a permission dialog, or typed while the agent is still
// starting, is not a prompt for the agent.
func (p *Pool) OwnerInput(sessionID, text string) {
	if s, ok := p.session(sessionID); ok && (s.Status == "approval" || s.Status == "starting") {
		return
	}
	p.mu.Lock()
	asking := p.askingOwner[sessionID]
	p.mu.Unlock()
	if asking {
		return // what the owner types into a question or plan feedback goes back to the agent as the tool's result
	}
	text = squash(text)
	if text == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ownerEnter == nil {
		p.ownerEnter = map[string][]credit{}
	}
	q := append(p.ownerEnter[sessionID], credit{time.Now(), text})
	if len(q) > maxCredits {
		q = q[len(q)-maxCredits:]
	}
	p.ownerEnter[sessionID] = q
}

// ownerSent consumes the owner's credit for this prompt: one the owner submitted in this session, with
// the same text, in the last half hour. A user turn the owner did not type (an agent posting one into its
// own session) matches none, whatever else the owner typed. Where the daemon has no gateway to say
// (tests, the offline evaluation), every prompt is believed.
func (p *Pool) ownerSent(sessionID, prompt string) bool {
	if !p.requireOwnerInput {
		return true
	}
	want := squash(prompt)
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.ownerEnter[sessionID]
	for len(q) > 0 && time.Since(q[0].at) > ownerEnterTTL {
		q = q[1:]
	}
	for i, c := range q {
		if c.text == want {
			p.ownerEnter[sessionID] = append(q[:i:i], q[i+1:]...)
			return true
		}
	}
	if len(q) == 0 {
		delete(p.ownerEnter, sessionID)
	} else {
		p.ownerEnter[sessionID] = q
	}
	return false
}

// InAgentSession: the process runs inside an agent session's process tree (the gateway refuses the owner's
// token from such a process). Where the tree cannot be read, it is taken to be inside.
func (p *Pool) InAgentSession(pid int) bool {
	for _, s := range p.st.State().Sessions {
		if s.State != "running" || s.Pid <= 0 || !p.onThisHost(s.NodeID) {
			continue
		}
		if _, agent := harness.Find(s.Harness); !agent {
			continue
		}
		if yes, known := descendsFrom(pid, s.Pid); yes || !known && procTreeReadable {
			return true
		}
	}
	return false
}

// asking marks a session while a tool that asks the owner something is open (Claude's AskUserQuestion,
// plan-mode feedback): the owner's typing then answers the tool, and the agent gets that exact text
// back, so it must not count as a prompt.
func (p *Pool) asking(sessionID, tool string, open bool) {
	if tool != "AskUserQuestion" && tool != "ExitPlanMode" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if open {
		if p.askingOwner == nil {
			p.askingOwner = map[string]bool{}
		}
		p.askingOwner[sessionID] = true
	} else {
		delete(p.askingOwner, sessionID)
	}
}

// withdrawOwnerInput takes back one credit for text in a session (a prompt that was never typed).
func (p *Pool) withdrawOwnerInput(sessionID, text string) {
	text = squash(text)
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.ownerEnter[sessionID]
	for i := len(q) - 1; i >= 0; i-- {
		if q[i].text == text {
			p.ownerEnter[sessionID] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}
