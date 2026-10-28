package agentdb

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)

func eff(at time.Time, session, kind string, w, sup float64) Effect {
	return Effect{SignalUID: fmt.Sprint(at.UnixNano(), session), LineUID: "l", PSup: sup, PCon: 1 - sup, Weight: w, At: at.Format(time.RFC3339), SessionID: session, Kind: kind}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestReplayPrior(t *testing.T) {
	b := Replay(PriorStated, nil, t0)
	if !near(b.Mu, 0.8) || !near(b.Sigma, 0.1206) || b.N != 10 || !b.Last.IsZero() || b.Confirmed {
		t.Fatalf("%+v", b)
	}
}

func TestReplayBurstCountsOnce(t *testing.T) {
	var burst, spread []Effect
	for i := 0; i < 10; i++ {
		burst = append(burst, eff(t0.Add(time.Duration(i*5)*time.Minute), "s", "chose", 1, 1))
		spread = append(spread, eff(t0.Add(time.Duration(i)*24*time.Hour), fmt.Sprint("s", i), "chose", 1, 1))
	}
	if b := Replay(PriorChose, burst, t0); !near(b.Alpha, 2) || !near(b.Beta, 1) {
		t.Errorf("burst: %+v", b)
	}
	if b := Replay(PriorChose, spread, t0); !near(b.Alpha, 11) {
		t.Errorf("spread: %+v", b)
	}
	// an hour after the burst's first effect starts a new one; an empty session never merges
	e := []Effect{eff(t0, "s", "chose", .5, 1), eff(t0.Add(61*time.Minute), "s", "chose", .5, 1), eff(t0, "", "chose", .5, 1), eff(t0, "", "chose", .5, 1)}
	if b := Replay(PriorChose, e, t0); !near(b.Alpha, 3) {
		t.Errorf("windows: %+v", b)
	}
}

func TestReplayDailyCap(t *testing.T) {
	var e []Effect
	for i := 0; i < 5; i++ {
		e = append(e, eff(t0.Add(time.Duration(i)*2*time.Hour), fmt.Sprint("s", i), "said", 3, 1))
	}
	if b := Replay(Prior{Beta: [2]float64{1, 1}}, e, t0); !near(b.Alpha+b.Beta, 2+3) {
		t.Errorf("%+v", b)
	}
}

func TestReplayForgetting(t *testing.T) {
	e := []Effect{eff(t0, "s", "said", 0, 1)} // no weight: only the time of the last signal
	for _, c := range []struct {
		months int
		sure   bool
	}{{14, true}, {16, false}} {
		b := Replay(PriorStated, e, t0.AddDate(0, 0, c.months*30))
		if (b.Sigma <= DefaultThresholds.SigmaMax) != c.sure || !near(b.Mu, 0.8) || b.N > 10 {
			t.Errorf("%d months: %+v", c.months, b)
		}
	}
	if b := Replay(PriorStated, e, t0.AddDate(-1, 0, 0)); b.N != 10 {
		t.Errorf("N rose: %+v", b)
	}
	if b := Replay(PriorStated, e, t0.AddDate(50, 0, 0)); b.N != 2 {
		t.Errorf("floor: %+v", b)
	}
}

func TestBeliefConfirmedGuides(t *testing.T) {
	th := DefaultThresholds
	said := Replay(PriorStated, []Effect{eff(t0, "a", "said", 3, 1)}, t0)
	if said.Confirmed || !said.Guides(th) || !said.Shown(th) {
		t.Errorf("said: %+v", said)
	}
	against := Replay(PriorStated, []Effect{eff(t0, "a", "chose", 1, 0.2)}, t0)
	if against.Confirmed {
		t.Error("a choice against confirmed")
	}
	chose := Replay(PriorStated, []Effect{eff(t0, "b", "chose", 1, 1)}, t0)
	if !chose.Confirmed || !chose.Guides(th) {
		t.Errorf("chose: %+v", chose)
	}
	if !(Belief{Mu: 0.2, Sigma: 0.1}).Dropped(th) || (Belief{Mu: 0.2, Sigma: 0.2}).Dropped(th) {
		t.Error("Dropped")
	}
}

func TestSignalRoundTripAndUndo(t *testing.T) {
	d, _ := openRaw(t)
	uid, err := d.AddSignal(Signal{Owner: "o", Kind: "chose", Words: "chose X over Y"})
	if err != nil || uid == "" {
		t.Fatal(uid, err)
	}
	if err := d.AddEffects([]Effect{{SignalUID: uid, LineUID: "l1", PSup: 1, Weight: 1, At: now(), Kind: "chose"}, {SignalUID: uid, LineUID: "l2", PCon: 1, Weight: 1, At: now()}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.Effects("l1"); len(m) != 1 || len(m["l1"]) != 1 || m["l1"][0].Weight != 1 {
		t.Fatalf("%+v", m)
	}
	if m, _ := d.Effects(); len(m) != 2 {
		t.Fatalf("%+v", m)
	}
	if err := d.UndoSignal(uid); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.Effects(); len(m) != 0 {
		t.Fatalf("effects stayed: %+v", m)
	}
	if ks, _ := d.strings(`SELECT key FROM tombstones`); len(ks) != 1 || ks[0] != "signals:"+uid {
		t.Fatalf("tombstones %v", ks)
	}
	if d.UndoSignal(uid) != ErrNotFound {
		t.Fatal("undo twice")
	}
}

func TestMergeSignals(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pengupool-agents.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	add := func(d *DB, uid string) {
		_, _ = d.AddSignal(Signal{UID: uid, Owner: "o", Kind: "said"})
		_ = d.AddEffects([]Effect{{SignalUID: uid, LineUID: "l", PSup: 1, Weight: 3, At: now()}})
	}
	add(a, "sa")
	add(b, "sb")
	for _, d := range []*DB{a, b, a, b} { // the equal rows on both sides merge to one
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*DB{a, b} {
		if m, _ := d.Effects("l"); len(m["l"]) != 2 {
			t.Fatalf("effects %+v", m)
		}
		if ids, _ := d.strings(`SELECT uid FROM signals`); len(ids) != 2 {
			t.Fatalf("signals %v", ids)
		}
	}
	if err := b.UndoSignal("sa"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DB{b, a} {
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := a.Effects("l"); len(m["l"]) != 1 || m["l"][0].SignalUID != "sb" {
		t.Fatalf("undo did not reach the other machine: %+v", m)
	}
}

func TestLearnThresholds(t *testing.T) {
	rep := func(o Outcome, n int) []Outcome {
		out := make([]Outcome, n)
		for i := range out {
			out[i] = o
		}
		return out
	}
	base := DefaultThresholds
	if got := LearnThresholds(nil, base); got != base {
		t.Errorf("no outcomes: %+v", got)
	}
	// nothing moves before 20 reviewed go-aheads, however wrong the first ones were
	if got := LearnThresholds(rep(OutcomeWrong, 19), base); got != base {
		t.Errorf("19 wrong: %+v", got)
	}
	// 2 wrong in 20 is 10%: too eager, a step
	twoWrong := append(rep(OutcomeRight, 18), OutcomeWrong, OutcomeWrong)
	if got := LearnThresholds(twoWrong, base); !near(got.Guide, 0.82) || !near(got.SigmaMax, 0.15) || got.Show != base.Show {
		t.Errorf("two wrong in 20: %+v", got)
	}
	// 1 in 20 is 5%, not above it
	if got := LearnThresholds(append(rep(OutcomeRight, 19), OutcomeWrong), base); got != base {
		t.Errorf("5%% wrong moved %+v", got)
	}
	// at most one step per 10 new reviewed go-aheads
	if got := LearnThresholds(rep(OutcomeWrong, 29), base); !near(got.Guide, 0.82) {
		t.Errorf("29 wrong: %+v", got)
	}
	if got := LearnThresholds(rep(OutcomeWrong, 30), base); !near(got.Guide, 0.84) {
		t.Errorf("30 wrong: %+v", got)
	}
	// misses loosen only from the 5th, once 20 are reviewed and while under 2% wrong
	if got := LearnThresholds(rep(OutcomeMissed, 200), base); got != base {
		t.Errorf("misses without reviews: %+v", got)
	}
	if got := LearnThresholds(append(rep(OutcomeRight, 20), rep(OutcomeMissed, 4)...), base); got != base {
		t.Errorf("4 misses: %+v", got)
	}
	if got := LearnThresholds(append(rep(OutcomeRight, 20), rep(OutcomeMissed, 5)...), base); !near(got.Guide, 0.78) || !near(got.SigmaMax, 0.19) {
		t.Errorf("5 misses: %+v", got)
	}
	if got := LearnThresholds(append(rep(OutcomeRight, 20), rep(OutcomeMissed, 10)...), base); !near(got.Guide, 0.78) { // another step waits for 10 more reviewed
		t.Errorf("10 misses: %+v", got)
	}
	wrongFirst := append(append([]Outcome{OutcomeWrong}, rep(OutcomeRight, 19)...), rep(OutcomeMissed, 5)...)
	if got := LearnThresholds(wrongFirst, base); got != base {
		t.Errorf("moved with a wrong in the window: %+v", got)
	}
	// bounds hold
	if got := LearnThresholds(rep(OutcomeWrong, 100), base); !near(got.Guide, 0.95) || !near(got.SigmaMax, 0.12) {
		t.Errorf("upper bound: %+v", got)
	}
	loosening := rep(OutcomeRight, 20)
	for range 10 {
		loosening = append(append(loosening, rep(OutcomeMissed, 5)...), rep(OutcomeRight, 10)...)
	}
	if got := LearnThresholds(loosening, base); !near(got.Guide, 0.70) || !near(got.SigmaMax, 0.22) {
		t.Errorf("lower bound: %+v", got)
	}
	// the window forgets: 50 rights after a wrong leave nothing above 5%, so the last steps are loosening ones
	old := append([]Outcome{OutcomeWrong}, rep(OutcomeRight, 60)...)
	if a, b := LearnThresholds(old, base), LearnThresholds(old, base); a != b {
		t.Error("not deterministic")
	}
	// an uncalibrated model's base is tighter, and the steps are from it
	hard := Thresholds{Guide: 0.8, Show: 0.65, SigmaMax: HardCountSigmaMax}
	if got := LearnThresholds(twoWrong, hard); !near(got.SigmaMax, 0.12) {
		t.Errorf("hard base: %+v", got)
	}
}

// syncFile merges d with the shared file the way a round does: the union replaces the file when it differs.
func syncFile(d *DB, file string) error {
	res, err := d.Merge(file, file+".out")
	if err == nil && res.Changes != "" {
		err = os.Rename(file+".out", file)
	}
	return err
}

func fileCount(t *testing.T, file, q string) (n int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// fileTables are the tables of a shared file, sorted.
func fileTables(t *testing.T, file string) ([]string, error) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rs, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name != 'sqlite_sequence' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		_ = rs.Scan(&s)
		out = append(out, s)
	}
	return out, nil
}

// A merge keeps the older line, moves the other's effects (the heavier one on a clash) and syncs
// without a duplicate or a resurrected effect.
func TestMergeLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pengupool-agents.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	for _, uid := range []string{"s1", "s2", "s3"} {
		_, _ = a.AddSignal(Signal{UID: uid, Owner: "o", Kind: "said"})
	}
	old, _ := a.PutLine(Line{Owner: "o", Body: "Prefer the standard library", CreatedAt: "2026-01-01T00:00:00Z"})
	dup, _ := a.PutLine(Line{Owner: "o", Body: "No new dependencies", Condition: "Go work"})
	at := now()
	_ = a.AddEffects([]Effect{
		{SignalUID: "s1", LineUID: old, PSup: 1, Weight: 1, At: at}, {SignalUID: "s1", LineUID: dup, PSup: 1, Weight: 3, At: at},
		{SignalUID: "s2", LineUID: dup, PSup: 1, Weight: 3, At: at}, {SignalUID: "s3", LineUID: old, PSup: 1, Weight: 3, At: at}, {SignalUID: "s3", LineUID: dup, PSup: 1, Weight: 1, At: at},
	})
	for _, d := range []*DB{a, b} { // b has the old rows before the merge
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.MergeLines(old, dup, "Prefer the standard library; add no dependencies"); err != nil {
		t.Fatal(err)
	}
	l, _ := a.Line(old)
	m, _ := a.Effects()
	w := map[string]float64{}
	for _, e := range m[old] {
		w[e.SignalUID] = e.Weight
	}
	if l.Body != "Prefer the standard library; add no dependencies" || l.Condition != "Go work" || l.Source != "" || len(m) != 1 || len(w) != 3 || w["s1"] != 3 || w["s3"] != 3 {
		t.Fatalf("line %+v, effects %+v", l, m)
	}
	if _, err := a.Line(dup); err != ErrNotFound {
		t.Fatalf("the merged line stayed: %v", err)
	}
	if ks, _ := a.strings(`SELECT key FROM tombstones ORDER BY key`); len(ks) != 2 || ks[0] != "compass:"+dup || ks[1] != "merged:"+dup+":"+old {
		t.Fatalf("tombstones %v", ks)
	}
	for _, d := range []*DB{a, b, a, b} {
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*DB{a, b} {
		m, _ := d.Effects()
		if ls, _ := d.Lines("o", "", "", 0); len(ls) != 1 || ls[0].UID != old || len(m) != 1 || len(m[old]) != 3 || m[old][0].Weight+m[old][1].Weight+m[old][2].Weight != 9 {
			t.Fatalf("after sync: %+v, %+v", ls, m)
		}
	}
}

// A stated line's creating signal weighs nothing (its prior is that signal); merged into another
// line, it becomes ordinary evidence there.
func TestMergeLinesKeepsAStatedLinesSignal(t *testing.T) {
	d, _ := openRaw(t)
	_, _ = d.AddSignal(Signal{UID: "s", Owner: "o", Kind: "said"})
	keep, _ := d.PutLine(Line{Owner: "o", Body: "Prefer the standard library", Origin: "chose"})
	drop, _ := d.PutLine(Line{Owner: "o", Body: "No new dependencies", Origin: "stated"})
	_ = d.AddEffects([]Effect{{SignalUID: "s", LineUID: drop, PSup: 1, Weight: 0, At: now(), Kind: "said"}})
	if err := d.MergeLines(keep, drop, ""); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.Effects(keep); len(m[keep]) != 1 || m[keep][0].Weight != WeightSaid {
		t.Fatalf("%+v", m)
	}
}

func TestOutcomesAreTheOwners(t *testing.T) {
	d, _ := openRaw(t)
	for _, o := range []string{"me", "someone-else", "someone-else"} {
		if _, err := d.AddSignal(Signal{Owner: o, Kind: "correction", Outcome: OutcomeWrong}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := d.Outcomes("me"); len(got) != 1 {
		t.Errorf("outcomes of me: %v", got)
	}
}

func TestReplayIsDeterministicAndSkipsNonFinite(t *testing.T) {
	ef := func(uid string, w, sup, con float64) Effect {
		return Effect{SignalUID: uid, LineUID: "l", PSup: sup, PCon: con, Weight: w, At: t0.Format(time.RFC3339), SessionID: "s", Kind: "chose"}
	}
	a, b := ef("a", 1, 0.9, 0.1), ef("b", 1, 0.1, 0.9)
	if x, y := Replay(PriorChose, []Effect{a, b}, t0), Replay(PriorChose, []Effect{b, a}, t0); x != y {
		t.Errorf("order of equal times decides: %+v vs %+v", x, y)
	}
	good := []Effect{ef("g", 1, 1, 0)}
	bad := append([]Effect{ef("i", math.Inf(1), 1, 0), ef("n", 1, math.NaN(), 0)}, good...)
	if got := Replay(PriorChose, bad, t0); got != Replay(PriorChose, good, t0) {
		t.Errorf("a non-finite effect counted: %+v", got)
	}
}

func TestUndoingAStatedLinesCreatingSignalDeletesIt(t *testing.T) {
	d, _ := openRaw(t)
	line := func(origin string) string {
		uid, err := d.PutLine(Line{Owner: "o", Body: "prefers sql for logging", Origin: origin})
		if err != nil {
			t.Fatal(err)
		}
		sig, _ := d.AddSignal(Signal{Owner: "o", Kind: "said"})
		w := 0.0 // a stated line's creating signal weighs nothing; a chosen one's is evidence
		if origin == "chose" {
			w = WeightChose
		}
		if err := d.AddEffects([]Effect{{SignalUID: sig, LineUID: uid, PSup: 1, Weight: w, At: now()}}); err != nil {
			t.Fatal(err)
		}
		if err := d.UndoSignal(sig); err != nil {
			t.Fatal(err)
		}
		return uid
	}
	if _, err := d.Line(line("stated")); err == nil {
		t.Error("a stated line stayed after its creating signal was undone")
	}
	if _, err := d.Line(line("chose")); err != nil {
		t.Errorf("a chosen line went: %v", err)
	}
}

func TestDeleteLineTakesItsOwnSignals(t *testing.T) {
	d, _ := openRaw(t)
	for _, uid := range []string{"own", "shared", "ledger"} {
		_, _ = d.AddSignal(Signal{UID: uid, Owner: "o", Kind: "said"})
	}
	at := now()
	_ = d.AddEffects([]Effect{{SignalUID: "own", LineUID: "l1", PSup: 1, Weight: 3, At: at}, {SignalUID: "shared", LineUID: "l1", PSup: 1, Weight: 3, At: at}, {SignalUID: "shared", LineUID: "l2", PSup: 1, Weight: 3, At: at}})
	if err := d.DeleteLine("l1"); err != nil {
		t.Fatal(err)
	}
	if ss, _ := d.strings(`SELECT uid FROM signals ORDER BY uid`); len(ss) != 2 || ss[0] != "ledger" || ss[1] != "shared" {
		t.Fatalf("signals %v", ss)
	}
	if m, _ := d.Effects(); len(m) != 1 || len(m["l2"]) != 1 {
		t.Fatalf("effects %+v", m)
	}
	if ks, _ := d.strings(`SELECT key FROM tombstones`); len(ks) != 1 || ks[0] != "signals:own" {
		t.Fatalf("tombstones %v", ks)
	}
}
