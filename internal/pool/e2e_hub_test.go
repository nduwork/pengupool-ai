package pool

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// End to end against the real, private Helper agent hub through the machine's gh sign-in. It needs the
// network, so it runs only with PENGUPOOL_E2E_HUB=1:
//
//	PENGUPOOL_E2E_HUB=1 go test ./internal/pool -run TestE2EHub -v -count=1
//
// PENGUPOOL_E2E_OLD=<sha of nduwork/pengupool-marketplace> (default 06443a3) is the old catalog the
// migration test starts from. Everything happens in temporary stores; the hub is only read.
func e2eHub(t *testing.T) {
	t.Helper()
	if os.Getenv("PENGUPOOL_E2E_HUB") != "1" {
		t.Skip("set PENGUPOOL_E2E_HUB=1 to run against the real hub")
	}
}

func e2ePool(t *testing.T) (*Pool, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The package's tests run with a throwaway HOME; gh needs the real one to find its sign-in.
	env := os.Environ()
	if u, err := user.Current(); err == nil {
		env = append(env, "HOME="+u.HomeDir)
	}
	return New(Options{Store: st, Exe: "pengupool", Env: env}), st
}

// e2eCatalog is what the hub's clone says every helper is pinned to.
func e2eCatalog(t *testing.T, p *Pool) map[string]MarketEntry {
	t.Helper()
	m, err := readMarketplace(p.marketDir())
	if err != nil || m.Skipped != 0 {
		t.Fatalf("catalog: %v skipped=%d", err, m.Skipped)
	}
	out := map[string]MarketEntry{}
	for _, e := range m.Skills {
		out[e.Name] = e
	}
	return out
}

func e2eSkills(st *store.Store) map[string]protocol.Skill {
	out := map[string]protocol.Skill{}
	for _, s := range st.State().Domain.Skills {
		out[s.Name] = s
	}
	return out
}

func e2eWant(t *testing.T, p *Pool, st *store.Store) {
	t.Helper()
	cat, got := e2eCatalog(t, p), e2eSkills(st)
	if len(got) != len(cat) {
		t.Fatalf("%d helpers installed, catalog has %d", len(got), len(cat))
	}
	for name, e := range cat {
		if got[name].Commit != e.Commit {
			t.Errorf("%s at %.7s, catalog pins %.7s", name, got[name].Commit, e.Commit)
		}
	}
}

