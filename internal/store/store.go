package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// ErrCorrupt reports a state.json that could not be parsed. It carries the file path and is
// returned from Open, so no caller can proceed and overwrite the user's file.
type ErrCorrupt struct {
	Path string
	Err  error
}

func (e *ErrCorrupt) Error() string {
	return fmt.Sprintf("state file %s is corrupt (%v); fix or move it before starting PenguPool", e.Path, e.Err)
}

func (e *ErrCorrupt) Unwrap() error { return e.Err }

// DefaultDir is the store directory with no override: $PENGUPOOL_HOME/v2,
// else ~/.pengupool-ai/v2.
func DefaultDir() string {
	if home := os.Getenv("PENGUPOOL_HOME"); home != "" {
		return filepath.Join(home, "v2")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".pengupool-ai", "v2")
	}
	return filepath.Join(home, ".pengupool-ai", "v2")
}

// IsHomeDefault reports whether dir is the owner's real store:
// PENGUPOOL_HOME must be unset and dir must resolve to
// ~/.pengupool-ai/v2. Installers that write outside the store (Muse
// plugin, Codex rules) run only then: a throwaway daemon with
// PENGUPOOL_HOME set is never the owner's home, even when its store
// equals DefaultDir. Temp HOME/CODEX_HOME/XDG dirs are a separate
// rule; this one guards the installers.
func IsHomeDefault(dir string) bool {
	if os.Getenv("PENGUPOOL_HOME") != "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	want := filepath.Join(home, ".pengupool-ai", "v2")
	cdir, cerr := filepath.EvalSymlinks(filepath.Clean(dir))
	cwant, werr := filepath.EvalSymlinks(filepath.Clean(want))
	if cerr != nil || werr != nil {
		return filepath.Clean(dir) == filepath.Clean(want)
	}
	return cdir == cwant
}

type Store struct {
	dir string

	mu    sync.Mutex
	state State
	cfg   Config
}

// Open reads (or creates) the store in dir. It returns *ErrCorrupt when state.json exists but does
// not parse.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	now := time.Now().UTC()

	raw, err := os.ReadFile(s.path(StateFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.state); err != nil {
			return nil, &ErrCorrupt{Path: s.path(StateFile), Err: err}
		}
		// A file that parses but carries no schema version or no node id is not ours: `null` and
		// `{}` both unmarshal happily and would silently become an empty pool, overwritten on the
		// next write. Refuse it instead of replacing the user's file.
		if err := validateState(s.state); err != nil {
			return nil, &ErrCorrupt{Path: s.path(StateFile), Err: err}
		}
		dropRetired(raw, &s.state)
	case errors.Is(err, os.ErrNotExist):
		s.state = DefaultState(protocol.NewID(), now)
	default:
		return nil, err
	}
	if s.state.Sessions == nil {
		s.state.Sessions = []Session{}
	}
	if s.state.Nodes == nil {
		s.state.Nodes = []Node{}
	}

	raw, err = os.ReadFile(s.path(ConfigFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.cfg); err != nil {
			return nil, &ErrCorrupt{Path: s.path(ConfigFile), Err: err}
		}
		if s.cfg.Version == 0 || s.cfg.Version > Version {
			return nil, &ErrCorrupt{Path: s.path(ConfigFile), Err: fmt.Errorf("schema version %d is not %d", s.cfg.Version, Version)}
		}
	case errors.Is(err, os.ErrNotExist):
		s.cfg = DefaultConfig(now)
	default:
		return nil, err
	}

	for _, d := range []string{RunDir, LogsDir, BackupsDir} {
		if err := os.MkdirAll(s.path(d), 0o700); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// dropRetired removes the helpers an earlier version kept retired ("archived"): there is no retired
// state now, and read as live they would come back on the map and in the library. Their record
// (its journal) stays under their repository key, so the same helper added again has it.
func dropRetired(raw []byte, st *State) {
	var old struct {
		Domain struct {
			Skills []struct {
				ID       string `json:"id"`
				Archived bool   `json:"archived"`
			} `json:"skills"`
		} `json:"domain"`
	}
	if json.Unmarshal(raw, &old) != nil {
		return
	}
	gone := map[string]bool{}
	for _, sk := range old.Domain.Skills {
		if sk.Archived {
			gone[sk.ID] = true
		}
	}
	if len(gone) > 0 {
		st.Domain.Skills = slices.DeleteFunc(st.Domain.Skills, func(sk protocol.Skill) bool { return gone[sk.ID] })
	}
}

// validateState rejects a state file that parses but is not a PenguPool 2.0 state file.
func validateState(s State) error {
	switch {
	case s.Version == 0:
		return errors.New("no schema version: the file is empty or was not written by PenguPool")
	case s.Version > Version:
		return fmt.Errorf("schema version %d is newer than this build's %d", s.Version, Version)
	case s.NodeID == "":
		return errors.New("no node id")
	default:
		return nil
	}
}

// Dir is the store directory, i.e. $PENGUPOOL_HOME/v2.
func (s *Store) Dir() string { return s.dir }

// Path resolves a store-relative path.
func (s *Store) Path(parts ...string) string { return s.path(parts...) }

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.dir}, parts...)...)
}

// NodeID is this host's stable identity.
func (s *Store) NodeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.NodeID
}

// State returns a deep copy of the current state.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Clone()
}

// Config returns the current configuration.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Update applies fn to the state under the store lock and, on success, writes it atomically with a
// bumped revision. If fn returns an error nothing is written and the in-memory state is restored.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.state.Clone()
	if err := fn(&next); err != nil {
		return err
	}
	next.Version = Version
	next.Rev = s.state.Rev + 1
	next.UpdatedAt = time.Now().UTC()
	if next.NodeID == "" {
		next.NodeID = s.state.NodeID
	}
	if err := s.saveLocked(StateFile, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

// SaveConfig writes config.json atomically.
func (s *Store) SaveConfig(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg.Version = Version
	cfg.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(ConfigFile, cfg); err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

func (s *Store) saveLocked(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.writeAtomic(name, data)
}

// writeAtomic writes name through name.tmp, keeping the previous contents as name.bak. A reader
// therefore always sees either the old file or the new one, never a partial write.
func (s *Store) writeAtomic(name string, data []byte) error {
	target := s.path(name)
	tmp := target + TempSuffix

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	if name == StateFile {
		if prev, err := os.ReadFile(target); err == nil {
			bak := s.path(BackupFile) + TempSuffix
			bf, err := os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := bf.Write(prev); err != nil {
				bf.Close()
				return err
			}
			if err := bf.Sync(); err != nil {
				bf.Close()
				return err
			}
			if err := bf.Close(); err != nil {
				return err
			}
			if err := os.Rename(bak, s.path(BackupFile)); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(s.dir)
}

// syncDir fsyncs a directory so a rename is durable across a host crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse fsync on a directory; the rename is still atomic.
		return nil
	}
	return nil
}
