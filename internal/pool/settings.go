package pool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Decision model settings: two kinds of decision model, each saved separately; saving one does not
// activate it. Online is Jev (System One); local is a model Ollama serves on this machine. Keys live
// in the macOS Keychain or behind an env: reference; config.json keeps only the reference.

var providerTypes = map[string]bool{"ollama": true, "jev": true}

// dropRetiredProviders removes saved decision models of a kind or API format PenguPool no longer
// speaks (online chat services, the formats an earlier version had), so triage never fails on them
// call after call; the active one goes to "no model" and the log says so.
func (p *Pool) dropRetiredProviders() {
	cfg := p.st.Config()
	var gone []string
	for k, pc := range cfg.Decision.Providers {
		if !providerTypes[pc.Type] || decision.Validate(pc) != nil {
			gone = append(gone, k)
			delete(cfg.Decision.Providers, k)
		}
	}
	if len(gone) == 0 {
		return
	}
	if _, ok := cfg.Decision.Providers[cfg.Decision.Active]; !ok {
		cfg.Decision.Active = ""
	}
	if err := p.st.SaveConfig(cfg); err != nil {
		p.logf("pool: decision models no longer supported (%s): %v", strings.Join(gone, ", "), err)
		return
	}
	p.logf("pool: removed decision models no longer supported: %s; set one in Settings → Models & agents", strings.Join(gone, ", "))
}

func (p *Pool) providers(_ context.Context, _ struct{}) (any, error) {
	c := p.st.Config().Decision
	defaults := map[string]store.ProviderConfig{
		"ollama": {Type: "ollama", Format: "ollama-systemone", Address: "http://127.0.0.1:11434", Model: "tev1:0.8b"},
		"jev":    {Type: "jev", Format: "systemone", Address: "https://api.typesafe.ai/v1/systemone", Model: "jev-latest"},
	}
	return map[string]any{
		"active": c.Active, "providers": c.Providers, "rules_fallback": c.RulesFallback,
		"formats": decision.Formats, "thresholds": decision.Thresholds,
		"defaults": defaults,
	}, nil
}

type providerSaveIn struct {
	Provider store.ProviderConfig `json:"provider"`
	Key      string               `json:"key,omitempty"` // new key; stored in the vault, never in config
}

func (p *Pool) providerSave(ctx context.Context, in providerSaveIn) (any, error) {
	pc := in.Provider
	if !providerTypes[pc.Type] {
		return nil, invalid("unknown provider type %q", pc.Type)
	}
	if in.Key != "" {
		account := "provider-" + pc.Type
		if err := decision.StoreKey(account, in.Key); err != nil {
			return nil, protocol.Errorf(protocol.CodeUnavailable, "%v", err)
		}
		pc.KeyRef = "keychain:" + account
	}
	if err := decision.Validate(pc); err != nil {
		return nil, invalid("%v", err)
	}
	cfg := p.st.Config()
	if cfg.Decision.Providers == nil {
		cfg.Decision.Providers = map[string]store.ProviderConfig{}
	}
	cfg.Decision.Providers[pc.Type] = pc
	return pc, p.st.SaveConfig(cfg)
}

type providerTypeIn struct {
	Type          string `json:"type"`
	RulesFallback *bool  `json:"rules_fallback,omitempty"`
}

// providerCheck is the explicit host-side connection check; a saved form is not a working
// connection until this passes.
func (p *Pool) providerCheck(ctx context.Context, in providerTypeIn) (any, error) {
	pc, ok := p.st.Config().Decision.Providers[in.Type]
	if !ok {
		return nil, notFound("save the %s provider first", in.Type)
	}
	res, items, err := decision.Check(ctx, pc)
	if err != nil {
		hint := ""
		if errors.Is(err, decision.ErrRefused) {
			hint = "The model answered, but not in the decision format: pick a model built for decisions, or the chat format for a general model."
		} else if pc.Type == "ollama" {
			hint = "Is Ollama running (`ollama serve`) and is the model pulled (`ollama pull " + pc.Model + "`)? System One needs Ollama 0.35 or later. PenguPool never installs Ollama or pulls weights."
		}
		return map[string]any{"ok": false, "error": err.Error(), "hint": hint}, nil
	}
	pass := !slices.ContainsFunc(items, func(c decision.CheckItem) bool { return !c.OK })
	out := map[string]any{"ok": pass, "model": res.Model, "latency_ms": res.Latency.Milliseconds(), "calibrated": res.Calibrated, "checks": items}
	if !pass {
		out["error"] = "The model answered in the decision format but got plain questions wrong; it is not fit to decide."
	}
	return out, nil
}

