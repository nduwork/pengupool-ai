package pool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func skillDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(filepath.Join(dir, "skills", name), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Use for "+name+".\n---\n# "+name+"\n"), 0o644)
	return dir
}

func skillNamed(p *Pool, name string) protocol.Skill {
	for _, s := range p.st.State().Domain.Skills {
		if s.Name == name {
			return s
		}
	}
	return protocol.Skill{}
}

// A helper the owner adds is on the map; one the library sync installs on its own is not; the
// owner's Add from the marketplace is.
func TestOnMapDefaults(t *testing.T) {
	c := newTestCatalog(t)
	ctx := context.Background()
	if _, err := c.p.skillAdd(ctx, skillAddIn{Name: "mine", Path: skillDir(t, "mine")}); err != nil {
		t.Fatal(err)
	}
	if !skillNamed(c.p, "mine").OnMap {
		t.Error("a helper the owner added is off the map")
	}
	c.publish(map[string]string{"agents/x": "", "agents/y": ""})
	if _, err := c.p.marketplaceInstall(ctx, marketIn{Name: "y"}); err != nil {
		t.Fatal(err)
	}
	if x := c.sync("x"); x.OnMap {
		t.Error("a helper the sync installed is on the map")
	}
	if !skillNamed(c.p, "y").OnMap {
		t.Error("a helper the owner added from the marketplace is off the map")
	}
}

// skill.update with only on_map changes only that; the Overview's form (with requires) still
// changes the rest.
func TestSkillUpdateOnMap(t *testing.T) {
	p, _ := twoTeams(t)
	ctx := context.Background()
	_ = p.st.Update(func(s *store.State) error {
		s.Domain.Skills[0].Method, s.Domain.Skills[0].OnMap = "plans epics", true
		return nil
	})
	off := false
	r, err := p.skillUpdate(ctx, skillUpdateIn{ID: "sk-plan", OnMap: &off})
	if sk := r.(*protocol.Skill); err != nil || sk.OnMap || sk.Method != "plans epics" {
		t.Fatalf("toggle: %+v %v", sk, err)
	}
	r, err = p.skillUpdate(ctx, skillUpdateIn{ID: "sk-plan", Method: "new", Requires: []string{}})
	if sk := r.(*protocol.Skill); err != nil || sk.OnMap || sk.Method != "new" {
		t.Fatalf("form: %+v %v", sk, err)
	}
}
