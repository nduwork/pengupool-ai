package pool

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Claude Code on Ollama is a built-in entry: a full Claude session (hooks, Compass, teams, resume)
// that runs through `ollama launch claude`. PenguPool names the model itself, so the launcher's model
// picker never shows: the model Ollama would preselect, read from Ollama's own record. A model that
// is not there is refused with the pull to run, never pulled behind the owner's back. Ollama's
// address is Ollama's own (OLLAMA_HOST, else the default); PenguPool keeps no model or address.

// ViaOllama marks a Claude session that runs through `ollama launch claude`.
const ViaOllama = "ollama"

const defaultOllamaURL = "http://127.0.0.1:11434"

// ollamaPrefix marked a session's model as an Ollama model ("ollama/qwen3-coder") before the entry.
const ollamaPrefix = "ollama/"

// ollamaHome is where Ollama keeps its record (~/.ollama), for this host's environment.
func (p *Pool) ollamaHome() string {
	home := harness.EnvValue(p.env, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".ollama")
}

func (p *Pool) ollamaURL() string {
	h := strings.TrimRight(harness.EnvValue(p.env, "OLLAMA_HOST"), "/")
	switch {
	case h == "":
		return defaultOllamaURL
	case !strings.Contains(h, "://"):
		return "http://" + h
	}
	return h
}

// ollamaModel is the model `ollama launch claude` would preselect: the last one used with Claude,
// else Ollama's last model, else the first it lists. It must be pulled, or a cloud model.
func (p *Pool) ollamaModel(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ollamaURL()+"/api/tags", nil)
	if err != nil {
		return "", invalid("%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", invalid("Ollama is not answering at %s: open the Ollama app, or run ollama serve", p.ollamaURL())
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tags)
	var have []string
	for _, m := range tags.Models {
		have = append(have, m.Name)
	}
	var rec struct {
		Integrations map[string]struct {
			Models []string `json:"models"`
		} `json:"integrations"`
		LastModel string `json:"last_model"`
	}
	if b, err := os.ReadFile(filepath.Join(p.ollamaHome(), "config.json")); err == nil {
		_ = json.Unmarshal(b, &rec)
	}
	want := rec.LastModel
	if ms := rec.Integrations["claude"].Models; len(ms) > 0 && ms[0] != "" {
		want = ms[0]
	}
	if want == "" {
		if len(have) == 0 {
			return "", invalid("Ollama has no model: pull one (ollama pull qwen3-coder), or sign in for cloud models (ollama signin)")
		}
		return have[0], nil
	}
	if !strings.HasSuffix(want, ":cloud") && !slices.Contains(have, want) && !slices.Contains(have, want+":latest") {
		return "", invalid("Ollama does not have %s: run ollama pull %s", want, want)
	}
	return want, nil
}

// ollamaWrap is the launcher Claude runs behind: Claude's own arguments follow the "--".
func (p *Pool) ollamaWrap(ctx context.Context) (string, error) {
	bin, err := harness.LookupOllama(p.env)
	if err != nil {
		return "", err
	}
	m, err := p.ollamaModel(ctx)
	if err != nil {
		return "", err
	}
	return shellWord(bin) + " launch claude --model " + shellWord(m) + " --yes --", nil
}

// MigrateAgentModels moves the old Claude-on-Ollama setting to the built-in entry, once: sessions
// started on an Ollama model (and, with "every Claude Code session on Ollama", every Claude session)
// resume through Ollama, and that setting makes the entry New session's default. Safe to redo: the
// setting is dropped last. The model and address are not kept: Ollama's own are used.
func (p *Pool) MigrateAgentModels() {
	am := p.st.Config().AgentModels
	always := am != nil && slices.Contains(am.Ollama, harness.Claude)
	if err := p.update(func(st *store.State) error {
		for i := range st.Sessions {
			s := &st.Sessions[i]
			if s.Harness != harness.Claude || s.Via != "" {
				continue
			}
			if strings.HasPrefix(s.Model, ollamaPrefix) {
				s.Via, s.Model = ViaOllama, ""
			} else if always {
				s.Via = ViaOllama
			}
		}
		return nil
	}); err != nil {
		p.logf("pool: move Ollama sessions to Claude Code (Ollama): %v", err)
		return
	}
	if am == nil {
		return
	}
	if err := p.st.UpdateConfig(func(cfg *store.Config) error {
		cfg.OllamaDefault = cfg.OllamaDefault || always
		cfg.AgentModels = nil
		return nil
	}); err != nil {
		p.logf("pool: drop the old Ollama setting: %v", err)
		return
	}
	p.logf("pool: the Ollama setting moved to Claude Code (Ollama)")
}

// shellWord quotes s for sh when it holds anything but plain characters.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:/@+=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
