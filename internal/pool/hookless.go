package pool

import (
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// hooklessAfter is how long after the owner's first prompt an agent launch with no hook at all is
// flagged. A hooked agent sends UserPromptSubmit (Codex and Muse SessionStart too) within seconds of a
// prompt, so a long quiet turn never trips it.
const hooklessAfter = 2 * time.Minute

// hookArrived notes any hook event, Status and Delivered included, and clears the note at the first.
func (p *Pool) hookArrived(s store.Session, event string) {
	p.mu.Lock()
	if p.hookAt == nil {
		p.hookAt = map[string]time.Time{}
	}
	p.hookAt[s.ID] = time.Now()
	p.mu.Unlock()
	if s.HooksMissing {
		p.setSession(s.ID, func(x *store.Session) { x.HooksMissing = false })
		p.logLine(s.ID, "hooks: first event arrived ("+event+")")
	}
}

// promptTyped notes the owner's first prompt in the session's launch (a later launch starts over).
func (p *Pool) promptTyped(s store.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.promptAt == nil {
		p.promptAt = map[string]time.Time{}
	}
	if !p.promptAt[s.ID].After(s.StartedAt) {
		p.promptAt[s.ID] = time.Now()
	}
}

// watchHooks flags each running agent the owner prompted hooklessAfter ago that has sent no hook since
// it launched. All in memory: a daemon restart checks the next prompt again.
func (p *Pool) watchHooks(now time.Time) {
	for _, s := range p.st.State().Sessions {
		p.mu.Lock()
		prompt, hook := p.promptAt[s.ID], p.hookAt[s.ID]
		p.mu.Unlock()
		want := harness.IsAgent(s.Harness) && s.State == "running" && prompt.After(s.StartedAt) &&
			!hook.After(s.StartedAt) && now.Sub(prompt) >= hooklessAfter
		if want == s.HooksMissing {
			continue
		}
		p.setSession(s.ID, func(x *store.Session) { x.HooksMissing = want })
		if want {
			p.logLine(s.ID, "hooks: none from "+s.Harness+" since launch, 2m after the first prompt; no Compass or team context reaches it")
		}
	}
}
