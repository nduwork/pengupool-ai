package pool

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/decision"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestParseTranscript(t *testing.T) {
	f, err := os.Open("testdata/transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	eps := parseTranscript(f, "file")
	if len(eps) != 2 {
		t.Fatalf("episodes = %+v", eps)
	}
	want := []struct{ prompt, reply string }{
		{"Add a hello command to the demo tool.", "Done: hello added."}, // the last assistant text, not the sidechain's
		{"Now add a goodbye command.", "Added goodbye."},
	}
	for i, w := range want {
		if eps[i].Prompt != w.prompt || eps[i].Reply != w.reply || eps[i].Session != "sess-1" {
			t.Fatalf("episode %d = %+v", i, eps[i])
		}
	}
	// Newest kept, and a date cuts.
	if got, _ := TranscriptEpisodes("testdata", time.Time{}, 1); len(got) != 1 || got[0].Prompt != want[1].prompt {
		t.Fatalf("limit: %+v", got)
	}
	if got, _ := TranscriptEpisodes("testdata", time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC), 0); len(got) != 0 {
		t.Fatalf("since: %+v", got)
	}
}

// labelDecider answers triage, the check-in and the match packs from the sample's labels. misread
// makes it misclassify prompts: prompt → the kind it wrongly answers.
type labelDecider struct {
	eps     []Episode
	misread map[string]string
}

func (d *labelDecider) Name() string { return "labels" }
func (d *labelDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	st, _ := req.State.(map[string]any)
	str := func(k string) string { s, _ := st[k].(string); return s }
	byPrompt := func(w string) (i int) { return slices.IndexFunc(d.eps, func(e Episode) bool { return e.Prompt == w }) }
	out := map[string]decision.Answer{}
	for _, q := range req.Questions {
		yes := func(v bool) decision.Answer {
			a := decision.Answer{Type: decision.Predicate, Confidence: 0.9}
			if v {
				a.Yes = 1
			}
			return a
		}
		switch {
		case q.Name == "record":
			out[q.Name] = decision.Answer{Type: decision.Choice, Choice: "record", Confidence: 0.9}
		case q.Name == "owner_signal":
			i := byPrompt(str("owner_message"))
			kind := d.eps[i].Kind
			if m := d.misread[kind+str("owner_message")]; m != "" {
				kind = m
			}
			out[q.Name] = decision.Answer{Type: decision.Choice, Choice: kind, Confidence: 0.9}
		case q.Name == "changes_preference":
			out[q.Name] = yes(d.eps[byPrompt(str("owner_message"))].ChangesPref)
		case q.Name == "waiting":
			i := slices.IndexFunc(d.eps, func(e Episode) bool { return tailRunes(e.Reply, 1500) == str("agent_reply_end") })
			out[q.Name] = yes(i >= 0 && d.eps[i].CheckIn)
		case q.Name == "proceed" || q.Name == "one_proposal":
			out[q.Name] = yes(true) // the fake is sure of everything: the floor alone stops it
		case q.Name == "answers":
			out[q.Name] = yes(true)
		case q.Name == "owner_proceed":
			out[q.Name] = yes(true)
		case strings.HasPrefix(q.Name, "line_"):
			out[q.Name] = decision.Answer{Type: decision.Choice, Choice: "unrelated", Probabilities: map[string]float64{"unrelated": 1}, Confidence: 0.9}
		}
	}
	return decision.Response{Answers: out, Calibrated: true}, nil
}

// evalFake replays eps with the fake decider.
func evalFake(t *testing.T, eps []Episode, d *labelDecider) (*Pool, EvalReport) {
	t.Helper()
	var ids []string
	for _, e := range eps {
		if !slices.Contains(ids, e.Session) {
			ids = append(ids, e.Session)
		}
	}
	p, dir, err := evalPool(store.Config{}, ids)
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err != nil {
		t.Fatal(err)
	}
	p.testDecider = d
	return p, p.replay(eps)
}

