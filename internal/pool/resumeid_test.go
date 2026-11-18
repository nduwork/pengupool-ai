package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// relaunchPool is a pool whose spawns are recorded and stored as running/idle sessions; after(r) runs
// once each spawn is stored, and testKill marks a session exited.
type relaunchPool struct {
	*Pool
	st    *store.Store
	mu    sync.Mutex
	reqs  []spawnReq
	kills []string
}

func newRelaunchPool(t *testing.T, logf func(string, ...any), after func(spawnReq)) *relaunchPool {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rp := &relaunchPool{st: st}
	rp.Pool = New(Options{Store: st, Exe: "pengupool", Logf: logf})
	rp.testSpawn = func(r spawnReq) (store.Session, error) {
		rp.mu.Lock()
		rp.reqs = append(rp.reqs, r)
		rp.mu.Unlock()
		// a new record replaces the old one, as the hub's Spawn does
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running", Status: "idle"}
		err := st.Update(func(x *store.State) error {
			if r.Decorate != nil {
				r.Decorate(&s)
			}
			if i := sessionIndex(x, s.ID); i >= 0 {
				x.Sessions[i] = s
			} else {
				x.Sessions = append(x.Sessions, s)
			}
			return nil
		})
		if err == nil && after != nil {
			after(r)
		}
		return s, err
	}
	rp.testKill = func(id string) error {
		rp.mu.Lock()
		rp.kills = append(rp.kills, id)
		rp.mu.Unlock()
		rp.setSession(id, func(x *store.Session) { x.State, x.Status = "exited", "ended" })
		return nil
	}
	return rp
}

func (rp *relaunchPool) add(t *testing.T, s store.Session) {
	t.Helper()
	if err := rp.st.Update(func(x *store.State) error { x.Sessions = append(x.Sessions, s); return nil }); err != nil {
		t.Fatal(err)
	}
}

func (rp *relaunchPool) spawns() []spawnReq {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return append([]spawnReq(nil), rp.reqs...)
}

func TestResumeIDOfNeedsTheSavedConversation(t *testing.T) {
	saved := filepath.Join(t.TempDir(), "conv.jsonl")
	if err := os.WriteFile(saved, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "never-written.jsonl")
	for i, c := range []struct {
		s    store.Session
		want string
	}{
		{store.Session{Harness: "claude", ConvID: "c1", Transcript: saved}, "c1"},
		{store.Session{Harness: "claude", ConvID: "c2", Transcript: gone}, ""},
		{store.Session{Harness: "claude", ConvID: "c3"}, "c3"},
		{store.Session{Harness: "codex", ConvID: "c4", Transcript: gone}, ""},
		{store.Session{Harness: "omp", ConvID: "x", Transcript: saved}, saved},
		{store.Session{Harness: "omp", ConvID: "x", Transcript: gone}, ""},
		{store.Session{Harness: "pi", ConvID: "x", Transcript: saved}, saved},
		{store.Session{Harness: "pi", ConvID: "x"}, "x"},
		{store.Session{Harness: "copilot", ConvID: "g", Transcript: gone}, ""},
		{store.Session{Harness: "gemini", ConvID: "g", Transcript: gone}, ""},
		{store.Session{Harness: "claude"}, ""},
	} {
		if got := resumeIDOf(c.s); got != c.want {
			t.Errorf("%d %s: resumeIDOf = %q, want %q", i, c.s.Harness, got, c.want)
		}
	}
}

