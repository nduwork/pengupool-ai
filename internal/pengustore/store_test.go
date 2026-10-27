package store

import (
	"path/filepath"
	"testing"
)

func TestHomeFollowsEnv(t *testing.T) {
	t.Setenv("PENGUPOOL_AI_HOME", "/x/y")
	if Home() != "/x/y" {
		t.Fatalf("home %q", Home())
	}
	t.Setenv("PENGUPOOL_AI_HOME", "")
	t.Setenv("HOME", "/h")
	if Home() != filepath.Join("/h", ".pengupool-ai") {
		t.Fatalf("default home %q", Home())
	}
}
