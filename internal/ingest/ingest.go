// Package ingest turns owner words into Compass proposals. Evidence
// writes directly; structural changes (new lines, rewordings,
// retirements) only ever become proposals the owner confirms.
//
// Proposal bodies quote the owner's words verbatim: shown to the owner,
// never to another model. Only generalized, redacted text leaves the
// store. Mirrors pengu/ingest.py.
package ingest

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/judge"
	"github.com/nduwork/pengupool-ai/internal/wording"
)

// Seams the owner package wires once ported; tests stub them.
var (
	Classify       = judge.Classify
	Relate         = judge.Relate
	Generalize     = wording.GeneralizeCompass
	LoadLines      = compass.Load
	RecordEvidence = compass.UpdateEvidence
	Propose        = func(words, kind string, ref map[string]any) (string, error) {
		return "", errors.New("proposals unported: owner package owns them")
	}
)

var tokRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)

func overlap(a, b string) int {
	toks := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range tokRe.FindAllString(strings.ToLower(s), -1) {
			out[w] = true
		}
		return out
	}
	ta, tb := toks(a), toks(b)
	n := 0
	for w := range ta {
		if tb[w] {
			n++
		}
	}
	return n
}

// bestMatch finds the line the judge relates to words. A word-overlap
// prefilter caps the judge to 10 lines when the store is big; small
// stores go whole so paraphrases still match.
func bestMatch(items []*compass.Line, words, model, relation string) (*compass.Line, string, float64, error) {
	type cand struct {
		ov   int
		line *compass.Line
	}
	scored := make([]cand, 0, len(items))
	for _, it := range items {
		scored = append(scored, cand{overlap(words, it.Rule), it})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].ov > scored[j].ov })
	var cands []*compass.Line
	if len(scored) <= 10 {
		for _, s := range scored {
			cands = append(cands, s.line)
		}
	} else {
		for _, s := range scored[:10] {
			if s.ov > 0 {
				cands = append(cands, s.line)
			}
		}
	}
	var best *compass.Line
	var bestRel string
	bestConf := -1.0
	for _, item := range cands {
		r, err := Relate(words, item.Rule, model)
		if err != nil {
			continue
		}
		if r.Relation == "unrelated" {
			continue
		}
		if relation != "" && r.Relation != relation {
			continue
		}
		if best == nil || r.Confidence > bestConf {
			best, bestRel, bestConf = item, r.Relation, r.Confidence
		}
	}
	if best == nil {
		return nil, "", 0, nil
	}
	return best, bestRel, bestConf, nil
}

func propose(words, kind string, ref map[string]any) (string, error) {
	return Propose(words, kind, ref)
}

// afterContradict records the contradiction, then proposes retirement,
// a rewording, or a note when there is no reword text.
func afterContradict(existing *compass.Line, words, session, rewordText string) (pid, outcome, msg string, err error) {
	updated, err := RecordEvidence(existing.ID, "contradicts", words, session, 0)
	if err != nil {
		return "", "", "", err
	}
	if updated == nil {
		pid, err := propose(
			fmt.Sprintf("Line vanished mid-ingest (%s): %s", existing.ID, existing.Rule),
			"note", map[string]any{"line_id": existing.ID, "reason": words})
		return pid, "vanished", "proposed note (line vanished)", err
	}
	conf := updated.Confidence
	if conf < 0.3 {
		pid, err := propose(
			fmt.Sprintf("Retire line (%s): %s — contradicted", existing.ID, existing.Rule),
			"retire", map[string]any{"line_id": existing.ID, "reason": words})
		return pid, "retired", fmt.Sprintf("proposed retirement (confidence %.2f)", conf), err
	}
	if rewordText == "" {
		pid, err := propose(
			fmt.Sprintf("Contradiction needs owner wording (%s): %s", existing.ID, existing.Rule),
			"note", map[string]any{"line_id": existing.ID, "reason": words})
		return pid, "noted", "proposed note (owner words the rewording)", err
	}
	pid, err = propose(
		fmt.Sprintf("Reword line (%s): %s — see: %s", existing.ID, existing.Rule, words),
		"reword", map[string]any{"line_id": existing.ID, "text": rewordText, "reason": words})
	return pid, "reworded", fmt.Sprintf("proposed rewording (confidence %.2f)", conf), err
}

// Ingest runs the wording routine; returns (proposal id or "", description).
// Empty id means no proposal: evidence recorded or nothing to save.
func Ingest(words, session, model string) (string, string, error) {
	if strings.TrimSpace(words) == "" {
		return "", "", errors.New("empty words")
	}
	c, err := Classify(words, model)
	if err != nil {
		return "", "", err
	}
	switch c.Kind {
	case "preference", "restriction":
		lines, err := LoadLines()
		if err != nil {
			return "", "", err
		}
		existing, rel, _, err := bestMatch(lines, words, model, "")
		if err != nil {
			return "", "", err
		}
		if existing == nil {
			line, err := Generalize(words, model)
			if err != nil {
				return "", "", err
			}
			if line = judge.Redact(line); line == "" {
				return "", "no standing rule in that", nil
			}
			pid, err := propose("New Compass line: "+line, "new-line", map[string]any{"text": line})
			return pid, "proposed new line: " + line, err
		}
		if rel == "supports" {
			updated, err := RecordEvidence(existing.ID, "supports", words, session, 0)
			if err != nil {
				return "", "", err
			}
			if updated == nil {
				pid, err := propose(fmt.Sprintf("Line vanished mid-ingest (%s)", existing.ID),
					"note", map[string]any{"line_id": existing.ID, "reason": words})
				return pid, "proposed note (line vanished)", err
			}
			return "", "supports line " + existing.ID + ": evidence recorded", nil
		}
		line, err := Generalize(words, model)
		if err != nil {
			return "", "", err
		}
		pid, _, msg, err := afterContradict(existing, words, session, judge.Redact(line))
		return pid, msg, err
	case "correction":
		lines, err := LoadLines()
		if err != nil {
			return "", "", err
		}
		existing, _, _, err := bestMatch(lines, words, model, "contradicts")
		if err != nil {
			return "", "", err
		}
		if existing == nil {
			pid, err := propose("Correction with no matching line: "+words, "note", nil)
			return pid, "proposed note (no matching line)", err
		}
		text, err := Generalize(words, model)
		if err != nil {
			return "", "", err
		}
		if text = judge.Redact(text); text == existing.Rule {
			text = ""
		}
		pid, outcome, msg, err := afterContradict(existing, words, session, text)
		if err != nil {
			return "", "", err
		}
		if outcome == "reworded" {
			return pid, "proposed rewording after correction", nil
		}
		return pid, msg, nil
	default:
		pid, err := propose("Decision for the owner: "+words, "note", nil)
		return pid, "proposed note (choice/fyi: owner decides)", err
	}
}
