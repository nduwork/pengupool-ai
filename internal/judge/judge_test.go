package judge

import (
	"errors"
	"testing"
)

func stub(answer any, err error) func() {
	old := AskJSON
	AskJSON = func(system, user, m string) (any, error) { return answer, err }
	return func() { AskJSON = old }
}

func TestClassify(t *testing.T) {
	defer stub(map[string]any{"kind": "restriction", "confidence": 0.9}, nil)()
	c, err := Classify("never touch migrations", "")
	if err != nil || c.Kind != "restriction" || c.Confidence != 0.9 {
		t.Fatalf("classify: %+v %v", c, err)
	}
}

func TestPromptsScrubbed(t *testing.T) {
	var seen string
	old := AskJSON
	AskJSON = func(system, user, m string) (any, error) {
		seen = user
		return map[string]any{"kind": "restriction", "confidence": 0.9}, nil
	}
	defer func() { AskJSON = old }()
	if _, err := Classify("set openai_api_key = SECRETVALUE now", ""); err != nil {
		t.Fatal(err)
	}
	if contains(seen, "SECRETVALUE") {
		t.Fatalf("secret reached the model: %q", seen)
	}
	if Redact("openai_api_key is abc123 done") != "[REDACTED] done" {
		t.Fatalf("redact: %q", Redact("openai_api_key is abc123 done"))
	}
	if Redact("the password is \"hunter2\" ok") != "the [REDACTED] ok" {
		t.Fatalf("redact password: %q", Redact("the password is \"hunter2\" ok"))
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

func TestTriageRoutes(t *testing.T) {
	for kind, route := range map[string]string{
		"choice": "question", "preference": "proposal", "restriction": "proposal",
		"correction": "proposal", "none": "fyi",
	} {
		kind := kind
		defer stub(map[string]any{"kind": kind, "confidence": 0.7}, nil)()
		r, err := TriageRoute("words", "")
		if err != nil || r.Route != route || r.Kind != kind || r.Confidence != 0.7 {
			t.Fatalf("%s: %+v %v", kind, r, err)
		}
	}
}

func TestClassifyClampAndReject(t *testing.T) {
	defer stub(map[string]any{"kind": "preference", "confidence": 5.0}, nil)()
	if c, _ := Classify("x", ""); c.Confidence != 1.0 {
		t.Fatalf("clamp: %+v", c)
	}
}

func TestClassifyRejects(t *testing.T) {
	for _, a := range []any{
		map[string]any{"kind": "whatever", "confidence": 0.5},
		[]any{"not", "a", "dict"},
	} {
		defer stub(a, nil)()
		if _, err := Classify("x", ""); err == nil {
			t.Fatalf("must fail: %v", a)
		}
	}
	defer stub(map[string]any{"kind": "restriction", "confidence": nan()}, nil)()
	if _, err := Classify("x", ""); err == nil {
		t.Fatal("NaN must fail")
	}
}

func nan() float64 {
	f := 0.0
	return f / f
}

func TestRelate(t *testing.T) {
	defer stub(map[string]any{"relation": "contradicts", "confidence": 0.8}, nil)()
	r, err := Relate("touch it", "never touch", "")
	if err != nil || r.Relation != "contradicts" {
		t.Fatalf("relate: %+v %v", r, err)
	}
}

func TestRateRelevance(t *testing.T) {
	defer stub(map[string]any{"ratings": []any{
		map[string]any{"id": "a", "relevant": true, "confidence": 0.9},
	}}, nil)()
	got, err := RateRelevance("t", []Candidate{{ID: "a", Lesson: "l"}}, "")
	if err != nil || !got[0].Relevant {
		t.Fatalf("rate: %+v %v", got, err)
	}

	defer stub([]any{"not", "a", "dict"}, nil)()
	if _, err := RateRelevance("t", []Candidate{{ID: "a", Lesson: "l"}}, ""); err == nil {
		t.Fatal("non-dict ratings must fail")
	}

	// Full id set: paired by id, not position.
	defer stub(map[string]any{"ratings": []any{
		map[string]any{"id": "b", "relevant": false, "confidence": 0.9},
		map[string]any{"id": "a", "relevant": true, "confidence": 0.9},
	}}, nil)()
	got, err = RateRelevance("t", []Candidate{{ID: "a", Lesson: "la"}, {ID: "b", Lesson: "lb"}}, "")
	if err != nil || got[0].ID != "a" || !got[0].Relevant || got[1].ID != "b" || got[1].Relevant {
		t.Fatalf("by id: %+v %v", got, err)
	}

	// Mangled ids: positional fallback.
	defer stub(map[string]any{"ratings": []any{
		map[string]any{"id": 1, "relevant": true, "confidence": 0.9},
		map[string]any{"id": 2, "relevant": false, "confidence": 0.9},
	}}, nil)()
	got, err = RateRelevance("t", []Candidate{{ID: "a", Lesson: "la"}, {ID: "b", Lesson: "lb"}}, "")
	if err != nil || got[0].ID != "a" || !got[0].Relevant || got[1].ID != "b" || got[1].Relevant {
		t.Fatalf("positional: %+v %v", got, err)
	}
}

func TestAskError(t *testing.T) {
	defer stub(nil, errors.New("down"))()
	if _, err := Classify("x", ""); err == nil {
		t.Fatal("model error must propagate")
	}
}
