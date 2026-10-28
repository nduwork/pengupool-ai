package agentdb

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// numericPosterior is the mean and variance of N(m, s2) times the likelihood (1-b)/2 + b·Φ(x/β),
// by the trapezoid rule over m±10σ.
func numericPosterior(m, s2, b, beta2 float64) (mean, v float64) {
	const n = 40001
	sd, beta := math.Sqrt(s2), math.Sqrt(beta2)
	lo, h := m-10*sd, 20*sd/(n-1)
	var z, z1, z2 float64
	for i := 0; i < n; i++ {
		x := lo + float64(i)*h
		f := math.Exp(-(x-m)*(x-m)/(2*s2)) * ((1-b)/2 + b*Phi(x/beta))
		if i == 0 || i == n-1 {
			f /= 2
		}
		z, z1, z2 = z+f, z1+f*x, z2+f*x*x
	}
	mean = z1 / z
	return mean, z2/z - mean*mean
}

func TestGaussUpdateMatchesNumericPosterior(t *testing.T) {
	for _, c := range []struct{ m, s2, b, beta2 float64 }{{0, 1, 0.9, 1}, {1.5, 0.25, -0.8, 1}, {-1, 0.49, 0.998, 0.2}} {
		got := Gauss{c.m, c.s2}.update(c.b, c.beta2)
		m, v := numericPosterior(c.m, c.s2, c.b, c.beta2)
		if math.Abs(got.M-m) > 1e-6 || math.Abs(got.S2-v) > 1e-6 {
			t.Errorf("%+v: got %+v, want %v %v", c, got, m, v)
		}
	}
}

func TestGaussUpdateNoOpAndConflict(t *testing.T) {
	g := Gauss{0.7, 0.3}
	if g.update(0, 1) != g {
		t.Error("b = 0 must be a no-op")
	}
	if u := (Gauss{1.5, 0.25}).update(-0.8, 1); u.S2 <= 0.25 || u.P() < 0.8 {
		t.Errorf("one contradiction: doubt grows, it stays Yes: %+v P=%v", u, u.P())
	}
	if u := (Gauss{0, 1}).update(1, 1); math.Abs(u.P()-0.753) > 1e-3 {
		t.Errorf("one hard signal from N(0,1): %v", u.P())
	}
}

func TestGaussExtremeContradictionIsFinite(t *testing.T) {
	for _, c := range []struct {
		g        Gauss
		b, beta2 float64
	}{{Gauss{8, 0.01}, -1, 0.2}, {Gauss{8, 0.01}, -1, 1e-6}, {Gauss{-8, 0.01}, 1, 1e-6}} {
		u := c.g.update(c.b, c.beta2)
		if math.IsNaN(u.M) || math.IsInf(u.M, 0) || !(u.S2 > 0) || (u.M-c.g.M)*c.b <= 0 {
			t.Fatalf("%+v: %+v", c, u)
		}
	}
}

func TestGaussForgetFloor(t *testing.T) {
	g := Gauss{1.5, 0.25}
	if g.forget(0) != g || g.forget(-3) != g {
		t.Error("no time, no forgetting")
	}
	if p := g.forget(1e6).P(); math.Abs(p-PFloor) > 1e-6 {
		t.Errorf("fades to the floor: %v", p)
	}
	if (Gauss{0.1, 3}).forget(10).S2 != 3 {
		t.Error("forgetting never removes doubt a conflict added")
	}
}

func TestGaussForgetSymmetric(t *testing.T) {
	if p := (Gauss{-1.5, 0.25}).forget(1e6).P(); math.Abs(p-(1-PFloor)) > 1e-6 {
		t.Errorf("a No line fades to 1-PFloor: %v", p)
	}
}

// The expected values come from compass_gauss.py.
func TestGaussReplay(t *testing.T) {
	day := func(d int) time.Time { return t0.AddDate(0, 0, d) }
	said := []Effect{eff(t0, "a", "said", 3, 1)}
	alone := []Effect{eff(t0, "s", "said", 0, 1)}
	choices := []Effect{eff(day(0), "s0", "chose", 1, 1), eff(day(1), "s1", "chose", 1, 1)}
	corr := func(d int, s string) Effect { return eff(day(d), s, "correction", 5, 0) }
	back := eff(day(16*30), "b", "chose", 1, 0.8)
	back.PCon = 0
	for _, c := range []struct {
		name  string
		prior Gauss
		es    []Effect
		at    time.Time
		want  float64
	}{
		{"stated", GaussStated, nil, t0, 0.85},
		{"chose", GaussChose, nil, t0, 0.5},
		{"one choice", GaussChose, choices[:1], day(0), 0.753},
		{"two choices", GaussChose, choices, day(1), 0.877},
		{"stated alone, 16 months", GaussStated, alone, day(16 * 30), 0.798},
		{"stated + said, 36 months", GaussStated, said, day(36 * 30), 0.827},
		{"back after one weak choice", GaussStated, append(slices.Clone(alone), back), day(16 * 30), 0.888},
		{"one correction", GaussStated, append(slices.Clone(said), corr(1, "c")), day(1), 0.346},
		{"two corrections", GaussStated, append(slices.Clone(said), corr(1, "c"), corr(2, "d")), day(2), 0.134},
	} {
		if b := ReplayGauss(c.prior, c.es, c.at); !near(b.Mu, c.want) {
			t.Errorf("%s: %v, want %v", c.name, b.Mu, c.want)
		}
	}
	if b := ReplayGauss(GaussStated, alone, day(14*30)); b.Mu < 0.8 {
		t.Errorf("guides at 14 months: %v", b.Mu)
	}
}

