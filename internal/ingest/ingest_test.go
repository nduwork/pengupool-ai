package ingest

import (
	"errors"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/judge"
)

func stubAll() func() {
	oldC, oldR, oldG, oldL, oldE, oldP := Classify, Relate, Generalize, LoadLines, RecordEvidence, Propose
	return func() {
		Classify, Relate, Generalize, LoadLines, RecordEvidence, Propose = oldC, oldR, oldG, oldL, oldE, oldP
	}
}

func TestIngestEmpty(t *testing.T) {
	defer stubAll()()
	if _, _, err := Ingest("  ", "s", ""); err == nil {
		t.Fatal("empty words must fail")
	}
}

func TestIngestNewLine(t *testing.T) {
	defer stubAll()()
	Classify = func(words, m string) (judge.Classification, error) {
		return judge.Classification{Kind: "preference"}, nil
	}
	LoadLines = func() ([]*compass.Line, error) { return nil, nil }
	Generalize = func(words, m string) (string, error) { return "Run tests first", nil }
	Propose = func(words, kind string, ref map[string]any) (string, error) {
		if kind != "new-line" {
			t.Fatalf("kind %q", kind)
		}
		return "p1", nil
	}
	pid, msg, err := Ingest("from now on run tests", "s", "")
	if err != nil || pid != "p1" || msg != "proposed new line: Run tests first" {
		t.Fatalf("got %q %q %v", pid, msg, err)
	}
}

func TestIngestSupports(t *testing.T) {
	defer stubAll()()
	existing := &compass.Line{ID: "abc", Rule: "Run tests", Confidence: 0.5}
	Classify = func(words, m string) (judge.Classification, error) {
		return judge.Classification{Kind: "preference"}, nil
	}
	LoadLines = func() ([]*compass.Line, error) { return []*compass.Line{existing}, nil }
	Relate = func(words, line, m string) (judge.Relation, error) {
		return judge.Relation{Relation: "supports", Confidence: 0.9}, nil
	}
	recorded := false
	RecordEvidence = func(id, effect, words, session string, now float64) (*compass.Line, error) {
		recorded = true
		return existing, nil
	}
	Propose = func(words, kind string, ref map[string]any) (string, error) {
		t.Fatal("supports records evidence, proposes nothing")
		return "", nil
	}
	pid, msg, err := Ingest("yes run tests", "s", "")
	if err != nil || pid != "" || !recorded || msg != "supports line abc: evidence recorded" {
		t.Fatalf("got %q %q %v", pid, msg, err)
	}
}

func TestIngestRetire(t *testing.T) {
	defer stubAll()()
	existing := &compass.Line{ID: "abc", Rule: "Skip tests", Confidence: 0.2}
	Classify = func(words, m string) (judge.Classification, error) {
		return judge.Classification{Kind: "correction"}, nil
	}
	LoadLines = func() ([]*compass.Line, error) { return []*compass.Line{existing}, nil }
	Relate = func(words, line, m string) (judge.Relation, error) {
		return judge.Relation{Relation: "contradicts", Confidence: 0.9}, nil
	}
	Generalize = func(words, m string) (string, error) { return "Run tests", nil }
	RecordEvidence = func(id, effect, words, session string, now float64) (*compass.Line, error) {
		low := *existing
		low.Confidence = 0.2
		return &low, nil
	}
	var gotKind string
	Propose = func(words, kind string, ref map[string]any) (string, error) {
		gotKind = kind
		return "p9", nil
	}
	pid, msg, err := Ingest("you should have run tests", "s", "")
	if err != nil || pid != "p9" || gotKind != "retire" {
		t.Fatalf("got %q %q kind=%q %v", pid, msg, gotKind, err)
	}
}

func TestIngestNoMatchNote(t *testing.T) {
	defer stubAll()()
	Classify = func(words, m string) (judge.Classification, error) {
		return judge.Classification{Kind: "choice"}, nil
	}
	Propose = func(words, kind string, ref map[string]any) (string, error) { return "p2", nil }
	pid, msg, err := Ingest("ship it?", "s", "")
	if err != nil || pid != "p2" || msg != "proposed note (choice/fyi: owner decides)" {
		t.Fatalf("got %q %q %v", pid, msg, err)
	}
}

func TestIngestClassifyError(t *testing.T) {
	defer stubAll()()
	Classify = func(words, m string) (judge.Classification, error) {
		return judge.Classification{}, errors.New("down")
	}
	if _, _, err := Ingest("x", "s", ""); err == nil {
		t.Fatal("model error must propagate")
	}
}
