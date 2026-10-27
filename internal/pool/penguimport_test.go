package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The one-time pengu import: temp pengu home against the default store
// (PENGUPOOL_HOME/v2), then a rerun, then a non-default store.
func TestPenguImport(t *testing.T) {
	root := t.TempDir()
	ai := filepath.Join(root, "ai")
	if err := os.MkdirAll(ai, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENGUPOOL_AI_HOME", ai)
	// The real store is the owner's own home with no PENGUPOOL_HOME.
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("PENGUPOOL_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	compass := `{"id":"a1","rule":"Always run the tests before committing","source":"app","created":"2026-10-27T08:32:56-0700","evidence":[],"confidence":1}` + "\n" +
		`{"id":"b2","rule":"Never touch prod","kind":"restriction","source":"app","created":"2026-10-27T08:32:56-0700","evidence":[],"confidence":1}` + "\n" +
		"{a truncated row\n" +
		`{"id":"c3","rule":"use password=hunter22! always","source":"app","created":"2026-10-27T08:32:56-0700","evidence":[],"confidence":1}` + "\n"
	if err := os.WriteFile(filepath.Join(ai, "compass.jsonl"), []byte(compass), 0o600); err != nil {
		t.Fatal(err)
	}
	exp := `{"id":"e1","helper":"planning-agent","title":"fixed the login","task":"login page","lesson":"keep the redirect target","evidence":[{"at":1793115000,"session":"s1","outcome":"worked"}]}` + "\n" +
		`{"id":"e2","helper":"ghost","title":"did a thing","task":"","lesson":"a raw lesson","evidence":[]}` + "\n"
	if err := os.WriteFile(filepath.Join(ai, "experience-plan-abc12345.jsonl"), []byte(exp), 0o600); err != nil {
		t.Fatal(err)
	}
	def, err := store.Open(store.DefaultDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := def.Update(func(s *store.State) error {
		s.Domain.Skills = append(s.Domain.Skills, protocol.Skill{ID: "sk-plan", Name: "planning-agent", Key: "github.com/o/planning-agent"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An explicit Env: the pool's own user PATH (Homebrew, npm) would find a real muse or codex.
	noAgents := []string{"PATH=/nonexistent", "HOME=" + filepath.Join(root, "home")}
	mk := func() *Pool { return New(Options{Store: def, Exe: "pengupool", Env: noAgents}) }
	p := mk()
	own := p.owner()

	lines, err := p.db.Lines(own, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]agentdb.Line{}
	for _, v := range lines {
		bodies[v.Body] = v
	}
	if l, ok := bodies["Always run the tests before committing"]; !ok || l.Source != "owner" || l.Kind != "preference" {
		t.Fatalf("line = %+v", bodies)
	}
	if l, ok := bodies["Never touch prod"]; !ok || l.Kind != "restriction" {
		t.Fatalf("restriction = %+v", bodies)
	}
	if _, ok := bodies["use password=hunter22! always"]; ok {
		t.Fatal("secret line was imported")
	}
	byTitle := map[string]agentdb.Entry{}
	for _, e := range entries(t, p) {
		got, err := p.db.ExperienceEntry(e.ID)
		if err != nil {
			t.Fatal(err)
		}
		byTitle[got.Title] = got
	}
	planEntry := byTitle["fixed the login"]
	if planEntry.Helper != "github.com/o/planning-agent" ||
		!strings.Contains(planEntry.Body, "keep the redirect target") || !strings.Contains(planEntry.Body, "login page") {
		t.Fatalf("plan entry = %+v", planEntry)
	}
	ghostList, err := p.db.ListExperience("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if len(ghostList) != 1 || ghostList[0].Title != "did a thing" {
		t.Fatalf("ghost entries = %+v", ghostList)
	}
	if g, err := p.db.ExperienceEntry(ghostList[0].ID); err != nil || !strings.Contains(g.Body, "a raw lesson") {
		t.Fatalf("ghost entry = %+v %v", g, err)
	}
	for _, f := range []string{"compass.jsonl", "experience-plan-abc12345.jsonl"} {
		if _, err := os.Stat(filepath.Join(ai, f)); !os.IsNotExist(err) {
			t.Fatalf("%s was not renamed", f)
		}
		if _, err := os.Stat(filepath.Join(ai, f+".imported")); err != nil {
			t.Fatalf("%s.imported missing: %v", f, err)
		}
	}

	// A rerun imports nothing.
	n0 := len(entries(t, p))
	l0, _ := p.db.Lines(own, "", "", 0)
	mk()
	if es := entries(t, p); len(es) != n0 {
		t.Fatalf("rerun added entries: %d -> %d", n0, len(es))
	}
	l1, _ := p.db.Lines(own, "", "", 0)
	if len(l1) != len(l0) {
		t.Fatalf("rerun added lines: %d -> %d", len(l0), len(l1))
	}

	// A non-default store imports nothing and renames nothing.
	ai2 := filepath.Join(root, "ai2")
	if err := os.MkdirAll(ai2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ai2, "compass.jsonl"), []byte(compass), 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENGUPOOL_AI_HOME", ai2)
	other := New(Options{Store: st2, Exe: "pengupool", Env: noAgents})
	ls, _ := other.db.Lines(other.owner(), "", "", 0)
	if len(ls) != 0 {
		t.Fatalf("non-default store imported: %+v", ls)
	}
	if _, err := os.Stat(filepath.Join(ai2, "compass.jsonl")); err != nil {
		t.Fatal("non-default store renamed the file")
	}

	// A store moved by PENGUPOOL_HOME is a throwaway, even at DefaultDir(): it never touches the real files.
	t.Setenv("PENGUPOOL_HOME", filepath.Join(root, "moved"))
	moved, err := store.Open(store.DefaultDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Store: moved, Exe: "pengupool", Env: noAgents})
	if ls, _ := m.db.Lines(m.owner(), "", "", 0); len(ls) != 0 {
		t.Fatalf("PENGUPOOL_HOME store imported: %+v", ls)
	}
	if _, err := os.Stat(filepath.Join(ai2, "compass.jsonl")); err != nil {
		t.Fatal("PENGUPOOL_HOME store renamed the file")
	}
}
