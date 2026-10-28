package agentdb

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCompassLinesAndSearch(t *testing.T) {
	d, _ := openRaw(t)
	a, _ := d.PutLine(Line{Owner: "me", Body: "Prefer deterministic routing over model calls"})
	b, _ := d.PutLine(Line{Owner: "me", Body: "CloudKit assets need a record zone", Condition: "iOS apps", Tag: "perm:allow:Bash:npm test"})
	_, _ = d.PutLine(Line{Owner: "someone else", Body: "The owner works in Pacific time"})

	if ls, _ := d.Lines("me", "", "", 0); len(ls) != 2 {
		t.Fatalf("me sees %d", len(ls))
	}
	// Full-text search (the condition too), with input that would be FTS syntax passed through safely.
	ls, err := d.Lines("me", `ios "zone" OR NOT(`, "", 0)
	if err != nil || len(ls) != 1 || ls[0].UID != b || ls[0].Origin != "stated" || ls[0].Kind != "preference" {
		t.Fatalf("search = %+v %v", ls, err)
	}
	if ls, _ := d.Lines("me", "", "perm:allow:Bash:npm test", 0); len(ls) != 1 || ls[0].UID != b {
		t.Fatalf("by tag = %+v", ls)
	}
	l, _ := d.Line(a)
	l.Body, l.Source = "Prefer lightweight deterministic paths", "owner"
	if _, err := d.PutLine(l); err != nil {
		t.Fatal(err)
	}
	if ls, _ := d.Lines("me", "routing", "", 0); len(ls) != 0 {
		t.Fatalf("stale FTS row: %+v", ls)
	}
	if ls, _ := d.Lines("me", "lightweight", "", 0); len(ls) != 1 || ls[0].Source != "owner" {
		t.Fatalf("updated row not indexed: %+v", ls)
	}
	if _, err := d.Line("nope"); err != ErrNotFound {
		t.Fatalf("Line(nope) = %v", err)
	}
	// A delete takes the line's effects and leaves a tombstone, so a shared folder deletes it everywhere.
	sig, _ := d.AddSignal(Signal{Owner: "me", Kind: "chose", Words: "x"})
	_ = d.AddEffects([]Effect{{SignalUID: sig, LineUID: a, PSup: 1, Weight: 1, At: "2026-01-01T00:00:00Z"}})
	if err := d.DeleteLine(a); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM tombstones WHERE key = 'compass:' || ?`, a); n != 1 {
		t.Fatalf("tombstones = %d", n)
	}
	if n := count(t, d, `SELECT count(*) FROM signal_effects WHERE line_uid = ?`, a); n != 0 {
		t.Fatalf("the deleted line's effects stayed: %d", n)
	}
}

func TestExperienceSearchAndCount(t *testing.T) {
	d, _ := openRaw(t)
	const gcp, web = "github.com/o/gcp", "github.com/o/web"
	add := func(helper, at, title, body string) {
		t.Helper()
		if ok, err := d.AddExperience(Entry{Helper: helper, At: at, Title: title, Body: body}); err != nil || !ok {
			t.Fatalf("add %q: %v %v", title, ok, err)
		}
	}
	add(gcp, "2026-01-01T00:00:00Z", "Deploy the API", "Pass --region us-west1; the default is wrong here")
	add(gcp, "2026-01-02T00:00:00Z", "Rotate keys", "Keys live in ~/creds; deploy needs none")
	add(gcp, "2026-01-03T00:00:00Z", "Fix the linter", "Run it before the tests")
	add(web, "2026-01-04T00:00:00Z", "Deploy the site", "another helper's entry")
	hits, total, err := d.SearchExperience(gcp, "", `deploy "region" OR NOT(`, 5)
	if err != nil || total != 2 || len(hits) != 2 || hits[0].Title != "Deploy the API" || hits[0].Snippet == "" || hits[0].Body != "" {
		t.Fatalf("search = %+v %d %v", hits, total, err)
	}
	if hits, total, _ := d.SearchExperience(gcp, "", "deploy", 1); total != 2 || len(hits) != 1 {
		t.Fatalf("limit: %+v %d", hits, total)
	}
	// No words or no match: the newest, all counted; never another helper's.
	for _, q := range []string{"", "zzzz"} {
		hits, total, _ := d.SearchExperience(gcp, "", q, 2)
		if total != 3 || len(hits) != 2 || hits[0].Title != "Fix the linter" {
			t.Fatalf("newest for %q = %+v %d", q, hits, total)
		}
	}
	l, _ := d.ListExperience(gcp)
	if len(l) != 3 || l[0].Body != "" {
		t.Fatalf("list = %+v", l)
	}
	e, err := d.ExperienceEntry(l[0].ID)
	if err != nil || e.Body != "Run it before the tests" || e.Helper != gcp {
		t.Fatalf("entry = %+v %v", e, err)
	}
	if _, err := d.ExperienceEntry(999); err != ErrNotFound {
		t.Fatalf("missing entry = %v", err)
	}
	if err := d.DeleteExperience(e.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _, _ := d.SearchExperience(gcp, "", "linter", 5); len(hits) > 0 && hits[0].Title == "Fix the linter" {
		t.Fatalf("a deleted entry is still found: %+v", hits)
	}
}

// An import twice adds nothing, and a deleted entry's tombstone keeps it from coming back.
func TestAddExperienceDedupesAndRespectsTombstones(t *testing.T) {
	d, _ := openRaw(t)
	e := Entry{Helper: "k", At: "2026-01-01T00:00:00Z", Title: "t", Body: "b"}
	if ok, _ := d.AddExperience(e); !ok {
		t.Fatal("first add refused")
	}
	if ok, _ := d.AddExperience(e); ok {
		t.Fatal("same helper, at and title added twice")
	}
	l, _ := d.ListExperience("k")
	if err := d.DeleteExperience(l[0].ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.AddExperience(e); ok {
		t.Fatal("a tombstoned entry came back")
	}
	if n := count(t, d, `SELECT count(*) FROM experience`); n != 0 {
		t.Fatalf("%d entries", n)
	}
}

func TestExperienceBodyHashAndTeam(t *testing.T) {
	d, _ := openRaw(t)
	if ok, _ := d.AddExperience(Entry{Helper: "a", At: "2026-01-01T00:00:00Z", Title: "one", Body: "same"}); !ok {
		t.Fatal("first add refused")
	}
	if ok, _ := d.AddExperience(Entry{Helper: "a", At: "2026-02-01T00:00:00Z", Title: "two", Body: "same"}); ok {
		t.Fatal("the same body was kept twice")
	}
	if ok, _ := d.AddExperience(Entry{Helper: "b", At: "2026-02-01T00:00:00Z", Title: "two", Body: "same"}); !ok {
		t.Fatal("another helper's same body refused")
	}
	_, _ = d.AddExperience(Entry{Helper: "a", At: "2026-03-01T00:00:00Z", Title: "other team", Body: "cache one", TeamID: "t1"})
	_, _ = d.AddExperience(Entry{Helper: "a", At: "2026-03-02T00:00:00Z", Title: "this team", Body: "cache two", TeamID: "t2"})
	_, _ = d.AddExperience(Entry{Helper: "a", At: "2026-03-03T00:00:00Z", Title: "newest", Body: "cache three", TeamID: "t1"})
	if hits, _, _ := d.SearchExperience("a", "t2", "cache", 3); hits[0].Title != "this team" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits, _, _ := d.SearchExperience("a", "t2", "", 3); hits[0].Title != "this team" {
		t.Fatalf("newest hits = %+v", hits)
	}
	if err := d.RekeyAgent("a", "c"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM experience WHERE helper = 'c' AND body_hash != '' AND (team_id = 't2') = (title = 'this team')`); n != 4 {
		t.Fatalf("%d rekeyed entries carry their hash and team", n)
	}
}