func (p *Pool) providerActivate(_ context.Context, in providerTypeIn) (any, error) {
	cfg := p.st.Config()
	if in.Type != "" {
		if _, ok := cfg.Decision.Providers[in.Type]; !ok {
			return nil, notFound("save the %s provider first", in.Type)
		}
	}
	cfg.Decision.Active = in.Type
	if in.RulesFallback != nil {
		cfg.Decision.RulesFallback = *in.RulesFallback
	}
	return nil, p.st.SaveConfig(cfg)
}

// Restarter re-executes the daemon so a new listener takes effect; workers survive it.
var Restarter func()

// HTTPAddr reports the gateway's bound address.
var HTTPAddr func() string

func (p *Pool) settingsGet(_ context.Context, _ struct{}) (any, error) {
	cfg := p.st.Config()
	out := map[string]any{"network": cfg.Network, "store": p.st.Dir(), "executable": p.exe, "agent_models": cfg.AgentModels, "agents": harness.Agents, "version": p.version}
	if HTTPAddr != nil {
		out["bound"] = HTTPAddr()
	}
	out["lan_ips"] = lanIPs()
	if ts, err := tailscaleName(); err == nil {
		out["tailscale_name"] = ts
	} else {
		out["tailscale_error"] = err.Error()
	}
	out["github"] = p.githubStatus()
	out["backup"] = p.backupView()
	return out, nil
}

type settingsIn struct {
	LAN         *bool              `json:"lan,omitempty"`
	LANAddr     *string            `json:"lan_addr,omitempty"`
	Tailscale   *string            `json:"tailscale,omitempty"`
	EnableServe bool               `json:"enable_serve,omitempty"`
	Restart     bool               `json:"restart,omitempty"`
	AgentModels *store.AgentModels `json:"agent_models,omitempty"`
}

func (p *Pool) settingsSave(ctx context.Context, in settingsIn) (any, error) {
	cfg := p.st.Config()
	if in.LAN != nil {
		cfg.Network.LAN = *in.LAN
	}
	if in.LANAddr != nil {
		cfg.Network.LANAddr = strings.TrimSpace(*in.LANAddr)
	}
	if in.Tailscale != nil {
		cfg.Network.Tailscale = strings.TrimRight(strings.TrimSpace(*in.Tailscale), "/")
	}
	if am := in.AgentModels; am != nil {
		for _, id := range am.Ollama {
			if a, ok := harness.Find(id); !ok || !a.Ollama {
				return nil, invalid("%s cannot run on Ollama", id)
			}
		}
		am.OllamaURL, am.OllamaModel = strings.TrimSpace(am.OllamaURL), strings.TrimSpace(am.OllamaModel)
		cfg.AgentModels = *am
	}
	if err := p.st.SaveConfig(cfg); err != nil {
		return nil, err
	}
	res := map[string]any{"saved": true}
	if in.EnableServe {
		port := "7787"
		if HTTPAddr != nil {
			if i := strings.LastIndex(HTTPAddr(), ":"); i >= 0 {
				port = HTTPAddr()[i+1:]
			}
		}
		// Tailscale Serve on the tailnet only; never Funnel.
		out, err := exec.CommandContext(ctx, "tailscale", "serve", "--bg", "--https=443", "http://127.0.0.1:"+port).CombinedOutput()
		res["serve"] = strings.TrimSpace(string(out))
		if err != nil {
			res["serve_error"] = err.Error()
		}
	}
	if in.Restart && Restarter != nil {
		go func() { time.Sleep(300 * time.Millisecond); Restarter() }()
		res["restarting"] = true
	}
	return res, nil
}

func tailscaleName() (string, error) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return "", errors.New("tailscale is not running or not installed")
	}
	var st struct {
		Self struct{ DNSName string } `json:"Self"`
	}
	if json.Unmarshal(out, &st) != nil || st.Self.DNSName == "" {
		return "", errors.New("no tailnet name")
	}
	return strings.TrimSuffix(st.Self.DNSName, "."), nil
}

func lanIPs() []string {
	out, err := exec.Command("ifconfig").Output()
	if err != nil {
		return nil
	}
	var ips []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "inet" && f[1] != "127.0.0.1" && !strings.HasPrefix(f[1], "100.") && !strings.HasPrefix(f[1], "169.254.") {
			ips = append(ips, f[1])
		}
	}
	return ips
}

// Node handlers delegate to the SSH layer.
func (p *Pool) nodeAdd(ctx context.Context, in protocol.NodeAdd) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	v, err := p.nodes.Add(ctx, in)
	if err != nil {
		return v, err
	}
	p.refreshNodes()
	return v, nil
}

