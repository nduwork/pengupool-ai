package pool

import (
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// setAgentPermission writes or removes PenguPool's team-command rules in one agent's own global
// config. Only the owner's own app call reaches it (settings.save is never an agent's request).
func (p *Pool) setAgentPermission(agent string, on bool) map[string]any {
	if !slices.Contains(harness.PermissionAgents, agent) {
		why := harness.PermissionNotWritten[agent]
		if why == "" {
			why = "unknown agent"
		}
		return map[string]any{"ok": false, "error": why}
	}
	var res map[string]any
	_ = p.st.UpdateConfig(func(cfg *store.Config) error {
		old, had := cfg.AgentPermissions[agent]
		if had {
			if on && !p.permissionsMoved(old) {
				res = map[string]any{"ok": true}
				return nil
			}
			if err := harness.RemovePermissions(agent, old); err != nil {
				res = map[string]any{"ok": false, "error": err.Error()}
				return nil
			}
			delete(cfg.AgentPermissions, agent)
		}
		if on {
			w, err := harness.WritePermissions(agent, harness.PermissionFile(agent, p.env), p.exe)
			if err != nil {
				res = map[string]any{"ok": false, "error": err.Error()}
				return nil
			}
			if cfg.AgentPermissions == nil {
				cfg.AgentPermissions = map[string]store.AgentPermission{}
			}
			cfg.AgentPermissions[agent] = w
		}
		res = map[string]any{"ok": true}
		return nil
	})
	return res
}

// permissionsMoved: a recorded line is not a rule for this executable (the app moved or updated).
func (p *Pool) permissionsMoved(w store.AgentPermission) bool {
	want := append(harness.ClaudeRules(p.exe), harness.CodexRules(p.exe)...)
	return slices.ContainsFunc(w.Lines, func(l string) bool { return !slices.Contains(want, l) })
}

// refreshAgentPermissions rewrites PenguPool's own lines at start when the app has moved, so no stale
// rule for an old path stays behind.
func (p *Pool) refreshAgentPermissions() {
	for agent, w := range p.st.Config().AgentPermissions {
		if !p.permissionsMoved(w) {
			continue
		}
		if r := p.setAgentPermission(agent, true); r["ok"] != true {
			p.logf("pool: agent permissions for %s: the app moved, and its rules could not be rewritten: %v", agent, r["error"])
		} else {
			p.logf("pool: agent permissions for %s: rewritten for the app's new path", agent)
		}
	}
}

// agentPermissionsView is Settings' Agent permissions: per agent whether its rules are written, the
// file and exact lines, and lines changed outside PenguPool.
func (p *Pool) agentPermissionsView() map[string]any {
	cfg := p.st.Config()
	agents := map[string]any{}
	for _, a := range harness.PermissionAgents {
		v := map[string]any{"on": false, "file": harness.PermissionFile(a, p.env)}
		if w, ok := cfg.AgentPermissions[a]; ok {
			v["on"], v["file"], v["lines"] = true, w.File, w.Lines
			if miss, err := harness.MissingPermissions(a, w); err != nil {
				v["error"] = err.Error()
			} else if len(miss) > 0 {
				v["changed"] = miss // removed outside PenguPool; turning it off and on writes them again
			}
		}
		agents[a] = v
	}
	cmds := make([]string, len(harness.CLIAllowed))
	for i, c := range harness.CLIAllowed {
		cmds[i] = p.cli() + " " + strings.Join(c, " ")
	}
	return map[string]any{"agents": agents, "not_written": harness.PermissionNotWritten, "commands": cmds}
}
