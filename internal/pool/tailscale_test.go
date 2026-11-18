package pool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Tailscale CLI is found on PATH first, then the userBins agent CLIs
// install into, then the macOS app bundle's CLI.
func TestTailscaleBinFallbackOrder(t *testing.T) {
	oldLook, oldApp, oldBins := tailscaleLookPath, tailscaleAppBundle, userBins
	defer func() { tailscaleLookPath, tailscaleAppBundle, userBins = oldLook, oldApp, oldBins }()
	tailscaleLookPath = exec.LookPath // explicit: never inherit another test's stub
	mk := func(dir string) string {
		p := filepath.Join(dir, "tailscale")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// PATH wins, through the real lookup.
	pathBin := mk(filepath.Join(t.TempDir(), "bin"))
	t.Setenv("PATH", filepath.Dir(pathBin))
	if got := tailscaleBin(); got != pathBin {
		t.Fatalf("PATH: %q", got)
	}
	// Then a userBins dir under a temp HOME.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	userBins = []string{"~/ubin"}
	homeBin := mk(filepath.Join(os.Getenv("HOME"), "ubin"))
	tailscaleLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if got := tailscaleBin(); got != homeBin {
		t.Fatalf("userBins: %q", got)
	}
	// Then the app bundle.
	if err := os.Remove(homeBin); err != nil {
		t.Fatal(err)
	}
	userBins = nil
	appBin := mk(filepath.Join(t.TempDir(), "Tailscale.app", "Contents", "MacOS"))
	tailscaleAppBundle = appBin
	if got := tailscaleBin(); got != appBin {
		t.Fatalf("bundle: %q", got)
	}
	if err := os.Remove(appBin); err != nil {
		t.Fatal(err)
	}
	if got := tailscaleBin(); got != "" {
		t.Fatalf("missing: %q", got)
	}
}

const tailscaleStatusRunning = `{"BackendState":"Running","CertDomains":null,"Self":{"DNSName":"probe-machine.tailnet.ts.net.","CertDomains":["probe-machine.tailnet.ts.net"]}}`

const tailscaleStatusStopped = `{"BackendState":"Stopped","AuthURL":"","Self":{"DNSName":"","CertDomains":null}}`

const tailscaleStatusNoCerts = `{"BackendState":"Running","CertDomains":null,"Self":{"DNSName":"probe-machine.tailnet.ts.net.","CertDomains":null}}`

// withUnsetEnv unsets key for the test, restoring it after.
func withUnsetEnv(t *testing.T, key string) {
	t.Helper()
	if v, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { os.Setenv(key, v) })
	} else {
		t.Cleanup(func() { os.Unsetenv(key) })
	}
	os.Unsetenv(key)
}

func tailscaleServeJSON(proxy string) string {
	return tailscaleServeJSONFor(proxy, "probe-machine.tailnet.ts.net:443")
}

func tailscaleServeJSONFor(proxy, addr string) string {
	if proxy == "" {
		return `{"Web":{}}`
	}
	return `{"Web":{"` + addr + `":{"Handlers":{"/":{"Proxy":"` + proxy + `"}}}}}`
}

// stubTailscale serves canned status/serve-status and records serve runs.
func stubTailscale(t *testing.T, status, serve string) *[]string {
	t.Helper()
	oldLook, oldCmd, oldAddr := tailscaleLookPath, tailscaleCmd, HTTPAddr
	var ran []string
	tailscaleLookPath = func(string) (string, error) { return "/bin/tailscale", nil }
	tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "serve" && args[1] == "status" {
			return []byte(serve), nil
		}
		if len(args) >= 1 && args[0] == "status" {
			return []byte(status), nil
		}
		if len(args) >= 1 && args[0] == "serve" {
			ran = append(ran, strings.Join(args, " "))
			return []byte("ok"), nil
		}
		return nil, os.ErrNotExist
	}
	HTTPAddr = func() string { return "127.0.0.1:7787" }
	oldSleep, oldNow, oldProbe := healSleep, healNow, serveProbe
	clock := time.Unix(0, 0) // a fake clock: each heal sleep advances it
	healNow = func() time.Time { return clock }
	healSleep = func(_ context.Context, d time.Duration) { clock = clock.Add(d) }
	serveProbe = func(context.Context, string) error { return nil }
	t.Cleanup(func() {
		tailscaleLookPath, tailscaleCmd, HTTPAddr, healSleep, healNow, serveProbe = oldLook, oldCmd, oldAddr, oldSleep, oldNow, oldProbe
	})
	return &ran
}

func checkByID(t *testing.T, cs []tailscaleCheck, id string) tailscaleCheck {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, cs)
	return tailscaleCheck{}
}

func testPool(t *testing.T) *Pool {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Store: st, Exe: "pengupool"})
}

