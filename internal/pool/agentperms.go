package pool

import (
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// setAgentPermissions records the owner's choice and writes or removes the rules for every agent
// PenguPool writes them for; each agent's result stands alone (one failing leaves the others).
func (p *Pool) setAgentPermissions(choice string) map[string]any {
	_ = p.st.UpdateConfig(func(cfg *store.Config) error {
		cfg.AgentPermissionsChoice = choice
		return nil
	})
	out := map[string]any{}
	for _, a := range harness.PermissionAgents {
		out[a] = p.setAgentPermission(a, choice == "allowed")
	}
	return out
}

// setAgentPermission writes or removes PenguPool's team-command rules in one agent's own global
// config. Only the owner's own app call reaches it (settings.save is never an agent's request). A
// failure is kept for Settings, with the host's reason.
func (p *Pool) setAgentPermission(agent string, on bool) map[string]any {
	var why string
	_ = p.st.UpdateConfig(func(cfg *store.Config) error {
		old, had := cfg.AgentPermissions[agent]
		if had && on && !p.permissionsMoved(old) {
			return nil
		}
		if had {
			if err := harness.RemovePermissions(agent, old); err != nil {
				why = err.Error()
				return nil
			}
			delete(cfg.AgentPermissions, agent)
		}
		if !on {
			return nil
		}
		w, err := harness.WritePermissions(agent, harness.PermissionFile(agent, p.env), p.exe)
		if err != nil {
			why = err.Error()
			return nil
		}
		if cfg.AgentPermissions == nil {
			cfg.AgentPermissions = map[string]store.AgentPermission{}
		}
		cfg.AgentPermissions[agent] = w
		return nil
	})
	p.mu.Lock()
	if p.permErr == nil {
		p.permErr = map[string]string{}
	}
	if why == "" {
		delete(p.permErr, agent)
	} else {
		p.permErr[agent] = why
	}
	p.mu.Unlock()
	if why != "" {
		return map[string]any{"ok": false, "error": why}
	}
	return map[string]any{"ok": true}
}

// permissionsMoved: a recorded line is not a rule for this executable (the app moved or updated).
func (p *Pool) permissionsMoved(w store.AgentPermission) bool {
	want := append(harness.ClaudeRules(p.exe), harness.CodexRules(p.exe)...)
	return slices.ContainsFunc(w.Lines, func(l string) bool { return !slices.Contains(want, l) })
}

// permissionsAdded are the agent's rules this version allows that the owner's recorded yes does not
// cover (the list grew since). They are shown, never written on their own: a wider grant needs the
// owner to turn the switch off and on.
func (p *Pool) permissionsAdded(agent string, w store.AgentPermission) []string {
	want := harness.ClaudeRules(p.exe)
	if agent == harness.Codex {
		want = harness.CodexRules(p.exe)
	}
	var out []string
	for _, l := range want {
		if !slices.Contains(w.Lines, l) {
			out = append(out, l)
		}
	}
	return out
}

// refreshAgentPermissions runs at start with the owner's consent already given: it rewrites
// PenguPool's own lines when the app has moved, so no stale rule for an old path stays behind, and
// tries again an agent whose rules could not be written.
func (p *Pool) refreshAgentPermissions() {
	cfg := p.st.Config()
	if cfg.AgentPermissionsChoice != "allowed" {
		return
	}
	for _, agent := range harness.PermissionAgents {
		if w, ok := cfg.AgentPermissions[agent]; ok && !p.permissionsMoved(w) {
			continue
		}
		if r := p.setAgentPermission(agent, true); r["ok"] != true {
			p.logf("pool: agent permissions for %s: not written: %v", agent, r["error"])
		} else {
			p.logf("pool: agent permissions for %s: written for the app's current path", agent)
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
			if added := p.permissionsAdded(a, w); len(added) > 0 {
				v["added"] = added // commands PenguPool allows since the owner said yes: never written without a new yes
			}
		}
		p.mu.Lock()
		if why := p.permErr[a]; why != "" {
			v["error"] = why
		}
		p.mu.Unlock()
		agents[a] = v
	}
	cmds := make([]string, len(harness.CLIAllowed))
	for i, c := range harness.CLIAllowed {
		cmds[i] = "pengupool " + strings.Join(c, " ")
	}
	return map[string]any{"choice": cfg.AgentPermissionsChoice, "agents": agents, "not_written": harness.PermissionNotWritten, "commands": cmds, "cli": p.cli()}
}
