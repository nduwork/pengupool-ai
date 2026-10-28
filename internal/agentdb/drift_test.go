package agentdb

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDriftBenchmark replays simulated lines whose truth drifts under both beliefs and logs how each
// tracks it. It asserts only that every belief is a probability; the comparison is read by a person
// before the Gaussian becomes the default.
//
// The "pahf" scenario takes its drift from PAHF's shopping personas (github.com/facebookresearch/PAHF,
// MIT License, Copyright (c) Meta Platforms, Inc. and affiliates): of 1200 preference lines (300
// favourite options, 900 dislikes, not counting like_second) 682 change between persona_info.json and updated_persona_info.json,
// all at once, between its learning phases; here at month 18.
func TestDriftBenchmark(t *testing.T) {
	pahfLine := 0
	scenarios := []struct {
		name  string
		lines int
		pick  func(r *rand.Rand) (kind, sw int) // kind: 0 stable, 1 switch at month sw, 2 recurring
	}{
		{"simulated", 300, func(r *rand.Rand) (int, int) { return r.IntN(3), 3 + r.IntN(16) }},
		{"pahf", 1200, func(r *rand.Rand) (int, int) {
			if pahfLine++; pahfLine <= 682 {
				return 1, 18
			}
			return 0, 0
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) { driftBenchmark(t, sc.lines, sc.pick) })
	}
}

func driftBenchmark(t *testing.T, lines int, pick func(*rand.Rand) (int, int)) {
	r := rand.New(rand.NewPCG(7, 7))
	type engine struct {
		name   string
		replay func(Prior, []Effect, time.Time) Belief
	}
	engines := []engine{
		{"beta", func(p Prior, es []Effect, at time.Time) Belief { return ReplayBeta(p.Beta, es, at) }},
		{"gauss", func(p Prior, es []Effect, at time.Time) Belief { return ReplayGauss(p.Gauss, es, at) }},
	}
	type stats struct {
		brier              [5]float64 // overall, then by months since the last signal: 0-3, 3-6, 6-12, 12+
		count              [5]int
		guided, wrongGuide int
		recover            []int // signals from a switch until the belief is right again
		unrecovered        int
		flips              int // Guides changing between month-ends under a stable truth
	}
	st := make([]stats, len(engines))
	th := DefaultThresholds
	const days = 36 * 30
	at := func(d int) time.Time { return t0.AddDate(0, 0, d) }
	for line := 0; line < lines; line++ {
		kind, sw := pick(r)
		truth := func(d int) bool {
			m := d / 30
			switch kind {
			case 1:
				return m < sw
			case 2:
				return m/9%2 == 0
			}
			return true
		}
		quiet := -1 // the start of a 6-month silence, if any
		if r.Float64() < 0.3 {
			quiet = r.IntN(days)
		}
		var es []Effect
		var prior Prior
		prev := true
		for d := 0; d < days; d++ {
			if r.Float64() >= 1.0/45 || quiet >= 0 && d >= quiet && d < quiet+180 {
				continue
			}
			yes, switched := truth(d), truth(d) != prev
			prev = yes
			p := 0.1
			if yes {
				p = 0.9
			}
			p = min(max(p+0.2*r.Float64()-0.1, 0), 1)
			e := Effect{SignalUID: fmt.Sprint(line, "-", d), LineUID: "l", PSup: p, PCon: 1 - p, At: at(d).Format(time.RFC3339), SessionID: fmt.Sprint(d)}
			switch u := r.Float64(); {
			case u < 0.15 && switched && !yes:
				e.Kind, e.Weight, e.PSup, e.PCon = "correction", WeightCorrect, 0, 1
			case u < 0.40 && u >= 0.15:
				e.Kind, e.Weight = "said", WeightSaid
				if r.Float64() < 0.1 { // hedged
					e.PSup, e.PCon = e.PSup*0.5, e.PCon*0.5
				}
			default:
				e.Kind, e.Weight = "chose", WeightChose
			}
			if len(es) == 0 {
				prior = PriorChose
				if e.Kind == "said" { // a stated line: its creating signal is its prior
					prior, e.Weight = PriorStated, 0
				}
			}
			es = append(es, e)
		}
		if len(es) == 0 {
			continue
		}
		first, _ := time.Parse(time.RFC3339, es[0].At)
		for i, en := range engines {
			s := &st[i]
			// month-end probes
			var lastGuides *bool
			for m := 1; m <= 36; m++ {
				now := at(m * 30)
				if now.Before(first) {
					continue
				}
				seen := es[:upTo(es, now)]
				b := en.replay(prior, seen, now)
				if math.IsNaN(b.Mu) || b.Mu < 0 || b.Mu > 1 {
					t.Fatalf("%s line %d month %d: Mu %v", en.name, line, m, b.Mu)
				}
				yes := truth(m*30 - 1)
				y := 0.0
				if yes {
					y = 1
				}
				silent := now.Sub(b.Last).Hours() / 24 / 30
				bin := 1 + min(int(silent/3), 2)
				if silent >= 12 {
					bin = 4
				}
				for _, k := range []int{0, bin} {
					s.brier[k] += (b.Mu - y) * (b.Mu - y)
					s.count[k]++
				}
				g := b.Guides(th)
				if g {
					s.guided++
					if !yes {
						s.wrongGuide++
					}
				}
				if kind == 0 && lastGuides != nil && *lastGuides != g {
					s.flips++
				}
				lastGuides = &g
			}
			// recovery after each switch the line saw a signal for
			if kind == 0 {
				continue
			}
			for j := 1; j < len(es); j++ {
				dj, _ := time.Parse(time.RFC3339, es[j].At)
				dp, _ := time.Parse(time.RFC3339, es[j-1].At)
				yes := truth(int(dj.Sub(t0).Hours() / 24))
				if yes == truth(int(dp.Sub(t0).Hours()/24)) {
					continue
				}
				n := -1
				for k := j; k < len(es) && n < 0; k++ {
					dk, _ := time.Parse(time.RFC3339, es[k].At)
					if truth(int(dk.Sub(t0).Hours()/24)) != yes {
						break // switched back first
					}
					b := en.replay(prior, es[:k+1], dk)
					if yes && b.Guides(th) || !yes && b.Dropped(th) {
						n = k - j + 1
					}
				}
				if n < 0 {
					s.unrecovered++
				} else {
					s.recover = append(s.recover, n)
				}
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-6s %7s %7s %7s %7s %7s %11s %9s %11s %6s\n", "engine", "brier", "0-3", "3-6", "6-12", "12+", "wrong-guide", "recover", "unrecovered", "flips")
	for i, en := range engines {
		s := st[i]
		fmt.Fprintf(&b, "%-6s", en.name)
		for k := range s.brier {
			fmt.Fprintf(&b, " %7.4f", s.brier[k]/max(float64(s.count[k]), 1))
		}
		med := math.NaN()
		if len(s.recover) > 0 {
			slices.Sort(s.recover)
			med = float64(s.recover[len(s.recover)/2])
		}
		fmt.Fprintf(&b, " %11.4f %9.1f %11d %6d\n", float64(s.wrongGuide)/max(float64(s.guided), 1), med, s.unrecovered, s.flips)
	}
	t.Log(b.String())
}

// upTo is how many of es (oldest first) are at or before now.
func upTo(es []Effect, now time.Time) int {
	n, _ := slices.BinarySearchFunc(es, now, func(e Effect, now time.Time) int {
		at, _ := time.Parse(time.RFC3339, e.At)
		if at.After(now) {
			return 1
		}
		return -1
	})
	return n
}
