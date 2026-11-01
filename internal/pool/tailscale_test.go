package pool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	t.Logf("DBG PATH=%q lookpath=%v", os.Getenv("PATH"), func() string { p, _ := tailscaleLookPath("tailscale"); return p }())
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
	t.Cleanup(func() { tailscaleLookPath, tailscaleCmd, HTTPAddr = oldLook, oldCmd, oldAddr })
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
func healHomeStore(t *testing.T) *store.Store {
	t.Helper()
	home := t.TempDir()
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
		withHome(t, t.TempDir())
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
		p.HealTailscaleServe()
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--https=443") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
	})
	t.Run("healthy", func(t *testing.T) {
		withHome(t, t.TempDir())
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
		p.HealTailscaleServe()
		if len(*ran) != 0 {
			t.Fatalf("healthy setup re-served: %v", *ran)
		}
	})
	t.Run("throwaway", func(t *testing.T) {
		withHome(t, t.TempDir())
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
		p.HealTailscaleServe()
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
		p.HealTailscaleServe()
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--https=443") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
	})
	t.Run("neverBound", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusRunning, tailscaleServeJSON("http://127.0.0.1:1111"))
		HTTPAddr = func() string { return "" }
		var mu sync.Mutex
		var logs []string
		p := New(Options{Store: st, Exe: "pengupool", Logf: func(f string, a ...any) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, fmt.Sprintf(f, a...))
		}})
		p.HealTailscaleServe()
		if len(*ran) != 0 {
			t.Fatalf("unbound heal served: %v", *ran)
		}
		mu.Lock()
		defer mu.Unlock()
		if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "not bound") {
			t.Fatalf("no skip logged: %q", joined)
		}
	})
	t.Run("httpStale", func(t *testing.T) {
		st := healHomeStore(t)
		setServePort(t, st, "1111")
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:1111", "probe-machine.tailnet.ts.net:80"))
		var logs []string
		p := New(Options{Store: st, Exe: "pengupool", Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
		p.HealTailscaleServe()
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "--http=80") || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
		if strings.Contains((*ran)[0], "--https=") {
			t.Fatalf("http mode switched to https: %v", *ran)
		}
		if joined := strings.Join(logs, "\n"); strings.Contains(joined, "re-point failed") {
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
		p.HealTailscaleServe()
		if len(*ran) != 0 {
			t.Fatalf("heal overwrote another app's route: %v", *ran)
		}
	})
	t.Run("legacyHTTPUnrecorded", func(t *testing.T) {
		// With no recorded port, only :443 is PenguPool's (as before); :80 is left alone.
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:1111", "probe-machine.tailnet.ts.net:80"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe()
		if len(*ran) != 0 {
			t.Fatalf("heal touched an unrecorded :80 route: %v", *ran)
		}
	})
	t.Run("httpHealthy", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSONFor("http://127.0.0.1:7787", "probe-machine.tailnet.ts.net:80"))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe()
		if len(*ran) != 0 {
			t.Fatalf("healthy http setup re-served: %v", *ran)
		}
	})
	t.Run("noServeConfig", func(t *testing.T) {
		st := healHomeStore(t)
		ran := stubTailscale(t, tailscaleStatusNoCerts, tailscaleServeJSON(""))
		p := New(Options{Store: st, Exe: "pengupool"})
		p.HealTailscaleServe()
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
		{"freshHTTP", tailscaleStatusNoCerts, tailscaleServeJSON(""), "--http=80", "", 1},
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
