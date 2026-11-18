package pool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// shortRestart shortens Restart all's clocks for one test, and puts them back once its loop ended.
func shortRestart(t *testing.T, p *Pool, gap, settle time.Duration) {
	t.Helper()
	g, k, s := restoreGap, restartTick, restartSettle
	restoreGap, restartTick, restartSettle = gap, 5*time.Millisecond, settle
	t.Cleanup(func() {
		_, _ = p.runRestartCancel(context.Background(), restartIn{})
		waitRestartIdle(t, p)
		restoreGap, restartTick, restartSettle = g, k, s
	})
}

func waitRestartIdle(t *testing.T, p *Pool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		p.restartMu.Lock()
		running := p.restartRunning
		p.restartMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(end) {
			t.Fatal("the Restart all loop never ended")
		}
	}
}

func restartItemOf(p *Pool, id string) restartItem {
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	for _, it := range p.restartQ {
		if it.SessionID == id {
			return *it
		}
	}
	return restartItem{}
}

func waitItem(t *testing.T, p *Pool, id, state string) restartItem {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if it := restartItemOf(p, id); it.State == state {
			return it
		}
		if time.Now().After(end) {
			t.Fatalf("%s never became %s: %+v", id, state, restartItemOf(p, id))
		}
	}
}

func (rp *relaunchPool) killed(id string) int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	n := 0
	for _, k := range rp.kills {
		if k == id {
			n++
		}
	}
	return n
}

func (rp *relaunchPool) spawnsOf(id string) []spawnReq {
	var out []spawnReq
	for _, r := range rp.spawns() {
		if r.ID == id {
			out = append(out, r)
		}
	}
	return out
}

func (rp *relaunchPool) status(id, status string) {
	rp.setSession(id, func(x *store.Session) { x.Status = status })
}

func ownerRPC(p *Pool, typ string, params any) (any, error) {
	raw, _ := json.Marshal(params)
	return p.RPC(context.Background(), gateway.Caller{Owner: true}, typ, raw)
}

// Restart all is the owner's alone; idle sessions resume (or start fresh) in their place, a busy one
// waits for its turn to end, a cancelled one and a shell are left alone.
func TestRestartAllResumesQueuesAndStartsFresh(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 40*time.Millisecond, 0)
	transcript := filepath.Join(t.TempDir(), "keep.jsonl")
	os.WriteFile(transcript, []byte("{}\n"), 0o600)
	dirs := map[string]string{}
	for _, s := range []store.Session{
		{ID: "idleaaaaaaaa", Name: "idle", Harness: harness.Claude, Status: "idle", ConvID: "conv-idle", TeamID: "team1111", Role: "builder"},
		{ID: "busyaaaaaaaa", Name: "busy", Harness: harness.Codex, Status: "working", ConvID: "conv-busy"},
		{ID: "freshaaaaaaa", Name: "fresh", Harness: harness.Claude, Status: "idle"},
		{ID: "keepaaaaaaaa", Name: "keep", Harness: harness.OMP, Status: "approval", Transcript: transcript},
		{ID: "shaaaaaaaaaa", Name: "sh", Harness: harness.Shell, Status: "idle"},
	} {
		s.State, s.Cwd = "running", t.TempDir()
		dirs[s.ID] = s.Cwd
		rp.add(t, s)
	}
	for _, c := range []gateway.Caller{{Unix: true}, {Terminal: true}, {Phone: true, Device: "d"}} {
		for _, typ := range []string{"run.restartAll", "run.restartStatus", "run.restartCancel"} {
			if _, err := rp.RPC(context.Background(), c, typ, nil); codeOf(err) != protocol.CodeForbidden {
				t.Fatalf("%+v %s: %v", c, typ, err)
			}
		}
	}
	got, err := ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	if err != nil || len(got.([]restartItem)) != 4 {
		t.Fatalf("restartAll: %v %+v", err, got)
	}
	for _, it := range got.([]restartItem) {
		if it.SessionID == "shaaaaaaaaaa" {
			t.Fatal("a shell was queued")
		}
	}
	if _, err := ownerRPC(rp.Pool, "run.restartCancel", map[string]any{"session_id": "keepaaaaaaaa"}); err != nil {
		t.Fatal(err)
	}
	waitItem(t, rp.Pool, "idleaaaaaaaa", "resumed")
	fresh := waitItem(t, rp.Pool, "freshaaaaaaa", "fresh")
	if rp.killed("busyaaaaaaaa") != 0 || restartItemOf(rp.Pool, "busyaaaaaaaa").State != "queued" {
		t.Fatalf("busy: killed %d, %+v", rp.killed("busyaaaaaaaa"), restartItemOf(rp.Pool, "busyaaaaaaaa"))
	}
	if r := rp.spawnsOf("idleaaaaaaaa"); len(r) != 1 || r[0].Resume != "conv-idle" || r[0].TeamID != "team1111" || r[0].Cwd != dirs["idleaaaaaaaa"] {
		t.Fatalf("idle spawned %+v", r)
	}
	if s, _ := rp.session("idleaaaaaaaa"); s.TeamID != "team1111" || s.Role != "builder" || s.ConvID != "conv-idle" {
		t.Fatalf("idle stored %+v", s)
	}
	if r := rp.spawnsOf("freshaaaaaaa"); len(r) != 1 || r[0].Resume != "" || r[0].Cwd != dirs["freshaaaaaaa"] || fresh.Reason == "" {
		t.Fatalf("fresh spawned %+v, item %+v", r, fresh)
	}
	rp.status("busyaaaaaaaa", "idle")
	waitItem(t, rp.Pool, "busyaaaaaaaa", "resumed")
	if r := rp.spawnsOf("busyaaaaaaaa"); len(r) != 1 || r[0].Resume != "conv-busy" || r[0].Harness != harness.Codex {
		t.Fatalf("busy spawned %+v", r)
	}
	waitRestartIdle(t, rp.Pool)
	if it := restartItemOf(rp.Pool, "keepaaaaaaaa"); it.State != "cancelled" || rp.killed("keepaaaaaaaa") != 0 || rp.killed("shaaaaaaaaaa") != 0 {
		t.Fatalf("keep %+v, kills %v", it, rp.kills)
	}
}