// A shared file from before body_hash and team_id still merges: the hash is made, the team is empty.
func TestMergeOldExperience(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE experience(uid TEXT NOT NULL UNIQUE, helper TEXT NOT NULL, at TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL)`,
		`INSERT INTO experience VALUES ('u', 'k', 'n', 't', 'b')`, `PRAGMA user_version = 3`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	old.Close()
	d, _ := openRaw(t)
	if _, err := d.Merge(file, file+".out"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM experience WHERE uid = 'u' AND team_id = '' AND body_hash = '`+bodyHash("b")+`'`); n != 1 {
		t.Fatalf("old entry not imported with its hash: %d", n)
	}
	if ok, _ := d.AddExperience(Entry{Helper: "k", Title: "again", Body: "b"}); ok {
		t.Fatal("the imported body was kept twice")
	}
}

// Entries merge by uid: they reach the other machine, and a delete (a tombstone) does too.
func TestMergeExperience(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	_, _ = a.AddExperience(Entry{Helper: "k", Title: "one", Body: "x"})
	for _, d := range []*DB{a, b, a} {
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := b.ListExperience("k")
	if len(l) != 1 {
		t.Fatalf("entries on b = %+v", l)
	}
	if err := b.DeleteExperience(l[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DB{b, a, b} {
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ := a.ListExperience("k"); len(l) != 0 {
		t.Fatalf("the deleted entry came back on a: %+v", l)
	}
}

// A table whose columns differ from the schema is rebuilt with the columns both share; tables of
// an earlier schema wait for Upgrade, which drops them, counted (a helper's lessons included).
func TestReconcileAndUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE memories(id INTEGER PRIMARY KEY, scope TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'note', tag TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, evidence TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'active', source TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, retired INTEGER NOT NULL DEFAULT 0);
CREATE TABLE raw_log(id INTEGER PRIMARY KEY, team_id TEXT NOT NULL DEFAULT '', line TEXT NOT NULL DEFAULT '', at TEXT NOT NULL DEFAULT '');
CREATE TABLE log_entries(id INTEGER PRIMARY KEY, scope TEXT NOT NULL, subject TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE team_tasks(id TEXT PRIMARY KEY);
CREATE TABLE experience(id INTEGER PRIMARY KEY, agent TEXT, lesson TEXT);
INSERT INTO experience VALUES (1, 'a', 'x'), (2, 'a', 'y');
CREATE TABLE kanban_state(team_id TEXT PRIMARY KEY, grouped_through INTEGER NOT NULL DEFAULT 0, built_at TEXT NOT NULL DEFAULT '');
INSERT INTO kanban_state VALUES ('t1', 7, 'then');
CREATE TABLE tombstones(uid TEXT PRIMARY KEY, at TEXT NOT NULL);
CREATE TRIGGER memories_tombstone AFTER DELETE ON memories BEGIN INSERT OR IGNORE INTO tombstones(uid, at) VALUES ('x', 'y'); END;
INSERT INTO memories(scope, body, created_at, updated_at) VALUES ('global', 'keep me', 'x', 'x'), ('skill:sk1', 'Verify every image''s copyright', 'x', 'x');
INSERT INTO memories(scope, tag, source, title, body, created_at, updated_at) VALUES ('team:t1', 'roster', 'pengupool', 'gcp', 'joined', 'x', 'x');
INSERT INTO raw_log(team_id, line, at) VALUES ('t1', '10:00 · a · did it', 'x');
INSERT INTO log_entries(scope, subject, title, body, created_at) VALUES
  ('skill:sk1', 'deploy', '2026-10-01 · session', 'Cue: deploying\nLesson: pass the region\nEvidence: failed twice', 'x'),
  ('team:t1', 'job', 'x', 'a team log entry', 'x');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('raw_log', 'raw_fts', 'summary_state')`); n != 0 { // the earlier raw log is no table of this schema
		t.Fatalf("raw log tables stayed: %d", n)
	}
	if !d.Legacy() {
		t.Fatal("legacy tables gone before Upgrade read them")
	}
	if _, err := os.Stat(PreUpgradePath(path)); err != nil {
		t.Fatalf("no copy before the upgrade: %v", err)
	}
	dropped, err := d.Upgrade("me")
	if err != nil || dropped["log_entries"] != 2 || dropped["kanban_state"] != 1 || dropped["memories (not the owner's)"] != 2 || dropped["memories (not carried over)"] != 1 {
		t.Fatalf("upgrade = %v %v", dropped, err)
	}
	if ls, _ := d.Lines("me", "", "", 0); len(ls) != 0 { // 'keep me' is a note, the roster row is the team's: neither is a line
		t.Fatalf("lines = %+v", ls)
	}
	if d.Legacy() {
		t.Fatal("legacy tables left")
	}
	if dropped, err := d.Upgrade("me"); err != nil || dropped != nil {
		t.Fatalf("a second upgrade did something: %v %v", dropped, err)
	}
}

// Another program editing the file directly is refused: it has no pp_host().
func TestTablesAreHostOnly(t *testing.T) {
	d, path := openRaw(t)
	d.Close()
	sh, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("no sqlite3 shell")
	}
	for _, q := range []string{
		"UPDATE compass SET body = 'x';", "INSERT INTO meta VALUES ('k', '1');", "INSERT INTO experience(uid, helper, at, title, body) VALUES ('u','a','n','t','b');",
		"DELETE FROM tombstones;",
	} {
		if out, err := exec.Command(sh, path, q).CombinedOutput(); err == nil {
			t.Fatalf("%s from the sqlite3 shell succeeded: %s", q, out)
		}
	}
}

// Two machines merging through one shared file end up with the same Compass lines, notes;  a delete reaches both, the newer edit wins, and the file holds the shared tables only.
func TestMergeSharedFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pengupool-agents.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	ma, _ := a.PutLine(Line{Owner: "me", Body: "from a"})
	_, _ = b.PutLine(Line{Owner: "me", Body: "from b"})
	_, _ = a.AddExperience(Entry{Helper: "github.com/o/x", Title: "a job", Body: "a lesson"})
	for _, d := range []*DB{a, b, a} {
		if err := syncFile(d, file); err != nil {
			t.Fatal(err)
		}
	}
	for name, d := range map[string]*DB{"a": a, "b": b} {
		if ls, _ := d.Lines("me", "", "", 0); len(ls) != 2 {
			t.Errorf("%s lines = %+v", name, ls)
		}
		if l, _ := d.ListExperience("github.com/o/x"); len(l) != 1 {
			t.Errorf("%s entries = %+v", name, l)
		}
	}
	if ts, _ := fileTables(t, file); !slices.Equal(ts, []string{"compass", "experience", "signal_effects", "signals", "tombstones"}) {
		t.Fatalf("the file holds %v", ts)
	}
	m, _ := a.Line(ma)
	m.Body = "from a, edited"
	_, _ = a.PutLine(m)
	_ = syncFile(a, file)
	_ = syncFile(b, file)
	if ls, _ := b.Lines("me", "edited", "", 0); len(ls) != 1 {
		t.Fatal("the newer edit did not win")
	}
	_ = b.DeleteLine(ma)
	_ = syncFile(b, file)
	_ = syncFile(a, file)
	if ls, _ := a.Lines("me", "edited", "", 0); len(ls) != 0 {
		t.Fatal("a delete did not reach the other machine")
	}
	if n := fileCount(t, file, `SELECT count(*) FROM compass WHERE body LIKE '%edited%'`); n != 0 {
		t.Fatal("the file kept a deleted row")
	}
}

// Every table's columns read from its definition are the file's, and a reopen rebuilds nothing:
// an entry stays.
func TestReopenKeepsTables(t *testing.T) {
	d, path := openRaw(t)
	for _, tb := range tables {
		have, _ := d.strings(`SELECT name FROM pragma_table_info(?)`, tb.name)
		if !slices.Equal(have, columnNames(tb.cols)) {
			t.Errorf("%s: definition reads %v, file has %v", tb.name, columnNames(tb.cols), have)
		}
	}
	root := func(d *DB) []string {
		r, _ := d.strings(`SELECT name || rootpage FROM sqlite_master WHERE type = 'table' ORDER BY name`)
		return r
	}
	before := root(d)
	d.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if after := root(d); !slices.Equal(before, after) {
		t.Fatalf("a reopen rebuilt tables: %v → %v", before, after)
	}
}

// Upgrade turns the owner's global preferences, restrictions and decisions into Compass lines (a
// decision with the choice it records), a team's direction into a PenguPool line of its raw log,
// and drops everything else, counted.
func TestUpgradeCompass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE memories(id INTEGER PRIMARY KEY, scope TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'note', tag TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, evidence TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'active', source TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE memory_shares(memory_id INTEGER NOT NULL, scope TEXT NOT NULL);
CREATE VIRTUAL TABLE memories_fts USING fts5(title, body, evidence, content='memories', content_rowid='id');
INSERT INTO memories(scope, kind, body, source, created_at, updated_at) VALUES
  ('global', 'preference', 'Keep replies short', 'owner', '2026-01-01T00:00:00Z', 'x'),
  ('global', 'restriction', 'Never touch prod', 'triage', '2026-01-02T00:00:00Z', 'x'),
  ('global', 'fact', 'Runner 3 has 16 GB', 'owner', '2026-01-03T00:00:00Z', 'x'),
  ('team:t1', 'fact', 'Deploy on Cloud Run', 'owner', '2026-01-04T00:00:00Z', 'x');
INSERT INTO memories(scope, kind, body, state, source, created_at, updated_at) VALUES ('global', 'preference', 'proposed one', 'proposed', 'triage', 'x', 'x');
INSERT INTO memories(scope, kind, tag, body, source, created_at, updated_at) VALUES ('global', 'decision', 'perm:allow:Bash:npm test', 'The owner allows Bash for npm test', 'owner-decision', '2026-01-05T00:00:00Z', 'x');
INSERT INTO memories(scope, kind, body, evidence, source, created_at, updated_at) VALUES ('global', 'decision', 'The owner lets agents go on', '2026-02-01 · The agent asked (end): go on?
Owner: yes
---
2026-02-02 · The agent asked (end): go on?
Owner: yes, go
---
2026-02-03 · The agent asked (end): go on?
Owner: go on', 'owner-decision', '2026-02-01T00:00:00Z', 'x');
INSERT INTO memory_shares VALUES (1, 'team:t1');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dropped, err := d.Upgrade("me")
	if err != nil || dropped["memories (not carried over)"] != 2 || dropped["memory_shares"] != 1 || dropped["memories"] != 0 {
		t.Fatalf("dropped = %v %v", dropped, err)
	}
	ls, _ := d.Lines("me", "", "", 0)
	got := map[string]Line{}
	for _, l := range ls {
		got[l.Body] = l
	}
	if len(ls) != 4 {
		t.Fatalf("lines = %+v", ls)
	}
	for body, want := range map[string][4]string{
		"Keep replies short":                 {"preference", "stated", "owner", ""},
		"Never touch prod":                   {"restriction", "stated", "triage", ""},
		"The owner allows Bash for npm test": {"preference", "chose", "gate", "perm:allow:Bash:npm test"},
	} {
		l := got[body]
		if [4]string{l.Kind, l.Origin, l.Source, l.Tag} != want {
			t.Errorf("%q migrated as %+v", body, l)
		}
	}
	es, _ := d.Effects()
	if e := es[got["The owner allows Bash for npm test"].UID]; len(e) != 1 || e[0].PSup != 1 || e[0].PCon != 0 || e[0].Weight != 1 || e[0].Kind != "chose" || e[0].At != "2026-01-05T00:00:00Z" {
		t.Errorf("the decision's choice = %+v", e)
	}
	// Three answers on three days replay to alpha 4, beta 1.
	multi := es[got["The owner lets agents go on"].UID]
	last, _ := time.Parse(time.RFC3339, multi[len(multi)-1].At)
	if b := Replay(PriorChose, multi, last); len(multi) != 3 || !near(b.Alpha, 4) || !near(b.Beta, 1) || multi[2].At != "2026-02-03T12:00:00Z" {
		t.Errorf("the decision's three answers = %+v, belief %+v", multi, b)
	}
	if len(es[got["Keep replies short"].UID]) != 0 {
		t.Error("a stated line got an effect")
	}
	if n := count(t, d, `SELECT count(*) FROM signals WHERE kind = 'chose'`); n != 4 {
		t.Errorf("signals = %d", n)
	}
	if d.Legacy() {
		t.Error("legacy tables left")
	}
}

// A file of schema 2 opens with the notes, raw log, cursors and lesson counts gone and the compass kept.
func TestReconcileDropsNotesAndRawLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := d.PutLine(Line{Owner: "me", Body: "keep the compass"})
	d.Close()
	raw, _ := sql.Open("sqlite", "file:"+path)
	if _, err := raw.Exec(`CREATE TABLE experience_docs(uid TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL, scope TEXT NOT NULL, version INTEGER NOT NULL, bullets TEXT NOT NULL DEFAULT '[]', by TEXT NOT NULL DEFAULT '', at TEXT NOT NULL, base TEXT NOT NULL DEFAULT '');
CREATE TABLE raw_log(id INTEGER PRIMARY KEY, line TEXT);
CREATE VIRTUAL TABLE raw_fts USING fts5(line, content='raw_log', content_rowid='id');
CREATE TABLE summary_state(owner TEXT PRIMARY KEY);
CREATE TABLE lesson_use(id INTEGER PRIMARY KEY);
INSERT INTO experience_docs(uid, agent, scope, version, at) VALUES ('u', 'k', 'solo', 1, 'n');
INSERT INTO tombstones(key, at) VALUES ('experience_docs:u', 'n');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('experience_docs', 'raw_log', 'raw_fts', 'summary_state', 'lesson_use') OR name LIKE 'raw_fts_%'`); n != 0 {
		t.Fatalf("%d old tables stayed", n)
	}
	if n := count(t, d, `SELECT count(*) FROM tombstones WHERE key LIKE 'experience_docs:%'`); n != 0 {
		t.Fatal("an old tombstone stayed")
	}
	if _, err := d.Line(uid); err != nil {
		t.Fatalf("the compass line is gone: %v", err)
	}
	if ok, err := d.AddExperience(Entry{Helper: "k", Title: "t", Body: "b"}); err != nil || !ok {
		t.Fatalf("add after the upgrade: %v %v", ok, err)
	}
}

func openRaw(t *testing.T) (*DB, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, p
}

func open(t *testing.T) *DB {
	t.Helper()
	d, _ := openRaw(t)
	return d
}

func count(t *testing.T, d *DB, q string, a ...any) (n int) {
	t.Helper()
	if err := d.db.QueryRow(q, a...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return
}

// An earlier file's team records go on Open: the Kanban tables and the tombstones of epics and tickets.
func TestOpenDropsTeamRecords(t *testing.T) {
	d, p := openRaw(t)
	for _, q := range []string{
		`CREATE TABLE epics(id TEXT PRIMARY KEY, team_id TEXT)`, `CREATE TABLE tickets(id TEXT PRIMARY KEY)`,
		`CREATE TABLE milestones(id INTEGER PRIMARY KEY)`, `CREATE TABLE ticket_entries(ticket_id TEXT)`, `CREATE TABLE handoffs(id INTEGER PRIMARY KEY)`,
		`INSERT INTO tombstones(key, at) VALUES ('epics:e1', 'x'), ('tickets:t1', 'x'), ('compass:c1', 'x')`,
	} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	d.Close()
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('epics', 'tickets', 'milestones', 'ticket_entries', 'handoffs')`); n != 0 {
		t.Errorf("%d Kanban tables stayed", n)
	}
	if n := count(t, d, `SELECT count(*) FROM tombstones`); n != 1 {
		t.Errorf("tombstones = %d", n)
	}
	if d.Legacy() {
		t.Error("legacy tables left")
	}
}
