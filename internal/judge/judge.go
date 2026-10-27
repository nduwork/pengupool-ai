// Package judge adapts the configured decision model to typed questions:
// signal kind, line relation, lesson relevance, and owner-message triage.
//
// AskJSON is a variable so tests can stub the model without a network.
package judge

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"

	"github.com/nduwork/pengupool-ai/internal/model"
)

// Kinds of utterances the decision model distinguishes.
var Kinds = []string{"preference", "restriction", "choice", "correction", "none"}

const tokenKey = `(api[ _-]?key|api[ _-]?token|auth[ _-]?token)`
const passKey = `(secret|password|passwd|pwd)`

var secretRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)["']?[A-Za-z0-9_]*` + tokenKey + `\b["']?\s*[:=]\s*("[^"]*"|'[^']*'|\S+)`),
	regexp.MustCompile(`(?i)["']?[A-Za-z0-9_]*` + passKey + `\b["']?\s*[:=]\s*("[^"]*"|'[^']*'|.+)`),
	regexp.MustCompile(`(?i)["']?[A-Za-z0-9_]*` + tokenKey + `\b["']?\s+(is|are)\s*[:=]?\s*("[^"]*"|'[^']*'|\S+)`),
	regexp.MustCompile(`(?i)["']?[A-Za-z0-9_]*` + passKey + `\b["']?\s+(is|are)\s*[:=]?\s*("[^"]*"|'[^']*'|.+)`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`\b(ghp_[A-Za-z0-9]+|gho_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+|AKIA[0-9A-Z]{16}|xox[bap]-[A-Za-z0-9-]+)\b`),
	regexp.MustCompile(`\b[A-Za-z0-9+/]{40,}={0,2}`),
}

// Redact scrubs secrets before a model sees them. It errs toward redaction.
func Redact(text string) string {
	for _, rx := range secretRes {
		text = rx.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}

// AskJSON calls the decision model. Tests replace it with a stub.
var AskJSON = func(system, user, m string) (any, error) {
	return model.AskJSON(system, user, resolve(m))
}

func resolve(m string) string {
	if m != "" {
		return m
	}
	return os.Getenv("PENGUPOOL_JUDGE_MODEL")
}

const signalPrompt = `Read what was said to or by a coding agent. Classify it into exactly one kind:
- preference: a standing like ("prefer small commits", "from now on always X")
- restriction: a standing ban ("never touch X", "don't do Y without asking")
- choice: a one-off decision between options ("ship it?", "A or B?")
- correction: feedback that past behavior was wrong ("that broke X", "you should have Y")
- none: anything else (status, FYI, question with no decision)
Answer with JSON only: {"kind": "<one of the five>", "confidence": <0..1>}`

const relationPrompt = `A Compass line and a new utterance. Decide whether the utterance supports the
line (evidence it still holds), contradicts it (evidence it changed), or is unrelated.
Answer with JSON only: {"relation": "supports"|"contradicts"|"unrelated", "confidence": <0..1>}`

const relevancePrompt = `A helper's task and candidate lessons from earlier tasks. For each candidate,
say whether its lesson would help this task and how confident you are.
Answer with JSON only: {"ratings": [{"id": "<candidate id>", "relevant": true|false,
"confidence": <0..1]}]} (one rating per candidate, in the same order as listed;
repeat each id exactly)`

func check(v any, key string, allowed []string) (string, float64, error) {
	d, ok := v.(map[string]any)
	if !ok {
		return "", 0, fmt.Errorf("bad answer: %.200v", v)
	}
	s, _ := d[key].(string)
	found := false
	for _, a := range allowed {
		if s == a {
			found = true
		}
	}
	if !found {
		return "", 0, fmt.Errorf("bad %s: %v", key, d[key])
	}
	c, ok := toFloat(d["confidence"])
	if !ok || math.IsNaN(c) || math.IsInf(c, 0) {
		return "", 0, fmt.Errorf("bad confidence: %v", d["confidence"])
	}
	return s, clamp(c), nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func clamp(c float64) float64 { return math.Max(0, math.Min(1, c)) }

// Classification is a signal kind plus confidence.
type Classification struct {
	Kind       string
	Confidence float64
}

// Classify returns the signal kind of an utterance. Invalid answers raise.
func Classify(words, m string) (Classification, error) {
	a, err := AskJSON(signalPrompt, Redact(words), m)
	if err != nil {
		return Classification{}, err
	}
	kind, conf, err := check(a, "kind", Kinds)
	if err != nil {
		return Classification{}, err
	}
	return Classification{Kind: kind, Confidence: conf}, nil
}

// Relation is how an utterance bears on one Compass line.
type Relation struct {
	Relation   string
	Confidence float64
}

// Relate judges words against one line.
func Relate(words, line, m string) (Relation, error) {
	a, err := AskJSON(relationPrompt, "Line: "+Redact(line)+"\nUtterance: "+Redact(words), m)
	if err != nil {
		return Relation{}, err
	}
	rel, conf, err := check(a, "relation", []string{"supports", "contradicts", "unrelated"})
	if err != nil {
		return Relation{}, err
	}
	return Relation{Relation: rel, Confidence: conf}, nil
}

// Candidate is one earlier lesson offered for a task.
type Candidate struct {
	ID     string
	Title  string
	Lesson string
}

// Rating pairs a candidate with the model's relevance verdict.
type Rating struct {
	ID         string
	Relevant   bool
	Confidence float64
}

// RateRelevance rates candidates in one batched call, in candidate order.
// Pairing is by echoed id when the model repeats the full set; mangled ids
// fall back to position.
func RateRelevance(task string, candidates []Candidate, m string) ([]Rating, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	oneLine := func(s string) string {
		fields := regexp.MustCompile(`\s+`).Split(Redact(s), -1)
		joined := ""
		for i, f := range fields {
			if i > 0 {
				joined += " "
			}
			joined += f
		}
		if len(joined) > 300 {
			joined = joined[:300]
		}
		return joined
	}
	listing := ""
	for i, c := range candidates {
		if i > 0 {
			listing += "\n"
		}
		listing += c.ID + ": " + oneLine(c.Title) + " — " + oneLine(c.Lesson)
	}
	a, err := AskJSON(relevancePrompt, "Task: "+Redact(task)+"\nCandidates:\n"+listing, m)
	if err != nil {
		return nil, err
	}
	d, ok := a.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("bad ratings: %.200v", a)
	}
	raw, ok := d["ratings"].([]any)
	if !ok || len(raw) != len(candidates) {
		return nil, fmt.Errorf("bad ratings: %.200v", a)
	}
	ratings := make([]Rating, len(raw))
	for i, r := range raw {
		rd, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("bad rating: %.200v", r)
		}
		rel, ok := rd["relevant"].(bool)
		if !ok {
			return nil, fmt.Errorf("bad rating: %.200v", r)
		}
		conf, ok := toFloat(rd["confidence"])
		if !ok || math.IsNaN(conf) || math.IsInf(conf, 0) {
			return nil, fmt.Errorf("bad confidence: %.200v", r)
		}
		id, _ := rd["id"].(string)
		ratings[i] = Rating{ID: id, Relevant: rel, Confidence: clamp(conf)}
	}
	want := make([]string, len(candidates))
	for i, c := range candidates {
		want[i] = c.ID
	}
	got := make([]string, len(ratings))
	for i, r := range ratings {
		got[i] = r.ID
	}
	sortedGot := append([]string{}, got...)
	sort.Strings(sortedGot)
	out := make([]Rating, len(candidates))
	if equalStrings(wantSorted(want), sortedGot) {
		byID := map[string]Rating{}
		for _, r := range ratings {
			byID[r.ID] = r
		}
		for i, c := range candidates {
			r := byID[c.ID]
			out[i] = Rating{ID: c.ID, Relevant: r.Relevant, Confidence: r.Confidence}
		}
		return out, nil
	}
	for i, c := range candidates {
		out[i] = Rating{ID: c.ID, Relevant: ratings[i].Relevant, Confidence: ratings[i].Confidence}
	}
	return out, nil
}

func wantSorted(w []string) []string {
	s := append([]string{}, w...)
	sort.Strings(s)
	return s
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Route is where an owner-bound message goes, decided in code, not by model.
type Route struct {
	Route      string
	Kind       string
	Confidence float64
}

// TriageRoute routes an owner-bound message: choices become questions,
// standing lines become proposals.
func TriageRoute(words, m string) (Route, error) {
	c, err := Classify(words, m)
	if err != nil {
		return Route{}, err
	}
	route := map[string]string{
		"choice": "question", "preference": "proposal", "restriction": "proposal",
		"correction": "proposal", "none": "fyi",
	}[c.Kind]
	return Route{Route: route, Kind: c.Kind, Confidence: c.Confidence}, nil
}