func (p *Pool) nodeRemove(_ context.Context, in idIn) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	return nil, p.nodes.Remove(in.ID)
}

func (p *Pool) nodeCheck(ctx context.Context, in idIn) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	return p.nodes.Check(ctx, in.ID), nil
}

type nodeUnlockIn struct {
	ID       string `json:"id"`
	Password string `json:"password"`
	Save     bool   `json:"save"`
}

func (p *Pool) nodeUnlock(ctx context.Context, in nodeUnlockIn) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	v, err := p.nodes.Unlock(ctx, in.ID, in.Password, in.Save)
	p.refreshNodes()
	return v, err
}

func (p *Pool) nodeForgetPassword(_ context.Context, in idIn) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	err := p.nodes.ForgetPassword(in.ID)
	p.refreshNodes()
	return nil, err
}

func (p *Pool) nodeKeys(_ context.Context, _ struct{}) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	return p.nodes.Keys(), nil
}

// nodeConnect readies an SSH host as the app's environment and returns where to reach its host.
func (p *Pool) nodeConnect(ctx context.Context, in idIn) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	c, err := p.nodes.Connect(ctx, in.ID)
	p.refreshNodes()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// nodeList lists the environments: this machine and the SSH hosts, for the app's switcher.
func (p *Pool) nodeList(_ context.Context, _ struct{}) (any, error) {
	if p.nodes == nil {
		return []protocol.NodeView{}, nil
	}
	return p.nodes.Status(), nil
}

type nodeKeySetupIn struct {
	ID  string `json:"id"`
	Key string `json:"key"` // a private key path, or "" for PenguPool's own key
}

func (p *Pool) nodeKeySetup(ctx context.Context, in nodeKeySetupIn) (any, error) {
	if p.nodes == nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "SSH nodes are not available")
	}
	steps, err := p.nodes.KeySetup(ctx, in.ID, in.Key)
	p.refreshNodes()
	return map[string]any{"steps": steps}, err
}

// historyRead returns a session's agent history from its harness transcript: Claude's JSONL
// records or OMP's session events. Only user and assistant text; tool noise is dropped.
func (p *Pool) historyRead(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
	}
	if err := p.iosMayReach(c, in.SessionID); err != nil {
		return nil, err
	}
	s, ok := p.session(in.SessionID)
	if !ok {
		return nil, notFound("no session")
	}
	if s.Transcript == "" {
		return map[string]any{"items": []any{}, "note": "no transcript recorded; the live screen is the only history"}, nil
	}
	f, err := os.Open(s.Transcript)
	if err != nil {
		return map[string]any{"items": []any{}, "note": err.Error()}, nil
	}
	defer f.Close()
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 200
	}
	type item struct {
		Role string `json:"role"`
		Text string `json:"text"`
		At   string `json:"at,omitempty"`
	}
	var items []item
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		role := rec.Message.Role
		if role != "user" && role != "assistant" {
			continue
		}
		text := contentText(rec.Message.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		items = append(items, item{Role: role, Text: text, At: rec.Timestamp})
	}
	if len(items) > in.Limit {
		items = items[len(items)-in.Limit:]
	}
	return map[string]any{"items": items}, nil
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// refreshNodes copies node status into the domain so clients see it through state.subscribe.
func (p *Pool) refreshNodes() {
	if p.nodes == nil {
		return
	}
	views := p.nodes.Status()
	if reflect.DeepEqual(views, p.st.State().Domain.Nodes) {
		return // no write: state.json changes only when something did
	}
	_ = p.update(func(st *store.State) error {
		st.Domain.Nodes = views
		return nil
	})
}

// endScribes ends the hidden scribe sessions an earlier version ran and forgets them: the writer
// does their work now, and read as ordinary sessions they would show in the session list.
func (p *Pool) endScribes(ctx context.Context) {
	found := false
	for _, s := range p.st.State().Sessions {
		if s.Role != "scribe" {
			continue
		}
		found = true
		if s.State == "running" && p.hub != nil {
			if err := p.hub.Kill(ctx, s.ID); err != nil {
				p.logf("pool: end scribe session %s: %v", s.Name, err)
			}
		}
	}
	if found {
		_ = p.update(func(st *store.State) error {
			st.Sessions = slices.DeleteFunc(st.Sessions, func(s store.Session) bool { return s.Role == "scribe" })
			return nil
		})
	}
}