// 1. A fresh store clones the hub and installs every helper at its pin. 4. The hub, as it is, has only the
// old catalog file, so that is the one read (the new file winning is TestCatalogFile).
// 3. A new pin in the catalog is pulled by Refresh and moves the helper.
func TestE2EHubFreshAndRefresh(t *testing.T) {
	e2eHub(t)
	p, st := e2ePool(t)
	ctx := context.Background()
	if _, err := p.marketplaceRefresh(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	e2eWant(t, p, st)
	m, _ := readMarketplace(p.marketDir())
	_, newFile := os.Stat(filepath.Join(p.marketDir(), marketFile))
	t.Logf("fresh store: %d helpers; catalog file: legacy=%v (new file present: %v)", len(m.Skills), m.Legacy, newFile == nil)
	if m.Legacy == (newFile == nil) {
		t.Fatalf("Legacy=%v but the new file present=%v", m.Legacy, newFile == nil)
	}

	// The hub "gets a new pin": the host's catalog clone is an earlier commit, the nearest one where a pin
	// differs from today's, as a host that has not pulled.
	head := gitIn2(t, p.marketDir(), "rev-parse", "HEAD")
	now := e2eCatalog(t, p)
	var behind map[string]MarketEntry
	for k := 1; k <= 40 && behind == nil; k++ {
		if out, err := exec.Command("git", "-C", p.marketDir(), "reset", "-q", "--hard", head+"~"+strconv.Itoa(k)).CombinedOutput(); err != nil {
			t.Logf("no earlier commit %d back: %s", k, out)
			break
		}
		if c := e2eCatalog(t, p); countSame(c, now) != len(now) || len(c) != len(now) {
			behind = c
		}
	}
	if behind == nil {
		t.Fatalf("no commit in the hub's last 40 has a pin that differs from today's: the pull cannot be shown to move anything")
	}
	was := 0
	p.syncLibrary(ctx) // from the earlier catalog: the helpers whose pin differs go back to it
	for name, s := range e2eSkills(st) {
		if s.Commit == behind[name].Commit {
			was++
		}
	}
	if was != len(behind) {
		t.Fatalf("sync at the earlier catalog: %d of %d at its pin", was, len(behind))
	}
	if _, err := p.marketplaceRefresh(ctx, struct{}{}); err != nil { // Refresh pulls the catalog, then syncs
		t.Fatal(err)
	}
	if got := gitIn2(t, p.marketDir(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("refresh left the catalog at %.7s, hub is at %.7s", got, head)
	}
	e2eWant(t, p, st)
	t.Logf("refresh pulled the catalog to %.7s and moved the %d helper(s) whose pin differed back to it", head, len(now)-countSame(behind, now))
}

func countSame(a, b map[string]MarketEntry) int {
	n := 0
	for k, e := range a {
		if b[k].Commit == e.Commit {
			n++
		}
	}
	return n
}

// 2. A store from the old marketplace, helpers at the old pins and one deleted, ends in the same state as a
// fresh one after migrateHub and a sync: same ids, nothing reinstalled, the deleted helper still deleted.
func TestE2EHubMigratesAnOldStore(t *testing.T) {
	e2eHub(t)
	oldSHA := os.Getenv("PENGUPOOL_E2E_OLD")
	if oldSHA == "" {
		oldSHA = "06443a3"
	}
	p, st := e2ePool(t)
	ctx := context.Background()
	// The old catalog clone, as the old version left it.
	oldCat := p.marketDirFor(legacyMarketplace)
	if _, err := p.clone(ctx, legacyMarketplace, oldCat); err != nil {
		t.Fatal(err)
	}
	gitIn2(t, oldCat, "checkout", "-q", oldSHA)
	old, err := readMarketplace(oldCat)
	if err != nil || len(old.Skills) == 0 {
		t.Fatalf("old catalog: %v", err)
	}
	// Install every old helper the way the old version did: its repository at the old pin under skills/.
	ids := map[string]string{}
	var gone string
	for i, e := range old.Skills {
		if i == len(old.Skills)-1 { // the owner deleted this one
			gone = e.Name
			if err := p.db.SetSetting(deletedKey(e.key()), "1"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		dest := filepath.Join(st.Dir(), "skills", e.Category, e.Name)
		if _, err := p.clone(ctx, e.Repo, dest); err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		gitIn2(t, dest, "checkout", "-q", e.Commit)
		sk := protocol.Skill{ID: "id-" + e.Name, Name: e.Name, Path: filepath.Join(dest, e.Path), Source: "git", Key: e.key(), Commit: e.Commit, Group: e.Category, Category: e.Category}
		ids[e.Name] = sk.ID
		_ = st.Update(func(s *store.State) error { s.Domain.Skills = append(s.Domain.Skills, sk); return nil })
	}
	before := len(e2eSkills(st))

	p.migrateHub(ctx)
	if _, err := os.Stat(oldCat); err == nil {
		t.Error("the old catalog clone is still there")
	}
	res := p.syncLibrary(ctx)
	e2eWant2 := func() {
		cat, got := e2eCatalog(t, p), e2eSkills(st)
		if len(got) != before {
			t.Errorf("%d helpers after, %d before (added %v)", len(got), before, res["added"])
		}
		if _, ok := got[gone]; ok {
			t.Errorf("deleted helper %s is back", gone)
		}
		for name, s := range got {
			if s.ID != ids[name] {
				t.Errorf("%s changed id", name)
			}
			if s.Commit != cat[name].Commit {
				t.Errorf("%s at %.7s, catalog pins %.7s", name, s.Commit, cat[name].Commit)
			}
			if !strings.HasPrefix(s.Key, repoKey(mainMarketplace)) {
				t.Errorf("%s key %s", name, s.Key)
			}
		}
	}
	e2eWant2()
	t.Logf("old store (%s): %d helpers, deleted %s; sync: %v", oldSHA, before, gone, res)
	if len(res["added"]) != 0 {
		t.Errorf("sync installed %v", res["added"])
	}
	// Once more: nothing moves.
	res = p.syncLibrary(ctx)
	e2eWant2()
	if len(res["added"])+len(res["moved"])+len(res["updated"]) != 0 {
		t.Errorf("a second sync changed %v", res)
	}
}
