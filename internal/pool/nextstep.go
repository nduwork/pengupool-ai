package pool

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// nextStepTimeout bounds a question asked inside the prompt hook (the subagent plan), which the CLI
// waits 11 s for (cmd/pengupool/pool.go). A local model can take several seconds to answer.
const nextStepTimeout = 8 * time.Second

// routingDesc is what a member is for, not what it was assigned: a helper's skill description, a
// repository agent's name and the first paragraph of its README; the assignment only when neither exists.
func (p *Pool) routingDesc(d *protocol.Domain, m protocol.Member) string {
	if sk := findSkill(d, m.SkillID); sk != nil && sk.Description != "" {
		return sk.Description
	}
	if r := findRepo(d, m.RepoID); r != nil && m.SkillID == "" {
		// ponytail: read at question time, no cache; cache at ingest if repos get large.
		if b, err := os.ReadFile(filepath.Join(r.Path, "README.md")); err == nil {
			var para []string
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "#") {
					if len(para) > 0 {
						break
					}
					continue
				}
				para = append(para, l)
			}
			if len(para) > 0 {
				return clipRunes("repository "+r.Name+": "+strings.Join(para, " "), 300)
			}
		}
		return "repository " + r.Name
	}
	return m.Assignment
}

func redactSecrets(s string) string { return decision.SecretRe.ReplaceAllString(s, "[left out]") }
