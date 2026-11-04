package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/decision"
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
	if n := strings.Count(out, "\n- ["); n != 10 || !strings.Contains(out, "2 more: `'pengupool' experience search <words>`") || strings.Contains(out, "another helper") {
		t.Fatalf("index:\n%s", out)
	}
	if p.journalForPrompt(store.Session{ID: plan}, "just do it") != "" {
		t.Fatal("the owner's prompt got an index")
	}
	// No match is no match: the newest are listed under a heading that says so, never as a best match.
	// Common words in the task are not searched for ("the", "with" would match every entry).
	_, _ = p.db.AddExperience(agentdb.Entry{Helper: planKey, At: "2026-10-20T10:00:00Z", Title: "notes with the links", Body: "n"})
	if out := p.journalLines(planKey, "", "the deploy with terraform"); !strings.Contains(out, "## Recent work (no matching entries") || strings.Contains(out, "best match") {
		t.Fatalf("no-match index:\n%s", out)
	}
	// Whole rows only, within journalIndexBytes: long titles end the list early, and the rest are counted.
	for i := 0; i < 10; i++ {
		_, _ = p.db.AddExperience(agentdb.Entry{Helper: planKey, At: fmt.Sprintf("2026-11-%02dT10:00:00Z", i+1), Title: fmt.Sprintf("long %d ", i) + strings.Repeat("queue ", 33), Body: fmt.Sprint("q", i)})
	}
	out = p.journalLines(planKey, "", "queue")
	rows := strings.Count(out, "\n- [")
	if rows == 0 || rows >= 10 || len(out) > journalIndexBytes+80 || !strings.Contains(out, fmt.Sprintf("%d more:", 10-rows)) {
		t.Fatalf("capped index (%d rows, %d bytes):\n%s", rows, len(out), out)
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
	// Rebuilt again, and again: each run replaces the entry, never loses it.
	for i, body := range []string{"Done: v3", "Done: v4"} {
		cur, _ := p.db.ListExperience(planKey)
		again := map[string]any{"entries": []map[string]string{{"helper": planKey, "at": l[0].At, "title": l[0].Title, "body": body}},
			"drop": []map[string]any{{"id": cur[0].ID, "reason": "rewritten"}}}
		res, err := call(t, p, true, "experience.import", again)
		cur, _ = p.db.ListExperience(planKey)
		if c, _ := res.(importCount); err != nil || c.Added != 1 || len(cur) != 1 {
			t.Fatalf("rebuild %d lost the entry: %+v %v %+v", i+3, res, err, cur)
		}
		if e, _ := p.db.ExperienceEntry(cur[0].ID); e.Body != body {
			t.Fatalf("rebuild %d = %+v", i+3, e)
		}
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
	other, _, _, _ := p.db.SearchExperience("someone-else", "", "", 1)
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

// A HELPER LEAD's natural compaction becomes its team's one project entry, under the host's
// facts about the team; a newer compaction replaces it, the same one is not kept twice, and another
// team keeps its own. A builder or skill-less LEAD gets no paragraph and no entry.
func TestLeadProjectEntry(t *testing.T) {
	p, st := helperPool(t)
	dir := t.TempDir()
	tr := func(id string) string { return filepath.Join(dir, id+".jsonl") }
	compact := func(id, at, text string) {
		f, _ := os.OpenFile(tr(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		defer f.Close()
		rec, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at, "isCompactSummary": true,
			"message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation that ran out of context.\n\n" + text + "\nIf you need specific details from before compaction, read the transcript."}})
		fmt.Fprintf(f, "{\"type\":\"assistant\",\"timestamp\":%q}\n%s\n", at, rec)
	}
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			s.Sessions[i].Harness, s.Sessions[i].Transcript = harness.Claude, tr(s.Sessions[i].ID)
			if s.Sessions[i].ID == "leadaaaaaaaa" || s.Sessions[i].ID == "otheraaaaaaa" {
				s.Sessions[i].SkillID = "sk-plan" // both LEADs are HELPERs of the same skill
			}
		}
		s.Domain.Teams[0].Goal = "ship login"
		s.Domain.Teams[0].Members[1].Assignment = "the web app"
		return nil
	})
	project := func(team string) []agentdb.Entry {
		es, err := p.db.TeamExperience(planKey, team)
		if err != nil {
			t.Fatal(err)
		}
		return es
	}
	if !strings.Contains(p.renderContext("leadaaaaaaaa"), leadGuide) || strings.Contains(p.renderContext("planaaaaaaaa"), leadGuide) {
		t.Fatal("the paragraph is not the HELPER LEAD's alone")
	}
	// A report the agent titled like a project entry is its own, never replaced.
	if _, err := p.db.AddExperience(agentdb.Entry{Helper: planKey, At: "2026-10-30T09:00:00Z", TeamID: "team1", Title: "Project: app", Body: "an agent's own report"}); err != nil {
		t.Fatal(err)
	}
	compact("leadaaaaaaaa", "2026-10-31T10:00:00Z", "Summary: first picture")
	p.leadCompaction("leadaaaaaaaa")
	compact("otheraaaaaaa", "2026-10-31T10:30:00Z", "Summary: ops picture")
	p.leadCompaction("otheraaaaaaa")
	compact("leadaaaaaaaa", "2026-10-31T11:00:00Z", "Summary: second picture")
	p.leadCompaction("leadaaaaaaaa")
	own := func(e agentdb.Entry) bool { return e.At == "2026-10-30T09:00:00Z" }
	kept := slices.DeleteFunc(project("team1"), own)
	p.leadCompaction("leadaaaaaaaa") // the same compaction again: nothing changes, not even its uid (sync)
	es := slices.DeleteFunc(project("team1"), own)
	if len(project("team1")) != 2 || len(es) != 1 || es[0].Title != "Project: app" || es[0].At != "2026-10-31T11:00:00Z" || len(kept) != 1 || kept[0].UID != es[0].UID {
		t.Fatalf("team1 entries %+v, before %+v", es, kept)
	}
	e, _ := p.db.ExperienceEntry(es[0].ID)
	for _, want := range []string{"## Team facts (PenguPool, as of 2026-10-31T11:00:00Z)", "- Team: app (team1)", "- Latest owner instruction: ship login",
		"web (session webaaaaaaaaa)", ": the web app", "- Captured from: the compaction of session leadaaaaaaaa", "## The lead's summary\n\nSummary: second picture"} {
		if !strings.Contains(e.Body, want) {
			t.Fatalf("body lacks %q:\n%s", want, e.Body)
		}
	}
	if strings.Contains(e.Body, "first picture") || strings.Contains(e.Body, "continued from a previous") || strings.Contains(e.Body, "purpose") {
		t.Fatalf("body:\n%s", e.Body)
	}
	if es := project("team2"); len(es) != 1 || es[0].Title != "Project: ops" {
		t.Fatalf("team2 entries %+v", es)
	}
	// A builder or skill-less LEAD: no paragraph, and its compaction is no entry.
	_ = st.Update(func(s *store.State) error {
		s.Sessions[sessionIndex(s, "otheraaaaaaa")].SkillID = ""
		return nil
	})
	compact("otheraaaaaaa", "2026-10-31T12:00:00Z", "Summary: not kept")
	p.leadCompaction("otheraaaaaaa")
	if es := project("team2"); len(es) != 1 || es[0].At != "2026-10-31T10:30:00Z" || strings.Contains(p.renderContext("otheraaaaaaa"), leadGuide) {
		t.Fatalf("a skill-less LEAD: entries %+v", es)
	}
	// A helper that is not the LEAD keeps no project entry from its compactions.
	compact("planaaaaaaaa", "2026-10-31T12:30:00Z", "Summary: a helper's own")
	p.leadCompaction("planaaaaaaaa")
	if es := project("team1"); len(es) != 2 || es[0].At != "2026-10-31T11:00:00Z" && es[1].At != "2026-10-31T11:00:00Z" {
		t.Fatalf("a non-lead helper made a project entry: %+v", es)
	}
}

