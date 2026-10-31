package pool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
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

// contraDecider says two lines contradict when the question names both of a pair in clash.
type contraDecider struct {
	clash [][2]string
	mu    sync.Mutex
	calls int
}

func (d *contraDecider) Name() string { return "contra" }
func (d *contraDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	out := map[string]decision.Answer{}
	for _, q := range req.Questions {
		a := decision.Answer{Type: decision.Predicate}
		for _, c := range d.clash {
			if strings.Contains(fmt.Sprint(q.Instructions), c[0]) && strings.Contains(fmt.Sprint(q.Instructions), c[1]) {
				a.Yes = 0.95
			}
		}
		out[q.Name] = a
	}
	return decision.Response{Answers: out, Calibrated: true}, nil
}

func TestCompassReviewConflicts(t *testing.T) {
	p, _ := twoTeams(t)
	if _, err := p.agentDB(); err != nil {
		t.Fatal(err)
	}
	items := func() []reviewItem {
		t.Helper()
		r, err := p.compassReview(nil, struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		var out []reviewItem
		for _, it := range r.(map[string]any)["items"].([]reviewItem) {
			if it.Kind == "conflict" {
				out = append(out, it)
			}
		}
		return out
	}
	yellow := stated(t, p, "The owner's favorite color is yellow")
	red, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: "The owner's favorite color is red", Condition: "on weekends", Origin: "stated"})
	if err != nil {
		t.Fatal(err)
	}
	green := stated(t, p, "The owner's favorite color is green")
	stated(t, p, "The owner's favorite food is rice") // a neighbour that agrees with them all
	if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Tag: "perm:allow:Bash:ls", Body: "The owner allows Bash for ls, the favorite color", Origin: "stated"}); err != nil {
		t.Fatal(err)
	}

	// No decision model: no conflict items.
	if got := items(); len(got) != 0 {
		t.Fatalf("no decider: %+v", got)
	}

	// yellow–red and red–green contradict (yellow–green not asked as such): one item of three lines,
	// each with its condition; a permission's line and the agreeing line stay out.
	d := &contraDecider{clash: [][2]string{{"yellow", "red"}, {"red", "green"}}}
	p.testDecider = d
	got := items()
	if len(got) != 1 || len(got[0].Lines) != 3 || len(got[0].Edges) != 2 {
		t.Fatalf("one item of three lines and two pairs: %+v", got)
	}
	cond := map[string]string{}
	for _, l := range got[0].Lines {
		cond[l.UID] = l.Condition
		if l.UpdatedAt == "" {
			t.Fatalf("no updated_at: %+v", l)
		}
	}
	if _, ok := cond[yellow]; !ok || cond[red] != "on weekends" || len(cond) != 3 {
		t.Fatalf("the lines: %+v", got[0].Lines)
	}
	if _, ok := cond[green]; !ok {
		t.Fatal("green is not in the item")
	}

	// A reload with the same lines asks no model again.
	calls := d.calls
	items()
	if d.calls != calls {
		t.Fatal("the review asked the model again for the same lines")
	}

	// Stale: a line edited after the review is refused, and nothing goes.
	effects := func(uid string) []agentdb.Effect { es, _ := p.db.Effects(uid); return es[uid] }
	stale := got[0].Lines
	if _, err := p.compassSave(nil, compassSaveIn{UID: green, Body: "The owner's favorite color is green, mostly"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, p, true, "compass.keep_one", compassKeepIn{Keep: yellow, Lines: stale, Edges: got[0].Edges}); err == nil || !strings.Contains(err.Error(), "refresh") {
		t.Fatalf("a stale item was applied: %v", err)
	}
	if _, err := p.db.Line(red); err != nil {
		t.Fatal("a stale apply deleted a line")
	}

	// Refreshed: keep yellow. Red, which contradicts it, goes; green contradicted only red, so it
	// stays; yellow is unchanged (body, condition, effects).
	got = items()
	if len(got) != 1 {
		t.Fatalf("after the edit: %+v", got)
	}
	sig := agentdb.Signal{Kind: "said", Words: "yellow", SessionID: "leadaaaaaaaa"}
	p.record(sig, agentdb.WeightSaid, effectOn{yellow, 1, 0}, effectOn{red, 0, 1})
	before, _ := p.db.Line(yellow)
	beforeEff := effects(yellow)
	if _, err := call(t, p, false, "compass.keep_one", compassKeepIn{Keep: yellow, Lines: got[0].Lines, Edges: got[0].Edges}); err == nil {
		t.Fatal("an agent resolved a conflict")
	}
	if _, err := call(t, p, true, "compass.keep_one", compassKeepIn{Keep: "nope", Lines: got[0].Lines, Edges: got[0].Edges}); err == nil {
		t.Fatal("kept a line not in the item")
	}
	if _, err := call(t, p, true, "compass.keep_one", compassKeepIn{Keep: yellow, Lines: got[0].Lines, Edges: [][2]string{{red, "nope"}}}); err == nil {
		t.Fatal("a pair naming a line not in the item was taken")
	}
	if _, err := call(t, p, true, "compass.keep_one", compassKeepIn{Keep: yellow, Lines: got[0].Lines, Edges: got[0].Edges}); err != nil {
		t.Fatal(err)
	}
	after, err := p.db.Line(yellow)
	if err != nil || after.Body != before.Body || after.Condition != before.Condition || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("yellow changed: %+v → %+v (%v)", before, after, err)
	}
	if a := effects(yellow); len(a) != len(beforeEff) || a[0].SignalUID != beforeEff[0].SignalUID || a[0].PSup != beforeEff[0].PSup {
		t.Fatalf("yellow's effects: %+v → %+v", beforeEff, a)
	}
	if _, err := p.db.Line(red); err == nil {
		t.Fatal("red was not deleted")
	}
	if _, err := p.db.Line(green); err != nil {
		t.Fatal("green, which never contradicted yellow, was deleted")
	}
	if got := items(); len(got) != 0 {
		t.Fatalf("after keep-one: %+v", got)
	}
}
