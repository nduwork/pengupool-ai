package compass

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/pengustore"
)

func testHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	// Keep the legacy-migration path out of unit tests.
	t.Setenv("PENGUPOOL_MUSE_HOME", filepath.Join(h, "no-legacy"))
	return h
}

func TestAddLoadRoundTrip(t *testing.T) {
	testHome(t)
	l, err := Add("run the tests without asking", "typed", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.ID) != 12 {
		t.Fatalf("id %q is not 12 hex chars", l.ID)
	}
	lines, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Rule != "run the tests without asking" {
		t.Fatalf("load got %+v", lines)
	}
	if got := FormatLines(lines); got != "- run the tests without asking" {
		t.Fatalf("format got %q", got)
	}
}

func TestNormalizeClamps(t *testing.T) {
	l := Normalize(&Line{ID: "x", Confidence: 9})
	if l.Confidence != 1.0 || l.Evidence == nil {
		t.Fatalf("normalize got %+v", l)
	}
}

func TestEvidenceMath(t *testing.T) {
	testHome(t)
	l, err := Add("small commits", "typed", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	now := l.Updated.(float64)
	up, err := UpdateEvidence(l.ID, "supports", "yes small", "s1", now)
	if err != nil || up == nil {
		t.Fatal(err)
	}
	if diff := up.Confidence - 0.65; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("supports: got %v want 0.65", up.Confidence)
	}
	up, err = UpdateEvidence(l.ID, "contradicts", "no big", "s1", now)
	if err != nil || up == nil {
		t.Fatal(err)
	}
	if diff := up.Confidence - 0.35; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("contradicts: got %v want 0.35", up.Confidence)
	}
	if len(up.Evidence) != 2 {
		t.Fatalf("evidence len %d", len(up.Evidence))
	}
	// 90 days later support halves 0.35 then adds 0.15.
	up, err = UpdateEvidence(l.ID, "supports", "yes", "s1", now+90*86400)
	if err != nil || up == nil {
		t.Fatal(err)
	}
	if diff := up.Confidence - 0.325; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("aged supports: got %v want 0.325", up.Confidence)
	}
}

func TestForget(t *testing.T) {
	testHome(t)
	l, _ := Add("x", "typed", 1.0)
	ok, err := Forget(l.ID)
	if err != nil || !ok {
		t.Fatalf("forget got %v %v", ok, err)
	}
	ok, _ = Forget(l.ID)
	if ok {
		t.Fatal("second forget should be false")
	}
	if _, err := UpdateEvidence("missing", "supports", "w", "s", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateEvidence(l.ID, "bogus", "w", "s", 0); err == nil {
		t.Fatal("bad effect must fail")
	}
}

func TestLegacyMigration(t *testing.T) {
	h := testHome(t)
	legacy := t.TempDir()
	t.Setenv("PENGUPOOL_MUSE_HOME", legacy)
	if err := os.WriteFile(filepath.Join(legacy, "compass.jsonl"),
		[]byte("{\"id\":\"abc\",\"rule\":\"old line\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = store.Ensure()
	lines, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	_ = h
	if len(lines) != 1 || lines[0].Rule != "old line" || lines[0].Confidence != 1.0 {
		t.Fatalf("migrated %+v", lines)
	}
}
