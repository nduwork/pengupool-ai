package pool

import (
	"context"
	"slices"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
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

// localNode is onThisHost for a state in hand (inside an update, where the store is locked).
func localNode(st *store.State, nodeID string) bool {
	if nodeID == "" {
		return true
	}
	for _, n := range st.Nodes {
		if n.ID == nodeID {
			return n.Kind == "local"
		}
	}
	return false
}

// restorableIn lists the sessions of restore that are still lost: a session the owner resumed or
// forgot meanwhile is not offered again.
func (p *Pool) restorableIn(st *store.State, restore []string) []restorable {
	var out []restorable
	for _, id := range restore {
		i := sessionIndex(st, id)
		if i < 0 || st.Sessions[i].State != "lost" || !localNode(st, st.Sessions[i].NodeID) {
			continue
		}
		s := st.Sessions[i]
		out = append(out, restorable{ID: s.ID, Name: s.Name, Harness: s.Harness, Agent: harness.IsAgent(s.Harness)})
	}
	return out
}

func (p *Pool) restorableSessions() []restorable {
	st := p.st.State()
	return p.restorableIn(&st, st.Restore)
}

// runRestorable is what the app asks the owner about at start: the sessions that stopped.
func (p *Pool) runRestorable(_ context.Context, _ struct{}) (any, error) {
	return p.restorableSessions(), nil
}

// runRestore resumes the stopped agent sessions, one at a time, in the background. The list is
// taken and cleared in one update, so an overlapping answer (the app and a paired device) gets
// nothing to resume.
func (p *Pool) runRestore(_ context.Context, _ struct{}) (any, error) {
	var todo []restorable
	if err := p.update(func(st *store.State) error {
		todo = p.restorableIn(st, st.Restore)
		st.Restore = nil
		return nil
	}); err != nil {
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
			if err := harness.Dropped(r.Harness); err != nil {
				p.logf("pool: restore %s: skipped: %v", r.Name, err)
				continue
			}
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
			p.logf("pool: restored %s", r.Name)
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

// runFresh is the owner's "start fresh": instead of restoring, the stopped sessions leave the map.
// A team none of whose members still runs is closed (it stays under Closed groups, where the owner
// can delete it); a stopped member of a team that still runs is kept. Nothing is deleted on disk:
// scrollback, worktrees, team notes, journals and the Compass stay.
func (p *Pool) runFresh(_ context.Context, _ struct{}) (any, error) {
	var closed []string
	removed := 0
	if err := p.update(func(st *store.State) error {
		gone := map[string]bool{}
		for _, r := range p.restorableIn(st, st.Restore) {
			gone[r.ID] = true
		}
		st.Restore = nil
		if len(gone) == 0 {
			return nil
		}
		for i := range st.Domain.Teams {
			t := &st.Domain.Teams[i]
			if t.State != "active" || !slices.ContainsFunc(t.Members, func(m protocol.Member) bool { return gone[m.SessionID] }) {
				continue
			}
			if slices.ContainsFunc(t.Members, func(m protocol.Member) bool {
				j := sessionIndex(st, m.SessionID)
				return m.State == "active" && j >= 0 && st.Sessions[j].State == "running"
			}) {
				continue
			}
			dissolve(st, t)
			for k := range st.Domain.Approvals { // as teamClose: a closed team's requests change nothing
				if a := &st.Domain.Approvals[k]; a.Kind == "formation" && a.TeamID == t.ID && a.State == "pending" {
					a.State = "denied"
				}
			}
			closed = append(closed, t.ID)
		}
		n := len(st.Sessions)
		st.Sessions = slices.DeleteFunc(st.Sessions, func(s store.Session) bool {
			return gone[s.ID] && teamOf(&st.Domain, s.ID) == nil
		})
		removed = n - len(st.Sessions)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, id := range closed {
		p.recordFormation(id, protocol.FormationChange{Op: "remove", Reason: "started fresh"}, "start fresh")
	}
	return map[string]int{"sessions": removed, "teams": len(closed)}, nil
}

// runDismiss is the owner's "don't restore": it forgets the question and deletes nothing.
func (p *Pool) runDismiss(_ context.Context, _ struct{}) (any, error) {
	return nil, p.clearRestore()
}
