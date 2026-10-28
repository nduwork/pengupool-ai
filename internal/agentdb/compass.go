package agentdb

import (
	"cmp"
	"database/sql"
	"math"
	"slices"
	"time"
)

// Signal kinds and their weights: how much one signal moves a Compass line (plan B1).
const (
	WeightChose     = 1
	WeightSaid      = 3
	WeightCorrect   = 5
	WeightSpotCheck = 2
)

// Priors of a new line: stated by the owner, or created from a choice (which then also gets its
// creating signal as a normal effect).
var (
	PriorStated = [2]float64{8, 2}
	PriorChose  = [2]float64{1, 1}
)

// Outcome is how a go-ahead turned out, on the signal that judged it: the owner's verdict right or
// wrong, or missed (the owner answered a check-in that triage could have answered). The learned
// thresholds replay these.
type Outcome string

const (
	OutcomeRight  Outcome = "right"
	OutcomeWrong  Outcome = "wrong"
	OutcomeMissed Outcome = "missed"
)

// Signal is one thing the owner did: said, chose, corrected or spot-checked. Append-only.
type Signal struct {
	UID       string  `json:"uid"`
	Owner     string  `json:"owner"`
	Kind      string  `json:"kind"` // said | chose | correction | spot_check
	Words     string  `json:"words,omitempty"`
	Options   string  `json:"options,omitempty"` // JSON
	Chosen    string  `json:"chosen,omitempty"`
	TeamID    string  `json:"team_id,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
	At        string  `json:"at"`
	Outcome   Outcome `json:"outcome,omitempty"`
	// Tentative is P(the owner was tentative, not sure): it scales the signal's effects down by up to
	// half when they are recorded, and is not stored itself.
	Tentative float64 `json:"-"`
}

// Effect is what a signal did to one line: the probabilities that it supports and contradicts the
// line, and the signal's weight. At, SessionID and Kind are the signal's, so replay needs no join.
type Effect struct {
	SignalUID string  `json:"signal_uid"`
	LineUID   string  `json:"line_uid"`
	PSup      float64 `json:"p_sup"`
	PCon      float64 `json:"p_con"`
	Weight    float64 `json:"weight"`
	At        string  `json:"at"`
	SessionID string  `json:"session_id,omitempty"`
	Kind      string  `json:"kind,omitempty"`
}

// AddSignal records a signal (uid and time assigned when empty) and returns its uid.
func (d *DB) AddSignal(s Signal) (string, error) {
	if s.UID == "" {
		s.UID = newUID()
	}
	if s.At == "" {
		s.At = now()
	}
	if s.Options == "" {
		s.Options = "[]"
	}
	_, err := d.db.Exec(`INSERT INTO signals(uid, owner, kind, words, options, chosen, team_id, session_id, at, outcome) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.UID, s.Owner, s.Kind, s.Words, s.Options, s.Chosen, s.TeamID, s.SessionID, s.At, s.Outcome)
	return s.UID, err
}

