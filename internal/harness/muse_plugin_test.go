package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMuseSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "muse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := Params{SessionID: "s1", Name: "n", Cwd: "/repo", Model: "m1", Effort: "low", Exe: "/x/pengupool", StoreDir: dir, Env: []string{"PATH=" + dir}}
	cmd, env, err := Spec(Muse, p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "muse"), "--workspace", "/repo", "--trust-workspace", "--model", "m1", "--reasoning-effort", "low"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("muse spec: %v", cmd)
	}
	if !strings.Contains(strings.Join(env, " "), "PENGUPOOL_HARNESS=muse") {
		t.Fatal("harness env missing")
	}
	p.Resume = "ref1"
	cmd, _, err = Spec(Muse, p)
	if err != nil || !slices.Equal(cmd, []string{filepath.Join(dir, "muse"), "resume", "ref1", "--trust-workspace"}) {
		t.Fatalf("muse resume: %v %v", cmd, err)
	}
	if _, ok := Find(Muse); !ok {
		t.Fatal("muse not in Agents")
	}
}

func TestMusePluginBakesHookIdentity(t *testing.T) {
	dir := t.TempDir()
	sum, err := writeMusePlugin(dir, "/x/pengupool")
	if err != nil {
		t.Fatal(err)
	}
	if sum == "" {
		t.Fatal("empty bundle hash")
	}
	b, err := os.ReadFile(filepath.Join(musePluginDir(dir), ".muse-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Name         string `json:"name"`
		Capabilities struct {
			Hooks []struct {
				ID      string   `json:"id"`
				Event   string   `json:"event"`
				Command []string `json:"command"`
			} `json:"hooks"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Name != "pengupool" || len(raw.Capabilities.Hooks) != len(museHookEvents) {
		t.Fatalf("manifest: %+v", raw.Name)
	}
	for i, h := range raw.Capabilities.Hooks {
		want := []string{"/x/pengupool", "hook", "--store", dir, "muse", cmp.Or(museHookEvents[i].pool, h.Event)}
		if !slices.Equal(h.Command, want) {
			t.Fatalf("hook %s: %v", h.ID, h.Command)
		}
	}
	// Deterministic: same inputs, same hash.
	sum2, err := writeMusePlugin(dir, "/x/pengupool")
	if err != nil || sum2 != sum {
		t.Fatalf("rewrite changed the hash: %q vs %q (%v)", sum, sum2, err)
	}
}

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

// A non-home store never reaches the installer: the exec entry points
// stay untouched, so no test can install into the owner's muse home.
func TestEnsureMusePluginSkipsTempStore(t *testing.T) {
	oldLook, oldRun := museLookPath, museRun
	museLookPath = func([]string) (string, error) { t.Error("lookpath called for a temp store"); return "", nil }
	museRun = func(_ context.Context, _ string, _ ...string) error {
		t.Error("installer ran for a temp store")
		return nil
	}
	defer func() { museLookPath, museRun = oldLook, oldRun }()
	var logged []string
	dir := t.TempDir()
	EnsureMusePlugin(dir, "/x/pengupool", nil, func(f string, a ...any) { logged = append(logged, f) })
	if len(logged) != 1 || !strings.Contains(logged[0], "skipped (not the owner's home)") {
		t.Fatalf("log = %q", logged)
	}
	if _, err := os.Stat(filepath.Join(dir, "harness")); !os.IsNotExist(err) {
		t.Fatal("bundle written for a skipped store")
	}
}

// The installer gate is the owner's real home, not DefaultDir: with
// PENGUPOOL_HOME set, even a store equal to DefaultDir installs nothing;

// The plugin installs once per bundle and Muse version: a Muse upgraded since gets it again.
func TestEnsureMusePluginReinstallsForANewMuse(t *testing.T) {
	oldLook, oldRun, oldVer := museLookPath, museRun, museVersion
	defer func() { museLookPath, museRun, museVersion = oldLook, oldRun, oldVer }()
	installs, ver := 0, "Muse Code 1.4.14"
	museLookPath = func([]string) (string, error) { return "/x/muse", nil }
	museVersion = func(string) string { return ver }
	museRun = func(_ context.Context, _ string, args ...string) error {
		if args[1] == "install" {
			installs++
		}
		return nil
	}
	dir := t.TempDir()
	logf := func(string, ...any) {}
	ensureMusePlugin(dir, "/x/pengupool", nil, logf)
	ensureMusePlugin(dir, "/x/pengupool", nil, logf) // a launch: already installed
	if installs != 1 {
		t.Fatalf("%d installs for one Muse", installs)
	}
	ver = "Muse Code 1.5.0"
	ensureMusePlugin(dir, "/x/pengupool", nil, logf)
	if installs != 2 {
		t.Fatal("an upgraded Muse did not get the plugin")
	}
}
