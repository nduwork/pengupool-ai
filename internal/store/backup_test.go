package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenRefusesEmptyButValidJSON is review finding N2: `null` and `{}` parse, would silently
// become an empty pool, and the next write would overwrite the user's file.
func TestOpenRefusesEmptyButValidJSON(t *testing.T) {
	for name, body := range map[string]string{
		"null":          "null\n",
		"empty object":  "{}\n",
		"no node id":    `{"version":1,"rev":3}`,
		"newer version": `{"version":9,"rev":3,"node_id":"0199a5f1-0000-7000-8000-000000000001"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, StateFile)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			var bad *ErrCorrupt
			if _, err := Open(dir); !errors.As(err, &bad) {
				t.Fatalf("got %v, want *ErrCorrupt", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != body {
				t.Fatalf("Open rewrote the file: %v %q", err, after)
			}
		})
	}
}

func TestOpenRefusesNewerConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`{"version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var bad *ErrCorrupt
	if _, err := Open(dir); !errors.As(err, &bad) {
		t.Fatalf("got %v, want *ErrCorrupt", err)
	}
}

// TestBackupSurvivesWithoutAnFsyncGap keeps N2's other half honest: the backup is written through a
// temporary file, fsynced and renamed, so a crash cannot leave a truncated .bak.
func TestBackupSurvivesWithoutAnFsyncGap(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := st.Update(func(s *State) error {
			s.Rev = uint64(i + 1)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, BackupFile+TempSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the backup's temporary file must not survive")
	}
	bak, err := os.ReadFile(filepath.Join(dir, BackupFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(bak) == 0 {
		t.Fatal("the backup is empty")
	}
}