// RecentSignals are the owner's newest signals, newest first.
func (d *DB) RecentSignals(owner string, limit int) ([]Signal, error) {
	rows, err := d.db.Query(`SELECT uid, owner, kind, words, options, chosen, team_id, session_id, at, outcome FROM signals WHERE owner = ? ORDER BY at DESC, uid DESC LIMIT ?`, owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Signal{}
	for rows.Next() {
		var s Signal
		if err := rows.Scan(&s.UID, &s.Owner, &s.Kind, &s.Words, &s.Options, &s.Chosen, &s.TeamID, &s.SessionID, &s.At, &s.Outcome); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// WaitingSignals counts the signals that have no effect: they wait in the ledger.
func (d *DB) WaitingSignals() (n int, err error) {
	err = d.db.QueryRow(`SELECT COUNT(*) FROM signals WHERE uid NOT IN (SELECT signal_uid FROM signal_effects)`).Scan(&n)
	return
}

// AddEffects records effects in one transaction.
func (d *DB) AddEffects(effects []Effect) error {
	return d.tx(func(tx *sql.Tx) error {
		for _, e := range effects {
			if _, err := tx.Exec(`INSERT INTO signal_effects(signal_uid, line_uid, p_sup, p_con, weight, at, session_id, kind) VALUES (?,?,?,?,?,?,?,?)`,
				e.SignalUID, e.LineUID, e.PSup, e.PCon, e.Weight, e.At, e.SessionID, e.Kind); err != nil {
				return err
			}
		}
		return nil
	})
}

// Answering a design or direction question for the owner (a line made from the owner's own choices)
// is a separate, tighter use of a belief than steering: an answer is auto-given when the line's
// Beta(α, β) has mean ≥ AnswerMu and α ≥ AnswerAlpha, from a prior Beta(1,1): two consistent
// owner answers make Beta(3,1) = 0.75, and one answer against it makes Beta(3,2) = 0.6, so the
// question goes back to the owner. The evidence is the owner's answers in that one team.
const (
	AnswerMu    = 0.75
	AnswerAlpha = 3.0
)

// answerFresh is how long an answer's evidence stays fresh: after it the question goes back to the
// owner, whatever the count (forgetting would fade it too, but slowly).
const answerFresh = 180 * 24 * time.Hour

// AnswerGuides: the evidence may answer the owner's question for the owner: the owner has chosen it
// (not only stated it), it is consistent (mean ≥ AnswerMu), its newest answer is not against it, there is
// enough of it (α ≥ AnswerAlpha,
// counted as it stood at the last answer, so a day of forgetting does not take it below 3) and it is
// recent.
func AnswerGuides(prior [2]float64, es []Effect, now time.Time) bool {
	b := Replay(prior, es, now)
	if b.Last.IsZero() || now.Sub(b.Last) > answerFresh {
		return false
	}
	// The newest evidence decides first: an answer against it, or the owner calling an answer wrong,
	// takes the answering away whatever the older count, and whatever the daily cap did to its weight
	// in the belief; a later consistent answer gives it back.
	last := es[0]
	for _, e := range es[1:] {
		if e.At > last.At || e.At == last.At && e.SignalUID > last.SignalUID {
			last = e
		}
	}
	if last.PCon > last.PSup {
		return false
	}
	return b.Confirmed && b.Mu >= AnswerMu && Replay(prior, es, b.Last).Alpha >= AnswerAlpha
}

// TeamEffects are the effects of the signals one team's work produced (the owner's answers to its
// agents' questions), by line, oldest first: what a team's own evidence for a line is.
func (d *DB) TeamEffects(teamID string) (map[string][]Effect, error) {
	rows, err := d.db.Query(`SELECT e.signal_uid, e.line_uid, e.p_sup, e.p_con, e.weight, e.at, e.session_id, e.kind
		FROM signal_effects e JOIN signals s ON s.uid = e.signal_uid WHERE s.team_id = ? ORDER BY e.at, e.signal_uid`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Effect{}
	for rows.Next() {
		var e Effect
		if err := rows.Scan(&e.SignalUID, &e.LineUID, &e.PSup, &e.PCon, &e.Weight, &e.At, &e.SessionID, &e.Kind); err != nil {
			return nil, err
		}
		out[e.LineUID] = append(out[e.LineUID], e)
	}
	return out, rows.Err()
}

// Effects are the effects on the given lines (all lines when none), by line, oldest first.
func (d *DB) Effects(lineUIDs ...string) (map[string][]Effect, error) {
	q, a := `SELECT signal_uid, line_uid, p_sup, p_con, weight, at, session_id, kind FROM signal_effects`, args(lineUIDs)
	if len(lineUIDs) > 0 {
		q += ` WHERE line_uid IN (` + placeholders(len(lineUIDs)) + `)`
	}
	rows, err := d.db.Query(q+` ORDER BY at, signal_uid`, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Effect{}
	for rows.Next() {
		var e Effect
		if err := rows.Scan(&e.SignalUID, &e.LineUID, &e.PSup, &e.PCon, &e.Weight, &e.At, &e.SessionID, &e.Kind); err != nil {
			return nil, err
		}
		out[e.LineUID] = append(out[e.LineUID], e)
	}
	return out, rows.Err()
}

// RenameOwner gives the lines and signals of one owner name to another: the local account's, when
// the owner signs in to GitHub. Lines get a new updated_at, so the rename wins the merge on other
// machines; a signal's owner is provenance only, so its rename stays local.
func (d *DB) RenameOwner(from, to string) error {
	return d.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE compass SET owner = ?, updated_at = ? WHERE owner = ?`, to, now(), from); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE signals SET owner = ? WHERE owner = ?`, to, from)
		return err
	})
}

// UndoSignal deletes a signal and its effects; the signal's tombstone carries the delete to every
// machine (sync.go). A stated line's creating signal weighed nothing (its prior was that signal),
// so undoing it takes the line too: it would otherwise stay shown at its prior.
func (d *DB) UndoSignal(uid string) error {
	lines, err := d.strings(`SELECT e.line_uid FROM signal_effects e JOIN compass c ON c.uid = e.line_uid
		WHERE e.signal_uid = ? AND e.weight = 0 AND c.origin = 'stated'`, uid)
	if err != nil {
		return err
	}
	return d.tx(func(tx *sql.Tx) error {
		for _, l := range lines {
			if _, err := tx.Exec(`DELETE FROM signal_effects WHERE line_uid = ?`, l); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM compass WHERE uid = ?`, l); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM signal_effects WHERE signal_uid = ?`, uid); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM signals WHERE uid = ?`, uid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Belief is a line's Beta(Alpha, Beta) estimate as read at one moment: Mu = α/(α+β), N the evidence
// left after forgetting, Sigma the uncertainty. A Gaussian belief (ReplayGauss) sets M and S2
// instead of Alpha and Beta, and Mu = P, N = 1/S2, Sigma = √S2.
type Belief struct {
	Alpha, Beta, Mu, N, Sigma float64
	M, S2                     float64
	Last                      time.Time // the latest signal's time
	Confirmed                 bool      // the owner has chosen or spot-checked in its favour
}

const (
	burstWindow = time.Hour
	dailyCap    = 3.0  // most evidence one line gains in a UTC day
	forgetting  = 0.95 // evidence kept per month without a signal
	minEvidence = 2.0
)

// Thresholds are when a belief guides decisions (Guide, SigmaMax), is shown (Show) or is dropped.
type Thresholds struct {
	Guide    float64 `json:"guide"`
	Show     float64 `json:"show"`
	SigmaMax float64 `json:"sigma_max"`
}

var DefaultThresholds = Thresholds{Guide: 0.8, Show: 0.65, SigmaMax: 0.17}

// Outcomes are the outcomes of the owner's synced signals, oldest first.
func (d *DB) Outcomes(owner string) ([]Outcome, error) {
	rows, err := d.db.Query(`SELECT outcome FROM signals WHERE owner = ? AND outcome != '' ORDER BY at, uid`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

const (
	reviewWindow  = 50
	minReviewed   = 20 // reviewed go-aheads before the thresholds move at all
	stepEvery     = 10 // new reviewed go-aheads between two moves
	thresholdStep = 0.02
)

// LearnThresholds replays the outcomes, oldest first, from base (plan C2: the thresholds are
// learned too). Nothing moves before minReviewed reviewed go-aheads, and then at most one step per
// stepEvery new ones. Among the last reviewWindow reviewed go-aheads, more than 5% wrong after a
// review raises Guide and lowers SigmaMax a step; under 2% wrong with 5 misses since the last
// change lowers Guide and raises SigmaMax. Show stays. A pure function of synced records, so every machine
// agrees.
func LearnThresholds(outcomes []Outcome, base Thresholds) Thresholds {
	t := base
	var reviewed []Outcome
	missed, wrongs, fresh := 0, 0, 0 // fresh: reviewed since the last step
	step := func(dir float64) {
		t.Guide = math.Round(min(max(t.Guide+dir*thresholdStep, 0.70), 0.95)*100) / 100
		t.SigmaMax = math.Round(min(max(t.SigmaMax-dir*thresholdStep, 0.12), 0.22)*100) / 100
		missed, fresh = 0, 0
	}
	for _, o := range outcomes {
		switch o {
		case OutcomeMissed:
			missed++
		case OutcomeRight, OutcomeWrong:
			reviewed, fresh = append(reviewed, o), fresh+1
			if o == OutcomeWrong {
				wrongs++
			}
			if len(reviewed) > reviewWindow {
				if reviewed[0] == OutcomeWrong {
					wrongs--
				}
				reviewed = reviewed[1:]
			}
		default:
			continue
		}
		if len(reviewed) < minReviewed || fresh < stepEvery {
			continue
		}
		wrong := float64(wrongs) / float64(len(reviewed))
		if o != OutcomeMissed && wrong > 0.05 {
			step(1)
		} else if wrong < 0.02 && missed >= 5 {
			step(-1)
		}
	}
	return t
}

// HardCountSigmaMax is SigmaMax when the decision model is not calibrated (updates are hard counts).
const HardCountSigmaMax = 0.14

type timed struct {
	Effect
	t time.Time
}

type burst struct {
	start time.Time
	best  Effect
}

// replayBursts are the finite effects in a fixed order and the bursts they make: signals of one
// session within burstWindow count as their heaviest member.
func replayBursts(effects []Effect) ([]timed, []*burst) {
	var es []timed
	for _, e := range effects {
		if finite(e.Weight, e.PSup, e.PCon) { // a foreign file may hold anything
			t, _ := time.Parse(time.RFC3339, e.At)
			es = append(es, timed{e, t})
		}
	}
	// Ties in time are ordered by weight and signal, so the input's order never decides.
	slices.SortFunc(es, func(a, b timed) int {
		return cmp.Or(a.t.Compare(b.t), cmp.Compare(a.Weight, b.Weight), cmp.Compare(a.SignalUID, b.SignalUID))
	})
	var bursts []*burst
	open := map[string]*burst{} // by session
	for _, e := range es {
		b := open[e.SessionID]
		if e.SessionID == "" || b == nil || e.t.Sub(b.start) > burstWindow {
			b = &burst{start: e.t, best: e.Effect}
			bursts = append(bursts, b)
			open[e.SessionID] = b
		} else if e.Weight >= b.best.Weight {
			b.best = e.Effect
		}
	}
	return es, bursts
}

// lastAndConfirmed are the latest effect's time and whether the owner has chosen or spot-checked in
// the line's favour.
func lastAndConfirmed(es []timed) (last time.Time, confirmed bool) {
	for _, e := range es {
		if e.t.After(last) {
			last = e.t
		}
		if (e.Kind == "chose" || e.Kind == "spot_check") && e.PSup > e.PCon {
			confirmed = true
		}
	}
	return
}

// Replay computes a line's belief from its prior and effects, a pure function so every machine
// agrees. Signals of one session within an hour are one judgement, not independent evidence, so a
// burst counts its heaviest member; a line gains at most dailyCap a day. Forgetting is applied
// here, at read time, and never stored, so a merge can't apply it twice.
func Replay(prior [2]float64, effects []Effect, now time.Time) Belief {
	es, bursts := replayBursts(effects)
	bl := Belief{Alpha: prior[0], Beta: prior[1]}
	day, spent := "", 0.0
	for _, b := range bursts {
		da, db := b.best.Weight*b.best.PSup, b.best.Weight*b.best.PCon
		if d := b.start.UTC().Format(time.DateOnly); d != day {
			day, spent = d, 0
		}
		if over := spent + da + db - dailyCap; over > 0 && da+db > 0 {
			k := math.Max(dailyCap-spent, 0) / (da + db)
			da, db = da*k, db*k
		}
		spent += da + db
		bl.Alpha += da
		bl.Beta += db
	}
	bl.Last, bl.Confirmed = lastAndConfirmed(es)
	n := bl.Alpha + bl.Beta
	bl.Mu = bl.Alpha / n
	months := 0.0
	if !bl.Last.IsZero() {
		months = math.Max(now.Sub(bl.Last).Hours()/24/30, 0)
	}
	bl.N = math.Min(n, math.Max(minEvidence, n*math.Pow(forgetting, months)))
	bl.Alpha, bl.Beta = bl.Mu*bl.N, (1-bl.Mu)*bl.N
	bl.Sigma = math.Sqrt(bl.Mu * (1 - bl.Mu) / (bl.N + 1))
	return bl
}

func finite(xs ...float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

// Guides: the line may steer autonomous decisions, once belief is high and settled.
func (b Belief) Guides(t Thresholds) bool {
	return b.Mu >= t.Guide && b.Sigma <= t.SigmaMax
}

// Shown: the line goes into agents' context.
func (b Belief) Shown(t Thresholds) bool { return b.Mu >= t.Show }

// Dropped: the owner clearly doesn't want it.
func (b Belief) Dropped(t Thresholds) bool { return b.Mu < 0.3 && b.Sigma <= t.SigmaMax }
