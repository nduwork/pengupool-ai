// Package decision answers typed questions for triage: team formation, assignment routing, model
// and skill selection, and skill-log duplicate checks. Providers are configured by wire protocol,
// not by brand. A model never produces an execution plan; it only picks among options PenguPool
// already validated, and callers keep a deterministic rules path for when no provider answers.
package decision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// Question kinds.
const (
	Predicate = "predicate"
	Choice    = "choice"
	Score     = "score"
)

// Question is one typed question.
type Question struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Instructions any               `json:"instructions"`
	Options      map[string]string `json:"options,omitempty"` // choice: option → description
	Levels       []string          `json:"levels,omitempty"`  // score: ordered level descriptions
}

// Request is the state plus the questions about it.
type Request struct {
	State     any        `json:"state"`
	Questions []Question `json:"questions"`
}

// Answer is one validated, typed answer.
type Answer struct {
	Type          string             `json:"type"`
	Yes           float64            `json:"yes,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Confidence is the provider's calibrated confidence.
	Confidence float64 `json:"confidence"`
}

// Response carries the answers and what produced them.
type Response struct {
	Answers    map[string]Answer `json:"answers"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	Calibrated bool              `json:"calibrated"`
	Latency    time.Duration     `json:"latency"`
	TokensIn   int               `json:"tokens_in,omitempty"`
	TokensOut  int               `json:"tokens_out,omitempty"`
}

// Provider is the internal decision interface.
type Provider interface {
	Decide(ctx context.Context, req Request) (Response, error)
	Name() string
}

// Errors callers map to protocol codes.
var (
	ErrTimeout = errors.New("provider_timeout")
	ErrRefused = errors.New("provider_refused")
)

// Thresholds are initial calibration candidates, not validated values.
var Thresholds = map[string]float64{"route": 0.80, "model": 0.75, "skill": 0.30, "team": 0.70, "duplicate": 0.75, "experience": 0.75, "agent": 0.50, "proceed": 0.80, "delegate": 0.80}

// Formats PenguPool speaks.
// Triage models speak System One only: online (Jev's) or local on Ollama. A general chat model
// gives no calibrated confidence, so PenguPool does not use one.
var Formats = []string{"systemone", "ollama-systemone"}

// FromConfig returns the active provider, or nil when only rules are configured.
func FromConfig(c store.DecisionConfig, logf func(string, ...any)) Provider {
	if c.Active == "" {
		return nil
	}
	pc, ok := c.Providers[c.Active]
	if !ok {
		return nil
	}
	p, err := New(pc)
	if err != nil {
		logf("decision: %v", err)
		return nil
	}
	return p
}

