package pool

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestDescendsFrom(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Skip("no sleep")
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	if yes, known := descendsFrom(c.Process.Pid, os.Getpid()); !known || !yes {
		t.Errorf("child: descends=%v known=%v", yes, known)
	}
	if yes, known := descendsFrom(os.Getpid(), c.Process.Pid); known && yes {
		t.Error("a parent does not descend from its child")
	}
}

// An agent's request names a session; it is taken only from a process of that session. A request
// from outside the session's tree, or naming another session, is refused; the owner's calls are not checked.
func TestBindSession(t *testing.T) {
	p, st := twoTeams(t)
	worker := exec.Command("sleep", "30") // stands in for session lead's worker, our child
	if err := worker.Start(); err != nil {
		t.Skip("no sleep")
	}
	defer func() { _ = worker.Process.Kill(); _ = worker.Wait() }()
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			switch s.Sessions[i].ID {
			case "leadaaaaaaaa":
				s.Sessions[i].Pid = worker.Process.Pid
			case "otheraaaaaaa":
				s.Sessions[i].Pid = os.Getpid() // this test process is "other"'s worker
			}
		}
		return nil
	})
	req := func(sid string) json.RawMessage { b, _ := json.Marshal(map[string]string{"session_id": sid}); return b }
	// the test process descends from itself ("other"), not from lead's worker (a sibling)
	me := gateway.Caller{PeerPID: os.Getpid()}
	if err := p.bindSession(me, req("otheraaaaaaa")); err != nil {
		t.Errorf("own session refused: %v", err)
	}
	if err := p.bindSession(me, req("leadaaaaaaaa")); err == nil {
		t.Error("an agent acted as another session")
	}
	if err := p.bindSession(me, req("webaaaaaaaaa")); err != nil { // no pid recorded: cannot tell
		t.Errorf("session without a pid refused: %v", err)
	}
	for _, c := range []gateway.Caller{{PeerPID: os.Getpid(), Owner: true}, {PeerPID: os.Getpid(), Terminal: true}, {PeerPID: os.Getpid(), Phone: true}, {}} {
		if err := p.bindSession(c, req("leadaaaaaaaa")); err != nil {
			t.Errorf("%+v refused: %v", c, err)
		}
	}
	// a message names its sender in from: the same binding
	msg := func(from string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"from": from, "to": "web", "text": "hi"})
		return b
	}
	if err := p.bindSession(me, msg("leadaaaaaaaa")); err == nil {
		t.Error("an agent sent a message as another session")
	}
	if err := p.bindSession(me, msg("otheraaaaaaa")); err != nil {
		t.Errorf("own message refused: %v", err)
	}
	// a unix caller whose process is gone (connected, forked, exited) or unknown is no agent of any session
	gone := exec.Command("true")
	_ = gone.Run()
	if procTreeReadable {
		for _, c := range []gateway.Caller{{Unix: true, PeerPID: gone.Process.Pid}, {Unix: true}} {
			if err := p.bindSession(c, req("otheraaaaaaa")); err == nil {
				t.Errorf("%+v was taken as an agent", c)
			}
		}
	}
	// a phone is never an agent: without a paired device it reaches nothing
	if _, err := p.RPC(t.Context(), gateway.Caller{Phone: true}, "search", req("leadaaaaaaaa")); err == nil || !strings.Contains(err.Error(), "for the owner") {
		t.Errorf("a phone called an agent request: %v", err)
	}
	// through RPC: search as the lead from a process that is not the lead's
	if _, err := p.RPC(t.Context(), me, "search", req("leadaaaaaaaa")); err == nil || !strings.Contains(err.Error(), "does not come from session") {
		t.Errorf("RPC as another session: %v", err)
	}
}
