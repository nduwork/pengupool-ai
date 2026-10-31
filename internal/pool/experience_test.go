package pool

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

const plan = "planaaaaaaaa"

// journalSetup makes plan an idle helper of the given harness.
func journalSetup(t *testing.T, h string) (*Pool, string, *[]string) {
	t.Helper()
	p, st := helperPool(t)
	_ = st.Update(func(s *store.State) error {
		i := sessionIndex(s, plan)
		s.Sessions[i].Harness, s.Sessions[i].Status = h, "idle"
		return nil
	})
	var typed []string
	p.testKeys = func(id, keys string) error { typed = append(typed, keys); return nil }
	return p, "", &typed
}

// done is a message from the helper to its parent, typed as on the way in (msgType).
func done(text string) protocol.TeamMessage {
	typ, _ := msgType("", text)
	return protocol.TeamMessage{TeamID: "team1", From: plan, To: "leadaaaaaaaa", Text: text, Type: typ}
}

func entries(t *testing.T, p *Pool) []agentdb.Entry {
	t.Helper()
	es, err := p.db.ListExperience(planKey)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// waitEntry waits for the helper's one entry and returns it in full.
func waitEntry(t *testing.T, p *Pool) agentdb.Entry {
	t.Helper()
	for i := 0; i < 400; i++ {
		if es := entries(t, p); len(es) > 0 {
			e, _ := p.db.ExperienceEntry(es[0].ID)
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no entry was written")
	return agentdb.Entry{}
}

// A Done: report to the parent is kept at once, whole, as the helper's entry: its first line is the
// title, the report the body. Nothing is typed into the helper. A second report is its own entry.
func TestDoneReportIsTheEntry(t *testing.T) {
	for _, h := range []string{harness.Claude, harness.Codex, harness.Muse} {
		t.Run(h, func(t *testing.T) {
			p, _, typed := journalSetup(t, h)
			report := "Done: login redirect lost the next page; fixed by keeping it in the state\n- cause: the guard dropped ?next\n- evidence: TestLoginRedirect"
			p.doneReport(done(report))
			es := entries(t, p)
			if len(es) != 1 {
				t.Fatalf("entries %+v", es)
			}
			e, _ := p.db.ExperienceEntry(es[0].ID)
			if e.Title != "login redirect lost the next page; fixed by keeping it in the state" || e.Body != report || e.Helper != planKey {
				t.Fatalf("entry = %+v", e)
			}
			p.doneReport(done("Done: second job\nits report"))
			if len(entries(t, p)) != 2 || len(*typed) != 0 {
				t.Fatalf("entries %+v, typed %q", entries(t, p), *typed)
			}
		})
	}
	t.Run("secrets", func(t *testing.T) {
		p, _, _ := journalSetup(t, harness.Muse)
		p.doneReport(done("Done: key sk-ant-abcdefghijklmnopqrstuvwxyz0123 rotated\nsk-ant-abcdefghijklmnopqrstuvwxyz0123"))
		if e := waitEntry(t, p); strings.Contains(e.Title+e.Body, "sk-ant-") {
			t.Fatalf("entry %+v", e)
		}
	})
}

func TestOnlyADoneReportMakesAnEntry(t *testing.T) {
	p, _, typed := journalSetup(t, harness.Claude)
	for _, m := range []protocol.TeamMessage{
		done("Question: which branch?"),
		done("progress: half way\nDone: not on the first line"),
		{TeamID: "team1", From: plan, To: "webaaaaaaaaa", Text: "Done: to a sibling, not the parent", Type: "done"},
		{TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "Done: a helper without a skill", Type: "done"},
		{TeamID: "team1", From: plan, To: "leadaaaaaaaa", Text: "Done: but sent as a tell", Type: "tell"},
	} {
		p.doneReport(m)
	}
	if es := entries(t, p); len(es) != 0 || len(*typed) != 0 {
		t.Fatalf("entries %+v, typed %q", es, *typed)
	}
}

func TestJournalIndexInContext(t *testing.T) {
	p, _, _ := journalSetup(t, harness.Claude)
	for i := 0; i < 12; i++ {
		if _, err := p.db.AddExperience(agentdb.Entry{Helper: planKey, At: fmt.Sprintf("2026-10-%02dT10:00:00Z", i+1), Title: fmt.Sprintf("job %d about caching", i), Body: fmt.Sprint("b", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = p.db.AddExperience(agentdb.Entry{Helper: "someone-else", At: "2026-10-01T10:00:00Z", Title: "caching of another helper", Body: "b"})
	out := p.journalForPrompt(store.Session{ID: plan}, "[PenguPool team message from lead (main)] please redo the caching\n\nTo reply, run")
	if n := strings.Count(out, "\n- ["); n != 10 || !strings.Contains(out, "2 more: `pengupool experience search <words>`") || strings.Contains(out, "another helper") {
		t.Fatalf("index:\n%s", out)
	}
	if p.journalForPrompt(store.Session{ID: plan}, "just do it") != "" {
		t.Fatal("the owner's prompt got an index")
	}
	// The helper is told how to write the report that becomes its entry: one task's lesson, the
	// transferable knowledge rather than the diff, durable know-how dated, never a credential's value.
	c := p.renderContext(plan)
	for _, want := range []string{"report to your parent with `Done:`", "subject and its result", "not a restatement of the diff", "the date you last checked it", "never its value", "A pointer to a temporary file alone is not a report"} {
		if !strings.Contains(c, want) {
			t.Fatalf("the helper's report guidance lacks %q", want)
		}
	}
}

// The owner's rebuild: export every entry in full, then one import that adds the rebuilt entries and
// drops the old ones first, each drop with a reason. A drop without a reason, or of an unknown entry,
// writes nothing. Agents can do neither.
func TestExperienceExportAndRebuild(t *testing.T) {
	p, _ := helperPool(t)
	_, _ = p.db.AddExperience(agentdb.Entry{Helper: planKey, At: "2026-09-01T10:00:00Z", Title: "four tasks mixed", Body: "a long compaction"})
	_, _ = p.db.AddExperience(agentdb.Entry{Helper: "someone-else", At: "2026-09-02T10:00:00Z", Title: "theirs", Body: "kept"})
	res, err := call(t, p, true, "experience.export", map[string]any{})
	es, _ := res.([]agentdb.Entry)
	if err != nil || len(es) != 2 || es[0].Body == "" || es[0].ID == 0 {
		t.Fatalf("export = %+v %v", res, err)
	}
	old := es[0].ID
	if es[0].Helper != planKey {
		old = es[1].ID
	}
	rebuilt := func(drop []map[string]any) map[string]any {
		return map[string]any{"entries": []map[string]string{{"helper": planKey, "at": "2026-09-01T10:00:00Z", "title": "cache warm-up cut cold starts; run it before deploy", "body": "Done: ..."}}, "drop": drop}
	}
	for _, bad := range [][]map[string]any{{{"id": old}}, {{"id": 999, "reason": "gone"}}} {
		if _, err := call(t, p, true, "experience.import", rebuilt(bad)); err == nil {
			t.Fatalf("drop %v was accepted", bad)
		}
	}
	if l, _ := p.db.ListExperience(planKey); len(l) != 1 || l[0].Title != "four tasks mixed" {
		t.Fatalf("a refused import wrote: %+v", l)
	}
	res, err = call(t, p, true, "experience.import", rebuilt([]map[string]any{{"id": old, "reason": "mixed four tasks; split and rewritten"}}))
	if c, _ := res.(importCount); err != nil || c.Added != 1 || c.Dropped != 1 {
		t.Fatalf("rebuild = %+v %v", res, err)
	}
	if l, _ := p.db.ListExperience(planKey); len(l) != 1 || l[0].Title != "cache warm-up cut cold starts; run it before deploy" {
		t.Fatalf("after rebuild: %+v", l)
	}
	// A rebuild that keeps the helper, time and title (a body rewritten in place) keeps the entry; the
	// same id twice is refused before anything is deleted.
	l, _ := p.db.ListExperience(planKey)
	same := map[string]any{"entries": []map[string]string{{"helper": planKey, "at": l[0].At, "title": l[0].Title, "body": "Done: rewritten digest"}}}
	same["drop"] = []map[string]any{{"id": l[0].ID, "reason": "x"}, {"id": l[0].ID, "reason": "x"}}
	if _, err := call(t, p, true, "experience.import", same); err == nil {
		t.Fatal("the same drop twice was accepted")
	}
	if l2, _ := p.db.ListExperience(planKey); len(l2) != 1 || l2[0].ID != l[0].ID {
		t.Fatalf("a refused import deleted: %+v", l2)
	}
	same["drop"] = []map[string]any{{"id": l[0].ID, "reason": "body rewritten"}}
	res, err = call(t, p, true, "experience.import", same)
	l2, _ := p.db.ListExperience(planKey)
	if c, _ := res.(importCount); err != nil || c.Added != 1 || c.Dropped != 1 || len(l2) != 1 {
		t.Fatalf("an in-place rebuild lost the entry: %+v %v %+v", res, err, l2)
	}
	if e, _ := p.db.ExperienceEntry(l2[0].ID); e.Body != "Done: rewritten digest" || e.Title != l[0].Title {
		t.Fatalf("rebuilt entry = %+v", e)
	}
	if _, err := call(t, p, false, "experience.export", map[string]any{"session_id": plan}); err == nil {
		t.Fatal("an agent exported the journal")
	}
}

// An agent searches and reads its own entries only.
func TestAgentExperienceRPCs(t *testing.T) {
	p, _, _ := journalSetup(t, harness.Claude)
	_, _ = p.db.AddExperience(agentdb.Entry{Helper: planKey, Title: "cache fix", Body: "invalidate on write"})
	_, _ = p.db.AddExperience(agentdb.Entry{Helper: "someone-else", Title: "cache secret", Body: "not yours"})
	res, err := call(t, p, false, "experience.search", map[string]any{"session_id": plan, "query": "cache"})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]any)
	if es := out["entries"].([]agentdb.Entry); len(es) != 1 || es[0].Title != "cache fix" || out["total"] != 1 {
		t.Fatalf("search = %+v", out)
	}
	id := out["entries"].([]agentdb.Entry)[0].ID
	if res, err = call(t, p, false, "experience.show", map[string]any{"session_id": plan, "id": id}); err != nil || res.(agentdb.Entry).Body != "invalidate on write" {
		t.Fatalf("show = %+v %v", res, err)
	}
	other, _, _ := p.db.SearchExperience("someone-else", "", "", 1)
	if _, err := call(t, p, false, "experience.show", map[string]any{"session_id": plan, "id": other[0].ID}); err == nil {
		t.Fatal("read another helper's entry")
	}
	for _, typ := range []string{"experience.list", "experience.get", "experience.delete"} {
		if _, err := call(t, p, false, typ, map[string]any{"agent": planKey, "id": id}); err == nil {
			t.Fatalf("an agent called %s", typ)
		}
	}
	if res, err = call(t, p, true, "experience.list", map[string]any{"agent": planKey}); err != nil || len(res.([]agentdb.Entry)) != 1 {
		t.Fatalf("list = %+v %v", res, err)
	}
	if _, err = call(t, p, true, "experience.delete", map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	if res, _ = call(t, p, true, "experience.list", map[string]any{"agent": planKey}); len(res.([]agentdb.Entry)) != 0 {
		t.Fatalf("list after the delete = %+v", res)
	}
}

// An import adds each entry once: a rerun adds nothing, a deleted entry does not come back, and a
// file naming an unknown helper writes nothing.
func TestExperienceImport(t *testing.T) {
	p, _ := helperPool(t)
	file := func(helper string) map[string]any {
		return map[string]any{"entries": []map[string]string{
			{"helper": helper, "at": "2026-09-01T10:00:00Z", "title": "first job", "body": "what happened"},
			{"helper": planKey, "at": "2026-09-02T10:00:00Z", "title": "second job", "body": "sk-ant-abcdefghijklmnopqrstuvwxyz0123"},
		}}
	}
	res, err := call(t, p, true, "experience.import", file("Planning-Agent"))
	if c := res.(importCount); err != nil || c.Added != 2 || c.Skipped != 0 {
		t.Fatalf("import = %+v %v", res, err)
	}
	if c, _ := call(t, p, true, "experience.import", file("planning-agent")); c.(importCount).Added != 0 || c.(importCount).Skipped != 2 {
		t.Fatalf("rerun = %+v", c)
	}
	es, _ := p.db.ListExperience(planKey)
	if len(es) != 2 || es[0].At != "2026-09-02T10:00:00Z" {
		t.Fatalf("entries = %+v", es)
	}
	if e, _ := p.db.ExperienceEntry(es[0].ID); strings.Contains(e.Body, "sk-ant-") {
		t.Fatalf("a secret was kept: %q", e.Body)
	}
	if _, err := call(t, p, true, "experience.delete", map[string]any{"id": es[1].ID}); err != nil {
		t.Fatal(err)
	}
	if c, _ := call(t, p, true, "experience.import", file("planning-agent")); c.(importCount).Added != 0 {
		t.Fatalf("a deleted entry came back: %+v", c)
	}
	if _, err := call(t, p, true, "experience.import", file("nobody")); err == nil {
		t.Fatal("an unknown helper was accepted")
	}
}
