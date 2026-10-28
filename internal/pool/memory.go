package pool

import (
	"path/filepath"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// Helpers of the record in the host's agents.db (see internal/agentdb), which lives in no
// repository; the Compass itself is compass.go.

func (p *Pool) agentDB() (*agentdb.DB, error) {
	if p.db == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "the agent database is not open")
	}
	return p.db, nil
}

// helperOf is the helper a session works as: its skill, or the registered helper whose repository
// it was opened in; nil for any other session.
func (p *Pool) helperOf(id string) *protocol.Skill {
	s, ok := p.session(id)
	if !ok {
		return nil
	}
	d := p.st.State().Domain
	if sk := findSkill(&d, s.SkillID); sk != nil {
		return sk
	}
	// A session started by hand in a helper's folder (a skill may live in a subfolder of its repo) is
	// that helper; the deepest skill folder holding the cwd wins.
	var best *protocol.Skill
	for i := range d.Skills {
		sp := filepath.Clean(d.Skills[i].Path)
		if s.Cwd != "" && (s.Cwd == sp || strings.HasPrefix(s.Cwd, sp+string(filepath.Separator))) &&
			(best == nil || len(sp) > len(filepath.Clean(best.Path))) {
			best = &d.Skills[i]
		}
	}
	return best
}

// agentOf is a helper's identity in the record: its repository's key, the same on every machine
// (local:<id> for a helper without a remote).
func agentOf(sk protocol.Skill) string { return firstNonEmpty(sk.Key, "local:"+sk.ID) }

// rekey moves a helper's journal when its key changed.
func (p *Pool) rekey(from, to string) error {
	if p.db == nil || from == "" {
		return nil
	}
	return p.db.RekeyAgent(from, to)
}

// detectSecret reports a likely credential; the Compass, logs and provider calls must never carry one.
func detectSecret(s string) bool { return decision.SecretRe.MatchString(s) }
