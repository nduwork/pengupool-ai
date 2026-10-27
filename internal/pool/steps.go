package pool

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Workflow-tracker chains (~/.claude/step-status/bin/steps.sh) for the map cards, read the way v1
// read them: <repo main root>/.step-status/sessions/<id>/<current>.state, one "status\tname\tdetail"
// line per step. A session's own tracker wins (keyed by its harness conversation id or its PenguPool
// id); a repo with a single session falls back to the repo's shared tracker.

type Step struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status"` // planned | active | done | failed
}

type Chain struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

var (
	chainRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	ctrlRE   = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	stepsTTL = 60 * time.Second // steps.sh DONE_TTL: a finished chain disappears a minute after its last update
)

// statusDir is the .step-status directory steps.sh writes for cwd: the nearest enclosing repo, and
// for a linked worktree (.git is a file) the main repo root, so a worktree shares its repo's tracker.
func statusDir(cwd string) string {
	root := cwd
	for d := cwd; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, ".git")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if gd, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok {
				gd = strings.TrimSpace(gd)
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(root, gd)
				}
				if main, _, found := strings.Cut(gd, "/.git/worktrees/"); found {
					root = main
				}
			}
		}
	}
	return filepath.Join(root, ".step-status")
}

// readChain reads the current chain of one tracker directory; nil when there is none or it expired.
func readChain(dir string) *Chain {
	name := "default"
	if b, err := os.ReadFile(filepath.Join(dir, "current")); err == nil {
		name = strings.TrimSpace(string(b))
	}
	if !chainRE.MatchString(name) {
		name = "default"
	}
	path := filepath.Join(dir, name+".state")
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 64<<10)) // a hostile file must not stall the poll
	c := &Chain{Name: name}
	active := false
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		st := Step{Status: parts[0], Name: ctrlRE.ReplaceAllString(parts[1], "")}
		if len(parts) == 3 {
			st.Detail = ctrlRE.ReplaceAllString(parts[2], "")
			if len(st.Detail) > 80 {
				st.Detail = st.Detail[:80]
			}
		}
		active = active || st.Status == "active"
		c.Steps = append(c.Steps, st)
	}
	if len(c.Steps) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, name+".cycle")); !active && err != nil && time.Since(fi.ModTime()) > stepsTTL {
		return nil
	}
	return c
}

// sessionSteps returns the live workflow chain of every running session that has one.
func (p *Pool) sessionSteps(_ context.Context, _ struct{}) (any, error) {
	st := p.st.State()
	perDir := map[string]int{}
	for _, s := range st.Sessions {
		if s.State == "running" {
			perDir[statusDir(s.Cwd)]++
		}
	}
	out := map[string]*Chain{}
	for _, s := range st.Sessions {
		if s.State != "running" {
			continue
		}
		dir := statusDir(s.Cwd)
		var c *Chain
		for _, key := range []string{s.ConvID, s.ID} {
			if c == nil && chainRE.MatchString(key) {
				c = readChain(filepath.Join(dir, "sessions", key))
			}
		}
		if c == nil && perDir[dir] == 1 {
			c = readChain(dir)
		}
		if c != nil {
			out[s.ID] = c
		}
	}
	return out, nil
}
