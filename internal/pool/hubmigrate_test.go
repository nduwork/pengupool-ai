package pool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// localHub is a repository standing in for the hub (its own history, unrelated to the old repository's), and
// the environment that makes git fetch the hub's URL from it: no test reaches the network.
func localHub(t *testing.T, files map[string]string) (dir string, env []string) {
	t.Helper()
	dir = t.TempDir()
	gitIn2(t, dir, "init", "-q", "-b", "main")
	for name, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	gitIn2(t, dir, "add", "-A")
	gitIn2(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "hub")
	// A git that fetches the hub's URL from dir. `remote` commands run plain: they must keep reporting the
	// URL PenguPool recorded (git expands insteadOf there too), which is what identity is read from.
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = remote ] && exec " + real + " \"$@\"; done\n" +
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=url." + dir + ".insteadOf GIT_CONFIG_VALUE_0=" + mainMarketplace + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, os.Environ()
}

func gitIn2(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// The catalog is the Helper hub, and it is read with the account's own credentials.
func TestHubIsTheCatalog(t *testing.T) {
	if mainMarketplace != "https://github.com/nduwork/pengupool-agent-hub.git" || repoKey(mainMarketplace) != "github.com/nduwork/pengupool-agent-hub" {
		t.Fatal(mainMarketplace)
	}
	if err := catalogFolder("https://github.com/nduwork/pengupool-agent-hub", "agents/code/x", "code", "x"); err != nil {
		t.Fatalf("the hub keeps helpers in agents/<group>/<name>: %v", err)
	}
	// A refused clone of the private hub says what to do; other failures pass through.
	got := hubAccess(errors.New("git clone failed: remote: Repository not found."), mainMarketplace)
	if got == nil || !strings.Contains(got.Error(), "private") || !strings.Contains(got.Error(), "Settings → GitHub") {
		t.Fatalf("%v", got)
	}
	if other := errors.New("git clone failed: could not resolve host"); hubAccess(other, mainMarketplace) != other {
		t.Fatal("a network failure was reworded")
	}
}

// helperClone makes a helper PenguPool cloned: a folder of a repository under the store's skills, with a commit.
func helperClone(t *testing.T, st *store.Store, name, origin string) (clone, sub, head string) {
	t.Helper()
	clone = filepath.Join(st.Dir(), "skills", "code", name)
	sub = filepath.Join(clone, "agents", "code", name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Use for "+name+".\n---\n# "+name+"\n"), 0o644)
	gitIn2(t, clone, "init", "-q")
	gitIn2(t, clone, "remote", "add", "origin", origin)
	gitIn2(t, clone, "add", "-A")
	gitIn2(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x")
	return clone, sub, gitIn2(t, clone, "rev-parse", "HEAD")
}

const hubSkill = "---\nname: x\ndescription: Use for x.\n---\n# x\n"

// First start after the move: the old catalog clone gives way to a fresh clone of the hub, and helpers
// cloned from the old repository, their keys and journals and the deleted markers follow; ids stay;
// running again changes nothing.
func TestMigrateHub(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub, env := localHub(t, map[string]string{"README.md": "hub"})
	p := New(Options{Store: st, Exe: "pengupool", Env: env})
	if p.db == nil {
		t.Fatal("no database")
	}
	ctx := context.Background()
	oldKey, newKey := repoKey(legacyMarketplace), repoKey(mainMarketplace)

	// The old catalog clone, with the old repository's history.
	oldCat := p.marketDirFor(legacyMarketplace)
	if err := os.MkdirAll(oldCat, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn2(t, oldCat, "init", "-q")
	gitIn2(t, oldCat, "remote", "add", "origin", legacyMarketplace)

	// A helper PenguPool cloned from it (a folder of the repository, under the store's skills).
	clone, sub, _ := helperClone(t, st, "x", "https://github.com/nduwork/pengupool-marketplace.git")
	// And one of the owner's own checkouts of the old repository, which is theirs.
	mine := t.TempDir()
	gitIn2(t, mine, "init", "-q")
	gitIn2(t, mine, "remote", "add", "origin", legacyMarketplace)

	oldAgent := oldKey + "/agents/code/x"
	_ = st.Update(func(s *store.State) error {
		s.Domain.Skills = append(s.Domain.Skills,
			protocol.Skill{ID: "sk1", Name: "x", Path: sub, Source: "git", Key: oldAgent, Commit: "abc"},
			protocol.Skill{ID: "sk2", Name: "mine", Path: mine, Source: "git", Key: oldKey})
		return nil
	})
	if _, err := p.db.AddExperience(agentdb.Entry{Helper: oldAgent, At: "2026-10-01T10:00:00Z", Title: "a finished job", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.SetSetting(deletedKey(oldKey+"/agents/code/gone"), "1"); err != nil {
		t.Fatal(err)
	}

	for run := 0; run < 2; run++ {
		p.migrateHub(ctx)
		if _, err := os.Stat(oldCat); err == nil {
			t.Fatalf("run %d: the old catalog clone is still there", run)
		}
		if got := gitIn2(t, p.marketDir(), "remote", "get-url", "origin"); got != mainMarketplace {
			t.Fatalf("run %d: catalog origin = %s", run, got)
		}
		if got, want := gitIn2(t, p.marketDir(), "rev-parse", "HEAD"), gitIn2(t, hub, "rev-parse", "HEAD"); got != want {
			t.Fatalf("run %d: the catalog is not the hub's", run)
		}
		if got := gitIn2(t, clone, "remote", "get-url", "origin"); got != "https://github.com/nduwork/pengupool-agent-hub.git" {
			t.Fatalf("run %d: helper origin = %s", run, got)
		}
		d := st.State().Domain
		sk := findSkill(&d, "sk1")
		if sk == nil || sk.Key != newKey+"/agents/code/x" || sk.Commit != "abc" || sk.Path != sub {
			t.Fatalf("run %d: helper = %+v", run, sk)
		}
		if mk := findSkill(&d, "sk2"); mk.Key != oldKey || gitIn2(t, mine, "remote", "get-url", "origin") != legacyMarketplace {
			t.Fatalf("run %d: the owner's own checkout was touched: %+v", run, mk)
		}
		if es, _ := p.db.ListExperience(newKey + "/agents/code/x"); len(es) != 1 {
			t.Fatalf("run %d: the journal did not follow: %d entries", run, len(es))
		}
		if es, _ := p.db.ListExperience(oldAgent); len(es) != 0 {
			t.Fatalf("run %d: entries left under the old key", run)
		}
		if p.db.Setting(deletedKey(newKey+"/agents/code/gone")) != "1" || p.db.Setting(deletedKey(oldKey+"/agents/code/gone")) != "" {
			t.Fatalf("run %d: the deleted marker did not move", run)
		}
	}
}

// When the hub's clone is already there, the old catalog clone is dropped, not left as a second catalog.
func TestMigrateHubDropsTheOldCatalogWhenTheNewOneExists(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	hub, env := localHub(t, map[string]string{"README.md": "hub"})
	p := New(Options{Store: st, Exe: "pengupool", Env: env})
	d := p.marketDir()
	_ = os.MkdirAll(filepath.Dir(d), 0o755)
	if out, err := exec.Command("git", "clone", "-q", hub, d).CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	gitIn2(t, d, "remote", "set-url", "origin", mainMarketplace)
	old := p.marketDirFor(legacyMarketplace)
	_ = os.MkdirAll(old, 0o755)
	gitIn2(t, old, "init", "-q")
	gitIn2(t, old, "remote", "add", "origin", legacyMarketplace)
	p.migrateHub(context.Background())
	if _, err := os.Stat(old); err == nil {
		t.Fatal("two catalogs left")
	}
	if got := gitIn2(t, d, "remote", "get-url", "origin"); got != mainMarketplace {
		t.Fatal(got)
	}
}

// The hub's history is not the old repository's. A catalog clone must not be carried over: its fast-forward
// pull fails for good and its entries still name the old URLs. A clone with the hub's name but the old
// origin or an unrelated history is cloned again, at every start.
func TestMigrateHubClonesTheCatalogAgain(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	hub, env := localHub(t, map[string]string{"README.md": "hub"})
	p := New(Options{Store: st, Exe: "pengupool", Env: env})
	ctx := context.Background()
	want := gitIn2(t, hub, "rev-parse", "HEAD")
	d := p.marketDir()
	_ = os.MkdirAll(d, 0o755)

	// 1. The old repository's clone sitting under the hub's name, origin still the old one.
	gitIn2(t, d, "init", "-q", "-b", "main")
	gitIn2(t, d, "remote", "add", "origin", legacyMarketplace)
	gitIn2(t, d, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "old")
	p.migrateHub(ctx)
	if got := gitIn2(t, d, "rev-parse", "HEAD"); got != want || gitIn2(t, d, "remote", "get-url", "origin") != mainMarketplace {
		t.Fatalf("1: the clone was kept: %s", got)
	}

	// 2. The hub's origin but a history of its own (the old repository's, re-pointed by hand or by an earlier build).
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(d, 0o755)
	gitIn2(t, d, "init", "-q", "-b", "main")
	gitIn2(t, d, "remote", "add", "origin", mainMarketplace)
	gitIn2(t, d, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "old")
	gitIn2(t, d, "fetch", "-q", "origin")
	gitIn2(t, d, "remote", "set-head", "origin", "main")
	p.migrateHub(ctx)
	if got := gitIn2(t, d, "rev-parse", "HEAD"); got != want {
		t.Fatalf("2: an unrelated history was kept: %s", got)
	}

	// 3. A clone that follows the hub's history is left alone (a local commit proves it was not cloned again).
	gitIn2(t, d, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "ahead")
	ahead := gitIn2(t, d, "rev-parse", "HEAD")
	p.migrateHub(ctx)
	if got := gitIn2(t, d, "rev-parse", "HEAD"); got != ahead {
		t.Fatal("3: a related clone was cloned again")
	}
}

// A run cut short at any step is finished by the next start: a helper re-pointed before its key moved, a
// key moved before its journal, and journal rows an un-upgraded machine wrote under the old key afterwards.
func TestMigrateHubFinishesAnInterruptedRun(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	p := New(Options{Store: st, Exe: "pengupool"})
	ctx := context.Background()
	oldKey, newKey := repoKey(legacyMarketplace), repoKey(mainMarketplace)
	journal := func(key string) int { es, _ := p.db.ListExperience(key); return len(es) }
	add := func(key, title string) {
		if _, err := p.db.AddExperience(agentdb.Entry{Helper: key, At: "2026-10-01T10:00:00Z", Title: title, Body: title}); err != nil {
			t.Fatal(err)
		}
	}
	hubURL := "https://github.com/nduwork/pengupool-agent-hub.git"
	// 1. A helper whose remote was re-pointed but whose key (and journal) are still the old ones.
	_, sub2, _ := helperClone(t, st, "a", hubURL)
	// 2. A helper whose key moved but whose journal did not.
	_, sub3, _ := helperClone(t, st, "b", hubURL)
	// 3. A helper fully on the hub, with rows an un-upgraded machine wrote under the old key since.
	_, sub4, _ := helperClone(t, st, "c", hubURL)
	_ = st.Update(func(s *store.State) error {
		s.Domain.Skills = append(s.Domain.Skills,
			protocol.Skill{ID: "a", Name: "a", Path: sub2, Source: "git", Key: oldKey + "/agents/code/a"},
			protocol.Skill{ID: "b", Name: "b", Path: sub3, Source: "git", Key: newKey + "/agents/code/b"},
			protocol.Skill{ID: "c", Name: "c", Path: sub4, Source: "git", Key: newKey + "/agents/code/c"})
		return nil
	})
	add(oldKey+"/agents/code/a", "job a")
	add(oldKey+"/agents/code/b", "job b")
	add(newKey+"/agents/code/c", "job c")
	add(oldKey+"/agents/code/c", "late job c")

	p.migrateHub(ctx)
	p.migrateHub(ctx)
	d := st.State().Domain
	for _, c := range []struct {
		id   string
		want int
	}{{"a", 1}, {"b", 1}, {"c", 2}} {
		k := newKey + "/agents/code/" + c.id
		if sk := findSkill(&d, c.id); sk.Key != k {
			t.Fatalf("%s: key %s", c.id, sk.Key)
		}
		if n := journal(k); n != c.want || journal(oldKey+"/agents/code/"+c.id) != 0 {
			t.Fatalf("%s: %d entries under the hub key, %d under the old", c.id, n, journal(oldKey+"/agents/code/"+c.id))
		}
	}
}

// The owner's own checkout of the old repository still holds the old key: the journal rows it keeps writing
// there are its own, and the straggler rekey of the hub copy leaves them.
func TestMigrateHubLeavesJournalRowsOfAnOwnersCheckout(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	p := New(Options{Store: st, Exe: "pengupool"})
	oldKey, newKey := repoKey(legacyMarketplace), repoKey(mainMarketplace)
	_, sub, _ := helperClone(t, st, "x", "https://github.com/nduwork/pengupool-agent-hub.git")
	mine := t.TempDir()
	gitIn2(t, mine, "init", "-q")
	gitIn2(t, mine, "remote", "add", "origin", legacyMarketplace)
	_ = st.Update(func(s *store.State) error {
		s.Domain.Skills = append(s.Domain.Skills,
			protocol.Skill{ID: "hub", Name: "x", Path: sub, Source: "git", Key: newKey + "/agents/code/x"},
			protocol.Skill{ID: "mine", Name: "x-mine", Path: mine, Source: "git", Key: oldKey + "/agents/code/x"})
		return nil
	})
	if _, err := p.db.AddExperience(agentdb.Entry{Helper: oldKey + "/agents/code/x", At: "2026-10-01T10:00:00Z", Title: "owner's own work", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	p.migrateHub(context.Background())
	if es, _ := p.db.ListExperience(oldKey + "/agents/code/x"); len(es) != 1 {
		t.Fatal("the owner's checkout lost its journal row")
	}
	if es, _ := p.db.ListExperience(newKey + "/agents/code/x"); len(es) != 0 {
		t.Fatal("the row was copied to the hub's helper")
	}
}

// The real case: the hub's history is unrelated to the old repository's. An install has the old catalog clone
// and helpers at old pins; a helper the owner deleted has its marker. After the migration, a sync against the
// hub updates the helpers to the hub's pins, installs nothing again, and the deleted helper stays deleted.
// Without the migration the old catalog (old URLs) matches nothing and the sync fails on every helper.
func TestMigrateHubThenSyncAgainstAnUnrelatedHub(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	hub, env := localHub(t, map[string]string{"agents/code/x/SKILL.md": hubSkill})
	pin := gitIn2(t, hub, "rev-parse", "HEAD")
	entry := func(name, sha string) string {
		return `{"name":"` + name + `","description":"d","category":"code","source":{"source":"git-subdir","url":"` + mainMarketplace + `","path":"agents/code/` + name + `","ref":"main","sha":"` + sha + `"}}`
	}
	hubEntry := func(name, sha string) string {
		return `{"name":"` + name + `","description":"d","group":"code","repo":"` + mainMarketplace + `","path":"agents/code/` + name + `","ref":"main","sha":"` + sha + `"}`
	}
	_ = os.MkdirAll(filepath.Join(hub, ".pengupool"), 0o755)
	_ = os.WriteFile(filepath.Join(hub, marketFile),
		[]byte(`{"name":"pengupool-agent-hub","agents":[`+hubEntry("x", pin)+`,`+hubEntry("gone", strings.Repeat("a", 40))+`]}`), 0o644)
	gitIn2(t, hub, "add", "-A")
	gitIn2(t, hub, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "catalog")

	p := New(Options{Store: st, Exe: "pengupool", Env: env})
	ctx := context.Background()
	oldKey := repoKey(legacyMarketplace)

	// The install before the move: the old repository's catalog clone, with its own history and old URLs.
	oldCat := p.marketDirFor(legacyMarketplace)
	_ = os.MkdirAll(filepath.Join(oldCat, ".claude-plugin"), 0o755)
	oldEntry := strings.ReplaceAll(entry("x", strings.Repeat("b", 40)), mainMarketplace, legacyMarketplace)
	_ = os.WriteFile(filepath.Join(oldCat, ".claude-plugin", "marketplace.json"), []byte(`{"name":"pengupool-marketplace","plugins":[`+oldEntry+`]}`), 0o644)
	gitIn2(t, oldCat, "init", "-q", "-b", "main")
	gitIn2(t, oldCat, "remote", "add", "origin", legacyMarketplace)
	gitIn2(t, oldCat, "add", "-A")
	gitIn2(t, oldCat, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "old catalog")
	// A helper at an old pin (a commit of the old history), and the deleted one's marker.
	clone, sub, _ := helperClone(t, st, "x", "https://github.com/nduwork/pengupool-marketplace.git")
	_ = st.Update(func(s *store.State) error {
		s.Domain.Skills = append(s.Domain.Skills, protocol.Skill{ID: "sk1", Name: "x", Path: sub, Source: "git", Key: oldKey + "/agents/code/x", Group: "code", Category: "code"})
		return nil
	})
	if err := p.db.SetSetting(deletedKey(oldKey+"/agents/code/gone"), "1"); err != nil {
		t.Fatal(err)
	}

	p.migrateHub(ctx)
	if _, err := p.pullCatalog(ctx); err != nil {
		t.Fatalf("the catalog cannot be pulled: %v", err)
	}
	out := p.syncLibrary(ctx)
	if len(out["added"]) != 0 || len(out["failed"]) != 0 || len(out["updated"]) != 1 {
		t.Fatalf("the sync: %v", out)
	}
	d := st.State().Domain
	if len(d.Skills) != 1 || d.Skills[0].ID != "sk1" || d.Skills[0].Path != sub {
		t.Fatalf("the helper was duplicated or moved: %+v", d.Skills)
	}
	if got := gitIn2(t, clone, "rev-parse", "HEAD"); got != pin {
		t.Fatalf("the helper is at %s, not the hub's pin", got)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), "skills", "code", "gone")); err == nil {
		t.Fatal("the deleted helper came back")
	}
}