func TestGaussBurstAndDailyCap(t *testing.T) {
	var burst, dayOf []Effect
	for i := 0; i < 10; i++ {
		burst = append(burst, eff(t0.Add(time.Duration(i*5)*time.Minute), "s", "chose", 1, 1))
	}
	for i := 0; i < 5; i++ {
		dayOf = append(dayOf, eff(t0.Add(time.Duration(i*2)*time.Hour), fmt.Sprint("s", i), "said", 3, 1))
	}
	if a, b := ReplayGauss(GaussChose, burst, t0), ReplayGauss(GaussChose, burst[:1], t0); a.Mu != b.Mu {
		t.Errorf("a burst counts once: %v %v", a.Mu, b.Mu)
	}
	if a, b := ReplayGauss(GaussStated, dayOf, t0), ReplayGauss(GaussStated, dayOf[:1], t0); math.Abs(a.Mu-b.Mu) > 1e-3 {
		t.Errorf("the daily cap: %v %v", a.Mu, b.Mu)
	}
}

func TestGaussWeightZeroOnlyTimes(t *testing.T) {
	b := ReplayGauss(GaussStated, []Effect{eff(t0, "a", "said", 0, 1)}, t0)
	if !near(b.Mu, 0.85) || !b.Last.Equal(t0) {
		t.Errorf("%+v", b)
	}
	e := eff(t0, "a", "chose", 1, 0.5) // p_sup = p_con
	if b := ReplayGauss(GaussStated, []Effect{e}, t0); b.M != GaussStated.M || b.S2 != GaussStated.S2 || !b.Last.Equal(t0) {
		t.Errorf("%+v", b)
	}
}

func TestGaussNowBeforeLastSignal(t *testing.T) {
	es := []Effect{eff(t0, "a", "said", 3, 1)}
	if a, b := ReplayGauss(GaussStated, es, t0.AddDate(-1, 0, 0)), ReplayGauss(GaussStated, es, t0); a.S2 != b.S2 {
		t.Errorf("a past now changed doubt: %v %v", a.S2, b.S2)
	}
}

func TestGaussDeterministic(t *testing.T) {
	es := []Effect{eff(t0, "a", "said", 3, 1), eff(t0, "b", "chose", 1, 1), eff(t0.Add(time.Hour), "c", "correction", 5, 0),
		eff(t0.AddDate(0, 2, 0), "d", "chose", 1, 0.3)}
	want := ReplayGauss(GaussStated, es, t0.AddDate(0, 3, 0))
	junk := eff(t0, "x", "said", math.NaN(), 1)
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 20; i++ {
		mixed := append(slices.Clone(es), junk)
		r.Shuffle(len(mixed), func(i, j int) { mixed[i], mixed[j] = mixed[j], mixed[i] })
		if got := ReplayGauss(GaussStated, mixed, t0.AddDate(0, 3, 0)); got != want {
			t.Fatalf("order or junk changed the belief: %+v vs %+v", got, want)
		}
	}
}

func withEngine(t *testing.T, e string) {
	old := Engine
	Engine = e
	t.Cleanup(func() { Engine = old })
}

func TestEngineSeam(t *testing.T) {
	said := []Effect{eff(t0, "a", "said", 3, 1)}
	if b := Replay(PriorStated, said, t0); b.S2 != 0 || !near(b.Mu, ReplayBeta(PriorStated.Beta, said, t0).Mu) {
		t.Errorf("beta is the default: %+v", b)
	}
	withEngine(t, "gauss")
	th := DefaultThresholds
	if b := Replay(PriorStated, nil, t0); !b.Guides(th) || !b.Shown(th) || b.Dropped(th) {
		t.Errorf("a stated line guides at once: %+v", b)
	}
	if b := (Belief{Mu: 0.19, S2: 1}); !b.Dropped(th) {
		t.Error("gauss drops below DropP whatever sigma")
	}
	day := func(d int) time.Time { return t0.AddDate(0, 0, d) }
	one := []Effect{eff(day(0), "s0", "chose", 1, 1)}
	two := append(slices.Clone(one), eff(day(1), "s1", "chose", 1, 1))
	if AnswerGuides(PriorChose, one, day(1)) || !AnswerGuides(PriorChose, two, day(2)) {
		t.Error("answering needs two consistent answers")
	}
	if AnswerGuides(PriorChose, append(slices.Clone(two), eff(day(3), "s2", "chose", 1, 0)), day(3)) {
		t.Error("the newest answer against takes answering away")
	}
}
