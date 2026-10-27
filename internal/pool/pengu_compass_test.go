package pool

import (
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

func TestPenguCompassRidesAlong(t *testing.T) {
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
	if got := penguCompass(); got != "" {
		t.Fatalf("empty store must add nothing, got %q", got)
	}
	if _, err := compass.Add("Run the tests without asking.", "typed", 1.0); err != nil {
		t.Fatal(err)
	}
	if _, err := compass.Add("Weak line.", "typed", 0.1); err != nil {
		t.Fatal(err)
	}
	got := penguCompass()
	if !strings.Contains(got, "PenguPool AI") || !strings.Contains(got, "Run the tests") {
		t.Fatalf("confirmed line missing: %q", got)
	}
	if strings.Contains(got, "Weak line") {
		t.Fatalf("below-floor line must not ride along: %q", got)
	}
}
