package pool

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// fs.dirs lists the folders in one folder of the host, for the owner's New session dialog: the
// desktop app's native picker reaches only this machine, while the host may be an SSH machine or the
// app a browser. Owner only (in no allow-list); hidden folders are left out.
//
// It looks inside a listed folder only to mark a Git repository, and never inside a folder macOS
// guards with a privacy prompt (Music, Pictures, Movies, Documents… in the home folder, and every
// volume): looking there makes macOS ask the owner to let PenguPool reach their music or photos
// just because the picker opened. A guarded folder is still listed; opening it asks, as it should.

type dirEntry struct {
	Name string `json:"name"`
	Git  bool   `json:"git,omitempty"`
}

type dirsOut struct {
	Path   string     `json:"path"`
	Parent string     `json:"parent,omitempty"`
	Home   string     `json:"home"`
	Dirs   []dirEntry `json:"dirs"`
}

type dirsIn struct {
	Path string `json:"path"`
}

const maxDirs = 500

func (p *Pool) fsDirs(_ context.Context, in dirsIn) (any, error) {
	home, _ := os.UserHomeDir()
	path := strings.TrimSpace(in.Path)
	switch {
	case path == "":
		path = home
	case path == "~" || strings.HasPrefix(path, "~/"):
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, invalid("%v", err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil, invalid("%s is not a folder", path)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, invalid("cannot read %s: %v", path, err)
	}
	out := dirsOut{Path: path, Home: home, Dirs: []dirEntry{}}
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || len(out.Dirs) >= maxDirs {
			continue
		}
		full := filepath.Join(path, e.Name())
		if fi, err := os.Stat(full); err != nil || !fi.IsDir() { // follows symlinks to folders
			continue
		}
		git := false
		if !guarded(path, e.Name(), home) {
			_, gerr := os.Stat(filepath.Join(full, ".git"))
			git = gerr == nil
		}
		out.Dirs = append(out.Dirs, dirEntry{Name: e.Name(), Git: git})
	}
	slices.SortFunc(out.Dirs, func(a, b dirEntry) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// guardedHome are the home folders macOS puts behind a privacy prompt (TCC) for an app that reads
// inside them; none of them is a repository itself.
var guardedHome = []string{"Desktop", "Documents", "Downloads", "Movies", "Music", "Pictures"}

// guarded reports whether looking inside the folder name in dir could raise a macOS privacy prompt:
// a guarded home folder, or a volume (removable and network volumes have prompts of their own).
func guarded(dir, name, home string) bool {
	return (dir == home && slices.Contains(guardedHome, name)) || dir == "/Volumes"
}
