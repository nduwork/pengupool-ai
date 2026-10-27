package agentdb

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The change count is by row content: a first merge adds, an unchanged one counts nothing, an edit
// is "~", and an older file is upgraded.
func TestMergeCounts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pengupool-agents.db")
	d, _ := openRaw(t)
	_, _ = d.PutLine(Line{Owner: "me", Body: "one"})
	id, _ := d.PutLine(Line{Owner: "me", Body: "two"})
	_, _ = d.AddExperience(Entry{Helper: "a", Title: "t", Body: "b"})
	res, err := d.Merge(file, file+".out")
	if err != nil || res.Changes != "compass +2, experience +1" {
		t.Fatalf("first merge: %+v %v", res, err)
	}
	_ = os.Rename(file+".out", file)
	if res, err = d.Merge(file, file+".out"); err != nil || res.Changes != "" {
		t.Fatalf("unchanged: %+v %v", res, err)
	}
	l, _ := d.Line(id)
	l.Body = "two, edited"
	_, _ = d.PutLine(l)
	if res, _ = d.Merge(file, file+".out"); res.Changes != "compass ~1" {
		t.Fatalf("edit: %+v", res)
	}
	if n := fileCount(t, file+".out", `PRAGMA user_version`); n != SchemaVersion {
		t.Fatalf("user_version %d", n)
	}
}

// A file that is not SQLite, or fails its check, is damaged and the database is untouched; a file
// from a newer PenguPool merges its shared columns and writes nothing.
func TestMergeDamagedAndNewer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pengupool-agents.db")
	d, _ := openRaw(t)
	_, _ = d.PutLine(Line{Owner: "me", Body: "mine"})
	_ = os.WriteFile(file, []byte(strings.Repeat("not a database ", 500)), 0o600)
	var bad *DamagedError
	if _, err := d.Merge(file, file+".out"); !errors.As(err, &bad) {
		t.Fatalf("a text file: %v", err)
	}
	if _, err := os.Stat(file + ".out"); err == nil {
		t.Fatal("an outgoing file was made from a damaged one")
	}
	// A newer file: an extra column and a row this database lacks.
	other, _ := openRaw(t)
	_, _ = other.PutLine(Line{Owner: "me", Body: "from a newer one"})
	if res, _ := other.Merge(filepath.Join(t.TempDir(), "none"), file+".new"); res.Changes == "" {
		t.Fatal("no file")
	}
	db, _ := sql.Open("sqlite", "file:"+file+".new")
	for _, q := range []string{`ALTER TABLE compass ADD COLUMN mood TEXT NOT NULL DEFAULT ''`, `PRAGMA user_version = 99`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	res, err := d.Merge(file+".new", file+".out")
	if err != nil || !res.Newer {
		t.Fatalf("newer: %+v %v", res, err)
	}
	if _, err := os.Stat(file + ".out"); err == nil {
		t.Fatal("wrote an outgoing file for a newer schema")
	}
	if ls, _ := d.Lines("me", "", "", 0); len(ls) != 2 {
		t.Fatalf("the shared columns did not merge: %+v", ls)
	}
}

// What a 0.10 machine left in shared/ merges once, through the same row merge.
func TestImportShared(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compass.jsonl"), []byte(`{"uid":"u1","owner":"me","body":"old line","kind":"preference","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "tombstones.jsonl"), []byte(`{"key":"compass:gone","at":"2026-01-02T00:00:00Z"}`+"\n"), 0o600)
	file := filepath.Join(t.TempDir(), "legacy.db")
	if err := ImportShared(dir, file, "me"); err != nil {
		t.Fatal(err)
	}
	d, _ := openRaw(t)
	if _, err := d.Merge(file, file+".out"); err != nil {
		t.Fatal(err)
	}
	if ls, _ := d.Lines("me", "", "", 0); len(ls) != 1 || ls[0].Body != "old line" {
		t.Fatalf("lines %+v", ls)
	}
	if n := fileCount(t, file+".out", `SELECT count(*) FROM tombstones`); n != 1 {
		t.Fatalf("tombstones %d", n)
	}
}

// A backup of version 1 still holding each team's epics, milestones and tickets merges without
// them (nor the helpers' old notes): its tombstones for them are dropped too, and the file written
// back holds the v3 layout only.
func TestMergeIgnoresTeamTables(t *testing.T) {
	file := filepath.Join(t.TempDir(), "backup.db")
	old, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE epics(id TEXT PRIMARY KEY, team_id TEXT NOT NULL, title TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE tickets(id TEXT PRIMARY KEY, team_id TEXT NOT NULL, title TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE milestones(epic_id TEXT NOT NULL, ord INTEGER NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE experience_docs(uid TEXT PRIMARY KEY, agent TEXT NOT NULL, scope TEXT NOT NULL, version INTEGER NOT NULL, bullets TEXT NOT NULL, at TEXT NOT NULL)`, `INSERT INTO experience_docs VALUES ('x', 'k', 'solo', 1, '[]', 'x')`,
		`INSERT INTO epics VALUES ('e1', 't', 'Ship it', 'x')`, `INSERT INTO tickets VALUES ('tk', 't', 'Build', 'x')`, `INSERT INTO milestones VALUES ('e1', 0, 'build')`,
		`CREATE TABLE tombstones(key TEXT PRIMARY KEY, at TEXT NOT NULL)`, `INSERT INTO tombstones VALUES ('epics:e2', 'x')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	old.Close()
	d, _ := openRaw(t)
	_, _ = d.PutLine(Line{Owner: "me", Body: "one"})
	res, err := d.Merge(file, file+".out")
	if err != nil || res.Changes == "" {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if ts, _ := fileTables(t, file+".out"); !slices.Equal(ts, []string{"compass", "experience", "signal_effects", "signals", "tombstones"}) {
		t.Fatalf("the file holds %v", ts)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('epics', 'tickets', 'milestones')`); n != 0 {
		t.Fatalf("the database took %d team tables", n)
	}
	if n := fileCount(t, file+".out", `SELECT count(*) FROM tombstones`); n != 0 {
		t.Fatalf("tombstones of team rows kept: %d", n)
	}
}

// A tombstone without a key (left by deleting a row whose uid was NULL) must not stop the merge.
func TestMergeDropsKeylessTombstones(t *testing.T) {
	file := filepath.Join(t.TempDir(), "backup.db")
	d, _ := openRaw(t)
	_, _ = d.PutLine(Line{Owner: "me", Body: "one"})
	if _, err := d.db.Exec(`INSERT INTO tombstones(key, at) VALUES (NULL, '2026-10-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Merge(file, file+".out"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if n := fileCount(t, file+".out", `SELECT count(*) FROM tombstones WHERE key IS NULL`); n != 0 {
		t.Fatalf("%d keyless tombstones went out", n)
	}
}
