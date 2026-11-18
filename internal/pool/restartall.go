package pool

import (
	"context"
	"fmt"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Restart all: after an agent update, every running agent session restarts on the new binary, one
// at a time, in its team place and folder. A busy session waits for its turn to end; a session that
// gets work while it is being picked is left running and waits again. Owner only; the queue lives in
// memory, so a host restart drops it and the sessions simply keep running.

// restartTick is how often the queue looks for an idle session; restartSettle how long after new
// work a session is not taken. Variables so tests can shorten them.
var (
	restartTick   = time.Second
	restartSettle = 15 * time.Second
)

const freshReason = "it had no saved conversation yet, so it started fresh in the same place"

type restartItem struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Harness   string `json:"harness"`
	State     string `json:"state"`            // queued | restarting | resumed | fresh | failed | cancelled
	Reason    string `json:"reason,omitempty"` // why it is fresh, failed or cancelled
}

type restartIn struct {
	SessionID string `json:"session_id"`
}

// busy is a session mid-turn, waiting on the owner, or still starting.
func busy(s store.Session) bool {
	switch s.Status {
	case "working", "approval", "starting":
		return true
	}
	return false
}

func (p *Pool) restartable(s store.Session) bool {
	return s.State == "running" && harness.IsAgent(s.Harness) && p.onThisHost(s.NodeID)
}

func (p *Pool) restartCopy() []restartItem {
	out := make([]restartItem, 0, len(p.restartQ))
	for _, it := range p.restartQ {
		out = append(out, *it)
	}
	return out
}

func (p *Pool) runRestartAll(_ context.Context, in restartIn) (any, error) {
	var named store.Session
	if in.SessionID != "" {
		s, ok := p.session(in.SessionID)
		if !ok {
			return nil, notFound("no session %s", in.SessionID)
		}
		if !p.restartable(s) {
			return nil, invalid("%s is not a running agent session", s.Name)
		}
		named = s
	}
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	if !p.restartRunning {
		p.restartQ = nil // a finished run's outcomes make way
	}
	item := func(id string) *restartItem {
		for _, it := range p.restartQ {
			if it.SessionID == id {
				return it
			}
		}
		return nil
	}
	if in.SessionID == "" {
		for _, s := range p.st.State().Sessions {
			if p.restartable(s) && item(s.ID) == nil {
				p.restartQ = append(p.restartQ, &restartItem{SessionID: s.ID, Name: s.Name, Harness: s.Harness, State: "queued"})
			}
		}
	} else if it := item(named.ID); it == nil {
		p.restartQ = append(p.restartQ, &restartItem{SessionID: named.ID, Name: named.Name, Harness: named.Harness, State: "queued"})
	} else if it.State != "queued" && it.State != "restarting" {
		it.State, it.Reason = "queued", "" // the owner asked for this session by name
	}
	if !p.restartRunning {
		for _, it := range p.restartQ {
			if it.State == "queued" {
				p.restartRunning = true
				go p.restartLoop()
				break
			}
		}
	}
	return p.restartCopy(), nil
}

func (p *Pool) runRestartStatus(context.Context, struct{}) (any, error) {
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	return p.restartCopy(), nil
}

// runRestartCancel takes queued sessions (all, or the one named) off the queue. A restart under way
// finishes.
func (p *Pool) runRestartCancel(_ context.Context, in restartIn) (any, error) {
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	for _, it := range p.restartQ {
		if it.State == "queued" && (in.SessionID == "" || it.SessionID == in.SessionID) {
			it.State, it.Reason = "cancelled", ""
		}
	}
	return p.restartCopy(), nil
}

// restartLoop restarts queued sessions one at a time, in queue order, skipping busy ones, until no
// item is queued. Each restart begins at least restoreGap after the one before, failures too.
func (p *Pool) restartLoop() {
	for {
		p.restartMu.Lock()
		var next *restartItem
		waiting := false
		for _, it := range p.restartQ {
			if it.State != "queued" {
				continue
			}
			waiting = true
			s, ok := p.session(it.SessionID)
			if (!ok || s.State != "running" || !busy(s)) && p.claimRestart(it.SessionID) {
				next = it
				break
			}
		}
		if !waiting { // nothing queued: the run is over
			p.restartRunning = false
			p.restartMu.Unlock()
			return
		}
		if next == nil { // everything queued is busy or just got work
			p.restartMu.Unlock()
			time.Sleep(restartTick)
			continue
		}
		next.State = "restarting"
		p.restartMu.Unlock()

		began := time.Now()
		state, reason := p.restartOne(next.SessionID)
		p.restartMu.Lock()
		next.State, next.Reason = state, reason
		p.restartMu.Unlock()
		if state == "queued" { // it got work after it was picked: it waits for idle again
			time.Sleep(restartTick)
			continue
		}
		p.logf("pool: restart all: %s %s %s", next.Name, state, reason)
		time.Sleep(time.Until(began.Add(restoreGap))) // spacing, failures too
	}
}

