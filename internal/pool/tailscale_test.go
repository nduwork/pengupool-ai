package pool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if proxy == "" {
		return `{"Web":{}}`
	}
	return `{"Web":{"probe-machine.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"` + proxy + `"}}}}}`
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
	oldLook, oldCmd := tailscaleLookPath, tailscaleCmd
	defer func() { tailscaleLookPath, tailscaleCmd = oldLook, oldCmd }()
	tailscaleLookPath = func(string) (string, error) { return "/bin/tailscale", nil }
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

// Self-heal re-serves a stale target once, and only for the owner's home.
func TestHealTailscaleServe(t *testing.T) {
	withHome := func(t *testing.T, home string) {
		t.Helper()
		t.Setenv("HOME", home)
		withUnsetEnv(t, "PENGUPOOL_HOME")
	}
	t.Run("stale", func(t *testing.T) {
		withHome(t, t.TempDir())
		st, err := store.Open(store.DefaultDir())
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
		if len(*ran) != 1 || !strings.Contains((*ran)[0], "127.0.0.1:7787") {
			t.Fatalf("serve runs: %v", *ran)
		}
	})
	t.Run("healthy", func(t *testing.T) {
		withHome(t, t.TempDir())
		st, err := store.Open(store.DefaultDir())
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
}
