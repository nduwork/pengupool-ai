// Package wording turns words into Compass lines and lessons, once each.
//
// Muse Spark, off the turn's hot path: only for a stated preference
// (ingest) or a finished task (learned). Inputs are scrubbed before the
// model sees them; callers scrub again before storing.
package wording

import (
	"strings"

	"github.com/nduwork/pengupool-ai/internal/judge"
	"github.com/nduwork/pengupool-ai/internal/model"
)

// Chat calls the model. Tests replace it with a stub.
var Chat = func(messages []model.Message, m string) (string, error) {
	return model.Chat(messages, m, 0)
}

// GeneralizeCompass returns the single standing rule behind what was said,
// or "" when there is none.
func GeneralizeCompass(words, m string) (string, error) {
	out, err := Chat([]model.Message{
		{Role: "system", Content: "Turn what was said into the single standing rule behind it: one short " +
			"imperative line a coding agent can follow every time (\"Always run the " +
			"test suite before committing\"). Generalize the case, keep the intent. " +
			"If it states no standing rule, answer exactly: none"},
		{Role: "user", Content: judge.Redact(words)},
	}, m)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(out)
	if len(line) >= 2 && line[0] == line[len(line)-1] && (line[0] == '"' || line[0] == '\'') {
		line = strings.TrimSpace(line[1 : len(line)-1])
	}
	if line == "" {
		return "", nil
	}
	switch strings.ToLower(strings.Trim(line, ". ")) {
	case "none", "no standing rule":
		return "", nil
	}
	return judge.Redact(strings.TrimSpace(strings.SplitN(line, "\n", 2)[0])), nil
}

// GeneralizeLesson distills a task summary into a general lesson (<=5 lines).
func GeneralizeLesson(summary, m string) (string, error) {
	out, err := Chat([]model.Message{
		{Role: "system", Content: "Turn this task summary into the general lesson behind it: what to do " +
			"or avoid next time a similar task comes up. At most 5 lines, concrete, " +
			"no task-specific names or secrets."},
		{Role: "user", Content: judge.Redact(summary)},
	}, m)
	if err != nil {
		return "", err
	}
	var kept []string
	for _, l := range strings.Split(out, "\n") {
		if l = judge.Redact(strings.TrimSpace(l)); l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > 5 {
		kept = kept[:5]
	}
	return strings.Join(kept, "\n"), nil
}
