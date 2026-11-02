package pool

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A restart or a restore starts a team specialist again with the model it was given and read
// access to the team's repositories, as its first start did.
func TestResumeKeepsModelAndReadDirs(t *testing.T) {
	agentOnPath(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hub.New(hub.Options{Store: st, Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Hub: h, Exe: "pengupool"})
	var reqs []spawnReq
	p.testSpawn = func(r spawnReq) (store.Session, error) {
		reqs = append(reqs, r)
		s := store.Session{ID: r.ID, Name: r.Name, Harness: r.Harness, Cwd: r.Cwd, State: "running", Status: "idle", RunLog: filepath.Join(t.TempDir(), "run.log")}
		if r.Decorate != nil {
			r.Decorate(&s)
		}
		return s, st.Update(func(x *store.State) error {
			if i := sessionIndex(x, s.ID); i >= 0 {
				x.Sessions[i] = s
				return nil
			}
			x.Sessions = append(x.Sessions, s)
			return nil
		})
	}
	repo, repo2 := t.TempDir(), t.TempDir()
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: "claude", State: "running", Status: "idle"})
		s.Domain.Repos = []protocol.Repo{{ID: "r1", Name: "app", Path: repo}, {ID: "r2", Name: "api", Path: repo2}}
		s.Domain.Skills = []protocol.Skill{{ID: "sk-rev", Name: "reviewer", Path: t.TempDir(), Source: "local"}}
		s.Domain.Teams = []protocol.Team{{ID: "team1111", Name: "app", RepoID: "r1", Repos: []string{"r2"}, Harness: "claude", State: "active",
			Members: []protocol.Member{{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"}}}}
		return nil
	})
	ctx := context.Background()
	res, err := p.placeHelper(ctx, placeIn{TeamID: "team1111", SkillID: "sk-rev", Model: "model-x"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.(protocol.Member).SessionID
	p.setSession(id, func(x *store.Session) { x.State, x.Status, x.ConvID, x.Model = "ended", "ended", "conv-1", "Model X (reported)" })
	if _, err := p.resume(ctx, sessionIn{SessionID: id}); err != nil {
		t.Fatal(err)
	}
	first, again := reqs[0], reqs[len(reqs)-1]
	if again.Model != "model-x" {
		t.Fatalf("resumed on model %q, want model-x (first start: %q)", again.Model, first.Model)
	}
	if !slices.Equal(again.ReadDirs, []string{repo, repo2}) || !slices.Equal(again.ReadDirs, first.ReadDirs) {
		t.Fatalf("resumed read dirs %v, first start %v", again.ReadDirs, first.ReadDirs)
	}
}