func TestEvalSample(t *testing.T) {
	eps, err := SampleEpisodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) < 80 || len(eps) > 120 {
		t.Fatalf("the sample has %d episodes", len(eps))
	}
	// Two instructions read as preferences, three preferences as instructions.
	d := &labelDecider{eps: eps, misread: map[string]string{}}
	var fp, fn int
	for _, e := range eps {
		if e.Kind == "instruction" && fp < 2 && !e.CheckIn {
			d.misread["instruction"+e.Prompt], fp = "preference", fp+1
		}
		if e.Kind == "preference" && fn < 3 {
			d.misread["preference"+e.Prompt], fn = "instruction", fn+1
		}
	}
	_, r := evalFake(t, eps, d)

	labelled := map[string]int{}
	for _, e := range eps {
		labelled[e.Kind]++
	}
	lp := labelled["preference"]
	pref := r.Scores["preference"]
	if pref.Predicted != lp-3+2 || pref.Correct != lp-3 || pref.Labelled != lp {
		t.Fatalf("preference score = %+v, want predicted %d correct %d labelled %d", pref, lp-1, lp-3, lp)
	}
	if math.Abs(*pref.Precision-float64(lp-3)/float64(lp-1)) > 1e-9 || math.Abs(*pref.Recall-float64(lp-3)/float64(lp)) > 1e-9 {
		t.Fatalf("preference precision %v recall %v", *pref.Precision, *pref.Recall)
	}
	if r.Episodes != len(eps) || r.Prompts != len(eps) || r.Sessions != 5 || r.From != "2026-03-02" || r.To != "2026-03-20" {
		t.Fatalf("totals: %+v", r)
	}

	// The check-in numbers, worked out from the labels: the fake would go ahead on every check-in the
	// floor lets by, but no Compass line guides yet (stated lines wait for a confirming choice, one
	// choice is not enough), so every check-in asks; the next prompt says whether the owner proceeded.
	sorted := slices.Clone(eps)
	slices.SortStableFunc(sorted, func(a, b Episode) int { return a.At.Compare(b.At) })
	var checkIns, floor, goes, known, wrong, missed, unknown int
	for i, e := range sorted {
		if !e.CheckIn {
			continue
		}
		checkIns++
		hit := floorHit(e.Reply) != ""
		if hit {
			floor++
		}
		j := slices.IndexFunc(sorted[i+1:], func(n Episode) bool { return n.Session == e.Session })
		if j < 0 || sorted[i+1+j].Proceed == nil {
			unknown++
			continue
		}
		known++
		if *sorted[i+1+j].Proceed {
			missed++
		}
	}
	if r.CheckIns != checkIns || r.Floor != floor || r.GoAheads != goes || r.Known != known || r.WrongGo != wrong || r.Missed != missed || r.Unknown != unknown {
		t.Fatalf("check-ins: got %+v\nwant %d floor %d goes %d known %d wrong %d missed %d unknown %d", r, checkIns, floor, goes, known, wrong, missed, unknown)
	}
	// Every check-in that asked has one reason, and every floor stop one item, settled no more often than stopped.
	asked, stops := 0, 0
	for _, n := range r.Asked {
		asked += n
	}
	for item, f := range r.Floors {
		stops += f.Stops
		if !floorItems()[item] || f.Known > f.Stops || f.Proceeded > f.Known {
			t.Fatalf("floor item %q: %+v", item, f)
		}
	}
	split := 0
	for _, n := range r.UnknownBy {
		split += n
	}
	if len(r.UnknownBy) > 0 && split != r.Unknown { // labelled episodes settle from their labels, not split
		t.Fatalf("unknown_because %v sums to %d, unknown %d", r.UnknownBy, split, r.Unknown)
	}
	if asked != r.CheckIns-r.GoAheads || stops != r.Floor || r.Asked["floor item named"] != r.Floor {
		t.Fatalf("asked %v (sum %d), floor items %d, want %d and %d", r.Asked, asked, stops, r.CheckIns-r.GoAheads, r.Floor)
	}
	if goes != 0 || wrong != 0 || r.Verdict != "INCONCLUSIVE" {
		t.Fatalf("with no guiding line nothing goes ahead, so the go-ahead bar can't be judged: %s", r.Verdict)
	}

	// Nothing the owner said, or a line says, is in the report.
	b, _ := json.Marshal(r)
	for _, w := range []string{"kelpdb", "lantern", "marsh", "table-driven", "sitemap", "changelog"} {
		if strings.Contains(r.String()+string(b), w) {
			t.Fatalf("the report leaks %q", w)
		}
	}
}

