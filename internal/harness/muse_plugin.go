package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// The PenguPool Muse plugin: hooks that call `pengupool hook` with each
// event, so a Muse session gets the team context, the Compass and the
// journal like the other harnesses. Muse scrubs the hook environment, so
// the hook command carries --store and the harness baked in; the pool
// resolves the session from the payload's Muse session id (see
// pool.resolveMuseSession). A plugin the owner runs outside PenguPool
// resolves to nothing and is a silent no-op.

// musePluginID is the plugin's stable id in the muse home.
const musePluginID = "pengupool"

// museHookEvents are the plugin hooks, in pool event order.
var museHookEvents = []struct{ id, event string }{
	{"session-start", "SessionStart"},
	{"user-prompt-submit", "UserPromptSubmit"},
	{"pre-tool-use", "PreToolUse"},
	{"post-tool-use", "PostToolUse"},
	{"stop", "Stop"},
	{"session-end", "SessionEnd"},
	{"notification", "Notification"},
	{"permission-request", "PermissionRequest"},
}

// musePluginDir is the bundle under the store the daemon installs from.
func musePluginDir(storeDir string) string {
	return filepath.Join(storeDir, "harness", "muse-plugin")
}

// writeMusePlugin writes the bundle with the hook command baked in and
// returns the manifest's sha: the install reruns only when it changes.
func writeMusePlugin(storeDir, exe string) (string, error) {
	hooks := make([]map[string]any, 0, len(museHookEvents))
	for _, h := range museHookEvents {
		hooks = append(hooks, map[string]any{
			"id": h.id, "event": h.event,
			"command": []string{exe, "hook", "--store", storeDir, Muse, h.event},
		})
	}
	manifest := map[string]any{
		"schemaVersion": 1, "name": musePluginID, "version": "1",
		"description":  "PenguPool session status and team context",
		"compat":       map[string]any{"manifestDir": ".muse-plugin"},
		"capabilities": map[string]any{"hooks": hooks},
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	b = append(b, '\n')
	dir := filepath.Join(musePluginDir(storeDir), ".muse-plugin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if _, err := writeFile(filepath.Join(dir, "plugin.json"), b); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// museLookPath and museRun are exec entry points, stubbed in tests so no
// test ever touches the owner's muse home.
var museLookPath = exec.LookPath

var museRun = func(ctx context.Context, args ...string) error {
	if out, err := exec.CommandContext(ctx, Muse, args...).CombinedOutput(); err != nil {
		line := string(out)
		if i := bytes.IndexByte(out, '\n'); i >= 0 {
			line = string(out[:i])
		}
		return fmt.Errorf("muse %s: %v: %s", args[0], err, line)
	}
	return nil
}

// EnsureMusePlugin installs and approves the PenguPool plugin in the
// user's muse home, once per bundle hash, and only for the owner's real
// home (store.IsHomeDefault): any other store logs one line and installs
// nothing, so a throwaway daemon never touches the owner's muse home.
// One log line when it installs; silence otherwise.
func EnsureMusePlugin(storeDir, exe string, logf func(string, ...any)) {
	if !store.IsHomeDefault(storeDir) {
		logf("muse plugin: skipped (not the owner's home)")
		return
	}
	sum, err := writeMusePlugin(storeDir, exe)
	if err != nil {
		logf("muse plugin: %v", err)
		return
	}
	marker := filepath.Join(musePluginDir(storeDir), ".installed-sha")
	if old, err := os.ReadFile(marker); err == nil && string(old) == sum {
		return // installed and approved for this bundle
	}
	if _, err := museLookPath(Muse); err != nil {
		return // muse is not here: nothing to wire
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := museRun(ctx, "plugins", "install", musePluginDir(storeDir), "--json"); err != nil {
		logf("muse plugin: %v", err)
		return
	}
	if err := museRun(ctx, "plugins", "approve", musePluginID, "--json"); err != nil {
		logf("muse plugin: %v", err)
		return
	}
	_ = os.WriteFile(marker, []byte(sum), 0o600)
	logf("muse plugin: installed and approved")
}
