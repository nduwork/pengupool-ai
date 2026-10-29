package pool

import (
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Custom agents: a name and the command that starts an agent, for anything PenguPool does not
// launch itself (`ollama launch claude …`, say). An entry that starts a known agent is that agent in
// full: PenguPool appends the agent's own arguments (hooks, settings, resume) after the command,
// which ends with "--". An entry that starts nothing runs its command as written: a terminal with
// a name, with no hooks, no teams, no Compass and no messages. Local only.

// offered reports whether New session and the install check list this agent.
func offered(id string) bool {
	a, ok := harness.Find(id)
	return ok && !a.Hidden
}

// checkCustomAgents validates the owner's list and gives new entries an id.
func checkCustomAgents(list []store.CustomAgent) ([]store.CustomAgent, error) {
	out := make([]store.CustomAgent, 0, len(list))
	names, ids := map[string]bool{}, map[string]bool{}
	hasDefault := false
	for _, c := range list {
		c.Name, c.Command, c.Starts = strings.TrimSpace(c.Name), strings.TrimSpace(c.Command), strings.TrimSpace(c.Starts)
		switch {
		case c.Name == "":
			return nil, invalid("a Custom agent needs a name")
		case names[strings.ToLower(c.Name)]:
			return nil, invalid("two Custom agents are named %s", c.Name)
		case c.Command == "":
			return nil, invalid("%s needs the command that starts it", c.Name)
		case c.Starts != "" && !offered(c.Starts):
			return nil, invalid("%s starts %q, which is not an agent PenguPool runs", c.Name, c.Starts)
		case c.Starts != "" && c.Command != "--" && !strings.HasSuffix(c.Command, " --"):
			return nil, invalid("%s starts an agent, so its command must end with -- (the agent's own arguments follow it)", c.Name)
		case c.Starts != "" && strings.ContainsAny(c.Command, "#\n"):
			return nil, invalid("%s: a comment or a second line would cut the agent's own arguments off the command", c.Name)
		}
		names[strings.ToLower(c.Name)] = true
		if c.ID == "" || ids[c.ID] {
			c.ID = protocol.NewID()
		}
		ids[c.ID] = true
		c.Default = c.Default && !hasDefault // one entry at most
		hasDefault = hasDefault || c.Default
		out = append(out, c)
	}
	return out, nil
}

func (p *Pool) customAgent(id string) (store.CustomAgent, bool) {
	i := slices.IndexFunc(p.st.Config().CustomAgents, func(c store.CustomAgent) bool { return c.ID == id })
	if i < 0 {
		return store.CustomAgent{}, false
	}
	return p.st.Config().CustomAgents[i], true
}

// customCommand is how a Custom agent that starts nothing runs: its command, as written, in sh
// (through env, so a leading VAR=value works).
func customCommand(c store.CustomAgent) []string {
	return []string{"/bin/sh", "-c", "exec env " + c.Command}
}

const defaultOllamaURL = "http://127.0.0.1:11434"

// MigrateAgentModels turns the old Claude-on-Ollama setting into a Custom agent, once: the setting
// is dropped afterwards. A session that ran on it resumes through the new entry: those the owner
// started on an Ollama model, and with "every Claude Code session on Ollama" every Claude session.
// The per-agent Ollama switches of the other agents have no entry: those agents start on their own
// account again.
func (p *Pool) MigrateAgentModels() {
	am := p.st.Config().AgentModels
	var models []string
	if am != nil && am.OllamaModel != "" {
		models = append(models, am.OllamaModel)
	}
	for _, s := range p.st.State().Sessions {
		if m, ok := strings.CutPrefix(s.Model, ollamaPrefix); ok && s.Harness == harness.Claude && m != "" && !slices.Contains(models, m) {
			models = append(models, m)
		}
	}
	if am == nil && len(models) == 0 {
		return
	}
	always := am != nil && slices.Contains(am.Ollama, harness.Claude)
	url := defaultOllamaURL
	if am != nil && am.OllamaURL != "" {
		url = strings.TrimRight(am.OllamaURL, "/")
	}
	// Three steps, each safe to repeat: the entries (an entry with the same command is reused), the
	// sessions, then the old setting dropped. A start that stops between them redoes the rest.
	byModel := map[string]string{}
	err := p.st.UpdateConfig(func(cfg *store.Config) error {
		for i, m := range models {
			cmd := ollamaCommand(url, m)
			if j := slices.IndexFunc(cfg.CustomAgents, func(c store.CustomAgent) bool { return c.Command == cmd && c.Starts == harness.Claude }); j >= 0 {
				byModel[m] = cfg.CustomAgents[j].ID
				continue
			}
			c := store.CustomAgent{ID: protocol.NewID(), Name: "Claude Code (Ollama)", Command: cmd, Starts: harness.Claude}
			if len(models) > 1 {
				c.Name = "Claude Code (Ollama: " + m + ")"
			}
			c.Default = always && i == 0 && !slices.ContainsFunc(cfg.CustomAgents, func(c store.CustomAgent) bool { return c.Default })
			cfg.CustomAgents = append(cfg.CustomAgents, c)
			byModel[m] = c.ID
		}
		return nil
	})
	if err != nil {
		p.logf("pool: move the Ollama setting to a Custom agent: %v", err)
		return
	}
	if len(models) > 0 {
		if err := p.update(func(st *store.State) error {
			for i := range st.Sessions {
				s := &st.Sessions[i]
				if s.Harness != harness.Claude || s.Custom != "" {
					continue
				}
				if m, ok := strings.CutPrefix(s.Model, ollamaPrefix); ok {
					s.Custom, s.Model = byModel[firstNonEmpty(m, models[0])], ""
				} else if always {
					s.Custom = byModel[models[0]]
				}
			}
			return nil
		}); err != nil {
			p.logf("pool: move Ollama sessions to their Custom agent: %v", err)
			return
		}
	}
	if err := p.st.UpdateConfig(func(cfg *store.Config) error { cfg.AgentModels = nil; return nil }); err != nil {
		p.logf("pool: drop the old Ollama setting: %v", err)
		return
	}
	p.logf("pool: Claude Code on Ollama is now a Custom agent (%d)", len(models))
}

// ollamaPrefix marked a session's model as an Ollama model ("ollama/qwen3-coder") before Custom agents.
const ollamaPrefix = "ollama/"

func ollamaCommand(url, model string) string {
	cmd := "ollama launch claude --model " + shellWord(model) + " --yes --"
	if url != defaultOllamaURL {
		cmd = "OLLAMA_HOST=" + shellWord(url) + " " + cmd
	}
	return cmd
}

// shellWord quotes s for sh when it holds anything but plain characters.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:/@+=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
