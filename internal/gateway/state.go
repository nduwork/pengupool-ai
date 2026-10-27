package gateway

import (
	"context"
	"reflect"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// subscribe answers state.subscribe: a full snapshot unless the client already holds exactly this
// revision of this daemon's epoch, then 1 Hz deltas for as long as the connection lives.
//
// The daemon keeps no delta history, so a since_rev it cannot describe (anything but the current
// one) is answered with a snapshot; the epoch makes a reconnect across a daemon restart fall into
// that case instead of trusting a revision counter that restarted with it.
func (c *conn) subscribe(ctx context.Context, msg *protocol.Message) {
	st := c.state()
	if msg.Epoch != c.srv.epoch || msg.SinceRev != st.Rev {
		snap := &protocol.Message{
			ID:       msg.ID,
			Type:     protocol.TypeStateSnapshot,
			Rev:      st.Rev,
			Epoch:    c.srv.epoch,
			Sessions: views(st.Sessions),
			Domain:   &st.Domain,
		}
		if err := c.writeControl(snap); err != nil {
			return
		}
	} else {
		// The client is exactly current: acknowledge the subscription with an empty delta at this
		// revision, so every state.subscribe gets exactly one response.
		ack := &protocol.Message{
			ID:    msg.ID,
			Type:  protocol.TypeStateDelta,
			Rev:   st.Rev,
			Epoch: c.srv.epoch,
		}
		if err := c.writeControl(ack); err != nil {
			return
		}
	}

	c.mu.Lock()
	if c.subCancel != nil {
		c.subCancel()
	}
	subCtx, cancel := context.WithCancel(ctx)
	c.subCancel = cancel
	c.subGen++
	gen := c.subGen
	c.mu.Unlock()

	go c.pollState(subCtx, gen, st)
}

// pollState samples the store and emits the difference since the last sample.
func (c *conn) pollState(ctx context.Context, gen uint64, last store.State) {
	ticker := time.NewTicker(c.srv.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cur := c.state()
		if cur.Rev == last.Rev {
			continue
		}
		upserted, removed := diffSessions(last.Sessions, cur.Sessions)
		var domain *protocol.Domain
		if !reflect.DeepEqual(last.Domain, cur.Domain) {
			domain = &cur.Domain
		}
		last = cur
		if len(upserted) == 0 && len(removed) == 0 && domain == nil {
			continue
		}
		c.mu.Lock()
		stale := c.subGen != gen
		c.mu.Unlock()
		if stale {
			return
		}
		delta := &protocol.Message{
			ID:       protocol.NewID(),
			Type:     protocol.TypeStateDelta,
			Rev:      cur.Rev,
			Epoch:    c.srv.epoch,
			Upserted: upserted,
			Removed:  removed,
			Domain:   domain,
		}
		if err := c.writeControl(delta); err != nil {
			return
		}
	}
}

// state is the store state as this connection may see it.
func (c *conn) state() store.State {
	st := c.srv.opts.Store.State()
	if c.srv.opts.Filter != nil {
		st = c.srv.opts.Filter(c.caller(), st)
	}
	return st
}

// list answers session.list from the store.
func (c *conn) list(msg *protocol.Message) {
	st := c.state()
	_ = c.writeControl(&protocol.Message{
		ID:       msg.ID,
		Type:     protocol.TypeSessionList,
		Sessions: views(st.Sessions),
	})
}

// create answers session.create by handing the request to the worker manager.
func (c *conn) create(ctx context.Context, msg *protocol.Message) {
	if c.srv.opts.Sessions == nil {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnavailable, "no worker manager is configured"))
		return
	}
	req := CreateRequest{
		Name:    msg.Name,
		Cwd:     msg.Cwd,
		Harness: msg.Harness,
		Command: msg.Command,
		Cols:    msg.Cols,
		Rows:    msg.Rows,
	}
	view, err := c.srv.opts.Sessions.Create(ctx, req)
	if err != nil {
		c.sendError(msg.ID, err)
		return
	}
	_ = c.writeControl(&protocol.Message{
		ID:       msg.ID,
		Type:     protocol.TypeSessionCreate,
		Sessions: []protocol.SessionView{view},
	})
}

// kill answers session.kill by handing the request to the worker manager.
func (c *conn) kill(ctx context.Context, msg *protocol.Message) {
	if msg.SessionID == "" {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeInvalidArgument, "session.kill needs session_id"))
		return
	}
	if c.srv.opts.Sessions == nil {
		c.sendError(msg.ID, protocol.Errorf(protocol.CodeUnavailable, "no worker manager is configured"))
		return
	}
	if err := c.srv.opts.Sessions.Kill(ctx, msg.SessionID); err != nil {
		c.sendError(msg.ID, err)
		return
	}
	_ = c.writeControl(&protocol.Message{
		ID:        msg.ID,
		Type:      protocol.TypeSessionKill,
		SessionID: msg.SessionID,
	})
}

func views(sessions []store.Session) []protocol.SessionView {
	out := make([]protocol.SessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.View())
	}
	return out
}

// diffSessions compares two session lists by id and content.
func diffSessions(prev, cur []store.Session) (upserted []protocol.SessionView, removed []string) {
	prevByID := make(map[string]store.Session, len(prev))
	for _, s := range prev {
		prevByID[s.ID] = s
	}
	curByID := make(map[string]struct{}, len(cur))
	for _, s := range cur {
		curByID[s.ID] = struct{}{}
		p, ok := prevByID[s.ID]
		if !ok || !reflect.DeepEqual(p, s) {
			upserted = append(upserted, s.View())
		}
	}
	for _, s := range prev {
		if _, ok := curByID[s.ID]; !ok {
			removed = append(removed, s.ID)
		}
	}
	return upserted, removed
}
