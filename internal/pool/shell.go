package pool

import (
	"context"
	"os"

	"github.com/nduwork/pengupool-ai/internal/harness"
)

type shellIn struct {
	Cwd  string `json:"cwd,omitempty"` // default: the owner's home folder
	Name string `json:"name,omitempty"`
}

// shellOpen starts the owner's login shell ($SHELL, as `-l`) in a folder, for work that belongs to no
// agent session: it is on no team and gets no hooks, no harness and none of the session markers. It
// is the same worker a session runs in, so it attaches, resizes and closes like one (session.close
// ends the shell); the app lists it under Terminals, apart from the agents.
func (p *Pool) shellOpen(ctx context.Context, in shellIn) (any, error) {
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	cwd, err := absDir(cwd)
	if err != nil {
		return nil, err
	}
	name := in.Name
	if name == "" {
		name = p.uniqueName("terminal")
	}
	s, err := p.spawn(ctx, spawnReq{Name: name, Harness: harness.Shell, Cwd: cwd})
	if err != nil {
		return nil, err
	}
	return s.View(), nil
}
