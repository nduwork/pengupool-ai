package pool

import (
	"context"
	"sync"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// shutdownDelay lets host.shutdown's answer reach the app before the sessions and the host stop.
var shutdownDelay = 200 * time.Millisecond

// hostShutdown is the owner's Shut down: no session can start any more, every running one is
// stopped, then the host. Owner only (in no allow-list).
func (p *Pool) hostShutdown(context.Context, struct{}) (any, error) {
	if p.shutdown == nil {
		return nil, protocol.Errorf(protocol.CodeUnsupported, "this host cannot be shut down from the app")
	}
	p.logf("pool: shut down by the owner: stopping every session, then the host")
	go func() {
		time.Sleep(shutdownDelay)
		p.shutOnce.Do(p.shutDown)
	}()
	return map[string]bool{"ok": true}, nil
}

func (p *Pool) shutDown() {
	if p.hub != nil {
		p.hub.StopSpawning()
	}
	var ids []string
	for _, s := range p.st.State().Sessions {
		if s.State == "running" {
			ids = append(ids, s.ID)
		}
	}
	p.logf("pool: shut down: stopping %d session(s)", len(ids))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.killWorker(ctx, id); err != nil {
				p.logf("pool: shut down: session %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	p.shutdown()
}
