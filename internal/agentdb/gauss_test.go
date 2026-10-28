package agentdb

import (
	"math"
	"testing"
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
