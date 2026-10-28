package pool

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// shell.open starts the owner's login shell as a plain session: no team, no agent, a name of its own
// per tab, the home folder unless one is chosen; session.close ends it like any session.
func TestShellOpen(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Hub: h, Exe: "pengupool"})
	var spawned []spawnReq
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		spawned = append(spawned, r)
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running"}
		if r.Decorate != nil {
			r.Decorate(&s)
		}
		return s, st.Update(func(x *store.State) error { x.Sessions = append(x.Sessions, s); return nil })
	}
	home, _ := os.UserHomeDir()
	dir := t.TempDir()
	for _, in := range []shellIn{{}, {}, {Cwd: dir}} {
		if _, err := p.shellOpen(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if len(spawned) != 3 || spawned[0].Cwd != home || spawned[2].Cwd != dir {
		t.Fatalf("cwds = %+v", spawned)
	}
	names := []string{spawned[0].Name, spawned[1].Name, spawned[2].Name}
	if names[0] != "terminal" || names[1] != "terminal-2" || names[2] != "terminal-3" {
		t.Fatalf("names = %v", names)
	}
	for _, s := range st.State().Sessions {
		if s.Harness != harness.Shell || s.Status != "" || s.TeamID != "" {
			t.Fatalf("a terminal is a plain session: %+v", s)
		}
	}
	if _, err := p.shellOpen(context.Background(), shellIn{Cwd: "relative/dir"}); err == nil {
		t.Fatal("a relative folder was accepted")
	}
	// Plain: nothing in it is a team session, so no team takes it and the drag refuses it.
	id := st.State().Sessions[0].ID
	if _, err := p.teamGroup(nil, groupIn{SessionID: id, ParentID: st.State().Sessions[1].ID}); err == nil || !strings.Contains(err.Error(), "only agent sessions") {
		t.Fatalf("a terminal was grouped: %v", err)
	}
	// Closing the tab removes the session (the hub kills a running worker first; no worker here).
	_ = st.Update(func(x *store.State) error {
		for i := range x.Sessions {
			x.Sessions[i].State = "exited"
		}
		return nil
	})
	if _, err := p.sessionClose(context.Background(), sessionIn{SessionID: id}); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(st.State().Sessions, func(s store.Session) bool { return s.ID == id }) {
		t.Fatal("the closed terminal is still listed")
	}
}

// The shell is the login shell from the environment, as `-l`; /bin/zsh when it names none.
func TestShellCommand(t *testing.T) {
	cmd, _, err := harness.Spec(harness.Shell, harness.Params{Env: []string{"SHELL=/bin/bash"}})
	if err != nil || strings.Join(cmd, " ") != "/bin/bash -l" {
		t.Fatalf("%v %v", cmd, err)
	}
	if cmd, _, _ = harness.Spec(harness.Shell, harness.Params{}); strings.Join(cmd, " ") != "/bin/zsh -l" {
		t.Fatalf("%v", cmd)
	}
}
