package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Opening a store made private only on creation would leave an older 0755 store and a 0644
// daemon.log readable: Open tightens them to the owner.
func TestOpenTightensExistingStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "v2")
	if err := os.MkdirAll(filepath.Join(dir, LogsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0o755)
	log := filepath.Join(dir, LogsDir, "daemon.log")
	if err := os.WriteFile(log, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, LogsDir): 0o700, filepath.Join(dir, RunDir): 0o700, log: 0o600} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s is %o, want %o", p, got, want)
		}
	}
}