// Each failing row shows exactly one fix.
func TestTailscaleChecklistFailures(t *testing.T) {
	// Tailscale missing entirely: one row.
	oldLook, oldApp, oldBins := tailscaleLookPath, tailscaleAppBundle, userBins
	defer func() { tailscaleLookPath, tailscaleAppBundle, userBins = oldLook, oldApp, oldBins }()
	tailscaleLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	userBins = nil
	tailscaleAppBundle = filepath.Join(t.TempDir(), "nope")
	p := testPool(t)
	cs := p.tailscaleChecks()
	if len(cs) != 1 || cs[0].ID != "installed" || cs[0].OK || cs[0].Fix == nil || cs[0].Fix.Kind != "link" {
		t.Fatalf("installed: %+v", cs)
	}

	// Logged out: installed green, login red with a fix.
	stubTailscale(t, tailscaleStatusStopped, tailscaleServeJSON(""))
	cs = p.tailscaleChecks()
	if got := checkByID(t, cs, "login"); got.OK || got.Fix == nil {
		t.Fatalf("login: %+v", cs)
	}

	// No HTTPS certificate: https red with an admin-console link.
	stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSON("http://127.0.0.1:7787"))
	cs = p.tailscaleChecks()
	if got := checkByID(t, cs, "https"); got.OK || got.Fix == nil || got.Fix.Kind != "link" || !strings.Contains(got.Fix.URL, "login.tailscale.com") {
		t.Fatalf("https: %+v", cs)
	}
	if got := checkByID(t, cs, "serve"); !got.OK {
		t.Fatalf("serve should be green: %+v", cs)
	}

	// Serve points elsewhere: serve red with a Serve button.
	stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
	cs = p.tailscaleChecks()
	if got := checkByID(t, cs, "serve"); got.OK || got.Fix == nil || got.Fix.Action != "serve" || !strings.Contains(got.Detail, "1111") {
		t.Fatalf("serve: %+v", cs)
	}

	// Serve missing: serve red with a Serve button.
	stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON(""))
	cs = p.tailscaleChecks()
	if got := checkByID(t, cs, "serve"); got.OK || got.Fix == nil || got.Fix.Action != "serve" {
		t.Fatalf("serve: %+v", cs)
	}
}

// The approval URL in serve output becomes an approve link, not an error.
func TestServeApproveURL(t *testing.T) {
	oldLook, oldCmd, oldAddr := tailscaleLookPath, tailscaleCmd, HTTPAddr
	defer func() { tailscaleLookPath, tailscaleCmd, HTTPAddr = oldLook, oldCmd, oldAddr }()
	tailscaleLookPath = func(string) (string, error) { return "/bin/tailscale", nil }
	HTTPAddr = func() string { return "127.0.0.1:7787" }
	tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return []byte("line1\nhttps://login.tailscale.com/f/serve/abc123\nline3\n"), nil
	}
	p := testPool(t)
	res, err := p.settingsSave(context.Background(), settingsIn{EnableServe: true})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["serve_approve_url"] != "https://login.tailscale.com/f/serve/abc123" {
		t.Fatalf("save = %+v", m)
	}
	if _, bad := m["serve_error"]; bad {
		t.Fatalf("approval reported as error: %+v", m)
	}
}

// healHomeStore opens the owner's home store with Serve enabled, for heal tests.
// looseHome is a temporary HOME whose cleanup tolerates late writers: background work New starts
// may still be writing under HOME when a quick test ends.
func looseHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "pp-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func healHomeStore(t *testing.T) *store.Store {
	t.Helper()
	home := looseHome(t)
	t.Setenv("HOME", home)
	withUnsetEnv(t, "PENGUPOOL_HOME")
	st, err := store.Open(filepath.Join(home, ".pengupool-ai", "v2"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.Config()
	cfg.Network.Tailscale = "https://probe.tailnet.ts.net"
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return st
}

// logSink is a race-safe Logf for heal tests (Heal's helpers may log from other goroutines).
func logSink() (logf func(string, ...any), joined func() string) {
	var mu sync.Mutex
	var logs []string
	logf = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(f, a...))
	}
	joined = func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(logs, "\n")
	}
	return logf, joined
}

// setServePort records port as the one PenguPool last served, as a successful serve does.
func setServePort(t *testing.T, st *store.Store, port string) {
	t.Helper()
	if err := st.UpdateConfig(func(c *store.Config) error { c.Network.ServePort = port; return nil }); err != nil {
		t.Fatal(err)
	}
}

// Self-heal re-serves a stale target once, and only for the owner's home.
func TestHealTailscaleServe(t *testing.T) {
	withHome := func(t *testing.T, home string) {
		t.Helper()
		t.Setenv("HOME", home)
		withUnsetEnv(t, "PENGUPOOL_HOME")
	}
	t.Run("stale", func(t *testing.T) {
		withHome(t, looseHome(t))
		st, err := store.Open(filepath.Join(os.Getenv("HOME"), ".pengupool-ai", "v2"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := st.Config()
		cfg.Network.Tailscale = "https://probe.tailnet.ts.net"
		if err := st.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--https=443") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
	})
	t.Run("healthy", func(t *testing.T) {
		withHome(t, looseHome(t))
		st, err := store.Open(filepath.Join(os.Getenv("HOME"), ".pengupool-ai", "v2"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := st.Config()
		cfg.Network.Tailscale = "https://probe.tailnet.ts.net"
		if err := st.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:7787"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 0 {
			t.Fatalf("healthy setup re-served: %v", *ran)
		}
	})
	t.Run("throwaway", func(t *testing.T) {
		withHome(t, looseHome(t))
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cfg := st.Config()
		cfg.Network.Tailscale = "https://probe.tailnet.ts.net"
		if err := st.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
		_ = ran
		p := New(Options{Store: st, Exe: "pengupool"})
		oldRun := tailscaleCmd
		called := false
		tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "serve" && len(args) > 1 && args[1] != "status" {
				called = true
			}
			return oldRun(ctx, bin, args...)
		}
		defer func() { tailscaleCmd = oldRun }()
		p.HealTailscaleServe(context.Background())
		if called {
			t.Fatal("throwaway store re-served")
		}
	})
	t.Run("unboundThenBound", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
		calls := 0
		HTTPAddr = func() string {
			calls++
			if calls < 5 {
				return ""
			}
			return "127.0.0.1:7787"
		}
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--https=443") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
	})
	t.Run("neverBound", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
		HTTPAddr = func() string { return "" }
		logf, logs := logSink()
		p := New(Options{Store: st, Exe: "pengupool", Logf: logf})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 0 {
			t.Fatalf("unbound heal served: %v", *ran)
		}
		if joined := logs(); !strings.Contains(joined, "not bound") {
			t.Fatalf("no skip logged: %q", joined)
		}
	})
	t.Run("httpStale", func(t *testing.T) {
		st := healHomeStore(t)
		setServePort(t, st, "1111")
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:1111", "probe-machine.tailnet.ts.net:80"))
		logf, logs := logSink()
		p := New(Options{Store: st, Exe: "pengupool", Logf: logf})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--http=80") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
		if strings.Contains((*ran)[0], "--https=") {
			t.Fatalf("http mode switched to https: %v", *ran)
		}
		if joined := logs(); strings.Contains(joined, "re-point failed") {
			t.Fatalf("failed re-point logged: %q", joined)
		}
		if got := st.Config().Network.ServePort; got != "7787" {
			t.Fatalf("serve port after heal = %q, want 7787", got)
		}
	})
	t.Run("foreignRoute", func(t *testing.T) {
		// Another app's :80 route is not PenguPool's: heal leaves it alone.
		st := healHomeStore(t)
		setServePort(t, st, "1111")
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:2222", "probe-machine.tailnet.ts.net:80"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 0 {
			t.Fatalf("heal overwrote another app's route: %v", *ran)
		}
	})
	t.Run("legacyHTTPUnrecorded", func(t *testing.T) {
		// With no recorded port, only :443 is PenguPool's (as before); :80 is left alone.
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:1111", "probe-machine.tailnet.ts.net:80"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 0 {
			t.Fatalf("heal touched an unrecorded :80 route: %v", *ran)
		}
	})
	t.Run("httpHealthy", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 0 {
			t.Fatalf("healthy http setup re-served: %v", *ran)
		}
	})
	t.Run("noServeConfig", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSON(""))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe(context.Background())
		if len(*ran) != 0 {
			t.Fatalf("heal added serve that was never configured: %v", *ran)
		}
	})
}