func TestEvalNeedsAModel(t *testing.T) {
	eps, _ := SampleEpisodes()
	if _, err := Eval(store.Config{}, eps); err != errNoModel {
		t.Fatalf("err = %v", err)
	}
}

func TestTranscriptErrorHidesPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "secret-project-zz.jsonl")
	if err := os.WriteFile(f, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := TranscriptEpisodes(dir, time.Time{}, 0)
	if err == nil || strings.Contains(err.Error(), "secret-project-zz") || strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "1 ") {
		t.Fatalf("err = %v", err)
	}
}

// TestEvalRemovesItsStoreOnInterrupt: Ctrl-C and SIGTERM skip deferred cleanups, so Eval handles them.
func TestEvalRemovesItsStoreOnInterrupt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	asked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(asked) })
		<-release
	}))
	defer srv.Close()
	defer close(release)
	pc := store.ProviderConfig{Type: "ollama", Format: "ollama-systemone", Address: srv.URL, Model: "m"}
	cfg := store.Config{Decision: store.DecisionConfig{Active: "x", Providers: map[string]store.ProviderConfig{"x": pc}}}
	exited := make(chan int, 1)
	evalExit = func(code int) { exited <- code }
	defer func() { evalExit = os.Exit }()
	eps, _ := SampleEpisodes()
	done := make(chan error, 1)
	go func() { _, err := Eval(cfg, eps); done <- err }()
	select {
	case <-asked:
	case err := <-done:
		t.Fatalf("Eval returned before asking: %v", err)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	if code := <-exited; code != 130 {
		t.Fatalf("exit code %d", code)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "pengupool-eval-*")); len(left) != 0 {
		t.Fatalf("store left behind: %v", left)
	}
}

// floorItems are the floor's item names: all the eval reports of a stop.
func floorItems() map[string]bool {
	out := map[string]bool{}
	for _, f := range floor {
		out[f.what] = true
	}
	for _, f := range floorProse {
		out[f.what] = true
	}
	return out
}

// replyDecider settles each owner reply to a check-in by its text: "yes" answers it and lets the agent
// proceed, "unrelated" is not an answer, "hmm" answers it without saying whether the agent may proceed.
type replyDecider struct{}

func (replyDecider) Name() string { return "replies" }
func (replyDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	st, _ := req.State.(map[string]any)
	ans, _ := st["owner_answer"].(string)
	out := map[string]decision.Answer{}
	for _, q := range req.Questions {
		a := decision.Answer{Type: q.Type, Confidence: 0.9}
		switch q.Type {
		case decision.Choice:
			a.Choice = "none"
			if _, ok := q.Options["none"]; !ok {
				a.Choice = slices.Sorted(maps.Keys(q.Options))[0]
			}
		case decision.Score:
		default:
			switch q.Name {
			case "waiting":
				a.Yes = 1
			case "answers":
				if !strings.HasPrefix(ans, "unrelated") {
					a.Yes = 1
				}
			case "owner_proceed":
				a.Yes = 1
				if strings.HasPrefix(ans, "hmm") {
					a.Confidence = 0.2
				}
			}
		}
		out[q.Name] = a
	}
	return decision.Response{Answers: out, Calibrated: true}, nil
}

// TestEvalSplitsUnknownOutcomes: on unlabelled history, every unknown outcome has one cause.
func TestEvalSplitsUnknownOutcomes(t *testing.T) {
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	var eps []Episode
	for i, prompt := range []string{"Start on the parser.", "yes, go ahead", "unrelated: rename the helper file", "hmm"} {
		eps = append(eps, Episode{Session: "s1", At: at.Add(time.Duration(i) * time.Hour), Prompt: prompt, Reply: "Step done. Shall I continue with the next one?"})
	}
	p, dir, err := evalPool(store.Config{}, []string{"s1"})
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err != nil {
		t.Fatal(err)
	}
	p.testDecider = replyDecider{}
	r := p.replay(eps)
	want := map[string]int{"next prompt not an answer (or the model failed)": 1, "not sure whether it proceeded": 1, "no next owner prompt": 1}
	if r.CheckIns != 4 || r.Known != 1 || r.Unknown != 3 || !maps.Equal(r.UnknownBy, want) {
		t.Fatalf("check-ins %d known %d unknown %d by %v", r.CheckIns, r.Known, r.Unknown, r.UnknownBy)
	}
}