// Run keeps node status fresh in the domain and runs the backups.
func (p *Pool) Run(ctx context.Context) {
	p.endScribes(ctx)
	go func() { // a helper's waiting done reports end with the pool
		<-ctx.Done()
		p.stopJournal()
	}()
	go p.backupLoop(ctx)
	go p.updateLoop(ctx)
	go p.codexLoop(ctx)
	go func() { // the Skill library follows the marketplace: a minute after start, then every 6 hours
		p.lockClonesIdle(ctx) // helpers' skills read-only, also those installed before 0.8.4
		wait := time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
				p.refreshLibrary(ctx)
			}
			wait = 6 * time.Hour
		}
	}()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		p.refreshNodes()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type providerModelsIn struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"` // a key typed but not saved yet
	// Address is the form's, which may not be saved yet; else the saved provider's.
	Address string `json:"address,omitempty"`
	// Capability keeps only the Ollama models that declare it: decision, completion or tools.
	Capability string `json:"capability,omitempty"`
}

// providerModels lists the models a provider offers, so the model is picked rather than typed:
// GET {address}/models (the OpenAI-compatible data[].id shape) or
// Ollama's /api/tags. Uses the typed key, else the saved one.
func (p *Pool) providerModels(ctx context.Context, in providerModelsIn) (any, error) {
	cfg := p.st.Config()
	pc := cfg.Decision.Providers[in.Type]
	// The saved key goes only to the saved address: a half-typed or other address gets a typed key or none.
	savedKey := in.Address == "" || strings.TrimRight(in.Address, "/") == strings.TrimRight(pc.Address, "/")
	if in.Address != "" {
		pc.Address = in.Address
	}
	if pc.Address == "" {
		return []string{}, nil
	}
	key := in.Key
	if key == "" && savedKey && pc.KeyRef != "" {
		key, _ = decision.ResolveKey(pc.KeyRef)
	}
	list, err := listProviderModels(ctx, in.Type, pc, key)
	if err != nil || in.Type != "ollama" || in.Capability == "" {
		return list, err
	}
	return ollamaCapable(ctx, pc.Address, list, in.Capability), nil
}

// ollamaCapable keeps the models whose /api/show lists the capability: what the model itself
// declares, not a guess from its name. A model Ollama cannot load or serve declares nothing.
func ollamaCapable(ctx context.Context, address string, models []string, capability string) []string {
	models = models[:min(len(models), 200)] // a picker past that is no use, and each costs a request
	ok := make([]bool, len(models))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ok[i] = slices.Contains(ollamaCapabilities(ctx, address, m), capability)
		}()
	}
	wg.Wait()
	out := []string{}
	for i, m := range models {
		if ok[i] {
			out = append(out, m)
		}
	}
	return out
}

// ollamaCapabilities is what Ollama says a model can do, or nil when it cannot show the model.
func ollamaCapabilities(ctx context.Context, address, model string) []string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(address, "/")+"/api/show", bytes.NewReader(b))
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	return out.Capabilities
}

func listProviderModels(ctx context.Context, typ string, pc store.ProviderConfig, key string) ([]string, error) {
	u := strings.TrimRight(pc.Address, "/") + "/models"
	if typ == "ollama" {
		u = strings.TrimRight(pc.Address, "/") + "/api/tags"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, invalid("%v", err)
	}
	if key != "" {
		if pc.Format == "anthropic-messages" {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "could not reach %s: %v", pc.Address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "%s answered %s; check the API key", pc.Address, resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "unexpected model list: %v", err)
	}
	out := []string{}
	for _, m := range body.Data {
		out = append(out, strings.TrimPrefix(m.ID, "models/"))
	}
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	slices.Sort(out)
	return out, nil
}

type ollamaStatusIn struct {
	URL string `json:"url,omitempty"`
}

// ollamaStatus is what Claude Code on Ollama needs from this machine: the ollama CLI (it launches
// Claude) and the server answering at the address.
func (p *Pool) ollamaStatus(ctx context.Context, in ollamaStatusIn) (any, error) {
	url := strings.TrimRight(firstNonEmpty(in.URL, p.st.Config().AgentModels.OllamaURL, "http://127.0.0.1:11434"), "/")
	out := map[string]any{"url": url}
	if bin, err := harness.LookupOllama(p.env); err != nil {
		out["cli_error"] = err.Error()
	} else {
		out["cli"] = bin
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/version", nil)
	if err != nil {
		return nil, invalid("%v", err)
	}
	var v struct {
		Version string `json:"version"`
	}
	if resp, err := http.DefaultClient.Do(req); err != nil {
		out["server_error"] = "Ollama is not answering at " + url
	} else {
		_ = json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		out["version"] = v.Version
	}
	return out, nil
}
