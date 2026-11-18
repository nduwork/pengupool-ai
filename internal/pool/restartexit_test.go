package pool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// diesAtOnce is a spawn hook: the session named "dies" exits with code 1 10 ms after it starts.
func diesAtOnce(rp **relaunchPool) func(spawnReq) {
	return func(r spawnReq) {
		if r.Name != "dies" {
			return
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			one := 1
			(*rp).setSession(r.ID, func(x *store.Session) { x.State, x.Status, x.ExitCode = "exited", "ended", &one })
		}()
	}
}

func TestSessionRestartReportsAnAgentThatExitsAtOnce(t *testing.T) {
	agentOnPath(t)
	defer func(g time.Duration) { restoreGap = g }(restoreGap)
	restoreGap = 50 * time.Millisecond
	var rp *relaunchPool
	rp = newRelaunchPool(t, nil, diesAtOnce(&rp))
	rp.add(t, store.Session{ID: "diesaaaaaaaa", Name: "dies", Harness: "claude", State: "running", Status: "idle", ConvID: "c1", Cwd: t.TempDir()})
	_, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: "diesaaaaaaaa"})
	if codeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("restart: %v, want a conflict naming exit code 1", err)
	}
}

func TestSessionRestartWaitsOutTheWatch(t *testing.T) {
	agentOnPath(t)
	defer func(g time.Duration) { restoreGap = g }(restoreGap)
	restoreGap = 50 * time.Millisecond
	rp := newRelaunchPool(t, nil, nil)
	rp.add(t, store.Session{ID: "livesaaaaaaa", Name: "lives", Harness: "claude", State: "running", Status: "idle", ConvID: "c1", Cwd: t.TempDir()})
	began := time.Now()
	if _, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: "livesaaaaaaa"}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < restoreGap {
		t.Fatalf("answered after %v, before the %v watch", took, restoreGap)
	}
}

func TestRestoreLogsAnAgentThatExitsAtOnce(t *testing.T) {
	agentOnPath(t)
	defer func(g time.Duration) { restoreGap = g }(restoreGap)
	restoreGap = 50 * time.Millisecond
	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	var rp *relaunchPool
	rp = newRelaunchPool(t, logf, diesAtOnce(&rp))
	rp.add(t, store.Session{ID: "diesaaaaaaaa", Name: "dies", Harness: "claude", State: "lost", ConvID: "c1", Cwd: t.TempDir()})
	_ = rp.st.Update(func(x *store.State) error { x.Restore = []string{"diesaaaaaaaa"}; return nil })
	if _, err := rp.runRestore(context.Background(), struct{}{}); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		all := strings.Join(lines, "\n")
		mu.Unlock()
		if strings.Contains(all, "pool: restore dies: it exited right after restarting (exit code 1)") {
			if strings.Contains(all, "restored dies") {
				t.Fatalf("logged as restored too:\n%s", all)
			}
			return
		}
		if time.Now().After(end) {
			t.Fatalf("no exit line in:\n%s", all)
		}
	}
}

// An agent that exits before its worker's handshake fails the spawn itself: the owner still reads why.
func TestSessionRestartReportsAnAgentThatExitsBeforeItsHandshake(t *testing.T) {
	agentOnPath(t)
	rp := newRelaunchPool(t, nil, nil)
	rp.testSpawn = func(spawnReq) (store.Session, error) {
		return store.Session{}, protocol.Errorf(protocol.CodeUnavailable, "it exited right after starting (exit code 1)")
	}
	rp.add(t, store.Session{ID: "diesaaaaaaaa", Name: "dies", Harness: "claude", State: "running", Status: "idle", ConvID: "c1", Cwd: t.TempDir()})
	_, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: "diesaaaaaaaa"})
	if codeOf(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "dies: it exited right after starting (exit code 1)") {
		t.Fatalf("restart: %v", err)
	}
}