// A session that never got a prompt has an id and no saved conversation: it starts fresh in place.
func TestResumeStartsFreshWithoutAConversation(t *testing.T) {
	agentOnPath(t)
	rp := newRelaunchPool(t, nil, nil)
	cwd := t.TempDir()
	rp.add(t, store.Session{ID: "freshaaaaaaa", Name: "fresh", Harness: "claude", State: "exited", Status: "ended", Cwd: cwd,
		ConvID: "c1", Transcript: filepath.Join(t.TempDir(), "gone.jsonl"), TeamID: "team1111", Role: "builder", LaunchModel: "model-x"})
	res, err := rp.resume(context.Background(), sessionIn{SessionID: "freshaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := res.(relaunched); !ok || !r.Fresh {
		t.Fatalf("answer %#v, want fresh", res)
	}
	r := rp.spawns()[0]
	if r.Resume != "" || r.ID != "freshaaaaaaa" || r.Name != "fresh" || r.Cwd != cwd || r.TeamID != "team1111" || r.Model != "model-x" {
		t.Fatalf("spawn %+v", r)
	}
	s, _ := rp.session("freshaaaaaaa")
	if s.TeamID != "team1111" || s.Role != "builder" || s.ConvID != "" || s.Transcript != "" {
		t.Fatalf("stored %+v", s)
	}
}

// A saved conversation is resumed, and the record keeps naming it.
func TestResumeKeepsASavedConversation(t *testing.T) {
	agentOnPath(t)
	rp := newRelaunchPool(t, nil, nil)
	saved := filepath.Join(t.TempDir(), "conv.jsonl")
	if err := os.WriteFile(saved, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rp.add(t, store.Session{ID: "keptaaaaaaaa", Name: "kept", Harness: "claude", State: "exited", Status: "ended", Cwd: t.TempDir(), ConvID: "c1", Transcript: saved})
	res, err := rp.resume(context.Background(), sessionIn{SessionID: "keptaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := res.(relaunched); !ok || r.Fresh || rp.spawns()[0].Resume != "c1" {
		t.Fatalf("answer %#v, spawn %+v", res, rp.spawns()[0])
	}
	if s, _ := rp.session("keptaaaaaaaa"); s.ConvID != "c1" || s.Transcript != saved {
		t.Fatalf("stored %+v", s)
	}
}

func TestSessionRestartStartsFreshInPlace(t *testing.T) {
	agentOnPath(t)
	defer func(g time.Duration) { restoreGap = g }(restoreGap)
	restoreGap = 50 * time.Millisecond
	rp := newRelaunchPool(t, nil, nil)
	rp.add(t, store.Session{ID: "newaaaaaaaaa", Name: "new", Harness: "claude", State: "running", Status: "idle", Cwd: t.TempDir(),
		ConvID: "c1", Transcript: filepath.Join(t.TempDir(), "gone.jsonl")})
	res, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: "newaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := res.(relaunched); !ok || !r.Fresh {
		t.Fatalf("answer %#v, want fresh", res)
	}
	if len(rp.kills) != 1 || len(rp.spawns()) != 1 || rp.spawns()[0].Resume != "" {
		t.Fatalf("kills %v, spawns %+v", rp.kills, rp.spawns())
	}
}

// Every reason a session cannot come back is found before it is stopped.
func TestSessionRestartChecksBeforeStopping(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	rp.add(t, store.Session{ID: "shellaaaaaaa", Name: "sh", Harness: "shell", State: "running", Cwd: t.TempDir()})
	rp.add(t, store.Session{ID: "ocaaaaaaaaaa", Name: "oc", Harness: "opencode", State: "running", ConvID: "c", Cwd: t.TempDir()})
	rp.add(t, store.Session{ID: "helperaaaaaa", Name: "rev", Harness: "claude", State: "running", ConvID: "c", SkillID: "sk-gone", Cwd: t.TempDir()})
	for _, id := range []string{"shellaaaaaaa", "ocaaaaaaaaaa", "helperaaaaaa"} {
		if _, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: id}); codeOf(err) != protocol.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid_argument", id, err)
		}
	}
	if len(rp.kills) != 0 {
		t.Fatalf("stopped %v before refusing", rp.kills)
	}
}

func TestRestoreStartsFreshWithoutAConversation(t *testing.T) {
	agentOnPath(t)
	defer func(g time.Duration) { restoreGap = g }(restoreGap)
	restoreGap = 50 * time.Millisecond
	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	rp := newRelaunchPool(t, logf, nil)
	rp.add(t, store.Session{ID: "lostaaaaaaaa", Name: "lost", Harness: "claude", State: "lost", Cwd: t.TempDir()})
	_ = rp.st.Update(func(x *store.State) error { x.Restore = []string{"lostaaaaaaaa"}; return nil })
	if _, err := rp.runRestore(context.Background(), struct{}{}); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		all := strings.Join(lines, "\n")
		mu.Unlock()
		if strings.Contains(all, "pool: restored lost fresh: it had no saved conversation") {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no fresh-restore line in:\n%s", all)
		}
	}
	if r := rp.spawns()[0]; r.ID != "lostaaaaaaaa" || r.Resume != "" {
		t.Fatalf("spawn %+v", r)
	}
}