// pi and OMP write a "compaction" record with its summary; the newest one counts. Codex's compaction
// is encrypted and gives none.
func TestLastCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(`{"type":"message","timestamp":"2026-10-31T09:00:00Z"}
{"type":"compaction","timestamp":"2026-10-31T10:00:00.5Z","summary":"older"}
{"type":"compaction","timestamp":"2026-10-31T11:00:00Z","summary":"  the newest picture  "}
{"type":"compacted","timestamp":"2026-10-31T12:00:00Z","payload":{"message":""}}
`), 0o644)
	for _, h := range []string{harness.Pi, harness.OMP} {
		if at, s := lastCompaction(h, path); s != "the newest picture" || !at.Equal(time.Date(2026, 10, 31, 11, 0, 0, 0, time.UTC)) {
			t.Fatalf("%s: %v %q", h, at, s)
		}
	}
	if _, s := lastCompaction(harness.Codex, path); s != "" {
		t.Fatalf("codex: %q", s)
	}
	if _, s := lastCompaction(harness.Claude, path); s != "" {
		t.Fatalf("claude read a pi record: %q", s)
	}
}

// keepDecider answers the "keep" question with yes, calibrated or not, or fails.
type keepDecider struct {
	yes, conf  float64
	calibrated bool
	err        error
	asked      chan decision.Request
	release    chan struct{} // the answer waits for it
}

