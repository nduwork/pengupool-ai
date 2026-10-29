package pool

import (
	"context"
	"slices"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Sessions a reboot (or a crash, or a killed worker) ended: the host records them in State.Restore
// when it starts, and the app asks the owner whether to restore them. Agents resume their
// conversations; a session whose worker survived is adopted by the host and never asked about.

// restoreGap spaces restored sessions, so a restore does not start every agent at once.
var restoreGap = 2 * time.Second

// MarkLost records the sessions of wasRunning (running when the host last stopped) that are now
// lost, their workers gone, for the owner's restore question.
func (p *Pool) MarkLost(wasRunning []string) {
	var lost []string
	for _, s := range p.st.State().Sessions {
		if s.State == "lost" && slices.Contains(wasRunning, s.ID) && p.onThisHost(s.NodeID) {
			lost = append(lost, s.ID)
		}
	}
	if len(lost) == 0 {
		return
	}
	if err := p.update(func(st *store.State) error {
		for _, id := range lost {
			if !slices.Contains(st.Restore, id) {
				st.Restore = append(st.Restore, id)
			}
		}
		return nil
	}); err != nil {
		p.logf("pool: record the sessions to restore: %v", err)
	}
}

type restorable struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Harness string `json:"harness"`
	Agent   bool   `json:"agent"` // an agent resumes its conversation; a terminal cannot be restored
}

// restorableSessions are the last run's sessions that are still on record and not running.
func (p *Pool) restorableSessions() []restorable {
	st := p.st.State()
	var out []restorable
	for _, id := range st.Restore {
		i := sessionIndex(&st, id)
		if i < 0 || st.Sessions[i].State == "running" || !p.onThisHost(st.Sessions[i].NodeID) {
			continue
		}
		s := st.Sessions[i]
		out = append(out, restorable{ID: s.ID, Name: s.Name, Harness: s.Harness, Agent: harness.IsAgent(s.Harness)})
	}
	return out
}

// runRestorable is what the app asks the owner about at start: the last run's sessions.
func (p *Pool) runRestorable(_ context.Context, _ struct{}) (any, error) {
	return p.restorableSessions(), nil
}

// runRestore resumes the last run's agent sessions, one at a time, in the background; the list is
// cleared first, so a second answer does nothing.
func (p *Pool) runRestore(_ context.Context, _ struct{}) (any, error) {
	todo := p.restorableSessions()
	if err := p.clearRestore(); err != nil {
		return nil, err
	}
	n := 0
	for _, r := range todo {
		if r.Agent {
			n++
		}
	}
	go func() {
		first := true
		for _, r := range todo {
			if !r.Agent {
				continue
			}
			if !first {
				time.Sleep(restoreGap)
			}
			first = false
			if _, err := p.resume(context.Background(), sessionIn{SessionID: r.ID}); err != nil {
				p.logf("pool: restore %s: %v", r.Name, err)
				continue
			}
			p.logf("pool: restored %s from the last run", r.Name)
		}
	}()
	return map[string]int{"restoring": n}, nil
}

func (p *Pool) clearRestore() error {
	return p.update(func(st *store.State) error {
		st.Restore = nil
		return nil
	})
}

// runDismiss is the owner's "don't restore": it forgets the question and deletes nothing.
func (p *Pool) runDismiss(_ context.Context, _ struct{}) (any, error) {
	return nil, p.clearRestore()
}
