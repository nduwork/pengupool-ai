package pool

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/decision"
)

// IsV1Command: a command of the PenguPool v1 python package (python -m pengupool.context, .routing,
// .statusline, or its oldest pengupool/session_start.sh), which v2 does not use: it passes its hooks
// and status line to Claude itself.
func IsV1Command(cmd string) bool {
	return strings.Contains(cmd, "pengupool/session_start.sh") || strings.Contains(cmd, "-m pengupool.") ||
		strings.Contains(cmd, "pengupool.context") || strings.Contains(cmd, "pengupool.routing") || strings.Contains(cmd, "pengupool.statusline")
}

type doctorRecordIn struct {
	File    string          `json:"file"`
	Entries json.RawMessage `json:"entries"`
}

// v1Only checks the removed entries are what doctor may hand over: hooks (by event, groups whose
// hooks are all v1 commands) and a status line whose command is v1's. Nothing else of a settings
// file is kept: it can hold tokens.
func v1Only(e map[string]json.RawMessage) bool {
	for k, raw := range e {
		switch k {
		case "hooks":
			var hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			}
			if json.Unmarshal(raw, &hooks) != nil {
				return false
			}
			for _, groups := range hooks {
				for _, g := range groups {
					if len(g.Hooks) == 0 {
						return false
					}
					for _, h := range g.Hooks {
						if !IsV1Command(h.Command) {
							return false
						}
					}
				}
			}
		case "statusLine":
			var sl struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(raw, &sl) != nil || !IsV1Command(sl.Command) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// doctorRecord is the owner's terminal handing over what `pengupool doctor --fix` is about to take
// out of Claude's settings, to be kept in the shared record, which is pushed to GitHub. Only the v1
// hooks and status line may come, none that looks like a secret.
func (p *Pool) doctorRecord(_ context.Context, in doctorRecordIn) (any, error) {
	db, err := p.agentDB()
	if err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"settings.json", "settings.local.json"}, in.File) {
		return nil, invalid("file must be settings.json or settings.local.json")
	}
	var e map[string]json.RawMessage
	if err := json.Unmarshal(in.Entries, &e); err != nil || len(e) == 0 || len(in.Entries) > 64<<10 || !v1Only(e) {
		return nil, invalid("entries must be a JSON object of at most 64 KiB holding only PenguPool v1 hooks and status line")
	}
	canon, _ := json.Marshal(e) // keys sorted, so the same entries hash the same
	if decision.SecretRe.Match(canon) {
		return nil, invalid("the entries contain something that looks like a secret, so they are not synced; remove them from the settings file by hand")
	}
	hash, added, err := db.AddRemoved(in.File, string(canon))
	if err != nil {
		return nil, err
	}
	return map[string]any{"hash": hash, "added": added}, nil
}

// doctorRemoved lists what doctor removed, for `doctor --restore`.
func (p *Pool) doctorRemoved(_ context.Context, _ struct{}) (any, error) {
	db, err := p.agentDB()
	if err != nil {
		return nil, err
	}
	rs, err := db.ListRemoved()
	return map[string]any{"removed": rs}, err
}
