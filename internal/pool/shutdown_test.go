package pool

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Only the owner's app connection shuts the host down, and only once however often it is asked.
func TestHostShutdownIsTheOwners(t *testing.T) {
	p := testPool(t)
	stops := make(chan struct{}, 4)
	p.shutdown = func() { stops <- struct{}{} }
	for _, c := range []gateway.Caller{{}, {Terminal: true}, {Unix: true}, {Phone: true, Device: "d"}} {
		if _, err := p.RPC(context.Background(), c, "host.shutdown", nil); err == nil {
			t.Fatalf("%+v shut the host down", c)
		}
	}
	select {
	case <-stops:
		t.Fatal("a refused caller stopped the host")
	case <-time.After(shutdownDelay + 100*time.Millisecond):
	}
	for range 2 {
		if _, err := p.RPC(context.Background(), gateway.Caller{Owner: true}, "host.shutdown", nil); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-stops:
	case <-time.After(2 * time.Second):
		t.Fatal("the owner's shut down never stopped the host")
	}
	select {
	case <-stops:
		t.Fatal("the host was stopped twice")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestHostShutdownNeedsAStop(t *testing.T) {
	p := testPool(t)
	if _, err := p.RPC(context.Background(), gateway.Caller{Owner: true}, "host.shutdown", nil); codeOf(err) != protocol.CodeUnsupported {
		t.Fatalf("no stop: %v", err)
	}
}

// Every session running on this Mac is stopped, and only then the host. A session on another
// machine is that machine's host's to stop.
func TestHostShutdownStopsSessionsBeforeTheHost(t *testing.T) {
	p := testPool(t)
	var mu sync.Mutex
	var killed []string
	p.testKill = func(id string) error {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		killed = append(killed, id)
		mu.Unlock()
		return nil
	}
	stopped := make(chan []string, 1)
	p.shutdown = func() {
		mu.Lock()
		defer mu.Unlock()
		stopped <- append([]string(nil), killed...)
	}
	p.st.Update(func(x *store.State) error {
		x.Sessions = append(x.Sessions,
			store.Session{ID: "aaaaaaaaaaaa", Harness: harness.Claude, State: "running"},
			store.Session{ID: "bbbbbbbbbbbb", Harness: harness.Shell, State: "running"},
			store.Session{ID: "cccccccccccc", Harness: harness.Claude, State: "exited"},
			store.Session{ID: "dddddddddddd", Harness: harness.Claude, State: "running", NodeID: "remote1"})
		x.Nodes = append(x.Nodes, store.Node{ID: "remote1", Name: "build box", Kind: "ssh"})
		return nil
	})
	if _, err := p.RPC(context.Background(), gateway.Caller{Owner: true}, "host.shutdown", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stopped:
		if len(got) != 2 || !(got[0] == "aaaaaaaaaaaa" && got[1] == "bbbbbbbbbbbb" || got[0] == "bbbbbbbbbbbb" && got[1] == "aaaaaaaaaaaa") {
			t.Fatalf("killed before the stop: %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the host was never stopped")
	}
}
