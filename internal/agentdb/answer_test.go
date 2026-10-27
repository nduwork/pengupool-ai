package agentdb

import (
	"testing"
	"time"
)

// A line made from the owner's choices answers for the owner on two consistent answers
// (Beta(3,1) = 0.75, α = 3), and one answer against it takes that away again (Beta(3,2) = 0.6).
func TestAnswerGuidesBeta(t *testing.T) {
	day := func(n int) time.Time { return t0.AddDate(0, 0, n) }
	chose := func(n int, sup float64) Effect { return eff(day(n), "s", "chose", WeightChose, sup) }
	for name, c := range map[string]struct {
		es   []Effect
		now  time.Time
		want bool
	}{
		"none":                 {nil, day(5), false},
		"one answer":           {[]Effect{chose(0, 1)}, day(5), false},
		"two consistent":       {[]Effect{chose(0, 1), chose(1, 1)}, day(5), true},
		"then one against":     {[]Effect{chose(0, 1), chose(1, 1), chose(2, 0)}, day(5), false},
		"three, one against":   {[]Effect{chose(0, 1), chose(1, 1), chose(2, 1), chose(3, 0)}, day(5), false},
		"six, newest against":  {[]Effect{chose(0, 1), chose(1, 1), chose(2, 1), chose(3, 1), chose(4, 1), chose(5, 1), chose(6, 0)}, day(8), false},
		"six, against, again":  {[]Effect{chose(0, 1), chose(1, 1), chose(2, 1), chose(3, 1), chose(4, 1), chose(5, 1), chose(6, 0), chose(7, 1)}, day(9), true},
		"capped day, against":  {[]Effect{chose(0, 1), chose(1, 1), eff(day(2), "a", "chose", 1, 1), eff(day(2).Add(2*time.Hour), "b", "chose", 1, 1), eff(day(2).Add(4*time.Hour), "c", "chose", 1, 1), eff(day(2).Add(6*time.Hour), "d", "correction", WeightCorrect, 0)}, day(5), false},
		"judged wrong":         {[]Effect{chose(0, 1), chose(1, 1), eff(day(2), "s", "correction", WeightCorrect, 0)}, day(5), false},
		"two in one burst":     {[]Effect{eff(t0, "s", "chose", 1, 1), eff(t0.Add(10*time.Minute), "s", "chose", 1, 1)}, day(5), false}, // one session, one hour: one judgement
		"stated, never chosen": {[]Effect{eff(day(0), "a", "said", WeightSaid, 1), eff(day(1), "b", "said", WeightSaid, 1)}, day(5), false},
		"stale after 200 days": {[]Effect{chose(0, 1), chose(1, 1)}, day(200), false},
		"fresh at 100 days":    {[]Effect{chose(0, 1), chose(1, 1)}, day(100), true},
	} {
		if got := AnswerGuides(PriorChose, c.es, c.now); got != c.want {
			b := Replay(PriorChose, c.es, c.now)
			t.Errorf("%s: AnswerGuides = %v, want %v (%+v)", name, got, c.want, b)
		}
	}
}

// The evidence for an answer is the owner's own answers in that team, not the other teams'.
func TestTeamEffectsAreOneTeamsAnswers(t *testing.T) {
	d := open(t)
	line, err := d.PutLine(Line{Body: "Use JSONL for logs.", Origin: "chose", Owner: "me"})
	if err != nil {
		t.Fatal(err)
	}
	for i, team := range []string{"a", "a", "b"} {
		at := t0.AddDate(0, 0, i).Format(time.RFC3339)
		uid, err := d.AddSignal(Signal{Owner: "me", Kind: "chose", TeamID: team, At: at})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.AddEffects([]Effect{{SignalUID: uid, LineUID: line, PSup: 1, Weight: WeightChose, At: at, Kind: "chose"}}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := d.TeamEffects("a")
	b, _ := d.TeamEffects("b")
	none, _ := d.TeamEffects("c")
	if len(a[line]) != 2 || len(b[line]) != 1 || len(none) != 0 {
		t.Fatalf("a %d, b %d, c %d", len(a[line]), len(b[line]), len(none))
	}
	if !AnswerGuides(PriorChose, a[line], t0.AddDate(0, 0, 5)) || AnswerGuides(PriorChose, b[line], t0.AddDate(0, 0, 5)) {
		t.Error("team a's two answers answer for team a only")
	}
}
