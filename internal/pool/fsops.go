package pool

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// fs.create, fs.rename and fs.trash change a session's workspace for the owner's Explorer (owner
// only, in no allow-list). Every path is relative to the session's cwd and goes through os.Root, so
// neither ".." nor a symbolic link leaves the workspace. Nothing is overwritten, and a delete goes
// to the Trash, never past it.

type fsOpIn struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	To        string `json:"to,omitempty"`  // fs.rename: the new path
	Dir       bool   `json:"dir,omitempty"` // fs.create: a folder rather than an empty file
}

// workspace opens the session's cwd as a root, and checks a relative path names something inside it.
func (p *Pool) workspace(sessionID string, rels ...string) (*os.Root, []string, error) {
	s, ok := p.session(sessionID)
	if !ok || s.Cwd == "" {
		return nil, nil, notFound("no session %s", sessionID)
	}
	clean := make([]string, len(rels))
	for i, r := range rels {
		c := path.Clean("/" + strings.TrimPrefix(r, "/"))[1:]
		if c == "" || !fs.ValidPath(c) {
			return nil, nil, invalid("%q is not a path inside the workspace", r)
		}
		clean[i] = c
	}
	root, err := os.OpenRoot(s.Cwd)
	if err != nil {
		return nil, nil, invalid("cannot open the workspace: %v", err)
	}
	return root, clean, nil
}

func (p *Pool) fsCreate(_ context.Context, in fsOpIn) (any, error) {
	root, rel, err := p.workspace(in.SessionID, in.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if in.Dir {
		err = root.Mkdir(rel[0], 0o755)
	} else {
		var f *os.File
		if f, err = root.OpenFile(rel[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			err = f.Close()
		}
	}
	return map[string]any{}, fsErr(err)
}

func (p *Pool) fsRename(_ context.Context, in fsOpIn) (any, error) {
	root, rel, err := p.workspace(in.SessionID, in.Path, in.To)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return map[string]any{}, fsErr(renameNoReplace(root, rel[0], rel[1]))
}

// renameNoReplace moves a file or folder inside the workspace and never replaces what is there: the
// kernel refuses (EEXIST) in the same step as the move. The parents are opened through the root, so
// neither ".." nor a link leaves the workspace.
func renameNoReplace(root *os.Root, from, to string) error {
	fd := func(rel string) (*os.File, string, error) {
		f, err := root.Open(path.Dir(rel))
		return f, path.Base(rel), err
	}
	f1, b1, err := fd(from)
	if err != nil {
		return err
	}
	defer f1.Close()
	f2, b2, err := fd(to)
	if err != nil {
		return err
	}
	defer f2.Close()
	return renameat(int(f1.Fd()), b1, int(f2.Fd()), b2)
}

// trashFile moves one file or folder to the Trash through Finder, so the owner can Put Back.
// macOS asks once to let PenguPool control Finder. ponytail: macOS only; other systems refuse
// rather than delete for good, add the freedesktop trash when a Linux host needs it.
var trashFile = func(abs string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("moving to the Trash is supported on macOS only")
	}
	out, err := exec.Command("osascript", "-e", "on run argv", "-e", `tell application "Finder" to delete (POSIX file (item 1 of argv))`, "-e", "end run", abs).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// trashTarget is the real path of a workspace entry for Finder, which follows links os.Root refused
// to: the parent must resolve inside the workspace and the entry itself must not be a link.
func trashTarget(cwd, rel string) (string, error) {
	base, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", invalid("%v", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(base, filepath.Dir(rel)))
	if err != nil || (parent != base && !strings.HasPrefix(parent, base+string(filepath.Separator))) {
		return "", invalid("%s is outside the workspace", rel)
	}
	abs := filepath.Join(parent, filepath.Base(rel))
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", fsErr(err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "", invalid("%s is a link; it is not moved to the Trash", rel)
	}
	return abs, nil
}

func (p *Pool) fsTrash(_ context.Context, in fsOpIn) (any, error) {
	root, rel, err := p.workspace(in.SessionID, in.Path)
	if err != nil {
		return nil, err
	}
	root.Close()
	s, _ := p.session(in.SessionID)
	abs, err := trashTarget(s.Cwd, rel[0])
	if err != nil {
		return nil, err
	}
	// Checked again right before Finder is asked, so a link swapped in meanwhile is caught.
	// ponytail: a window of microseconds remains; a no-follow rename into ~/.Trash would close it but
	// loses Put Back and needs Full Disk Access.
	if again, err := trashTarget(s.Cwd, rel[0]); err != nil || again != abs {
		return nil, invalid("%s changed while it was being moved", rel[0])
	}
	return map[string]any{}, trashFile(abs)
}

// fsErr turns the file system's refusals into the owner's words.
func fsErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return conflict("it already exists")
	case errors.Is(err, fs.ErrNotExist):
		return notFound("not found")
	}
	return invalid("%v", err)
}