// claimRestart claims an idle session. It fails when the session is running and busy, is already
// claimed, or got work within restartSettle.
func (p *Pool) claimRestart(id string) bool {
	s, ok := p.session(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, held := p.restartClaim[id]; held || ok && s.State == "running" && busy(s) || time.Since(p.lastWork[id]) < restartSettle {
		return false
	}
	if p.restartClaim == nil {
		p.restartClaim = map[string]bool{}
	}
	p.restartClaim[id] = false
	return true
}

// workArrives is a team message reaching id. False while id is claimed: the message must wait.
// Otherwise the arrival is noted in lastWork.
func (p *Pool) workArrives(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, claimed := p.restartClaim[id]; claimed {
		return false
	}
	p.noteWork(id)
	return true
}

// ownerWorkArrived is the owner's input to id: it voids a claim on id and is noted in lastWork.
func (p *Pool) ownerWorkArrived(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, claimed := p.restartClaim[id]; claimed {
		p.restartClaim[id] = true
	}
	p.noteWork(id)
}

func (p *Pool) noteWork(id string) { // under p.mu
	if p.lastWork == nil {
		p.lastWork = map[string]time.Time{}
	}
	p.lastWork[id] = time.Now()
}

// claimStillGood: the claim was not voided, and the session is not running-and-busy.
func (p *Pool) claimStillGood(id string) bool {
	s, ok := p.session(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	voided, claimed := p.restartClaim[id]
	return claimed && !voided && !(ok && s.State == "running" && busy(s))
}

func (p *Pool) claimed(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.restartClaim[id]
	return ok
}

// restartOne stops and starts one claimed session. It answers the item's new state and reason.
func (p *Pool) restartOne(id string) (state, reason string) {
	defer func() { p.releaseRestart(id, state != "queued") }() // a requeued session's messages wait for its restart
	s, ok := p.session(id)
	if !ok {
		return "cancelled", "it was closed"
	}
	if s.State != "running" {
		return "cancelled", "it stopped before its turn came"
	}
	if err := p.canRelaunch(s); err != nil {
		return "failed", errText(err)
	}
	if p.testBeforeStop != nil {
		p.testBeforeStop(id)
	}
	if !p.claimStillGood(id) {
		return "queued", ""
	}
	resumeID := resumeIDOf(s)
	ctx := context.Background()
	if err := p.stopWorker(ctx, s); err != nil {
		return "failed", "could not stop it: " + errText(err)
	}
	cur, ok := p.session(id)
	if !ok {
		return "failed", "the session is gone"
	}
	if cur.State == "running" {
		return "failed", "it was still running 5 s after it was told to stop"
	}
	if _, err := p.relaunch(ctx, cur, resumeID); err != nil {
		return "failed", errText(err)
	}
	if why := p.watchRelaunch(id); why != "" {
		return "failed", why
	}
	if resumeID == "" {
		return "fresh", freshReason
	}
	return "resumed", ""
}

// errText is an error's message without its code.
func errText(err error) string {
	if pe, ok := err.(*protocol.Error); ok {
		return pe.Message
	}
	return err.Error()
}

// takeHeld is true for exactly one caller once id's claim is released and messages were held for it.
func (p *Pool) takeHeld(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, claimed := p.restartClaim[id]; claimed || !p.restartHeld[id] {
		return false
	}
	delete(p.restartHeld, id)
	return true
}

// releaseRestart ends the claim. Once the restart is over (done), a session that is running and idle
// gets its held messages now: relaunched, or left running by a restart that failed before the stop.
func (p *Pool) releaseRestart(id string, done bool) {
	p.mu.Lock()
	delete(p.restartClaim, id)
	p.mu.Unlock()
	if s, ok := p.session(id); done && ok && s.State == "running" && s.Status == "idle" && p.takeHeld(id) {
		if p.testBeforeFlush != nil {
			p.testBeforeFlush(id)
		}
		p.flushQueue(id)
	}
}

// restartByHand: a per-session restart refuses a session Restart all is restarting, and takes a
// queued one off the queue so it is not restarted twice.
func (p *Pool) restartByHand(s store.Session) error {
	if p.claimed(s.ID) {
		return conflict("%s is being restarted", s.Name)
	}
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	for _, it := range p.restartQ {
		if it.SessionID == s.ID && it.State == "queued" {
			it.State, it.Reason = "cancelled", "restarted by hand"
		}
	}
	return nil
}

// watchRelaunch watches a session just started again for restoreGap, looking every restoreGap/10.
// It returns "" when the session is still running at the end, else why it is not.
func (p *Pool) watchRelaunch(id string) string {
	for end := time.Now().Add(restoreGap); time.Now().Before(end); time.Sleep(restoreGap / 10) {
		if s, ok := p.session(id); ok && s.State != "running" {
			if s.ExitCode != nil {
				return fmt.Sprintf("it exited right after restarting (exit code %d)", *s.ExitCode)
			}
			return "it stopped right after restarting"
		}
	}
	return ""
}