func TestRestartAllSkipsASessionThatStoppedWhileQueued(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 20*time.Millisecond, 0)
	rp.add(t, store.Session{ID: "busyaaaaaaaa", Name: "busy", Harness: harness.Claude, State: "running", Status: "working", ConvID: "c", Cwd: t.TempDir()})
	if _, err := ownerRPC(rp.Pool, "run.restartAll", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	rp.setSession("busyaaaaaaaa", func(x *store.Session) { x.State, x.Status = "exited", "ended" })
	it := waitItem(t, rp.Pool, "busyaaaaaaaa", "cancelled")
	if it.Reason != "it stopped before its turn came" || len(rp.spawns()) != 0 {
		t.Fatalf("%+v, spawns %v", it, rp.spawns())
	}
}

// Naming one session queues only it; while a run goes on, naming a finished one restarts it again.
func TestRestartOneSessionByID(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 20*time.Millisecond, 0)
	for _, s := range []store.Session{
		{ID: "oneaaaaaaaaa", Name: "one", Harness: harness.Claude, State: "running", Status: "idle", ConvID: "c1"},
		{ID: "busyaaaaaaaa", Name: "busy", Harness: harness.Claude, State: "running", Status: "working", ConvID: "c2"},
		{ID: "shaaaaaaaaaa", Name: "sh", Harness: harness.Shell, State: "running", Status: "idle"},
		{ID: "goneaaaaaaaa", Name: "gone", Harness: harness.Claude, State: "exited", Status: "ended"},
	} {
		s.Cwd = t.TempDir()
		rp.add(t, s)
	}
	for id, code := range map[string]string{"nopeaaaaaaaa": protocol.CodeNotFound, "shaaaaaaaaaa": protocol.CodeInvalidArgument, "goneaaaaaaaa": protocol.CodeInvalidArgument} {
		if _, err := ownerRPC(rp.Pool, "run.restartAll", map[string]any{"session_id": id}); codeOf(err) != code {
			t.Fatalf("%s: %v", id, err)
		}
	}
	got, _ := ownerRPC(rp.Pool, "run.restartAll", map[string]any{"session_id": "oneaaaaaaaaa"})
	if items := got.([]restartItem); len(items) != 1 || items[0].SessionID != "oneaaaaaaaaa" {
		t.Fatalf("queued %+v", items)
	}
	waitItem(t, rp.Pool, "oneaaaaaaaaa", "resumed")
	waitRestartIdle(t, rp.Pool)
	// A run kept going by a busy session: naming the resumed one again restarts it again.
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{"session_id": "busyaaaaaaaa"})
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{"session_id": "oneaaaaaaaaa"})
	waitItem(t, rp.Pool, "oneaaaaaaaaa", "resumed")
	time.Sleep(60 * time.Millisecond)
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{"session_id": "oneaaaaaaaaa"})
	if st := restartItemOf(rp.Pool, "oneaaaaaaaaa").State; st != "queued" && st != "restarting" && st != "resumed" {
		t.Fatalf("named again: %s", st)
	}
	for end := time.Now().Add(5 * time.Second); len(rp.spawnsOf("oneaaaaaaaaa")) < 3; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("one was spawned %d times, want 3", len(rp.spawnsOf("oneaaaaaaaaa")))
		}
	}
}

