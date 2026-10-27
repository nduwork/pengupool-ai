package pool

import (
	"context"
	"regexp"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

func overlap(a, b string) float64 {
	wa, wb := map[string]bool{}, map[string]bool{}
	for _, w := range words(a) {
		wa[w] = true
	}
	for _, w := range words(b) {
		wb[w] = true
	}
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(wa)+len(wb)-inter)
}

func request(prompt string) string {
	if from, text, ok := outsideMessage(prompt); ok {
		return oneLine("From " + from + ": " + text)
	}
	if strings.HasPrefix(prompt, "[PenguPool") {
		if i := strings.Index(prompt, "] "); i > 0 {
			prompt = prompt[i+2:]
		}
		prompt, _, _ = strings.Cut(prompt, "\n\nTo reply, run")
	}
	return oneLine(prompt)
}

var outsideRe = regexp.MustCompile(`(?s)^\s*<cross-session-message\b([^>]*)>(.*?)(?:</cross-session-message>\s*)?$`)
var fromNameRe = regexp.MustCompile(`from-name="([^"]*)"`)

func outsideMessage(prompt string) (from, text string, ok bool) {
	m := outsideRe.FindStringSubmatch(prompt)
	if m == nil {
		return "", "", false
	}
	from = "another session"
	if n := fromNameRe.FindStringSubmatch(m[1]); n != nil && strings.TrimSpace(n[1]) != "" {
		from = strings.TrimSpace(n[1])
	}
	return from, strings.TrimSpace(m[2]), true
}

func firstWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) <= n {
		return strings.TrimRight(strings.Join(w, " "), ",;:")
	}
	return strings.TrimRight(strings.Join(w[:n], " "), ",;:.") + "…"
}

type teamIn struct {
	TeamID string `json:"team_id"`
}

// teamMessages is every message between the team's members PenguPool still holds, oldest first.
func (p *Pool) teamMessages(_ context.Context, in teamIn) (any, error) {
	d := p.st.State().Domain
	t := findTeam(&d, in.TeamID)
	if t == nil {
		return nil, notFound("no team %s", in.TeamID)
	}
	out := []protocol.TeamMessage{}
	for _, m := range d.Messages {
		if m.TeamID == t.ID && m.From != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
