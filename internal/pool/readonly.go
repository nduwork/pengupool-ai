package pool

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A helper's installed skill files are read-only: what the agent learns goes to its memory and
// experience in PenguPool, not into the repository. Its folder is not: temp files and folders are fine.
// Three layers hold it: in a clone PenguPool installed, the files Git tracks are read-only on disk,
// the PreToolUse guard refuses an edit to one, and the context says so. A checkout the owner works
// in (the marketplace repository, a helper added from a local folder) is never locked or guarded.

// setWritable gives every file and folder under root, except .git, its owner's write permission
// back (w), or takes write permission from everyone (!w). Symbolic links are left alone.
func setWritable(root string, w bool) error {
	return filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if w || e.IsDir() { // folders stay writable: a helper makes its own files there
			mode |= 0o200
		} else {
			mode &^= 0o222
		}
		if mode == info.Mode().Perm() {
			return nil
		}
		return os.Chmod(path, mode)
	})
}

// unlockClones makes every clone under <store>/skills writable for the library sync's Git work,
// moves and removals; lockClones puts the locks back afterwards.
func (p *Pool) unlockClones() {
	if err := setWritable(filepath.Join(p.st.Dir(), "skills"), true); err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.logf("pool: helper skills: unlock: %v", err)
	}
}

// lockClones makes the installed skill files of each clone PenguPool made for helpers from a Git
// source read-only. A helper the owner created or converted in the store stays the owner's.
func (p *Pool) lockClones(ctx context.Context) {
	roots := map[string]bool{}
	for _, sk := range p.st.State().Domain.Skills {
		if sk.Source != "git" {
			continue
		}
		if root, own := p.ownClone(ctx, sk.Path); own {
			roots[root] = true
		}
	}
	for root := range roots {
		if err := p.lockClone(ctx, root); err != nil {
			p.logf("pool: helper skills: %s: %v", root, err)
		}
	}
}

// lockClone takes write permission from the files Git tracks in root and gives every folder its
// owner's write permission (a clone locked by PenguPool before 0.11.2 had read-only folders). A
// helper's own files stay writable and, untracked, out of the clone's status: the library sync
// refuses a clone with changes.
func (p *Pool) lockClone(ctx context.Context, root string) error {
	out, err := p.gitIn(ctx, root, "ls-files", "-z")
	if err != nil {
		return err
	}
	real := realPath(root)
	for _, name := range strings.Split(out, "\x00") {
		if name == "" {
			continue
		}
		path := filepath.Join(root, name)
		// Its folder must be the clone's own: with folders writable, one can be swapped for a link
		// to a checkout of the owner's, whose files a chmod through it would lock.
		// ponytail: checked, then chmod'ed; a swap in between is not caught (openat with no-follow would).
		if realPath(filepath.Dir(path)) != filepath.Join(real, filepath.Dir(name)) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue // deleted, a link, or a submodule
		}
		if mode := info.Mode().Perm() &^ 0o222; mode != info.Mode().Perm() {
			if err := os.Chmod(path, mode); err != nil {
				return err
			}
		}
	}
	if err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return err
		}
		if e.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := e.Info()
		if err != nil || info.Mode().Perm()&0o200 != 0 {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()|0o200)
	}); err != nil {
		return err
	}
	_, err = p.gitIn(ctx, root, "config", "status.showUntrackedFiles", "no")
	return err
}

// lockClonesIdle is lockClones outside the library sync: a sync running now is skipped rather than
// waited for or raced (a chmod in the middle of its checkout or move would leave a clone it can no
// longer update), and the sync's own lockClones at its end locks what it added.
func (p *Pool) lockClonesIdle(ctx context.Context) {
	if !p.syncMu.TryLock() {
		p.logf("pool: helper skills: a library sync is running; it applies the locks when it ends")
		return
	}
	defer p.syncMu.Unlock()
	p.lockClones(ctx)
}