// A per-session restart takes the session off the queue, and refuses one Restart all holds.
func TestSessionRestartTakesItOffTheQueue(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 20*time.Millisecond, 0)
	rp.add(t, store.Session{ID: "busyaaaaaaaa", Name: "busy", Harness: harness.Claude, State: "running", Status: "working", ConvID: "c", Cwd: t.TempDir()})
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	if _, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: "busyaaaaaaaa"}); err != nil {
		t.Fatal(err)
	}
	it := waitItem(t, rp.Pool, "busyaaaaaaaa", "cancelled")
	waitRestartIdle(t, rp.Pool)
	if it.Reason != "restarted by hand" || len(rp.spawnsOf("busyaaaaaaaa")) != 1 {
		t.Fatalf("%+v, spawned %d", it, len(rp.spawnsOf("busyaaaaaaaa")))
	}
	rp.Pool.mu.Lock()
	rp.restartClaim = map[string]bool{"busyaaaaaaaa": false}
	rp.Pool.mu.Unlock()
	if _, err := rp.sessionRestart(context.Background(), sessionIn{SessionID: "busyaaaaaaaa"}); codeOf(err) != protocol.CodeConflict {
		t.Fatalf("claimed: %v", err)
	}
}

func TestRestartAllAgainDuringARunDoesNotRestartTwice(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 20*time.Millisecond, 0)
	rp.add(t, store.Session{ID: "idleaaaaaaaa", Name: "idle", Harness: harness.Claude, State: "running", Status: "idle", ConvID: "c1", Cwd: t.TempDir()})
	rp.add(t, store.Session{ID: "busyaaaaaaaa", Name: "busy", Harness: harness.Claude, State: "running", Status: "working", ConvID: "c2", Cwd: t.TempDir()})
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	waitItem(t, rp.Pool, "idleaaaaaaaa", "resumed")
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	time.Sleep(10 * restoreGap)
	if restartItemOf(rp.Pool, "idleaaaaaaaa").State != "resumed" || len(rp.spawnsOf("idleaaaaaaaa")) != 1 || restartItemOf(rp.Pool, "busyaaaaaaaa").State != "queued" {
		t.Fatalf("idle %+v spawned %d, busy %+v", restartItemOf(rp.Pool, "idleaaaaaaaa"), len(rp.spawnsOf("idleaaaaaaaa")), restartItemOf(rp.Pool, "busyaaaaaaaa"))
	}
}

func TestRestartAllSpacesFailures(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 60*time.Millisecond, 0)
	var mu sync.Mutex
	var at []time.Time
	// Each attempt is timed where restartOne begins its work (testBeforeStop), not at the spawn, which
	// follows a stop whose length varies under load.
	rp.testBeforeStop = func(string) {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
	}
	rp.testSpawn = func(spawnReq) (store.Session, error) { return store.Session{}, errors.New("the agent is broken") }
	for _, id := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb", "cccccccccccc"} {
		rp.add(t, store.Session{ID: id, Name: id[:1], Harness: harness.Claude, State: "running", Status: "idle", ConvID: "c", Cwd: t.TempDir()})
	}
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	waitRestartIdle(t, rp.Pool)
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 3 {
		t.Fatalf("%d attempts", len(at))
	}
	for i := 1; i < 3; i++ {
		if d := at[i].Sub(at[i-1]); d < restoreGap*9/10 {
			t.Fatalf("attempt %d came %v after the one before", i+1, d)
		}
	}
	if it := restartItemOf(rp.Pool, "aaaaaaaaaaaa"); it.State != "failed" || !strings.Contains(it.Reason, "the agent is broken") {
		t.Fatalf("%+v", it)
	}
}

