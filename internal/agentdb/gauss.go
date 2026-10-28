package agentdb

import "math"

// Gauss is a line's belief as N(M, S2) on a latent "the owner wants this" strength, read as
// P = Φ(M/√S2). It runs in shadow beside the Beta belief (docs/research/2026-10-27-bayesian-memory).
type Gauss struct{ M, S2 float64 }

// The Gaussian's priors and constants, as compass_gauss.py has them. A stated line starts at P = 0.85
// (shows and guides at once, as Beta(8,2) does); a chosen line starts unknown.
var (
	GaussStated = Gauss{0.7328690779592167, 0.5}
	GaussChose  = Gauss{0, 1}
)

const (
	S0Sq    = 1.0                // prior variance: a correction re-opens a line to it; silence never fades below it
	Beta0   = 1.0                // probit noise of weight-1 evidence; weight w uses Beta0/w
	Tau2    = 0.017              // doubt added per 30 silent days: a stated line alone leaves Guide after ~15 months
	PFloor  = 0.65               // silence fades a line to here (Show) and no further
	kFloor  = 0.3853204664075676 // Φ⁻¹(PFloor)
	AnswerP = 0.85               // P a chosen line needs to answer for the owner
	DropP   = 0.2                // P under which a line is dropped
)

// Phi is the standard normal CDF.
func Phi(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

func phi(x float64) float64 { return math.Exp(-x*x/2) / math.Sqrt(2*math.Pi) }

// P is the probability the owner wants the line.
func (g Gauss) P() float64 { return Phi(g.M / math.Sqrt(g.S2)) }

// update moment-matches N(M, S2) times the likelihood (1-b)/2 + b·Φ(x/β), b = p_sup - p_con and
// β² = beta2. b = 0 is a no-op.
func (g Gauss) update(b, beta2 float64) Gauss {
	s := math.Sqrt(beta2 + g.S2)
	t := g.M / s
	// Written this way Z stays finite in the tails; when even so it underflows (a near-certain line
	// fully contradicted), the inverse Mills ratio's asymptote stands in for b·φ(t)/Z.
	z := (1+b)/2*Phi(t) + (1-b)/2*Phi(-t)
	v := b * phi(t) / z
	if z < 1e-300 {
		v = -(t + 1/t - 2/(t*t*t))
	}
	w := v * (v + t)
	return Gauss{g.M + g.S2/s*v, g.S2 * (1 - g.S2/(s*s)*w)}
}

// forget adds doubt for months of silence until the line has faded to PFloor (or 1-PFloor for a
// No line); M stays, and doubt never shrinks.
func (g Gauss) forget(months float64) Gauss {
	if months <= 0 {
		return g
	}
	c := max(S0Sq, (g.M/kFloor)*(g.M/kFloor))
	g.S2 = max(g.S2, min(g.S2+Tau2*months, c))
	return g
}
