package pool

import (
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
)

func TestCompassReview(t *testing.T) {
	p, _ := twoTeams(t)
	if _, err := p.agentDB(); err != nil {
		t.Fatal(err)
	}
	add := func(way, pattern, shown string) string {
		verb := map[string]string{"allow": "allows", "deny": "does not allow"}[way]
		uid := p.newDecision(agentdb.Line{Tag: "perm:" + way + ":Bash:" + pattern, Body: "The owner " + verb + " Bash for `" + shown + "`"})
		if uid == "" {
			t.Fatalf("line %s not kept", pattern)
		}
		return uid
	}
	add("allow", "go test", "go test")
	rm, git, ghpr, ghrel := add("allow", "rm", "rm"), add("allow", "git", "git"), add("allow", "gh pr", "gh pr"), add("allow", "gh release", "gh release")
	pinned := add("allow", "mv", "mv")
	if _, err := p.compassSave(nil, compassSaveIn{UID: pinned, Body: "mv"}); err != nil {
		t.Fatal(err)
	}
	add("allow", "git status", "git status")
	// Whole commands kept before 0.11.4: two for `go test`, one compound, an exact script, a floor item.
	w1 := add("allow", "go test ./internal/pool -run X", "go test ./internal/pool -run X")
	w2 := add("allow", "go test ./cmd/... -count=1", "go test ./cmd/... -count=1")
	c1 := add("allow", "cd /work/app && git fetch -q origin", "cd /work/app && git fetch -q origin")
	script := add("allow", "=0123456789abcdef", "python3 x.py")
	floor := add("allow", "rm -rf /tmp/x", "rm -rf /tmp/x")

	r, err := p.compassReview(nil, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	items := r.(map[string]any)["items"].([]reviewItem)
	kinds := []string{}
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	// Broad ones first (bare words, git before rm is by name: git, rm), then the pairs, then merges.
	if got := strings.Join(kinds, " "); got != "broad broad broad broad merge merge" {
		t.Fatalf("kinds = %s", got)
	}
	var broad []string
	for _, it := range items[:4] {
		broad = append(broad, it.Lines[0].UID)
	}
	if want := []string{git, rm, ghpr, ghrel}; strings.Join(broad, ",") != strings.Join(want, ",") {
		t.Fatalf("broad order = %v, want %v", broad, want)
	}
	for _, it := range items {
		for _, l := range it.Lines {
			if l.UID == pinned || l.UID == script || l.UID == floor {
				t.Fatalf("a pinned, script or floor line was proposed: %+v", it)
			}
		}
	}
	// Listing changed nothing.
	if ls, _ := p.db.Lines(p.owner(), "", "", 0); len(ls) != 12 {
		t.Fatalf("review changed the Compass: %d lines", len(ls))
	}
	var goTest, compound reviewItem
	for _, it := range items[4:] {
		if len(it.Patterns) == 1 {
			goTest = it
		} else {
			compound = it
		}
	}
	if goTest.Patterns[0] != "go test" || len(goTest.Lines) != 2 || len(compound.Patterns) != 2 || compound.Patterns[0] != "cd" || compound.Patterns[1] != "git fetch" {
		t.Fatalf("merges = %+v / %+v", goTest, compound)
	}
	// A stale or invented request changes nothing.
	if _, err := p.compassApplyMerge(nil, compassApplyIn{From: []string{w1}, Patterns: []string{"go test"}}); err == nil {
		t.Fatal("applied a merge the owner was not shown")
	}
	if _, err := p.compassApplyMerge(nil, compassApplyIn{From: []string{w1, w2}, Patterns: []string{"go vet"}}); err == nil {
		t.Fatal("applied a merge with other patterns")
	}
	// Apply: the two lines fold into the existing `go test` line, and are gone.
	if _, err := p.compassApplyMerge(nil, compassApplyIn{From: []string{w1, w2}, Patterns: []string{"go test"}}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{w1, w2} {
		if _, err := p.db.Line(uid); err == nil {
			t.Fatalf("%s is still there", uid)
		}
	}
	if have, _ := p.db.Lines(p.owner(), "", "perm:allow:Bash:go test", 0); len(have) != 1 {
		t.Fatalf("go test lines = %d", len(have))
	}
	// The compound becomes a line per statement.
	if _, err := p.compassApplyMerge(nil, compassApplyIn{From: []string{c1}, Patterns: []string{"cd", "git fetch"}}); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"perm:allow:Bash:cd", "perm:allow:Bash:git fetch"} {
		if have, _ := p.db.Lines(p.owner(), "", tag, 0); len(have) != 1 {
			t.Fatalf("%s lines = %d", tag, len(have))
		}
	}
	// What was proposed no longer is; the broad lines wait for the owner.
	r, _ = p.compassReview(nil, struct{}{})
	if got := r.(map[string]any)["items"].([]reviewItem); len(got) != 4 {
		t.Fatalf("items after = %d", len(got))
	}
}

func TestCompassReviewFloorToolsAndPins(t *testing.T) {
	for _, pat := range []string{"npm", "dd", "find", "terraform", "aws", "helm", "podman", "git merge", "git branch", "docker push", "npm publish", "cargo", "docker buildx", "psql", "launchctl", "cp"} {
		if broadWhy(pat) == "" {
			t.Errorf("%q is not broad", pat)
		}
	}
	for _, pat := range []string{"go test", "git status", "git diff", "ls", "cd", "docker ps", "=0123456789abcdef", "git push origin main"} {
		if w := broadWhy(pat); w != "" {
			t.Errorf("%q is broad: %s", pat, w)
		}
	}
	// A broad line the owner saved stays off the review; the review marks the kept line the owner's.
	p, _ := twoTeams(t)
	if _, err := p.agentDB(); err != nil {
		t.Fatal(err)
	}
	add := func(tag, body string) string {
		return p.newDecision(agentdb.Line{Tag: "perm:allow:Bash:" + tag, Body: body})
	}
	narrow := add("kill -9 123", "kill -9 123")
	if _, err := p.compassApplyMerge(nil, compassApplyIn{From: []string{narrow}, Patterns: []string{"kill"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := p.compassReview(nil, struct{}{})
	if got := r.(map[string]any)["items"].([]reviewItem); len(got) != 0 {
		t.Fatalf("the owner's merged `kill` line was flagged: %+v", got)
	}
}