// Work that arrives as a session is picked leaves it running: a team message waits for it, the
// owner's prompt voids the claim, and it is restarted once things are quiet.
func TestRestartAllLeavesNewWorkAlone(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 20*time.Millisecond, 200*time.Millisecond)
	var keys []string
	var kmu sync.Mutex
	rp.testKeys = func(id, k string) error { kmu.Lock(); keys = append(keys, k); kmu.Unlock(); return nil }
	rp.add(t, store.Session{ID: "codexaaaaaaa", Name: "worker", Harness: harness.Codex, State: "running", Status: "idle", ConvID: "c", Cwd: t.TempDir()})
	rp.add(t, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: harness.Shell, State: "running", Status: "idle", Cwd: t.TempDir()})
	m1 := protocol.TeamMessage{ID: "m1", From: "leadaaaaaaaa", To: "codexaaaaaaa", Text: "your next task", State: "queued"}
	rp.st.Update(func(x *store.State) error { x.Domain.Messages = append(x.Domain.Messages, m1); return nil })
	var once sync.Once
	rp.testBeforeStop = func(id string) {
		once.Do(func() {
			rp.deliver(m1)
			rp.OwnerInput(id, "do this now")
		})
	}
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	time.Sleep(50 * time.Millisecond)
	kmu.Lock()
	typed := len(keys)
	kmu.Unlock()
	rp.Pool.mu.Lock()
	held := len(rp.pending["codexaaaaaaa"])
	rp.Pool.mu.Unlock()
	if s, _ := rp.session("codexaaaaaaa"); rp.killed("codexaaaaaaa") != 0 || typed != 0 || s.State != "running" || restartItemOf(rp.Pool, "codexaaaaaaa").State != "queued" || held != 1 {
		t.Fatalf("kills %d typed %d state %s item %+v held %d", rp.killed("codexaaaaaaa"), typed, s.State, restartItemOf(rp.Pool, "codexaaaaaaa"), held)
	}
	waitItem(t, rp.Pool, "codexaaaaaaa", "resumed")
	if rp.killed("codexaaaaaaa") != 1 {
		t.Fatalf("killed %d times", rp.killed("codexaaaaaaa"))
	}
}

// A message sent during the restart reaches the restarted session, through its own channel.
func TestRestartAllDeliversHeldMessagesWhenReady(t *testing.T) {
	for _, h := range []string{harness.Claude, harness.OMP, harness.Pi} {
		t.Run(h, func(t *testing.T) {
			var rp *relaunchPool
			rp = newRelaunchPool(t, nil, func(r spawnReq) {
				if r.Harness == harness.Claude {
					rp.status(r.ID, "starting") // Claude reports its start with SessionStart
				}
			})
			shortRestart(t, rp.Pool, 20*time.Millisecond, 0)
			id := "agentaaaaaaa"
			rp.add(t, store.Session{ID: id, Name: "agent", Harness: h, State: "running", Status: "idle", ConvID: "c", Cwd: t.TempDir()})
			rp.add(t, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: harness.Shell, State: "running", Status: "idle", Cwd: t.TempDir()})
			m := protocol.TeamMessage{ID: "m1", From: "leadaaaaaaaa", To: id, Text: "your next task", State: "queued"}
			rp.st.Update(func(x *store.State) error { x.Domain.Messages = append(x.Domain.Messages, m); return nil })
			rp.testBeforeStop = func(string) { rp.deliver(m) }
			ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
			waitItem(t, rp.Pool, id, "resumed")
			got := make(chan string, 1)
			if h == harness.Claude {
				dir, _ := os.MkdirTemp("/tmp", "pprs")
				defer os.RemoveAll(dir)
				sock := filepath.Join(dir, "s")
				ln, err := net.Listen("unix", sock)
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				go func() {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					defer c.Close()
					sc := bufio.NewScanner(c)
					for sc.Scan() {
						if strings.Contains(sc.Text(), "your next task") {
							got <- sc.Text()
							return
						}
					}
				}()
				if _, err := rp.hookEvent(context.Background(), hookIn{SessionID: id, Event: "SessionStart", Env: map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": sock}}); err != nil {
					t.Fatal(err)
				}
			} else {
				go func() {
					for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
						if b, _ := os.ReadFile(filepath.Join(rp.st.Dir(), store.RunDir, "inbox", id+".jsonl")); strings.Contains(string(b), "your next task") {
							got <- string(b)
							return
						}
					}
				}()
			}
			select {
			case <-got:
			case <-time.After(2 * time.Second):
				t.Fatal("the held message never reached the restarted session")
			}
			rp.Pool.mu.Lock()
			left := len(rp.pending[id])
			rp.Pool.mu.Unlock()
			if left != 0 {
				t.Fatalf("%d messages still pending", left)
			}
		})
	}
}

