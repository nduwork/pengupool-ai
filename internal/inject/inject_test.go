package inject

import (
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

func lines() []*compass.Line {
	return []*compass.Line{
		{ID: "a", Rule: "Run the test suite before committing.", Confidence: 1.0},
		{ID: "b", Rule: "Never touch the migrations folder.", Confidence: 1.0},
		{ID: "c", Rule: "Prefer small commits.", Confidence: 0.1},
	}
}

func ids(ls []*compass.Line) []string {
	out := []string{}
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSelect(t *testing.T) {
	// Relevant only; weak + off-topic excluded.
	if got := ids(Select(lines(), "should I run the tests now?", 8, 2000)); !equal(got, []string{"a"}) {
		t.Fatalf("relevant: %v", got)
	}
	// Non-empty context, zero overlap: nothing.
	if got := Select(lines(), "something entirely different here", 8, 2000); len(got) != 0 {
		t.Fatalf("off-topic: %v", ids(got))
	}
	// Empty context: confidence order.
	if got := ids(Select(lines(), "", 8, 2000)); !equal(got, []string{"a", "b"}) {
		t.Fatalf("empty: %v", got)
	}
	if got := Select(nil, "x", 8, 2000); len(got) != 0 {
		t.Fatalf("nil: %v", ids(got))
	}
	// Budget cap keeps the first line.
	if got := ids(Select(lines(), "", 8, 10)); !equal(got, []string{"a"}) {
		t.Fatalf("budget: %v", got)
	}
}
