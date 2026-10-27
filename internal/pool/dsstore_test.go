package pool

import "testing"

func TestRealChangesIgnoresDSStore(t *testing.T) {
	out := "?? agents/team/.DS_Store\n?? agents/team/x/.DS_Store\n"
	if got := realChanges(out); got != "" {
		t.Fatalf("Finder files counted as changes: %q", got)
	}
	if got := realChanges(out + " M agents/team/x/AGENTS.md\n!! .DS_Store\n"); got != "M agents/team/x/AGENTS.md" {
		t.Fatalf("got %q", got)
	}
}