// The release and SessionStart race for held messages; exactly one sends them.
func TestRestartHeldMessagesGoOutOnce(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	id := "ompaaaaaaaaa"
	rp.add(t, store.Session{ID: id, Name: "omp", Harness: harness.OMP, State: "running", Status: "idle", Cwd: t.TempDir()})
	rp.add(t, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: harness.Shell, State: "running", Status: "idle", Cwd: t.TempDir()})
	m := protocol.TeamMessage{ID: "m1", From: "leadaaaaaaaa", To: id, Text: "your next task", State: "queued"}
	rp.st.Update(func(x *store.State) error { x.Domain.Messages = append(x.Domain.Messages, m); return nil })
	rp.Pool.mu.Lock()
	rp.restartClaim = map[string]bool{id: false}
	rp.pending[id] = []protocol.TeamMessage{m}
	rp.restartHeld = map[string]bool{id: true}
	rp.Pool.mu.Unlock()
	rp.testBeforeFlush = func(string) {
		if _, err := rp.hookEvent(context.Background(), hookIn{SessionID: id, Event: "SessionStart"}); err != nil {
			t.Error(err)
		}
	}
	rp.releaseRestart(id, true)
	b, _ := os.ReadFile(filepath.Join(rp.st.Dir(), store.RunDir, "inbox", id+".jsonl"))
	if n := strings.Count(string(b), "your next task"); n != 1 {
		t.Fatalf("delivered %d times: %s", n, b)
	}
}

// A restart that fails before it stops the session leaves it running and idle: a message held
// during that restart reaches it then, once.
func TestRestartAllDeliversHeldMessagesAfterAFailedStop(t *testing.T) {
	rp := newRelaunchPool(t, nil, nil)
	shortRestart(t, rp.Pool, 20*time.Millisecond, 0)
	id := "agentaaaaaaa"
	rp.add(t, store.Session{ID: id, Name: "agent", Harness: harness.OMP, State: "running", Status: "idle", ConvID: "c", Cwd: t.TempDir()})
	rp.add(t, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: harness.Shell, State: "running", Status: "idle", Cwd: t.TempDir()})
	m := protocol.TeamMessage{ID: "m1", From: "leadaaaaaaaa", To: id, Text: "your next task", State: "queued"}
	rp.st.Update(func(x *store.State) error { x.Domain.Messages = append(x.Domain.Messages, m); return nil })
	rp.testBeforeStop = func(string) { rp.deliver(m) }
	rp.testKill = func(string) error { return errors.New("kill refused") }
	ownerRPC(rp.Pool, "run.restartAll", map[string]any{})
	waitItem(t, rp.Pool, id, "failed")
	inbox := filepath.Join(rp.st.Dir(), store.RunDir, "inbox", id+".jsonl")
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, _ := os.ReadFile(inbox); strings.Count(string(b), "your next task") == 1 {
			break
		}
		if time.Now().After(end) {
			b, _ := os.ReadFile(inbox)
			t.Fatalf("the held message was not delivered once after a failed restart: %q", b)
		}
	}
}
