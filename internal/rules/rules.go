// Package rules answers the two standing-rule questions: is this a rule,
// and does a Compass line allow this tool call. Both go to the model;
// an id the model invents allows nothing.
package rules

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/model"
)

const rulePrompt = `You read what a developer said to their coding agent. Decide whether it sets a
standing rule: something the agent should do every time a kind of situation comes up ("from now on",
"always", "never", "whenever X, do Y", "you can X without asking"). A task about this one piece of
work is not a rule, even with a condition in it.
Answer with JSON only: {"is_rule": true|false, "rule": "<the rule as one short imperative line, or empty>"}`

const allowPrompt = `A coding agent is about to make a tool call that would normally need the owner's
approval. Below are the owner's standing rules. Decide whether one rule clearly allows this exact
call. If none clearly does, answer null; never stretch a rule.
Answer with JSON only: {"allowed_by": "<rule id>" | null, "reason": "<one sentence>"}`

// AskJSON calls the model. Tests replace it with a stub.
var AskJSON = func(system, user, m string) (any, error) {
	return model.AskJSON(system, user, m)
}

// Extract returns the standing rule in what was said, or "".
func Extract(said string) (string, error) {
	a, err := AskJSON(rulePrompt, said, "")
	if err != nil {
		return "", err
	}
	d, ok := a.(map[string]any)
	if !ok {
		return "", &model.Error{Msg: fmt.Sprintf("bad answer: %.200v", a)}
	}
	isRule, _ := d["is_rule"].(bool)
	rule, _ := d["rule"].(string)
	rule = strings.TrimSpace(rule)
	if !isRule || rule == "" {
		return "", nil
	}
	return rule, nil
}

// AllowedBy returns the id of the Compass line that allows this tool call,
// or "". No lines means no model call at all.
func AllowedBy(call any, lines []*compass.Line) (string, error) {
	if len(lines) == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("Rules:\n")
	for _, l := range lines {
		sb.WriteString(l.ID + ": " + l.Rule + "\n")
	}
	raw, err := json.Marshal(call)
	if err != nil {
		return "", err
	}
	sb.WriteString("\nTool call:\n" + string(raw))
	a, err := AskJSON(allowPrompt, sb.String(), "")
	if err != nil {
		return "", err
	}
	d, ok := a.(map[string]any)
	if !ok {
		return "", &model.Error{Msg: fmt.Sprintf("bad answer: %.200v", a)}
	}
	lid, _ := d["allowed_by"].(string)
	for _, l := range lines {
		if l.ID == lid {
			return lid, nil
		}
	}
	return "", nil
}
