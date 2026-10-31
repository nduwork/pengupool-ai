package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

const plan = "planaaaaaaaa"

// journalSetup makes plan a helper of the given harness, idle, with a transcript, and fast waits.
func journalSetup(t *testing.T, h string) (*Pool, string, *[]string) {
	t.Helper()
	p, st := helperPool(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	_ = os.WriteFile(path, nil, 0o600)
	_ = st.Update(func(s *store.State) error {
		i := sessionIndex(s, plan)
		s.Sessions[i].Harness, s.Sessions[i].Status, s.Sessions[i].Transcript = h, "idle", path
		return nil
	})
	oldWait, oldPoll := journalWait, journalPoll
	journalWait, journalPoll = 2*time.Second, 5*time.Millisecond
	t.Cleanup(func() { journalWait, journalPoll = oldWait, oldPoll })
	var typed []string
	p.testKeys = func(id, keys string) error { typed = append(typed, keys); return nil }
	return p, path, &typed
}

// done is a message from the helper to its parent, typed as on the way in (msgType).
func done(text string) protocol.TeamMessage {
	typ, _ := msgType("", text)
	return protocol.TeamMessage{TeamID: "team1", From: plan, To: "leadaaaaaaaa", Text: text, Type: typ}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintln(f, line)
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

// A Done: report to the parent, then the helper's turn end: the compact is typed once and the entry's
// body is the compaction summary the harness wrote after it, in each harness's transcript format,
// with the report kept under it.
func TestDoneReportKeepsTheCompaction(t *testing.T) {
	record := map[string]string{
		harness.Claude: `{"type":"user","isCompactSummary":true,"timestamp":"%s","message":{"role":"user","content":[{"type":"text","text":"%s"}]}}`,
		harness.Pi:     `{"type":"compaction","timestamp":"%s","summary":"%s","firstKeptEntryId":"e1"}`,
		harness.OMP:    `{"type":"compaction","timestamp":"%s","summary":"%s","firstKeptEntryId":"e1"}`,
		harness.Codex:  `{"timestamp":"%s","type":"compacted","payload":{"message":"%s"}}`,
	}
	for h, format := range record {
		t.Run(h, func(t *testing.T) {
			p, path, typed := journalSetup(t, h)
			line := func(at time.Time, summary string) string {
				return fmt.Sprintf(format, at.UTC().Format(time.RFC3339Nano), summary)
			}
			appendLine(t, path, line(time.Now().Add(-time.Hour), "an earlier compaction")) // ignored: before the compact
			p.testKeys = func(id, keys string) error {
				*typed = append(*typed, keys)
				appendLine(t, path, line(time.Now(), "the summary of the work"))
				return nil
			}
			p.doneReport(done("done: fixed the login redirect\nIt now keeps the next page."))
			if len(entries(t, p)) != 0 {
				t.Fatal("an entry before the compaction")
			}
			p.journalTurnEnd(plan)
			e := waitEntry(t, p)
			if e.Title != "fixed the login redirect" || e.Body != "the summary of the work\n\n## Done report\n\ndone: fixed the login redirect\nIt now keeps the next page." || e.Helper != planKey {
				t.Fatalf("entry = %+v", e)
			}
			if len(*typed) != 1 || (*typed)[0] != "/compact\r" {
				t.Fatalf("typed %q", *typed)
			}
		})
	}
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
	p.journalTurnEnd(plan)
	time.Sleep(50 * time.Millisecond)
	if es := entries(t, p); len(es) != 0 || len(*typed) != 0 {
		t.Fatalf("entries %+v, typed %q", es, *typed)
	}
}

// The report is the body when nothing better exists: a harness with no readable transcript, a helper
// that is working again, a compaction that never lands, and a second report joins the first.
func TestDoneReportFallsBackToTheReport(t *testing.T) {
	t.Run("no compact for the harness", func(t *testing.T) {
		p, _, typed := journalSetup(t, harness.Muse) // Muse has no compact command
		p.doneReport(done("Done: shipped\nthe report"))
		if e := waitEntry(t, p); e.Title != "shipped" || e.Body != "Done: shipped\nthe report" || len(*typed) != 0 {
			t.Fatalf("entry %+v, typed %q", e, *typed)
		}
	})
	t.Run("working again", func(t *testing.T) {
		p, st := helperPool(t)
		path := filepath.Join(t.TempDir(), "s.jsonl")
		_ = os.WriteFile(path, nil, 0o600)
		_ = st.Update(func(s *store.State) error {
			i := sessionIndex(s, plan)
			s.Sessions[i].Harness, s.Sessions[i].Status, s.Sessions[i].Transcript = harness.Claude, "working", path
			return nil
		})
		p.testKeys = func(string, string) error { t.Error("typed into a working helper"); return nil }
		p.doneReport(done("Done: x\nthe report"))
		p.journalTurnEnd(plan)
		if e := waitEntry(t, p); e.Body != "Done: x\nthe report" {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("the compaction never lands", func(t *testing.T) {
		p, _, typed := journalSetup(t, harness.Claude)
		journalWait = 100 * time.Millisecond
		p.doneReport(done("Done: first\none"))
		p.doneReport(done("Done: second\ntwo")) // joins; one compact
		p.journalTurnEnd(plan)
		p.journalTurnEnd(plan)
		e := waitEntry(t, p)
		if e.Title != "first" || e.Body != "Done: first\none\n\nDone: second\ntwo" || len(*typed) != 1 {
			t.Fatalf("entry %+v, typed %q", e, *typed)
		}
		time.Sleep(200 * time.Millisecond)
		if es := entries(t, p); len(es) != 1 {
			t.Fatalf("entries %+v", es)
		}
	})
	t.Run("the host shuts down first", func(t *testing.T) {
		p, _, _ := journalSetup(t, harness.Claude)
		p.doneReport(done("Done: migrated\nthe report"))
		p.journalTurnEnd(plan) // the compact is typed; no summary lands before the host stops
		p.StopJournal()
		if es := entries(t, p); len(es) != 1 {
			t.Fatalf("a waiting report was lost at shutdown: %+v", es)
		}
		if e := waitEntry(t, p); e.Body != "Done: migrated\nthe report" {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("secrets", func(t *testing.T) {
		p, _, _ := journalSetup(t, harness.Muse)
		p.doneReport(done("Done: key sk-ant-abcdefghijklmnopqrstuvwxyz0123 rotated\nsk-ant-abcdefghijklmnopqrstuvwxyz0123"))
		if e := waitEntry(t, p); strings.Contains(e.Title+e.Body, "sk-ant-") {
			t.Fatalf("entry %+v", e)
		}
	})
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
	if !strings.Contains(p.renderContext(plan), "start your report to your parent with `Done:`") {
		t.Fatal("the helper is not told to start its report with Done:")
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

// Claude Code's compaction text loses its wrapping: the opening, the tools list and the transcript pointer.
func TestClaudeSummaryUnwrapped(t *testing.T) {
	in := "This session is being continued from a previous conversation that ran out of context. The summary below covers it.\n\n<tools-used-before-summary>Bash, Read</tools-used-before-summary>\nSummary:\n1. Fixed the redirect.\n\nIf you need specific details from before compaction, read the full transcript at: /x.jsonl\nContinue."
	if got := claudeSummary(in); got != "Summary:\n1. Fixed the redirect." {
		t.Fatalf("claudeSummary = %q", got)
	}
}