// New builds a provider from saved settings.
func New(pc store.ProviderConfig) (Provider, error) {
	if err := Validate(pc); err != nil {
		return nil, err
	}
	return &httpProvider{cfg: pc, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Validate checks address syntax and required fields. It says nothing about reachability; that is
// Check's job.
func Validate(pc store.ProviderConfig) error {
	u, err := url.Parse(pc.Address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("address must be an http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" {
		return fmt.Errorf("address must not carry credentials or a query; keys go in the key field")
	}
	known := false
	for _, f := range Formats {
		known = known || f == pc.Format
	}
	if !known {
		return fmt.Errorf("unknown API format %q", pc.Format)
	}
	needModel := pc.Format != "systemone" // Laya-style System One may route without a model
	if needModel && strings.TrimSpace(pc.Model) == "" {
		return fmt.Errorf("the %s format needs a model string", pc.Format)
	}
	if pc.KeyRef != "" && !strings.HasPrefix(pc.KeyRef, "keychain:") && !strings.HasPrefix(pc.KeyRef, "env:") {
		return fmt.Errorf("key reference must be keychain:<name> or env:<VAR>")
	}
	if pc.Type == "jev" && pc.KeyRef == "" {
		return fmt.Errorf("this provider needs an API key")
	}
	return nil
}

type httpProvider struct {
	cfg    store.ProviderConfig
	client *http.Client
}

func (p *httpProvider) Name() string {
	return firstNonEmpty(p.cfg.Name, p.cfg.Type) + "/" + p.cfg.Format
}

func (p *httpProvider) endpoint() string {
	a := strings.TrimRight(p.cfg.Address, "/")
	switch p.cfg.Format {
	case "ollama-systemone":
		return a + "/v1/systemone"
	}
	return a // systemone: a full endpoint URL
}

// redactQuestions returns the questions with secrets removed from the text that goes to the model: their
// instructions and options embed Compass lines and raw lines.
func redactQuestions(qs []Question) []Question {
	out := slices.Clone(qs)
	for i, q := range out {
		out[i].Instructions = Redact(q.Instructions) // any shape: a helper's `decide` may send structured instructions
		if q.Options != nil {
			out[i].Options = map[string]string{}
			for k, v := range q.Options {
				out[i].Options[k], _ = Redact(v).(string)
			}
		}
	}
	return out
}

// Decide asks the questions and validates every answer before returning it.
func (p *httpProvider) Decide(ctx context.Context, req Request) (Response, error) {
	if err := validQuestions(req.Questions); err != nil {
		return Response{}, err
	}
	key, err := ResolveKey(p.cfg.KeyRef)
	if err != nil {
		return Response{}, err
	}
	state := Redact(req.State)
	req.Questions = redactQuestions(req.Questions)
	start := time.Now()
	res, err := p.systemOne(ctx, key, state, req.Questions)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return Response{}, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return Response{}, err
	}
	res.Provider, res.Latency = p.Name(), time.Since(start)
	if err := validAnswers(req.Questions, res.Answers); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	return res, nil
}

func (p *httpProvider) post(ctx context.Context, key string, body any, headers map[string]string) ([]byte, error) {
	b, _ := json.Marshal(body)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrRefused, resp.StatusCode, msg)
	}
	return out, nil
}

// systemOne maps predicates to noul, choices to a criteria map and scores to a criteria list.
func (p *httpProvider) systemOne(ctx context.Context, key string, state any, qs []Question) (Response, error) {
	questions := map[string]any{}
	for _, q := range qs {
		switch q.Type {
		case Predicate:
			questions[q.Name] = map[string]any{"type": "noul", "instructions": q.Instructions}
		case Choice:
			crit := map[string]any{}
			for k, v := range q.Options {
				if v == "" {
					crit[k] = nil
				} else {
					crit[k] = v
				}
			}
			questions[q.Name] = map[string]any{"type": "choice", "instructions": q.Instructions, "criteria": crit}
		case Score:
			questions[q.Name] = map[string]any{"type": "score", "instructions": q.Instructions, "criteria": q.Levels}
		}
	}
	body := map[string]any{"state": state, "questions": questions}
	if p.cfg.Model != "" {
		body["model"] = p.cfg.Model
	}
	raw, err := p.post(ctx, key, body, nil)
	if err != nil {
		return Response{}, err
	}
	var out struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string             `json:"type"`
			Noul          *float64           `json:"noul"`
			Choice        string             `json:"choice"`
			Score         *float64           `json:"score"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		} `json:"answers"`
		Usage struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("%w: unreadable System One response: %v", ErrRefused, err)
	}
	res := Response{Answers: map[string]Answer{}, Model: firstNonEmpty(out.Model, p.cfg.Model), Calibrated: true,
		TokensIn: out.Usage.In, TokensOut: out.Usage.Out}
	for _, q := range qs {
		a, ok := out.Answers[q.Name]
		if !ok {
			continue
		}
		ans := Answer{Type: q.Type, Probabilities: a.Probabilities, Confidence: a.Confidence}
		switch q.Type {
		case Predicate:
			if a.Noul != nil {
				ans.Yes = *a.Noul
				ans.Confidence = math.Abs(*a.Noul-0.5) * 2
			} else {
				ans.Yes = math.NaN()
			}
		case Choice:
			ans.Choice = a.Choice
		case Score:
			if a.Score != nil {
				ans.Score = *a.Score
			} else {
				ans.Score = math.NaN()
			}
		}
		res.Answers[q.Name] = ans
	}
	return res, nil
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

var nameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

func validQuestions(qs []Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("no questions")
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if !nameRe.MatchString(q.Name) || seen[q.Name] {
			return fmt.Errorf("bad or duplicate question name %q", q.Name)
		}
		seen[q.Name] = true
		switch q.Type {
		case Predicate:
		case Choice:
			if len(q.Options) < 2 || len(q.Options) > 255 {
				return fmt.Errorf("choice %s needs 2..255 options", q.Name)
			}
		case Score:
			if len(q.Levels) < 2 || len(q.Levels) > 10 {
				return fmt.Errorf("score %s needs 2..10 levels", q.Name)
			}
		default:
			return fmt.Errorf("unknown question type %q", q.Type)
		}
	}
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// validAnswers rejects a missing answer, a choice outside the options, or non-finite numbers.
func validAnswers(qs []Question, as map[string]Answer) error {
	for _, q := range qs {
		a, ok := as[q.Name]
		if !ok {
			return fmt.Errorf("no answer for %s", q.Name)
		}
		if !finite(a.Confidence) || a.Confidence < 0 || a.Confidence > 1 {
			return fmt.Errorf("%s: confidence out of range", q.Name)
		}
		for k, v := range a.Probabilities {
			if !finite(v) || v < 0 || v > 1 {
				return fmt.Errorf("%s: probability for %s out of range", q.Name, k)
			}
		}
		switch q.Type {
		case Predicate:
			if !finite(a.Yes) || a.Yes < 0 || a.Yes > 1 {
				return fmt.Errorf("%s: not a probability", q.Name)
			}
		case Choice:
			if _, ok := q.Options[a.Choice]; !ok {
				return fmt.Errorf("%s: %q is not a permitted choice", q.Name, a.Choice)
			}
		case Score:
			if !finite(a.Score) || a.Score < 0 || a.Score > float64(len(q.Levels)-1) {
				return fmt.Errorf("%s: score outside its legend", q.Name)
			}
		}
	}
	return nil
}

// QuestionHash identifies a question set for the decision line, without the state.
func QuestionHash(qs []Question) string {
	b, _ := json.Marshal(qs)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:12]
}

// Line renders the one-line decision record for a run log.
func Line(point string, res Response, qs []Question, fallback bool, err error) string {
	ans, _ := json.Marshal(res.Answers)
	conf := 0.0
	for _, a := range res.Answers {
		conf = math.Max(conf, a.Confidence)
	}
	s := fmt.Sprintf("decision %s: provider=%s model=%s questions=%s answers=%s confidence=%.2f fallback=%t latency=%dms tokens=%d/%d",
		point, firstNonEmpty(res.Provider, "rules"), firstNonEmpty(res.Model, "-"), QuestionHash(qs), ans, conf, fallback,
		res.Latency.Milliseconds(), res.TokensIn, res.TokensOut)
	if err != nil {
		s += " error=" + err.Error()
	}
	return s
}

// SecretRe matches the shapes of credentials; the pool detects and redacts with it too.
var SecretRe = regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|xox[abprs]-[A-Za-z0-9-]{10,}|[sr]k_(live|test)_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{22,}|AIza[0-9A-Za-z_-]{35}|eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)|(api[_-]?key|secret|password|token)\s*[:=]\s*['"]?[A-Za-z0-9_\-/+=]{16,}|[A-Za-z0-9_-]*(api[_-]?key|private[_-]?key|secret|password|token)[A-Za-z0-9_-]*["']?[:=]\s*["']?[^\s"']{8,}|authorization\s*:\s*(bearer|basic|token)\s+\S+|\bbearer\s+[A-Za-z0-9._~+/-]{16,}=*|://[^/\s:@]+:[^@\s]+@|(?:^|[^.\w])(password|passwd|pwd)\s*=\s*(?:"[^"\n]{6,}"|'[^'\n]{6,}'|(?:[0-9][^\s'"&;,()\[\]]*|[^\s'"&;,()\[\]$/=]*[^\w.\s'"&;,()\[\]$/=][^\s'"&;,()\[\]]*)(?:$|[\s'"&;,])))`)

// Redact replaces detected secrets in every string of the state before it leaves the host.
func Redact(v any) any {
	switch t := v.(type) {
	case string:
		return SecretRe.ReplaceAllString(t, "[redacted]")
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			out[k] = Redact(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = Redact(x)
		}
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var generic any
	if json.Unmarshal(b, &generic) != nil {
		return v
	}
	if _, ok := generic.(string); ok {
		return Redact(generic)
	}
	if _, ok := generic.(map[string]any); ok {
		return Redact(generic)
	}
	if _, ok := generic.([]any); ok {
		return Redact(generic)
	}
	return v
}

// KeychainService is the macOS Keychain service name for provider keys.
const KeychainService = "PenguPool"

// ResolveKey reads a key from its reference: keychain:<account> or env:<VAR>.
func ResolveKey(ref string) (string, error) {
	switch {
	case ref == "":
		return "", nil
	case strings.HasPrefix(ref, "env:"):
		v := os.Getenv(strings.TrimPrefix(ref, "env:"))
		if v == "" {
			return "", fmt.Errorf("environment variable %s is not set for the daemon", strings.TrimPrefix(ref, "env:"))
		}
		return v, nil
	case strings.HasPrefix(ref, "keychain:"):
		out, err := exec.Command("security", "find-generic-password", "-s", KeychainService, "-a", strings.TrimPrefix(ref, "keychain:"), "-w").Output()
		if err != nil {
			return "", fmt.Errorf("the key is not in the OS vault (%v); save it again", err)
		}
		return strings.TrimRight(string(out), "\n"), nil
	}
	return "", fmt.Errorf("bad key reference")
}

// StoreKey saves a key in the macOS Keychain. The key goes to security(1) on stdin, never in argv.
func StoreKey(account, key string) error {
	if strings.ContainsAny(account+key, "\"\n\\") {
		return fmt.Errorf("the key contains characters the vault command cannot take")
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a \"%s\" -w \"%s\"\n", KeychainService, account, key))
	if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "error") {
		return fmt.Errorf("OS vault unavailable: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// CheckItem is one of the connection check's questions: what a fit model answers, and what this one did.
type CheckItem struct {
	Name string `json:"name"`
	Want string `json:"want"`
	Got  string `json:"got"`
	OK   bool   `json:"ok"`
}

// Check asks one question of each type PenguPool asks, about a state whose answers are plain, and
// grades the answers: a reply in the decision format that gets them wrong is not fit to decide.
// A calibrated (System One) model must also give the choice's probabilities, which matching weighs.
func Check(ctx context.Context, pc store.ProviderConfig) (Response, []CheckItem, error) {
	p, err := New(pc)
	if err != nil {
		return Response{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := p.Decide(ctx, Request{
		State: "PenguPool connection check. The build failed with 3 errors. The owner said: fix them before you push.",
		Questions: []Question{
			{Name: "build_failed", Type: Predicate, Instructions: "Did the build fail?"},
			{Name: "next", Type: Choice, Instructions: "What does the owner want done before pushing?",
				Options: map[string]string{"fix_errors": "Fix the build errors", "push_now": "Push the change now", "write_docs": "Write documentation"}},
			{Name: "errors", Type: Score, Instructions: "How many build errors are there?", Levels: []string{"none", "one or two", "three or more"}},
		},
	})
	if err != nil {
		return res, nil, err
	}
	a := res.Answers
	items := []CheckItem{
		{Name: "yes/no", Want: "yes", Got: fmt.Sprintf("yes %.2f", a["build_failed"].Yes), OK: a["build_failed"].Yes >= 0.5},
		{Name: "pick one", Want: "fix_errors", Got: a["next"].Choice, OK: a["next"].Choice == "fix_errors"},
		{Name: "score", Want: "level 2", Got: fmt.Sprintf("level %.1f", a["errors"].Score), OK: math.Round(a["errors"].Score) == 2},
	}
	if res.Calibrated {
		sum := 0.0
		for _, v := range a["next"].Probabilities {
			sum += v
		}
		items = append(items, CheckItem{Name: "probabilities", Want: "one per option, summing to 1", Got: fmt.Sprintf("%d, sum %.2f", len(a["next"].Probabilities), sum),
			OK: len(a["next"].Probabilities) == 3 && math.Abs(sum-1) < 0.05})
	}
	return res, items, nil
}