// An unbound daemon port is a plain error, never a guessed serve.
func TestSettingsSaveServeUnbound(t *testing.T) {
	oldAddr, oldCmd := HTTPAddr, tailscaleCmd
	defer func() { HTTPAddr, tailscaleCmd = oldAddr, oldCmd }()
	HTTPAddr = func() string { return "" }
	tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Error("tailscale ran with an unbound port")
		return nil, os.ErrNotExist
	}
	p := testPool(t)
	res, err := p.settingsSave(context.Background(), settingsIn{EnableServe: true})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if e, _ := m["serve_error"].(string); !strings.Contains(e, "not bound") {
		t.Fatalf("save = %+v", m)
	}
}

// Fresh setups get HTTPS with certs, else plain HTTP; stale modes keep their mode.
func TestSettingsSaveServeModes(t *testing.T) {
	for _, c := range []struct {
		name, status, serve, wantFlag, servePort string
		wantRuns                                 int
	}{
		{"freshHTTPS", tailscaleStatusRunning, tailscaleServeJSON(""), "--https=443", "", 1},
		{"staleHTTPS", tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"), "--https=443", "", 1},
		{"staleHTTP", tailscaleStatusRunning, tailscaleServeJSONFor("http://127.0.0.1:1111", "probe-machine.tailnet.ts.net:80"), "--http=80", "1111", 1},
		{"healthyHTTPS", tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:7787"), "", "", 0},
		{"healthyHTTP", tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"), "", "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := stubTailscale(t, c.status, c.serve)
			p := testPool(t)
			setServePort(t, p.st, c.servePort)
			res, err := p.settingsSave(context.Background(), settingsIn{EnableServe: true})
			if err != nil {
				t.Fatal(err)
			}
			m := res.(map[string]any)
			if e, _ := m["serve_error"].(string); e != "" {
				t.Fatalf("serve_error = %q", e)
			}
			if len(*ran) != c.wantRuns {
				t.Fatalf("serve runs = %v, want %d", *ran, c.wantRuns)
			}
			if c.wantRuns > 0 && !strings.Contains((*ran)[0], c.wantFlag) {
				t.Fatalf("serve runs = %v, want flag %s", *ran, c.wantFlag)
			}
		})
	}
}

// The checklist accepts Serve in plain-HTTP mode too.
func TestTailscaleChecklistServeHTTP(t *testing.T) {
	p := testPool(t)
	stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"))
	cs := p.tailscaleChecks()
	if got := checkByID(t, cs, "serve"); !got.OK || !strings.Contains(got.Detail, "http://") {
		t.Fatalf("serve: %+v", cs)
	}
	stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:1111", "probe-machine.tailnet.ts.net:80"))
	cs = p.tailscaleChecks()
	if got := checkByID(t, cs, "serve"); got.OK || got.Fix == nil || got.Fix.Action != "serve" {
		t.Fatalf("serve: %+v", cs)
	}
}

// The explicit HTTP mode serves :80 even on a tailnet with certificates, under the serve_http keys.
func TestSettingsSaveServeHTTPExplicit(t *testing.T) {
	ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON(""))
	p := testPool(t)
	res, err := p.settingsSave(context.Background(), settingsIn{EnableServeHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if e, _ := m["serve_http_error"].(string); e != "" {
		t.Fatalf("serve_http_error = %q", e)
	}
	if _, ok := m["serve_http"]; !ok {
		t.Fatalf("no serve_http result: %+v", m)
	}
	if len(*ran) != 1 || !strings.Contains((*ran)[0], "--http=80") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
		t.Fatalf("serve runs = %v", *ran)
	}
	if got := p.st.Config().Network.ServePort; got != "7787" {
		t.Fatalf("serve port = %q, want 7787", got)
	}
}

// Reachable probes plain HTTP only when HTTP alone serves this app.
func TestReachableURL(t *testing.T) {
	const want = "127.0.0.1:7787"
	for _, c := range []struct{ https, http, url string }{
		{want, "", "https://m.ts.net/"},
		{"", want, "http://m.ts.net/"},
		{want, want, "https://m.ts.net/"},
		{"", "", "https://m.ts.net/"},
	} {
		if got := reachableURL("m.ts.net", want, c.https, c.http); got != c.url {
			t.Errorf("reachableURL(%q, %q) = %q, want %q", c.https, c.http, got, c.url)
		}
	}
}

// The pairing URL keeps the scheme saved with the Serve name.
func TestMuseBaseURLScheme(t *testing.T) {
	p := testPool(t)
	for _, c := range []struct{ saved, want string }{
		{"https://m.ts.net", "https://m.ts.net"},
		{"http://m.ts.net", "http://m.ts.net"},
		{"", "https://<tailnet-name>"},
	} {
		if err := p.st.UpdateConfig(func(cfg *store.Config) error { cfg.Network.Tailscale = c.saved; return nil }); err != nil {
			t.Fatal(err)
		}
		if got := p.museBaseURL(); got != c.want {
			t.Errorf("museBaseURL(%q) = %q, want %q", c.saved, got, c.want)
		}
	}
}

// The Serve button never takes over a mode another app's route already uses, and never falls
// back to plain HTTP on a tailnet without certificates: it reports either and runs nothing.
func TestSettingsSaveServeForeignRoute(t *testing.T) {
	for _, c := range []struct {
		name, status, serve, servePort, wantErr string
	}{
		{"freshNoCerts", tailscaleStatusNoCerts, tailscaleServeJSON(""), "", "HTTPS certificates are not enabled"},
		{"foreignHTTPNoCerts", tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:2222", "probe-machine.tailnet.ts.net:80"), "", "HTTPS certificates are not enabled"},
		{"foreignHTTPSRecorded", tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:2222"), "1111", ":443 already points at 127.0.0.1:2222"},
		{"foreignRemoteHTTPS", tailscaleStatusRunning, tailscaleServeJSON("http://192.168.1.5:8080"), "", ":443 already points at http://192.168.1.5:8080"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := stubTailscale(t, c.status, c.serve)
			p := testPool(t)
			setServePort(t, p.st, c.servePort)
			res, err := p.settingsSave(context.Background(), settingsIn{EnableServe: true})
			if err != nil {
				t.Fatal(err)
			}
			m := res.(map[string]any)
			if e, _ := m["serve_error"].(string); !strings.Contains(e, c.wantErr) {
				t.Fatalf("serve_error = %q, want %q", e, c.wantErr)
			}
			if len(*ran) != 0 {
				t.Fatalf("took over another app's route: %v", *ran)
			}
		})
	}
}

// An HTTP click turns PenguPool's own :443 route off (a switch, never both); another app's :80
// route is refused.
func TestSettingsSaveServeHTTPPlan(t *testing.T) {
	t.Run("staleOwnHTTPS", func(t *testing.T) {
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
		p := testPool(t)
		setServePort(t, p.st, "1111")
		res, err := p.settingsSave(context.Background(), settingsIn{EnableServeHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		if e, _ := res.(map[string]any)["serve_http_error"].(string); e != "" {
			t.Fatalf("serve_http_error = %q", e)
		}
		if len(*ran) != 2 || !strings.Contains((*ran)[0], "--http=80") || (*ran)[1] != "serve --https=443 off" {
			t.Fatalf("serve runs = %v, want :80 then PenguPool's :443 off", *ran)
		}
		if got := p.st.Config().Network.ServePort; got != "7787" {
			t.Fatalf("serve port = %q, want 7787", got)
		}
	})
	t.Run("unrecordedLocalHTTPS", func(t *testing.T) {
		// With no recorded port a local :443 route may be another app's: HTTP leaves it alone.
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:2222"))
		p := testPool(t)
		if _, err := p.settingsSave(context.Background(), settingsIn{EnableServeHTTP: true}); err != nil {
			t.Fatal(err)
		}
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--http=80") {
			t.Fatalf("serve runs = %v, want only :80", *ran)
		}
	})
	t.Run("foreignHTTP", func(t *testing.T) {
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSONFor("http://127.0.0.1:2222", "probe-machine.tailnet.ts.net:80"))
		p := testPool(t)
		setServePort(t, p.st, "1111")
		res, err := p.settingsSave(context.Background(), settingsIn{EnableServeHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		if e, _ := res.(map[string]any)["serve_http_error"].(string); !strings.Contains(e, ":80 already points at 127.0.0.1:2222") {
			t.Fatalf("serve_http_error = %q", e)
		}
		if len(*ran) != 0 {
			t.Fatalf("took over another app's :80 route: %v", *ran)
		}
	})
}

// The pairing URL prefers HTTPS when it serves this app, even with http:// saved.
func TestMuseBaseURLPrefersHTTPS(t *testing.T) {
	stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:7787"))
	p := testPool(t)
	if err := p.st.UpdateConfig(func(cfg *store.Config) error { cfg.Network.Tailscale = "http://m.ts.net"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := p.museBaseURL(); got != "https://m.ts.net" {
		t.Fatalf("museBaseURL = %q, want https://m.ts.net", got)
	}
}

// At login tailscaled may come up after the daemon: heal keeps trying, with backoff, until Serve
// status is readable, then re-points the stale route; it gives up after about healBudget.
func TestHealTailscaleServeWaitsForTailscaled(t *testing.T) {
	for _, c := range []struct {
		name     string
		failures int
		wantRuns int
	}{{"upAfterAWhile", 8, 1}, {"neverUp", 1000, 0}} {
		t.Run(c.name, func(t *testing.T) {
			st := healHomeStore(t)
			ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
			stub := tailscaleCmd
			tries := 0
			tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
				if len(args) >= 2 && args[0] == "serve" && args[1] == "status" {
					if tries++; tries <= c.failures {
						return nil, errors.New("tailscaled not running")
					}
				}
				return stub(ctx, bin, args...)
			}
			var slept time.Duration
			sleep := healSleep
			healSleep = func(ctx context.Context, d time.Duration) { slept += d; sleep(ctx, d) }
			logf, logs := logSink()
			p := New(Options{Store: st, Exe: "pengupool", Logf: logf})
			p.HealTailscaleServe(context.Background())
			if len(*ran) != c.wantRuns {
				t.Fatalf("serve runs = %v, want %d", *ran, c.wantRuns)
			}
			if c.wantRuns == 0 {
				if slept > healBudget || slept < healBudget/2 {
					t.Fatalf("waited %s, want about %s", slept, healBudget)
				}
				if !strings.Contains(logs(), "status unreadable") {
					t.Fatalf("no give-up logged: %q", logs())
				}
			}
		})
	}
}

// The Settings buttons switch the mode: each serves its own, then turns off PenguPool's route in the
// other mode, never another app's; HTTPS without certificates keeps HTTP up and its address.
func TestSettingsSaveServeSwitch(t *testing.T) {
	both := func(https, http string) string {
		return `{"Web":{"probe-machine.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"` + https +
			`"}}},"probe-machine.tailnet.ts.net:80":{"Handlers":{"/":{"Proxy":"` + http + `"}}}}}`
	}
	for _, c := range []struct {
		name, status, serve string
		in                  settingsIn
		key                 string
		wantRuns            []string
		wantErr, wantURL    string
	}{
		{"httpsToHTTP", tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:7787"),
			settingsIn{EnableServeHTTP: true}, "serve_http",
			[]string{"serve --bg --http=80 http://127.0.0.1:7787", "serve --https=443 off"}, "", "http://"},
		{"httpToHTTPS", tailscaleStatusRunning, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"),
			settingsIn{EnableServeHTTPS: true}, "serve",
			[]string{"serve --bg --https=443 http://127.0.0.1:7787", "serve --http=80 off"}, "", "https://"},
		{"foreignHTTPSKept", tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:2222"),
			settingsIn{EnableServeHTTP: true}, "serve_http",
			[]string{"serve --bg --http=80 http://127.0.0.1:7787"}, "", "http://"},
		{"foreignHTTPKept", tailscaleStatusRunning, both("http://127.0.0.1:7787", "http://127.0.0.1:2222"),
			settingsIn{EnableServeHTTPS: true}, "serve",
			[]string{"serve --bg --https=443 http://127.0.0.1:7787"}, "", "https://"},
		{"httpsNoCertsKeepsHTTP", tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"),
			settingsIn{EnableServeHTTPS: true}, "serve",
			nil, "HTTPS certificates are not enabled", "http://"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := stubTailscale(t, c.status, c.serve)
			p := testPool(t)
			setServePort(t, p.st, "7787")
			old := "http://probe-machine.tailnet.ts.net"
			if c.in.EnableServeHTTP {
				old = "https://probe-machine.tailnet.ts.net"
			}
			_ = p.st.UpdateConfig(func(cfg *store.Config) error { cfg.Network.Tailscale = old; return nil })
			url := "https://probe-machine.tailnet.ts.net"
			if c.in.EnableServeHTTP {
				url = "http://probe-machine.tailnet.ts.net"
			}
			c.in.Tailscale = &url
			res, err := p.settingsSave(context.Background(), c.in)
			if err != nil {
				t.Fatal(err)
			}
			if e, _ := res.(map[string]any)[c.key+"_error"].(string); (c.wantErr == "") != (e == "") || !strings.Contains(e, c.wantErr) {
				t.Fatalf("%s_error = %q, want %q", c.key, e, c.wantErr)
			}
			if !slices.Equal(*ran, c.wantRuns) {
				t.Fatalf("serve runs = %q, want %q", *ran, c.wantRuns)
			}
			if got := p.st.Config().Network.Tailscale; !strings.HasPrefix(got, c.wantURL) {
				t.Fatalf("tailscale address = %q, want %s…", got, c.wantURL)
			}
		})
	}
}

// A switch probes the new address first: a 502 keeps the old mode up and its address; an answer
// lets the old mode go.
func TestSettingsSaveServeSwitchProbe(t *testing.T) {
	for _, c := range []struct {
		name     string
		http     bool
		serve    string
		probe    error
		wantRuns []string
		wantErr  string
		wantURL  string
	}{
		{"answers", false, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"), nil, []string{"serve --bg --https=443 http://127.0.0.1:7787", "serve --http=80 off"}, "", "https://"},
		{"badGateway", false, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"), errors.New("HTTP 502 Bad Gateway"), []string{"serve --bg --https=443 http://127.0.0.1:7787", "serve --https=443 off"}, "502 Bad Gateway", "http://"},
		// :443 was already PenguPool's: a 502 leaves it as it was rather than turning it off.
		{"badGatewayOwnHTTPS", false, `{"Web":{"probe-machine.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7787"}}},"probe-machine.tailnet.ts.net:80":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7787"}}}}}`,
			errors.New("HTTP 502 Bad Gateway"), []string{"serve --bg --https=443 http://127.0.0.1:7787"}, "502 Bad Gateway", "http://"},
		// The HTTP mirror: :80 was already PenguPool's, so a 502 on an HTTP click leaves it on too.
		{"badGatewayOwnHTTP", true, `{"Web":{"probe-machine.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7787"}}},"probe-machine.tailnet.ts.net:80":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7787"}}}}}`,
			errors.New("HTTP 502 Bad Gateway"), []string{"serve --bg --http=80 http://127.0.0.1:7787"}, "502 Bad Gateway", "https://"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := stubTailscale(t, tailscaleStatusRunning, c.serve)
			var probed []string
			serveProbe = func(_ context.Context, url string) error { probed = append(probed, url); return c.probe }
			p := testPool(t)
			setServePort(t, p.st, "7787")
			_ = p.st.UpdateConfig(func(cfg *store.Config) error {
				cfg.Network.Tailscale = "http://probe-machine.tailnet.ts.net"
				return nil
			})
			url, in, key := "https://probe-machine.tailnet.ts.net", settingsIn{EnableServeHTTPS: true}, "serve"
			if c.http {
				url, in, key = "http://probe-machine.tailnet.ts.net", settingsIn{EnableServeHTTP: true}, "serve_http"
				_ = p.st.UpdateConfig(func(cfg *store.Config) error {
					cfg.Network.Tailscale = "https://probe-machine.tailnet.ts.net"
					return nil
				})
			}
			in.Tailscale = &url
			res, err := p.settingsSave(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(probed, []string{url + "/"}) {
				t.Fatalf("probed %q", probed)
			}
			if e, _ := res.(map[string]any)[key+"_error"].(string); (c.wantErr == "") != (e == "") || !strings.Contains(e, c.wantErr) {
				t.Fatalf("%s_error = %q, want %q", key, e, c.wantErr)
			}
			if !slices.Equal(*ran, c.wantRuns) {
				t.Fatalf("serve runs = %q, want %q", *ran, c.wantRuns)
			}
			if got := p.st.Config().Network.Tailscale; !strings.HasPrefix(got, c.wantURL) {
				t.Fatalf("tailscale address = %q, want %s…", got, c.wantURL)
			}
		})
	}
}

// serveProbe fails on a 502 and passes a page that answers.
func TestServeProbe(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer bad.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	if err := serveProbe(context.Background(), bad.URL); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("502: err = %v", err)
	}
	if err := serveProbe(context.Background(), ok.URL); err != nil {
		t.Fatalf("200: err = %v", err)
	}
}

// fakeServe is a stateful Tailscale Serve: serve --bg sets a route, serve --X off removes it, and
// serve status reports what is set. down makes status fail (tailscaled not up yet).
type fakeServe struct {
	routes map[string]string // "443" or "80" → proxy
	down   int
	onRead func()
}

func (f *fakeServe) install(t *testing.T) {
	stubTailscale(t, tailscaleStatusRunning, "")
	tailscaleCmd = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "serve" && args[1] == "status":
			if f.down > 0 {
				f.down--
				return nil, errors.New("tailscaled not running")
			}
			if f.onRead != nil {
				f.onRead()
			}
			web := []string{}
			for port, proxy := range f.routes {
				web = append(web, `"probe-machine.tailnet.ts.net:`+port+`":{"Handlers":{"/":{"Proxy":"`+proxy+`"}}}`)
			}
			return []byte(`{"Web":{` + strings.Join(web, ",") + `}}`), nil
		case len(args) >= 1 && args[0] == "status":
			return []byte(tailscaleStatusRunning), nil
		case len(args) == 3 && args[0] == "serve" && args[2] == "off":
			delete(f.routes, strings.TrimPrefix(strings.TrimPrefix(args[1], "--https="), "--http="))
			return []byte("ok"), nil
		case len(args) == 4 && args[0] == "serve" && args[1] == "--bg":
			f.routes[strings.TrimPrefix(strings.TrimPrefix(args[2], "--https="), "--http=")] = args[3]
			return []byte("ok"), nil
		}
		return nil, os.ErrNotExist
	}
}

// A Settings switch to HTTP that lands while the heal waits for tailscaled: the heal reads the
// state afresh after the wait and does not bring the stale HTTPS route back.
func TestHealAfterSwitchDuringWait(t *testing.T) {
	st := healHomeStore(t)
	setServePort(t, st, "1111")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:1111"}, down: 3}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	clicked := false
	sleep := healSleep
	healSleep = func(ctx context.Context, d time.Duration) {
		if !clicked && f.down == 0 { // tailscaled is up now; the owner clicks before the heal's next try
			clicked = true
			url := "http://probe-machine.tailnet.ts.net"
			res, err := p.settingsSave(context.Background(), settingsIn{Tailscale: &url, EnableServeHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			if e, _ := res.(map[string]any)["serve_http_error"].(string); e != "" {
				t.Fatalf("switch: %s", e)
			}
		}
		sleep(ctx, d)
	}
	p.HealTailscaleServe(context.Background())
	if !clicked {
		t.Fatal("the switch never ran during the wait")
	}
	if len(f.routes) != 1 || f.routes["80"] != "http://127.0.0.1:7787" {
		t.Fatalf("routes after heal = %v, want only :80 at 7787", f.routes)
	}
}

// The heal holds the Serve lock from its fresh read through its re-point, so a click cannot
// interleave and be undone.
func TestHealHoldsServeLock(t *testing.T) {
	st := healHomeStore(t)
	setServePort(t, st, "1111")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:1111"}}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	reads, lockedOnRead := 0, false
	f.onRead = func() {
		if reads++; reads == 2 { // the read after the wait, under the lock
			if p.serveMu.TryLock() {
				p.serveMu.Unlock()
			} else {
				lockedOnRead = true
			}
		}
	}
	p.HealTailscaleServe(context.Background())
	if !lockedOnRead {
		t.Fatal("the heal re-read Serve without holding the Serve lock")
	}
	if f.routes["443"] != "http://127.0.0.1:7787" {
		t.Fatalf("routes = %v, want :443 re-pointed at 7787", f.routes)
	}
}

// The heal stops when the daemon's context ends, without serving.
func TestHealStopsOnShutdown(t *testing.T) {
	st := healHomeStore(t)
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:1111"}, down: 1000}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	ctx, cancel := context.WithCancel(context.Background())
	tries := 0
	sleep := healSleep
	healSleep = func(c context.Context, d time.Duration) {
		if tries++; tries == 3 {
			cancel()
		}
		sleep(c, d)
	}
	p.HealTailscaleServe(ctx)
	if tries != 3 || f.routes["443"] != "http://127.0.0.1:1111" {
		t.Fatalf("tries = %d, routes = %v: heal went on after shutdown", tries, f.routes)
	}
}

// A page reload mid-switch cancels the click's context: the off (after an answer) and the undo
// (after a 502) still run, so the switch never leaves both modes up.
func TestSettingsSaveServeSwitchCancelled(t *testing.T) {
	for _, c := range []struct {
		name     string
		probe    error
		wantRuns []string
	}{
		{"offAfterAnswer", nil, []string{"serve --bg --https=443 http://127.0.0.1:7787", "serve --http=80 off"}},
		{"undoAfter502", errors.New("HTTP 502 Bad Gateway"), []string{"serve --bg --https=443 http://127.0.0.1:7787", "serve --https=443 off"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"))
			stub := tailscaleCmd
			tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
				if err := ctx.Err(); err != nil {
					return nil, err // a cancelled command does not run
				}
				return stub(ctx, bin, args...)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			serveProbe = func(context.Context, string) error { cancel(); return c.probe } // the reload lands during the probe
			p := testPool(t)
			setServePort(t, p.st, "7787")
			if _, err := p.settingsSave(ctx, settingsIn{EnableServeHTTPS: true}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*ran, c.wantRuns) {
				t.Fatalf("serve runs = %q, want %q", *ran, c.wantRuns)
			}
		})
	}
}

// The app bundle's binary is the app unless told to be the CLI: from the host, which has no TERM,
// it printed "The Tailscale GUI failed to start" and exited 0, and Settings showed the JSON
// decoder's "invalid character 'T'". The fake below acts the same way.
func TestTailscaleStatusRunsTheBundleAsCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "Tailscale")
	script := "#!/bin/sh\n" +
		"if [ -z \"$TAILSCALE_BE_CLI\" ]; then echo 'The Tailscale GUI failed to start: (Tailscale.CLIError error 3.)'; exit 0; fi\n" +
		"echo '" + tailscaleStatusNoCerts + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM", "")
	st, err := runTailscaleStatus(context.Background(), bin)
	if err != nil {
		t.Fatalf("runTailscaleStatus: %v", err)
	}
	if st.BackendState != "Running" {
		t.Fatalf("BackendState = %q, want Running", st.BackendState)
	}
}

// Output that is not JSON reaches the Logged in row in the CLI's own words.
func TestTailscaleStatusNotJSONShowsTheCLIText(t *testing.T) {
	stubTailscale(t, "The Tailscale GUI failed to start\n", tailscaleServeJSON(""))
	p := testPool(t)
	c := checkByID(t, p.tailscaleChecks(), "login")
	if c.OK || c.Detail != "The Tailscale GUI failed to start" {
		t.Fatalf("login check = %+v, want the CLI's text", c)
	}
}

// watchStore is a home store whose saved Serve name is the stub's own tailnet name, served by
// PenguPool at port 1111 last time.
func watchStore(t *testing.T, saved string) *store.Store {
	t.Helper()
	st := healHomeStore(t)
	if err := st.UpdateConfig(func(c *store.Config) error { c.Network.Tailscale = saved; return nil }); err != nil {
		t.Fatal(err)
	}
	setServePort(t, st, "1111")
	return st
}

// A route left stale after start-up (tailscaled restarted onto the old port) is re-pointed on the
// watchdog's next round, with no click; a route that is gone stays gone.
func TestServeWatchRepointsAStaleRoute(t *testing.T) {
	st := watchStore(t, "https://probe-machine.tailnet.ts.net")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:1111"}}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	p.serveWatchRound(context.Background())
	if f.routes["443"] != "http://127.0.0.1:7787" || !p.serveWatch.Repointed {
		t.Fatalf("routes = %v, watch = %+v: want :443 re-pointed at 7787", f.routes, p.serveWatch)
	}
	f.routes = map[string]string{}
	p.serveWatchRound(context.Background())
	if len(f.routes) != 0 || p.serveWatch.Repointed {
		t.Fatalf("routes = %v: the watchdog served a route the owner may have turned off", f.routes)
	}
}

// Another app's route is never touched, and does not make the name follow a rename.
func TestServeWatchLeavesAnotherAppsRoute(t *testing.T) {
	st := watchStore(t, "https://old-name.tailnet.ts.net")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:9999"}}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	p.serveWatchRound(context.Background())
	if f.routes["443"] != "http://127.0.0.1:9999" || len(f.routes) != 1 {
		t.Fatalf("routes = %v: another app's route changed", f.routes)
	}
	if got := st.Config().Network.Tailscale; got != "https://old-name.tailnet.ts.net" {
		t.Fatalf("saved name = %q: renamed with no PenguPool route", got)
	}
}

// A store from before the ServePort record whose route already points here records it on the first
// round, so a route the owner later points at another local app is left alone.
func TestServeWatchRecordsALegacyRoute(t *testing.T) {
	st := watchStore(t, "https://probe-machine.tailnet.ts.net")
	setServePort(t, st, "")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:7787"}}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	p.serveWatchRound(context.Background())
	if got := st.Config().Network.ServePort; got != "7787" {
		t.Fatalf("ServePort = %q, want 7787 recorded", got)
	}
	f.routes["443"] = "http://127.0.0.1:3000"
	p.serveWatchRound(context.Background())
	if f.routes["443"] != "http://127.0.0.1:3000" {
		t.Fatalf("routes = %v: another app's route was taken back", f.routes)
	}
}

// A Settings switch to HTTP made during a round wins: the round re-reads Serve under the lock.
func TestServeWatchAfterSwitchDuringRound(t *testing.T) {
	st := watchStore(t, "https://probe-machine.tailnet.ts.net")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:1111"}}
	f.install(t)
	p := New(Options{Store: st, Exe: "pengupool"})
	run, clicked := tailscaleCmd, false
	tailscaleCmd = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "status" && !clicked { // the round has read tailscaled; the owner clicks
			clicked = true
			url := "http://probe-machine.tailnet.ts.net"
			if res, err := p.settingsSave(context.Background(), settingsIn{Tailscale: &url, EnableServeHTTP: true}); err != nil {
				t.Fatal(err)
			} else if e, _ := res.(map[string]any)["serve_http_error"].(string); e != "" {
				t.Fatalf("switch: %s", e)
			}
		}
		return run(ctx, bin, args...)
	}
	p.serveWatchRound(context.Background())
	if !clicked || len(f.routes) != 1 || f.routes["80"] != "http://127.0.0.1:7787" {
		t.Fatalf("clicked = %v, routes = %v: want only :80 at 7787", clicked, f.routes)
	}
	if got := st.Config().Network.Tailscale; got != "http://probe-machine.tailnet.ts.net" {
		t.Fatalf("saved = %q: the round undid the switch's scheme", got)
	}
}

// After a machine or tailnet rename a saved MagicDNS name follows, keeping its scheme; a saved short
// name or tailnet IP is the owner's choice and stays.
func TestServeWatchFollowsARename(t *testing.T) {
	for _, scheme := range []string{"https://", "http://"} {
		st := watchStore(t, scheme+"old-name.tailnet.ts.net")
		f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:7787"}}
		f.install(t)
		p := New(Options{Store: st, Exe: "pengupool"})
		p.serveWatchRound(context.Background())
		if got := st.Config().Network.Tailscale; got != scheme+"probe-machine.tailnet.ts.net" || p.serveWatch.RenamedTo == "" {
			t.Fatalf("saved = %q, watch = %+v", got, p.serveWatch)
		}
	}
	for _, saved := range []string{"https://probe-machine", "http://100.101.102.103"} {
		st := watchStore(t, saved)
		f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:7787"}}
		f.install(t)
		p := New(Options{Store: st, Exe: "pengupool"})
		p.serveWatchRound(context.Background())
		if got := st.Config().Network.Tailscale; got != saved {
			t.Fatalf("saved %q became %q", saved, got)
		}
	}
}

// Nothing happens with no Serve name saved, or on a store that isn't the owner's home; a stopped
// tailscaled is recorded for the panel and nothing is served.
func TestServeWatchGuards(t *testing.T) {
	st := watchStore(t, "")
	ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
	p := New(Options{Store: st, Exe: "pengupool"})
	p.serveWatchRound(context.Background())
	if len(*ran) != 0 || !p.serveWatch.At.IsZero() {
		t.Fatalf("ran %v with no Serve name saved", *ran)
	}

	st = watchStore(t, "https://probe-machine.tailnet.ts.net")
	ran = stubTailscale(t, tailscaleStatusStopped, tailscaleServeJSON("http://127.0.0.1:1111"))
	p = New(Options{Store: st, Exe: "pengupool"})
	p.serveWatchRound(context.Background())
	if len(*ran) != 0 || p.serveWatch.Problem != "Tailscale isn't running" {
		t.Fatalf("ran %v, watch %+v", *ran, p.serveWatch)
	}

	t.Setenv("PENGUPOOL_HOME", t.TempDir()) // a throwaway store: the watchdog never starts
	done := make(chan struct{})
	go func() { p.WatchTailscaleServe(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog ran on a throwaway store")
	}
}

// settings.serveCheck runs a round at once; the watchdog stops when its context ends.
func TestServeWatchCheckNowAndStop(t *testing.T) {
	st := watchStore(t, "https://probe-machine.tailnet.ts.net")
	f := &fakeServe{routes: map[string]string{"443": "http://127.0.0.1:1111"}}
	f.install(t)
	oldAfter := serveWatchAfter
	serveWatchAfter = func(time.Duration) <-chan time.Time { return nil } // only a kick wakes it
	t.Cleanup(func() { serveWatchAfter = oldAfter })
	p := New(Options{Store: st, Exe: "pengupool"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.WatchTailscaleServe(ctx); close(done) }()
	if _, err := p.RPC(context.Background(), gateway.Caller{Owner: true}, "settings.serveCheck", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		p.serveWatchMu.Lock()
		at := p.serveWatch.At
		p.serveWatchMu.Unlock()
		if !at.IsZero() {
			break
		}
		if i > 500 {
			t.Fatal("settings.serveCheck did not run a round")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog did not stop with its context")
	}
}
