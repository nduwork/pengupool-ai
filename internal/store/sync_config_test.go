package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A config.json written before the rename holds the settings under "backup": they are read as the
// sync's, and the next save writes both, for one release. An existing "sync" wins over a stale "backup".
func TestConfigReadsTheOldBackupKey(t *testing.T) {
	dir := t.TempDir()
	old := `{"version":` + strconv.Itoa(Version) + `,"backup":{"github":true,"interval_min":10,"repo":"me/pengupool-memory"}}`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Sync{GitHub: true, IntervalMin: 10, Repo: "me/pengupool-memory"}
	if got := st.Config().Sync; got != want {
		t.Fatalf("sync = %+v, want %+v", got, want)
	}
	if err := st.SaveConfig(st.Config()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ConfigFile))
	if strings.Count(string(b), `"repo": "me/pengupool-memory"`) != 2 || !strings.Contains(string(b), `"backup"`) || !strings.Contains(string(b), `"sync"`) {
		t.Fatalf("the saved config holds the settings under both keys:\n%s", b)
	}

	both := `{"version":` + strconv.Itoa(Version) + `,"sync":{"github":true,"repo":"me/new"},"backup":{"github":false,"repo":"me/old"}}`
	_ = os.WriteFile(filepath.Join(dir, ConfigFile), []byte(both), 0o600)
	if st, err = Open(dir); err != nil || st.Config().Sync.Repo != "me/new" {
		t.Fatalf("both keys: %+v %v", st.Config().Sync, err)
	}
}
