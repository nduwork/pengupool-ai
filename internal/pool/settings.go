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
	"path/filepath"
	"reflect"
	"regexp"
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
	if cfg.Decision.RulesFallback { // removed: the triage model is required, there is no rules mode
		cfg.Decision.RulesFallback = false
		if err := p.st.SaveConfig(cfg); err == nil {
			p.logf("pool: \"If the model fails, use simple matching\" is removed; PenguPool needs a working triage model (Settings → Decision model)")
		}
	}
	var gone []string
	for k, pc := range cfg.Decision.Providers {
		if !providerTypes[pc.Type] || decision.Validate(pc) != nil {
			gone = append(gone, k)
			delete(cfg.Decision.Providers, k)
			// The note says triage no longer speaks the format, and saving that type clears it: so only for a
			// kind still offered (an old online kind could never be saved again to clear it).
			if providerTypes[pc.Type] && !slices.Contains(decision.Formats, pc.Format) {
				cfg.Decision.Removed = slices.DeleteFunc(cfg.Decision.Removed, func(r store.RemovedProvider) bool { return r.Type == pc.Type })
				cfg.Decision.Removed = append(cfg.Decision.Removed, store.RemovedProvider{Type: pc.Type, Format: pc.Format, Model: pc.Model, Active: k == cfg.Decision.Active})
			}
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
	p.logf("pool: removed decision models no longer supported: %s; set one in Settings → Decision model", strings.Join(gone, ", "))
}

func (p *Pool) providers(_ context.Context, _ struct{}) (any, error) {
	c := p.st.Config().Decision
	defaults := map[string]store.ProviderConfig{
		"ollama": {Type: "ollama", Format: "ollama-systemone", Address: "http://127.0.0.1:11434", Model: "tev1:0.8b"},
		"jev":    {Type: "jev", Format: "systemone", Address: "https://api.typesafe.ai/v1/systemone", Model: "jev-latest"},
	}
	checked := []string{}
	p.mu.Lock()
	for t, pc := range c.Providers {
		if p.passed[t] == pc {
			checked = append(checked, t)
		}
	}
	p.mu.Unlock()
	return map[string]any{
		"active": c.Active, "providers": c.Providers, "checked": checked, "removed": c.Removed,
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
	cfg.Decision.Removed = slices.DeleteFunc(cfg.Decision.Removed, func(r store.RemovedProvider) bool { return r.Type == pc.Type })
	if err := p.st.SaveConfig(cfg); err != nil {
		return nil, err
	}
	p.mu.Lock()
	delete(p.passed, pc.Type) // saved again (maybe a new key under the same reference): check it again
	if p.saves == nil {
		p.saves = map[string]int{}
	}
	p.saves[pc.Type]++
	p.mu.Unlock()
	if cfg.Decision.Active == pc.Type { // the active model changed: check it again before trusting it
		p.mu.Lock()
		p.triageErr = triageUnchecked
		p.mu.Unlock()
		go p.checkTriage(context.WithoutCancel(ctx))
	}
	return pc, nil
}

type providerTypeIn struct {
	Type string `json:"type"`
}

// providerCheck is the explicit host-side connection check; a saved form is not a working
// connection until this passes.
func (p *Pool) providerCheck(ctx context.Context, in providerTypeIn) (any, error) {
	p.mu.Lock()
	saves := p.saves[in.Type]
	p.mu.Unlock()
	pc, ok := p.st.Config().Decision.Providers[in.Type]
	if !ok {
		return nil, notFound("save the %s provider first", in.Type)
	}
	res, items, err := decision.Check(ctx, pc)
	pass := err == nil && !slices.ContainsFunc(items, func(c decision.CheckItem) bool { return !c.OK })
	p.mu.Lock()
	stale := p.saves[in.Type] != saves // saved while it ran: its answer is about what was there before
	if pass && !stale {
		if p.passed == nil {
			p.passed = map[string]store.ProviderConfig{}
		}
		p.passed[in.Type] = pc
	} else {
		delete(p.passed, in.Type)
	}
	p.mu.Unlock()
	if stale {
		return map[string]any{"ok": false, "error": "The provider was saved again while it was checked: run Check connection again."}, nil
	}
	if err != nil {
		hint := ""
		if errors.Is(err, decision.ErrRefused) {
			hint = "The model answered, but not in the decision format: pick a System One decision model."
		} else if pc.Type == "ollama" {
			hint = "Is Ollama running (`ollama serve`) and is the model pulled (`ollama pull " + pc.Model + "`)? System One needs Ollama 0.35 or later. PenguPool never installs Ollama or pulls weights."
		}
		return map[string]any{"ok": false, "error": err.Error(), "hint": hint}, nil
	}
	out := map[string]any{"ok": pass, "model": res.Model, "latency_ms": res.Latency.Milliseconds(), "calibrated": res.Calibrated, "checks": items}
	if !pass {
		out["error"] = "The model answered in the decision format but got plain questions wrong; it is not fit to decide."
	}
	return out, nil
}

// providerActivate makes a saved model the triage model. PenguPool needs one: it cannot be set to
// none, and a model is made active only once the host's last connection check of exactly its saved
// settings passed.
func (p *Pool) providerActivate(_ context.Context, in providerTypeIn) (any, error) {
	if in.Type == "" {
		return nil, invalid("a triage model is required: PenguPool's memory and check-ins need one; pick a local (Ollama) or remote model")
	}
	cfg := p.st.Config()
	pc, ok := cfg.Decision.Providers[in.Type]
	if !ok {
		return nil, notFound("save the %s provider first", in.Type)
	}
	p.mu.Lock()
	checked := p.passed[in.Type] == pc
	p.mu.Unlock()
	if !checked {
		return nil, invalid("Run Check connection first: the %s model's saved settings have not passed their check", in.Type)
	}
	cfg.Decision.Active = in.Type
	if err := p.st.SaveConfig(cfg); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.triageErr = ""
	p.mu.Unlock()
	return nil, nil
}

// HTTPAddr reports the gateway's bound address.
var HTTPAddr func() string

// settingsTailscale is the Tailscale checklist, on its own: it runs the CLI and a probe (seconds when
// Tailscale is slow), so only the Tailscale panel asks for it, never every settings read.
func (p *Pool) settingsTailscale(_ context.Context, _ struct{}) (any, error) {
	return p.tailscaleChecks(), nil
}

func (p *Pool) settingsGet(_ context.Context, _ struct{}) (any, error) {
	cfg := p.st.Config()
	out := map[string]any{"network": cfg.Network, "store": p.st.Dir(), "executable": p.exe, "ollama_default": cfg.OllamaDefault, "agents": harness.Agents, "version": p.version}
	if HTTPAddr != nil {
		out["bound"] = HTTPAddr()
	}
	if ts, err := tailscaleName(); err == nil {
		out["tailscale_name"] = ts
	} else {
		out["tailscale_error"] = err.Error()
	}
	out["github"] = p.githubStatus()
	out["agent_permissions"] = p.agentPermissionsView()
	out["sync"] = p.syncView()
	out["backup"] = out["sync"] // the old name, kept one release
	return out, nil
}

type settingsIn struct {
	Tailscale *string `json:"tailscale,omitempty"`
	// AgentPermissions is the owner's consent, "allowed" or "off", to PenguPool's rules in each agent's
	// own config: from the app only (agents' CLI calls never reach settings.save).
	AgentPermissions string `json:"agent_permissions,omitempty"`
	EnableServe      bool   `json:"enable_serve,omitempty"`
}

func (p *Pool) settingsSave(ctx context.Context, in settingsIn) (any, error) {
	// One locked read-modify-write: a Muse device revoked meanwhile stays revoked.
	err := p.st.UpdateConfig(func(cfg *store.Config) error {
		if in.Tailscale != nil {
			cfg.Network.Tailscale = strings.TrimRight(strings.TrimSpace(*in.Tailscale), "/")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{"saved": true}
	if in.AgentPermissions != "" {
		if in.AgentPermissions != "allowed" && in.AgentPermissions != "off" {
			return nil, invalid("agent_permissions is allowed or off")
		}
		res["agent_permissions"] = p.setAgentPermissions(in.AgentPermissions)
	}
	if in.EnableServe {
		port := boundPort()
		if port == "" {
			// Never guess a port: Serve pointed at a port nothing
			// listens on fails silently for every remote client.
			res["serve_error"] = "daemon HTTP port is not bound yet; try again in a moment"
		} else {
			// Tailscale Serve on the tailnet only; never Funnel. Stale modes keep
			// their mode; a fresh setup gets HTTPS with certs, else plain HTTP.
			var outs, errs []string
			approve := ""
			plan, perr := servePlan(ctx, port, p.st.Config().Network.ServePort)
			if perr != nil {
				errs = append(errs, perr.Error())
			}
			for _, mode := range plan {
				out, url, err := serveOnce(ctx, mode, port)
				switch {
				case url != "":
					approve = url
				case err != nil:
					errs = append(errs, string(mode)+": "+err.Error())
					outs = append(outs, out)
				default:
					outs = append(outs, out)
					p.recordServePort(port)
				}
			}
			if approve != "" {
				res["serve_approve_url"] = approve
			} else {
				res["serve"] = strings.Join(outs, "\n")
				if len(errs) > 0 {
					res["serve_error"] = strings.Join(errs, "; ")
				}
			}
		}
	}
	return res, nil
}

// tailscaleLookPath, tailscaleAppBundle and tailscaleCmd are the exec
// seams for Tailscale: tests stub them, production uses the real CLI.
var tailscaleLookPath = exec.LookPath

var tailscaleAppBundle = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

var tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).CombinedOutput()
}

// tailscaleBin finds the Tailscale CLI: PATH first, then the userBins
// agent CLIs install into, then the macOS app bundle's CLI. A
// GUI-launched daemon's PATH misses all but /usr/bin:/bin.
func tailscaleBin() string {
	if p, err := tailscaleLookPath("tailscale"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, d := range userBins {
		if rest, ok := strings.CutPrefix(d, "~/"); ok {
			if home == "" {
				continue
			}
			d = filepath.Join(home, rest)
		}
		p := filepath.Join(d, "tailscale")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	if st, err := os.Stat(tailscaleAppBundle); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return tailscaleAppBundle
	}
	return ""
}

func tailscaleName() (string, error) {
	bin := tailscaleBin()
	if bin == "" {
		return "", errors.New("tailscale is not running or not installed")
	}
	// Bounded: every settings read asks, and a hung tailscaled must not hold the Settings page.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
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

// tailscaleFix is the one fix a failing checklist row offers: a link to
// open, or a button that runs an action.
type tailscaleFix struct {
	Kind   string `json:"kind"` // link | button
	Label  string `json:"label"`
	URL    string `json:"url,omitempty"`
	Action string `json:"action,omitempty"` // serve | retry
}

// tailscaleCheck is one row of the Tailscale checklist.
type tailscaleCheck struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	OK     bool          `json:"ok"`
	Detail string        `json:"detail"`
	Fix    *tailscaleFix `json:"fix,omitempty"`
}

type tailscaleStatus struct {
	BackendState string   `json:"BackendState"`
	CertDomains  []string `json:"CertDomains"`
	Self         struct {
		DNSName     string   `json:"DNSName"`
		CertDomains []string `json:"CertDomains"`
	} `json:"Self"`
}

type tailscaleServeStatus struct {
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
}

// serveMode is how Tailscale Serve publishes the app: HTTPS on :443 or plain HTTP on :80.
// The companion reaches either (PENGUPOOL_TRANSPORT serve or serve-http); the default is HTTPS
// on a tailnet with HTTPS certificates, else plain HTTP.
type serveMode string

const (
	serveHTTPS serveMode = "https"
	serveHTTP  serveMode = "http"
)

// flag is the tailscale serve flag enabling this mode.
func (m serveMode) flag() string {
	if m == serveHTTP {
		return "--http=80"
	}
	return "--https=443"
}

// serveTargets returns what each Serve mode proxies to ("127.0.0.1:port" for a local app), or ""
// when that mode is not configured. A route to anything else is returned as its proxy (or
// "another handler"), so it is never mistaken for a free mode. A tailnet may serve both modes.
func serveTargets(s tailscaleServeStatus) (httpsTarget, httpTarget string) {
	for name, web := range s.Web {
		var dst *string
		switch {
		case strings.HasSuffix(name, ":443"):
			dst = &httpsTarget
		case strings.HasSuffix(name, ":80"):
			dst = &httpTarget
		default:
			continue
		}
		for _, h := range web.Handlers {
			switch {
			case strings.HasPrefix(h.Proxy, "http://127.0.0.1:"):
				*dst = strings.TrimPrefix(h.Proxy, "http://")
			case *dst == "" && h.Proxy != "":
				*dst = h.Proxy
			case *dst == "":
				*dst = "another handler"
			}
		}
	}
	return httpsTarget, httpTarget
}

// serveCerts reports whether the tailnet has HTTPS certificates: Serve --https needs them.
func serveCerts(st tailscaleStatus) bool {
	return len(st.Self.CertDomains) > 0 || len(st.CertDomains) > 0
}

// currentServeTargets asks Tailscale what Serve points at the app per mode. ok is false when the
// status could not be read.
func currentServeTargets(ctx context.Context, bin string) (httpsTarget, httpTarget string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := tailscaleCmd(ctx, bin, "serve", "status", "--json")
	if err != nil {
		return "", "", false
	}
	var ss tailscaleServeStatus
	if json.Unmarshal(raw, &ss) != nil {
		return "", "", false
	}
	httpsTarget, httpTarget = serveTargets(ss)
	return httpsTarget, httpTarget, true
}

// ownServeTargets keeps only PenguPool's own Serve targets: those at port (this daemon) or at
// servePort (the port it last served). With no port recorded (a setup older than the record),
// a local :443 route counts as PenguPool's, as it always did.
func ownServeTargets(httpsTarget, httpTarget, servePort, port string) (string, string) {
	mine := func(t string) bool {
		return t == "127.0.0.1:"+port || (servePort != "" && t == "127.0.0.1:"+servePort)
	}
	if !mine(httpsTarget) && (servePort != "" || !strings.HasPrefix(httpsTarget, "127.0.0.1:")) {
		httpsTarget = ""
	}
	if !mine(httpTarget) {
		httpTarget = ""
	}
	return httpsTarget, httpTarget
}

// recordServePort remembers the port PenguPool just pointed Serve at.
func (p *Pool) recordServePort(port string) {
	if err := p.st.UpdateConfig(func(c *store.Config) error { c.Network.ServePort = port; return nil }); err != nil {
		p.logf("pool: record serve port: %v", err)
	}
}

// servePlan is the Serve modes settingsSave (re-)points at port: every mode PenguPool configured
// whose target is stale, in its own mode; when it configured none, the tailnet's default mode. It
// never switches a mode, never adds HTTPS where the tailnet has no certificates, and never takes
// over a mode another app's route already uses: that is an error naming the route.
func servePlan(ctx context.Context, port, servePort string) ([]serveMode, error) {
	bin := tailscaleBin()
	want := "127.0.0.1:" + port
	if bin == "" {
		return []serveMode{serveHTTPS}, nil // serveOnce reports that Tailscale is missing
	}
	rawHTTPS, rawHTTP, ok := currentServeTargets(ctx, bin)
	if ok {
		httpsTarget, httpTarget := ownServeTargets(rawHTTPS, rawHTTP, servePort, port)
		if httpsTarget != "" || httpTarget != "" {
			var plan []serveMode
			if httpsTarget != "" && httpsTarget != want {
				plan = append(plan, serveHTTPS)
			}
			if httpTarget != "" && httpTarget != want {
				plan = append(plan, serveHTTP)
			}
			return plan, nil // possibly empty: everything already points here
		}
	}
	mode, servePort, other := serveHTTPS, ":443", rawHTTPS
	if st, err := runTailscaleStatus(ctx, bin); err == nil && !serveCerts(st) {
		mode, servePort, other = serveHTTP, ":80", rawHTTP
	}
	if other != "" {
		return nil, errors.New("Serve on " + servePort + " already points at " + other + " (another app); PenguPool leaves it alone")
	}
	return []serveMode{mode}, nil
}

func boundPort() string {
	if HTTPAddr == nil {
		return ""
	}
	if i := strings.LastIndex(HTTPAddr(), ":"); i >= 0 {
		return HTTPAddr()[i+1:]
	}
	return ""
}

// runTailscaleStatus runs status --json (10 s) and parses what the checklist needs.
func runTailscaleStatus(ctx context.Context, bin string) (tailscaleStatus, error) {
	var st tailscaleStatus
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := tailscaleCmd(ctx, bin, "status", "--json")
	if err != nil {
		return st, err
	}
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:] // the CLI may print a version-skew warning before the JSON
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return st, err
	}
	return st, nil
}

// tailscaleChecks builds the checklist, in order. Every failing row
// carries exactly one fix.
func (p *Pool) tailscaleChecks() []tailscaleCheck {
	bin := tailscaleBin()
	if bin == "" {
		return []tailscaleCheck{{
			ID: "installed", Label: "Installed", Detail: "The Tailscale CLI was not found.",
			Fix: &tailscaleFix{Kind: "link", Label: "Install Tailscale", URL: "https://tailscale.com/download"},
		}}
	}
	ctx := context.Background()
	st, serr := runTailscaleStatus(ctx, bin)
	checks := []tailscaleCheck{{ID: "installed", Label: "Installed", OK: true, Detail: bin}}
	if serr != nil {
		return append(checks, tailscaleCheck{
			ID: "login", Label: "Logged in", Detail: strings.TrimSpace(serr.Error()),
			Fix: &tailscaleFix{Kind: "button", Label: "Check again", Action: "retry"},
		})
	}
	if st.BackendState == "Running" {
		checks = append(checks, tailscaleCheck{ID: "login", Label: "Logged in", OK: true, Detail: "Backend running"})
	} else {
		checks = append(checks, tailscaleCheck{
			ID: "login", Label: "Logged in", Detail: "Backend state: " + firstNonEmpty(st.BackendState, "not running"),
			Fix: &tailscaleFix{Kind: "button", Label: "Check again", Action: "retry"},
		})
		return checks
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	certs := append(append([]string{}, st.Self.CertDomains...), st.CertDomains...)
	if len(certs) > 0 {
		checks = append(checks, tailscaleCheck{ID: "https", Label: "HTTPS on the tailnet", OK: true, Detail: strings.Join(certs, ", ")})
	} else {
		checks = append(checks, tailscaleCheck{
			ID: "https", Label: "HTTPS on the tailnet", Detail: "No HTTPS certificate on the tailnet.",
			Fix: &tailscaleFix{Kind: "link", Label: "Enable HTTPS in the admin console", URL: "https://login.tailscale.com/admin/dns"},
		})
	}
	port := boundPort()
	httpsTarget, httpTarget, _ := currentServeTargets(ctx, bin)
	want := "127.0.0.1:" + port
	serving := ""
	if port != "" {
		switch {
		case httpsTarget == want:
			serving = "https://" + name + " serves this app"
		case httpTarget == want:
			serving = "http://" + name + " serves this app"
		}
	}
	switch {
	case serving != "":
		checks = append(checks, tailscaleCheck{ID: "serve", Label: "Serve points at this app", OK: true, Detail: serving})
	case httpsTarget != "" || httpTarget != "":
		target := firstNonEmpty(httpsTarget, httpTarget)
		checks = append(checks, tailscaleCheck{
			ID: "serve", Label: "Serve points at this app", Detail: "Serve points at " + target + ", not this app.",
			Fix: &tailscaleFix{Kind: "button", Label: "Serve", Action: "serve"},
		})
	default:
		checks = append(checks, tailscaleCheck{
			ID: "serve", Label: "Serve points at this app", Detail: "Serve is not pointing at this app.",
			Fix: &tailscaleFix{Kind: "button", Label: "Serve", Action: "serve"},
		})
	}
	if name == "" {
		return append(checks, tailscaleCheck{
			ID: "reachable", Label: "Reachable", Detail: "No tailnet name.",
			Fix: &tailscaleFix{Kind: "button", Label: "Retry", Action: "retry"},
		})
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://" + name + "/")
	if err == nil {
		resp.Body.Close()
		checks = append(checks, tailscaleCheck{ID: "reachable", Label: "Reachable", OK: true, Detail: "HTTP " + resp.Status})
	} else {
		checks = append(checks, tailscaleCheck{
			ID: "reachable", Label: "Reachable", Detail: strings.TrimSpace(err.Error()),
			Fix: &tailscaleFix{Kind: "button", Label: "Retry", Action: "retry"},
		})
	}
	return checks
}

var tailscaleApproveRe = regexp.MustCompile(`https://login\.tailscale\.com/[^\s"']*`)

// serveOnce runs Tailscale Serve at this daemon's bound port in the given mode (20 s). A
// login.tailscale.com approval URL in the output is returned, not an error.
func serveOnce(ctx context.Context, mode serveMode, port string) (out, approveURL string, err error) {
	bin := tailscaleBin()
	if bin == "" {
		return "", "", errors.New("tailscale is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := tailscaleCmd(ctx, bin, "serve", "--bg", mode.flag(), "http://127.0.0.1:"+port)
	out = strings.TrimSpace(string(raw))
	if approveURL = tailscaleApproveRe.FindString(out); approveURL != "" {
		return out, approveURL, nil
	}
	return out, "", err
}

// HealTailscaleServe re-runs Serve when a Serve target PenguPool configured is
// not this daemon's bound port (e.g. after a restart onto a new port). Each mode
// keeps its mode; another app's route is never touched; when nothing is
// configured there is nothing to heal. Only for the
// owner's real home; never for throwaway stores.
func (p *Pool) HealTailscaleServe() {
	cfg := p.st.Config()
	if cfg.Network.Tailscale == "" || !store.IsHomeDefault(p.st.Dir()) {
		return
	}
	// cmdDaemon launches this before the gateway listener exists, and
	// boundPort() stays "" until it binds. Wait for the bound address
	// the way the hub.addr writer in cmdDaemon does; Heal runs in its
	// own goroutine, so the bounded wait blocks nothing. If the
	// listener never reports an address, there is nothing to heal.
	port := ""
	for range 200 {
		if port = boundPort(); port != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if port == "" {
		p.logf("pool: tailscale serve heal skipped: daemon HTTP port not bound")
		return
	}
	bin := tailscaleBin()
	if bin == "" {
		return
	}
	httpsTarget, httpTarget, ok := currentServeTargets(context.Background(), bin)
	httpsTarget, httpTarget = ownServeTargets(httpsTarget, httpTarget, cfg.Network.ServePort, port)
	if !ok || (httpsTarget == "" && httpTarget == "") {
		return
	}
	want := "127.0.0.1:" + port
	for _, mode := range []serveMode{serveHTTPS, serveHTTP} {
		target := httpsTarget
		if mode == serveHTTP {
			target = httpTarget
		}
		if target == "" || target == want {
			continue
		}
		if out, approve, err := serveOnce(context.Background(), mode, port); err != nil && approve == "" {
			p.logf("pool: tailscale serve re-point failed: %v (%s)", err, out)
		} else if approve != "" {
			p.logf("pool: tailscale serve needs approval in the admin console")
		} else {
			p.recordServePort(port)
			p.logf("pool: tailscale serve re-pointed at 127.0.0.1:%s", port)
		}
	}
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
// records or OMP's session events. Only user and assistant text, credentials left out; tool noise
// is dropped. A phone may read it, so nothing secret leaves the host.
func (p *Pool) historyRead(_ context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("params: %v", err)
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
		items = append(items, item{Role: role, Text: redact(text), At: rec.Timestamp})
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

// Run keeps node status fresh in the domain and runs the daily backup and the sync.
func (p *Pool) Run(ctx context.Context) {
	p.endScribes(ctx)
	go p.checkTriage(ctx) // the triage model's state is known before work asks it
	go p.syncLoop(ctx)
	go p.updateLoop(ctx)
	go p.readyLoop(ctx)
	go func() { // the Skill library follows the marketplace: a minute after start, then every 6 hours
		p.syncMu.Lock() // the move to the Specialist hub, before any sync reads the catalog
		p.unlockClones()
		p.migrateHub(ctx)
		p.syncMu.Unlock()
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
