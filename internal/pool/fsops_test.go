package pool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestFsOpsStayInTheWorkspace(t *testing.T) {
	p, st := twoTeams(t)
	cwd := t.TempDir()
	outside := t.TempDir()
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			if s.Sessions[i].ID == "leadaaaaaaaa" {
				s.Sessions[i].Cwd = cwd
			}
		}
		return nil
	})
	_ = os.Symlink(outside, filepath.Join(cwd, "link"))
	op := func(typ string, in map[string]any) error {
		in["session_id"] = "leadaaaaaaaa"
		_, err := call(t, p, true, typ, in)
		return err
	}
	if err := op("fs.create", map[string]any{"path": "a/../notes.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "notes.md")); err != nil {
		t.Fatal("not created")
	}
	if op("fs.create", map[string]any{"path": "notes.md"}) == nil {
		t.Fatal("overwrote an existing file")
	}
	if err := op("fs.create", map[string]any{"path": "src", "dir": true}); err != nil {
		t.Fatal(err)
	}
	if err := op("fs.rename", map[string]any{"path": "notes.md", "to": "src/readme.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "src", "readme.md")); err != nil {
		t.Fatal("not renamed")
	}
	_ = op("fs.create", map[string]any{"path": "other.md"})
	if op("fs.rename", map[string]any{"path": "other.md", "to": "src/readme.md"}) == nil {
		t.Fatal("renamed over an existing file")
	}
	// The kernel refuses to replace: a file and a folder each keep what was there.
	_ = os.WriteFile(filepath.Join(cwd, "src", "keep.md"), []byte("keep"), 0o644)
	_ = os.WriteFile(filepath.Join(cwd, "mine.md"), []byte("mine"), 0o644)
	if op("fs.rename", map[string]any{"path": "mine.md", "to": "src/keep.md"}) == nil {
		t.Fatal("a file replaced another")
	}
	if b, _ := os.ReadFile(filepath.Join(cwd, "src", "keep.md")); string(b) != "keep" {
		t.Fatalf("keep.md = %q", b)
	}
	_ = os.Mkdir(filepath.Join(cwd, "a-dir"), 0o755)
	if op("fs.rename", map[string]any{"path": "a-dir", "to": "src"}) == nil {
		t.Fatal("a folder replaced another")
	}
	if err := op("fs.rename", map[string]any{"path": "a-dir", "to": "b-dir"}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(cwd, "mine.md"))
	// Nothing leaves the workspace: not by "..", not by a link.
	for _, in := range []map[string]any{
		{"path": "link/x"}, {"path": ""},
	} {
		if op("fs.create", in) == nil {
			t.Fatalf("created outside: %v", in)
		}
	}
	_ = op("fs.create", map[string]any{"path": "../escape"}) // ".." is cleaned away, as the file views do: it lands inside
	if _, err := os.Stat(filepath.Join(filepath.Dir(cwd), "escape")); err == nil {
		t.Fatal("a file appeared above the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Fatal("a file appeared behind the link")
	}
	_ = op("fs.rename", map[string]any{"path": "src/readme.md", "to": "../moved.md"})
	if _, err := os.Stat(filepath.Join(filepath.Dir(cwd), "moved.md")); err == nil {
		t.Fatal("renamed out of the workspace")
	}
	if err := op("fs.rename", map[string]any{"path": "moved.md", "to": "src/readme.md"}); err != nil {
		t.Fatal(err) // the file the cleaned path landed on goes back, for the trash step below
	}
	// Trash hands Finder the real path, and refuses one behind a link.
	var got string
	old := trashFile
	trashFile = func(abs string) error { got = abs; return nil }
	defer func() { trashFile = old }()
	if err := op("fs.trash", map[string]any{"path": "src/readme.md"}); err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(cwd, "src", "readme.md")); got != want {
		t.Fatalf("trashed %q, want %q", got, want)
	}
	got = ""
	// A link that points inside is still a link: the entry itself is never followed. A link as the
	// parent is resolved, and what it names must be inside the workspace.
	_ = os.Symlink(filepath.Join(cwd, "src"), filepath.Join(cwd, "alias"))
	_ = os.Symlink(filepath.Join(cwd, "src", "keep.md"), filepath.Join(cwd, "leaf"))
	if op("fs.trash", map[string]any{"path": "leaf"}) == nil || got != "" {
		t.Fatal("trashed a link")
	}
	if err := op("fs.trash", map[string]any{"path": "alias/keep.md"}); err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(cwd, "src", "keep.md")); got != want {
		t.Fatalf("trashed through a link parent: %q, want %q", got, want)
	}
	got = ""
	if op("fs.trash", map[string]any{"path": "link/x"}) == nil || got != "" {
		t.Fatal("trashed something behind a link")
	}
	if op("fs.trash", map[string]any{"path": "missing.md"}) == nil {
		t.Fatal("trashed a file that is not there")
	}
	// Agents never reach these.
	if _, err := call(t, p, false, "fs.trash", map[string]any{"session_id": "leadaaaaaaaa", "path": "other.md"}); err == nil {
		t.Fatal("an agent trashed a file")
	}
}

func TestTrashTargetRefusesLinks(t *testing.T) {
	cwd, outside := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(cwd, "real"), nil, 0o644)
	_ = os.Symlink(outside, filepath.Join(cwd, "linkdir"))
	_ = os.Symlink(filepath.Join(cwd, "real"), filepath.Join(cwd, "linkleaf"))
	for _, rel := range []string{"linkdir/secret", "linkleaf", "missing"} {
		if abs, err := trashTarget(cwd, rel); err == nil {
			t.Fatalf("%s resolved to %s", rel, abs)
		}
	}
	if _, err := trashTarget(cwd, "real"); err != nil {
		t.Fatal(err)
	}
}
