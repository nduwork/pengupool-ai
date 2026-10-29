package pool

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// An app-owned run (`pengupool daemon --owner-pid`): the daemon belongs to one run of the desktop app,
// so it is always the app's build. When the run ends (quit, crash, power loss) its sessions end with
// it and are recorded in State.Restore; the next run asks the owner to restore them or start fresh.

// restoreGap spaces restored sessions, so a restore does not start every agent at once.
var restoreGap = 2 * time.Second

// RunningSessions are the sessions of this host the record says are running.
func (p *Pool) RunningSessions() []string {
	var ids []string
	for _, s := range p.st.State().Sessions {
		if s.State == "running" && p.onThisHost(s.NodeID) {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// MarkRestore records sessions of a run that ended for the next run's question.
func (p *Pool) MarkRestore(ids []string) {
	if len(ids) == 0 {
		return
	}
	if err := p.update(func(st *store.State) error {
		for _, id := range ids {
			if !slices.Contains(st.Restore, id) {
				st.Restore = append(st.Restore, id)
			}
		}
		return nil
	}); err != nil {
		p.logf("pool: record the sessions to restore: %v", err)
	}
}

// EndRun ends every running session of this host and records it for the next run.
func (p *Pool) EndRun(ctx context.Context) {
	ids := p.RunningSessions()
	p.MarkRestore(ids)
	if p.hub == nil {
		return
	}
	// All at once: each may wait seconds for its agent to exit, and the app waits for the host.
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			if err := p.hub.Kill(ctx, id); err != nil {
				p.logf("pool: end session %s with the run: %v", id, err)
			}
		})
	}
	wg.Wait()
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
