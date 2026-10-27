package session

import (
	"errors"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

func testEnv(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
}

func TestPrefixCarriesLines(t *testing.T) {
	testEnv(t)
	if _, err := compass.Add("Always run the test suite before committing.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	if _, err := compass.Add("Never touch the migrations folder.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	got, err := Prefix("should I run the tests?")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Owner's Compass", "test suite", "experience_for"} {
		if !contains(got, want) {
			t.Fatalf("prefix missing %q:\n%s", want, got)
		}
	}
}

func TestPrefixEmptyStore(t *testing.T) {
	testEnv(t)
	got, err := Prefix("anything")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got, "experience_for") || contains(got, "Owner's Compass") {
		t.Fatalf("hint only expected, got:\n%s", got)
	}
}

func TestNewRejects(t *testing.T) {
	testEnv(t)
	if _, err := New("bogus", "", "", false); err == nil {
		t.Fatal("bad agent must fail")
	}
	old := LookPath
	LookPath = func(string) (string, error) { return "", errors.New("missing") }
	defer func() { LookPath = old }()
	if _, err := New("claude", "", "", false); err == nil {
		t.Fatal("missing tmux must fail")
	}
}

func TestStopMissingAndLsEmpty(t *testing.T) {
	testEnv(t)
	ok, err := Stop("nope")
	if err != nil || ok {
		t.Fatalf("stop missing: %v %v", ok, err)
	}
	rows, err := Ls()
	if err != nil || len(rows) != 0 {
		t.Fatalf("ls empty: %+v %v", rows, err)
	}
}

func TestShellQuote(t *testing.T) {
	if shellQuote("simple") != "simple" || shellQuote("") != "''" {
		t.Fatal("safe quoting")
	}
	if q := shellQuote("a b'c"); q != "'a b'\"'\"'c'" {
		t.Fatalf("got %q", q)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
