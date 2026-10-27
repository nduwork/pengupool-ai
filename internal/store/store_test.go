package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestOpenCreatesDirectoriesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{RunDir, LogsDir, BackupsDir} {
		if fi, err := os.Stat(filepath.Join(dir, d)); err != nil || !fi.IsDir() {
			t.Fatalf("%s: %v", d, err)
		}
	}
	s := st.State()
	if s.Version != Version || s.Rev != 1 || s.NodeID == "" {
		t.Fatalf("unexpected default state %+v", s)
	}
	if len(s.Nodes) != 1 || s.Nodes[0].Kind != "local" {
		t.Fatalf("expected one local node, got %+v", s.Nodes)
	}
	if got := st.Config().Listener; got.Kind != "loopback" {
		t.Fatalf("a fresh store must listen on loopback only, got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, StateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Open must not write state.json before there is state to write")
	}
}

func TestUpdatePersistsAndBumpsRev(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := st.State()
	sess := Session{ID: "s1", Name: "alpha", NodeID: first.NodeID, Cwd: "/tmp", State: "running", Cols: 80, Rows: 24}
	if err := st.Update(func(s *State) error {
		s.Sessions = append(s.Sessions, sess)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.State().Rev; got != first.Rev+1 {
		t.Fatalf("rev is %d, want %d", got, first.Rev+1)
	}
	// The first update has no previous revision to keep; the second one must leave one behind.
	if err := st.Update(func(s *State) error {
		s.Sessions[0].Name = "alpha-2"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.State()
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "s1" || got.Sessions[0].State != "running" {
		t.Fatalf("session did not survive reopen: %+v", got.Sessions)
	}
	bak, err := os.ReadFile(filepath.Join(dir, BackupFile))
	if err != nil {
		t.Fatalf("previous revision must be kept as a backup: %v", err)
	}
	var prev State
	if err := json.Unmarshal(bak, &prev); err != nil {
		t.Fatalf("backup is not valid state: %v", err)
	}
	if prev.Rev != first.Rev+1 || prev.Sessions[0].Name != "alpha" {
		t.Fatalf("backup holds rev %d, want the previous revision %d: %+v", prev.Rev, first.Rev+1, prev.Sessions)
	}
	if _, err := os.Stat(filepath.Join(dir, StateFile+TempSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the temporary file must not survive a successful write")
	}
}

func TestUpdateRollsBackOnError(t *testing.T) {
	st := openTemp(t)
	before := st.State()
	sentinel := errors.New("nope")
	if err := st.Update(func(s *State) error { s.Sessions = append(s.Sessions, Session{ID: "x"}); return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the caller's error", err)
	}
	if got := st.State(); got.Rev != before.Rev || len(got.Sessions) != 0 {
		t.Fatalf("state changed after a failed update: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), StateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed update must not write the state file")
	}
}

func TestCloneIsDeep(t *testing.T) {
	code := 3
	st := openTemp(t)
	if err := st.Update(func(s *State) error {
		s.Sessions = append(s.Sessions, Session{ID: "s1", Command: []string{"claude"}, ExitCode: &code})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap := st.State()
	snap.Sessions[0].Command[0] = "mutated"
	*snap.Sessions[0].ExitCode = 99
	if got := st.State().Sessions[0]; got.Command[0] != "claude" || *got.ExitCode != 3 {
		t.Fatalf("callers can mutate store state through a returned copy: %+v", got)
	}
}

// TestCorruptStateStopsChanges is the plan's state-file gate: a corrupt state.json must stop
// changes and name its path, never be replaced by an empty file.
func TestCorruptStateStopsChanges(t *testing.T) {
	dir := t.TempDir()
	corrupt := []byte(`{"version":1,"rev":4,"sessions":[{"id":`)
	if err := os.WriteFile(filepath.Join(dir, StateFile), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir)
	var bad *ErrCorrupt
	if !errors.As(err, &bad) {
		t.Fatalf("got %v, want *ErrCorrupt", err)
	}
	if bad.Path != filepath.Join(dir, StateFile) {
		t.Fatalf("error names %q, want %q", bad.Path, filepath.Join(dir, StateFile))
	}
	after, readErr := os.ReadFile(filepath.Join(dir, StateFile))
	if readErr != nil || string(after) != string(corrupt) {
		t.Fatalf("Open modified the corrupt file: %v %q", readErr, after)
	}
}

func TestCorruptConfigStopsChanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var bad *ErrCorrupt
	if _, err := Open(dir); !errors.As(err, &bad) {
		t.Fatalf("got %v, want *ErrCorrupt", err)
	}
}

// TestInterruptedWriteKeepsOldState simulates a host crash between writing state.json.tmp and the
// rename: the old file must still load and the abandoned temporary file must not be read.
func TestInterruptedWriteKeepsOldState(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *State) error {
		s.Sessions = append(s.Sessions, Session{ID: "kept", State: "running"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFile+TempSuffix), []byte(`{"version":1,"rev":`), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.State()
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "kept" {
		t.Fatalf("state did not survive an interrupted write: %+v", got)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, StateFile)); string(now) != string(good) {
		t.Fatal("Open rewrote the state file")
	}
}

func TestSaveConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.Config()
	cfg.Listener = Listener{Kind: "lan", Addr: "0.0.0.0:7787"}
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Config().Listener; got != cfg.Listener {
		t.Fatalf("got %+v, want %+v", got, cfg.Listener)
	}
}

func TestViewTimestampsAreUTCRFC3339(t *testing.T) {
	st := openTemp(t)
	if err := st.Update(func(s *State) error {
		s.Sessions = append(s.Sessions, Session{ID: "s1", State: "running", StartedAt: mustParse(t, "2026-10-13T18:00:00+02:00")})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := st.State().Sessions[0].View()
	if v.StartedAt != "2026-10-13T16:00:00Z" {
		t.Fatalf("got %q, want 2026-10-13T16:00:00Z", v.StartedAt)
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestStateFileIsValidJSON(t *testing.T) {
	st := openTemp(t)
	if err := st.Update(func(s *State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(st.Dir(), StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("state.json is not plain JSON: %v", err)
	}
	if _, ok := decoded["rev"]; !ok {
		t.Fatal("state.json has no revision")
	}
}

// A helper an earlier version kept retired is gone at load; the live ones stay.
func TestRetiredHelpersDropAtLoad(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *State) error {
		s.Domain.Skills = []protocol.Skill{{ID: "live", Name: "a"}, {ID: "old", Name: "b"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, StateFile))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["domain"].(map[string]any)["skills"].([]any)[1].(map[string]any)["archived"] = true
	raw, _ = json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, StateFile), raw, 0o600)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sk := st.State().Domain.Skills; len(sk) != 1 || sk[0].ID != "live" {
		t.Fatalf("skills = %+v", sk)
	}
}

// A whole-config save from an older copy never brings a revoked Muse device back.
func TestSaveConfigKeepsMuseDevices(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateConfig(func(c *Config) error {
		c.MuseDevices = []MuseDevice{{Name: "phone1", TokenHash: "h"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := st.Config()
	if err := st.UpdateConfig(func(c *Config) error { c.MuseDevices = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConfig(stale); err != nil {
		t.Fatal(err)
	}
	if d := st.Config().MuseDevices; len(d) != 0 {
		t.Fatalf("revoked device came back: %+v", d)
	}
}
