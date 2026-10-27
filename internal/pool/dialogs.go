package pool

import (
	"fmt"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// A new agent can stop at a first-run dialog (a folder it must trust, a sign-in, a theme, an update)
// before it ever reads a message. While a session is "starting", its screen is read for these; the
// session then shows "waiting on a dialog" in `ls` and on the map, and a team message that waits on it
// fails with that reason rather than waiting forever.

// dialogs are the screens a starting agent stops at, matched in lower case anywhere on the screen.
// ponytail: tied to Claude Code's and Codex's wording; a changed dialog shows as plain "starting"
// (and a queued message still fails after messageWait).
var dialogs = []struct{ reason, text string }{
	{"folder trust", "do you trust the files in this folder"},
	{"folder trust", "do you trust the contents of this directory"},
	{"folder trust", "is this a project you created or one you trust"},
	{"folder trust", "trust this folder"},
	{"sign-in", "sign in with chatgpt"},
	{"sign-in", "select login method"},
	{"sign-in", "paste code here"},
	{"sign-in", "run /login"},
	{"sign-in", "log in to your"},
	{"setup", "choose the text style"},
	{"setup", "let's get started"},
	{"update", "update available"},
	{"update", "press enter to continue"},
}

// dialogReason is the first-run dialog the screen shows, or "".
func dialogReason(screen string) string {
	low := strings.ToLower(screen)
	for _, d := range dialogs {
		if strings.Contains(low, d.text) {
			return d.reason
		}
	}
	return ""
}

// messageWait is how long a team message waits for a session that is not ready before it fails.
const messageWait = 5 * time.Minute

// watchStartup is the startup loop's pass: it marks the sessions stuck on a first-run dialog (and
// clears the mark once they are past it), and fails the queued messages that cannot be delivered.
func (p *Pool) watchStartup() {
	screen := p.hub.ScreenText
	if p.testScreen != nil {
		screen = p.testScreen
	}
	for _, s := range p.st.State().Sessions {
		want := ""
		if s.State == "running" && s.Status == "starting" {
			if text, ok := screen(s.ID); ok {
				want = dialogReason(text)
			}
		}
		if want == s.Waiting {
			continue
		}
		p.setSession(s.ID, func(x *store.Session) { x.Waiting = want })
		if want != "" {
			p.logLine(s.ID, "session: "+s.Harness+" is waiting on a first-run dialog ("+want+"); open its terminal to answer it")
		}
	}
	p.expireQueued(time.Now())
}

// expireQueued fails the agent messages that wait on a session that will not take them: one that has
// ended, or one that is still starting (stuck, often on a dialog) after messageWait. The sender is
// told with a notice. A session that is busy with a turn only makes its messages wait, as intended.
func (p *Pool) expireQueued(at time.Time) {
	st := p.st.State()
	for _, m := range st.Domain.Messages {
		if m.State != "queued" || m.From == "" {
			continue
		}
		to, ok := sessionByID(&st, m.To)
		var why string
		switch {
		case !ok || to.State != "running":
			why = "the session is no longer running"
		case to.Status == "starting" && olderThan(m.At, at, messageWait) && at.Sub(to.StartedAt) > messageWait: // a resumed session starts the wait again
			why = "the session is not ready after " + messageWait.String()
			if to.Waiting != "" {
				why = "the session is waiting on a " + to.Waiting + " dialog that needs the owner"
			}
		default:
			continue
		}
		p.mu.Lock()
		q := p.pending[m.To]
		for i := range q {
			if q[i].ID == m.ID {
				p.pending[m.To] = append(q[:i:i], q[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		if !p.failQueued(m.ID, why) { // delivered meanwhile: nothing to tell
			continue
		}
		p.logLine(m.To, "message: not delivered: "+why)
		p.notice(m.TeamID, m.From, fmt.Sprintf("your message to %s was not delivered: %s", firstNonEmpty(to.Name, m.To), why))
	}
}

// failQueued fails a message only while it is still queued, and reports whether it did: a delivery
// that got there first stands.
func (p *Pool) failQueued(id, why string) (failed bool) {
	_ = p.update(func(st *store.State) error {
		for i := range st.Domain.Messages {
			if m := &st.Domain.Messages[i]; m.ID == id && m.State == "queued" {
				m.State, m.Reason, failed = "failed", why, true
			}
		}
		return nil
	})
	return failed
}

func sessionByID(st *store.State, id string) (store.Session, bool) {
	if i := sessionIndex(st, id); i >= 0 {
		return st.Sessions[i], true
	}
	return store.Session{}, false
}

func olderThan(stamp string, now time.Time, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, stamp)
	return err == nil && now.Sub(t) > d
}
