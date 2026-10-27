package experience

import (
	"fmt"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/judge"
)

func testEnv(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
}

func TestSaveLoadRoundTrip(t *testing.T) {
	testEnv(t)
	e, err := Save("h", "Title", "do the thing", "always do the thing", "worked", "s1")
	if err != nil || len(e.ID) != 12 {
		t.Fatalf("save: %+v %v", e, err)
	}
	got, err := Load("h")
	if err != nil || len(got) != 1 || got[0].Lesson != "always do the thing" {
		t.Fatalf("load: %+v %v", got, err)
	}
	if _, err := Save("h", "t", "t", "l", "maybe", "s"); err == nil {
		t.Fatal("bad outcome must fail")
	}
}

func TestSaveRedacts(t *testing.T) {
	testEnv(t)
	e, err := Save("h", "t", "openai_api_key is abc123 done", "lesson", "worked", "s")
	if err != nil {
		t.Fatal(err)
	}
	if e.Task != "[REDACTED] done" {
		t.Fatalf("task not redacted: %q", e.Task)
	}
}

func TestIngestNewAndMerge(t *testing.T) {
	testEnv(t)
	oldG, oldR := GeneralizeLesson, Relate
	defer func() { GeneralizeLesson, Relate = oldG, oldR }()
	GeneralizeLesson = func(summary, m string) (string, error) { return "general lesson", nil }
	Relate = func(words, line, m string) (judge.Relation, error) {
		return judge.Relation{Relation: "unrelated"}, nil
	}
	r, err := Ingest("h", "T", "task", "summary", "worked", "s", "")
	if err != nil || r.Action != "new" {
		t.Fatalf("new: %+v %v", r, err)
	}
	Relate = func(words, line, m string) (judge.Relation, error) {
		return judge.Relation{Relation: "supports", Confidence: 0.9}, nil
	}
	r, err = Ingest("h", "T", "task", "summary", "worked", "s", "")
	if err != nil || r.Action != "merge" || len(r.Entry.Evidence) != 2 {
		t.Fatalf("merge: %+v %v", r, err)
	}
}

func TestForTaskRanksAndStamps(t *testing.T) {
	testEnv(t)
	if _, err := Save("h", "tests", "run the test suite", "run tests before committing", "worked", "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := Save("h", "other", "unrelated chore", "unrelated lesson", "worked", "s"); err != nil {
		t.Fatal(err)
	}
	oldR := RateRelevance
	defer func() { RateRelevance = oldR }()
	RateRelevance = func(task string, cands []judge.Candidate, m string) ([]judge.Rating, error) {
		var out []judge.Rating
		for _, c := range cands {
			out = append(out, judge.Rating{ID: c.ID, Relevant: c.Lesson == "run tests before committing", Confidence: 0.9})
		}
		return out, nil
	}
	got, err := ForTask("h", "should I run the tests now?", 5, Threshold, "")
	if err != nil || len(got) != 1 || got[0].Uses != 1 || got[0].LastUsed == 0 {
		t.Fatalf("for_task: %+v %v", got, err)
	}
	if got, err := ForTask("h", "task", 0, Threshold, ""); err != nil || len(got) != 0 {
		t.Fatalf("k=0: %+v %v", got, err)
	}
}

// At most k come back, best first, however many the judge calls relevant.
func TestForTaskCapsAtK(t *testing.T) {
	testEnv(t)
	for i := 0; i < 8; i++ {
		if _, err := Save("h", fmt.Sprintf("tests %d", i), "run the test suite", fmt.Sprintf("run tests before committing %d", i), "worked", "s"); err != nil {
			t.Fatal(err)
		}
	}
	oldR := RateRelevance
	defer func() { RateRelevance = oldR }()
	RateRelevance = func(task string, cands []judge.Candidate, m string) ([]judge.Rating, error) {
		var out []judge.Rating
		for i, c := range cands {
			out = append(out, judge.Rating{ID: c.ID, Relevant: true, Confidence: 0.6 + 0.04*float64(i)})
		}
		return out, nil
	}
	got, err := ForTask("h", "should I run the tests now?", 5, Threshold, "")
	if err != nil || len(got) != 5 {
		t.Fatalf("cap: %d entries, %v", len(got), err)
	}
}