func (d *keepDecider) Name() string { return "fake-s1" }
func (d *keepDecider) Decide(_ context.Context, req decision.Request) (decision.Response, error) {
	d.asked <- req
	<-d.release
	return decision.Response{Provider: "fake-s1", Model: "s1-test", Calibrated: d.calibrated,
		Answers: map[string]decision.Answer{"keep": {Type: decision.Predicate, Yes: d.yes, Confidence: d.conf}}}, d.err
}

// A Done: report is kept unless triage gives a calibrated no at the threshold: an unsure or
// uncalibrated answer and a failed call keep it. Each decision is a line on the helper's log.
func TestExperienceSaveIsTriageDecided(t *testing.T) {
	for _, c := range []struct {
		name string
		d    keepDecider
		want string
	}{
		{"kept", keepDecider{yes: 0.9, conf: 0.9, calibrated: true}, `experience: kept "fixed the cache" p=0.90 provider=fake-s1 model=s1-test`},
		{"skipped", keepDecider{yes: 0.1, conf: 0.9, calibrated: true}, `experience: skipped "fixed the cache" p=0.10 provider=fake-s1 model=s1-test`},
		{"unsure keeps", keepDecider{yes: 0.4, conf: 0.6, calibrated: true}, `experience: kept "fixed the cache" p=0.40`},
		{"uncalibrated no keeps", keepDecider{yes: 0.1, conf: 0.9}, `experience: kept "fixed the cache" p=0.10`},
		{"error keeps", keepDecider{err: decision.ErrTimeout}, `experience: kept "fixed the cache" provider=fake-s1 error="provider_timeout"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _, _ := journalSetup(t, harness.Claude)
			runLog := filepath.Join(t.TempDir(), "plan.log")
			_ = p.st.Update(func(s *store.State) error { s.Sessions[sessionIndex(s, plan)].RunLog = runLog; return nil })
			_, _ = p.db.AddExperience(agentdb.Entry{Helper: planKey, At: "2026-10-01T10:00:00Z", Title: "an older job", Body: "b"})
			d := c.d
			d.asked, d.release = make(chan decision.Request, 1), make(chan struct{})
			p.testDecider = &d
			p.doneReport(done("Done: fixed the cache\nkey sk-ant-abcdefghijklmnopqrstuvwxyz0123 and evidence"))
			req := <-d.asked
			if n := len(entries(t, p)); n != 2 {
				t.Fatalf("%d entries while triage decides: the report is kept before it asks", n)
			}
			close(d.release)
			if st, _ := json.Marshal(req.State); !strings.Contains(string(st), "an older job") || strings.Contains(string(st), "sk-ant-") {
				t.Fatalf("state %s", st)
			}
			var logged string
			for i := 0; i < 400 && !strings.Contains(logged, "experience:"); i++ {
				time.Sleep(5 * time.Millisecond)
				b, _ := os.ReadFile(runLog)
				logged = string(b)
			}
			if !strings.Contains(logged, c.want) {
				t.Fatalf("log %q, want %q", logged, c.want)
			}
			if n := len(entries(t, p)); n != map[bool]int{true: 1, false: 2}[c.name == "skipped"] {
				t.Fatalf("%d entries", n)
			}
		})
	}
}

// A helper with no entries is still told its Experience exists, how it fills and how to search it.
func TestEmptyJournalLine(t *testing.T) {
	p, _, _ := journalSetup(t, harness.Claude)
	if out := p.journalLines(planKey, "", "anything"); !strings.Contains(out, "You have an Experience journal") || !strings.Contains(out, "`Done:`") || !strings.Contains(out, "experience search <words>") {
		t.Fatalf("empty journal: %q", out)
	}
}
