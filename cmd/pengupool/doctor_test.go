package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMuseVerdict(t *testing.T) {
	exe, _ := os.Executable()
	list := func(enabled bool, cmd ...string) []byte {
		q := make([]string, len(cmd))
		for i, c := range cmd {
			q[i] = `"` + c + `"`
		}
		e := "false"
		if enabled {
			e = "true"
		}
		return []byte(`{"plugins":[{"record":{"id":"pengupool","enabled":` + e + `},"active":` + e +
			`,"plugin":{"capabilities":{"hooks":[{"command":[` + strings.Join(q, ",") + `]}]}}}]}`)
	}
	for _, c := range []struct {
		name, want string
		json       []byte
		warn       bool
	}{
		{"good", "installed and active", list(true, exe, "hook", "--store", "/s", "muse", "Stop"), false},
		{"off", "not active", list(false, exe, "hook", "--store", "/s"), true},
		{"gone", "which is gone", list(true, "/tmp/pengu-h2-gone", "hook", "--store", "/s"), true},
		{"other store", "another store", list(true, exe, "hook", "--store", "/tmp/h2store"), true},
		{"missing", "not installed", []byte(`{"plugins":[]}`), true},
	} {
		got := museVerdict(c.json, "/s", true)
		if got.Warn != c.warn || !strings.Contains(got.Detail, c.want) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := museVerdict([]byte(`{"plugins":[]}`), "/s", false); got.Warn {
		t.Errorf("a throwaway store warns: %+v", got)
	}
}

func TestHostVerdict(t *testing.T) {
	dir, def := "/u/.pengupool-ai/v2", "/u/.pengupool-ai/v2"
	ps := strings.Join([]string{
		"  51139 /Volumes/PenguPool-AI 4/PenguPool AI.app/Contents/Resources/bin/pengupool daemon --store " + dir,
		"  600 /Applications/PenguPool AI.app/Contents/Resources/bin/pengupool daemon",
		"  700 /x/pengupool daemon --store /tmp/other",
		"  800 /x/pengupool daemonize --store " + dir,
		"  900 /x/pengupool hook --store " + dir + " muse Stop",
	}, "\n")
	var all []string
	for _, c := range hostVerdict(ps, dir, def, 51139, false) {
		if !c.Warn {
			t.Errorf("not a warning: %+v", c)
		}
		all = append(all, c.Detail)
	}
	got := strings.Join(all, "\n")
	for _, want := range []string{"2 hosts", "pids 51139, 600", "pid 51139 runs from a disk image", "does not answer; `kill -9 51139`"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if cs := hostVerdict("  600 /Applications/PenguPool AI.app/Contents/Resources/bin/pengupool daemon", dir, def, 600, true); len(cs) != 1 || cs[0].Warn {
		t.Errorf("a healthy host: %+v", cs)
	}
	if cs := hostVerdict("", dir, def, 0, false); len(cs) != 1 || cs[0].Warn {
		t.Errorf("no host: %+v", cs)
	}
}

func TestMCPCheck(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".config", "muse"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"ok":{"command":"x"}},"projects":{"/r":{"mcpServers":{"pengu-compass":{"command":"pengupool"}}}}}`), 0o600)
	os.WriteFile(filepath.Join(home, ".config", "muse", "settings.json"), []byte(`{"mcpServers":{"c":{"args":["/a/pengupool","mcp"]}}}`), 0o600)
	c := mcpCheck(home)
	if !c.Warn || !strings.Contains(c.Detail, `"pengu-compass"`) || !strings.Contains(c.Detail, `"c" in`) || strings.Contains(c.Detail, `"ok"`) {
		t.Errorf("%+v", c)
	}
	if c := mcpCheck(t.TempDir()); c.Warn {
		t.Errorf("empty home warns: %+v", c)
	}
}
