package agentdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// The same removed entries are one row, however often and on whichever machine they are recorded.
func TestAddRemovedDedups(t *testing.T) {
	d := open(t)
	h1, added, err := d.AddRemoved("settings.json", `{"statusLine":{"command":"x"}}`)
	if err != nil || !added {
		t.Fatalf("first: %v %v", added, err)
	}
	if h2, added, _ := d.AddRemoved("settings.json", `{"statusLine":{"command":"x"}}`); added || h2 != h1 {
		t.Errorf("a repeat added a row or changed the hash: %v %s", added, h2)
	}
	if _, added, _ := d.AddRemoved("settings.local.json", `{"statusLine":{"command":"x"}}`); !added {
		t.Error("the same entries of another file are another record")
	}
	if rs, _ := d.ListRemoved(); len(rs) != 2 {
		t.Errorf("%d records", len(rs))
	}
}

// Two machines each hold what they recorded; a sync unions them with no duplicate, both ways.
func TestRemovedMergesAcrossTwoDBs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "shared.db")
	a, b := open(t), open(t)
	_, _, _ = a.AddRemoved("settings.json", `{"a":1}`)
	_, _, _ = a.AddRemoved("settings.json", `{"both":1}`)
	_, _, _ = b.AddRemoved("settings.json", `{"both":1}`)
	_, _, _ = b.AddRemoved("settings.json", `{"b":1}`)
	for _, d := range []*DB{a, b, a} { // a pushes, b merges and pushes, a merges
		res, err := d.Merge(file, file+".out")
		if err != nil || res.Newer {
			t.Fatalf("merge: %+v %v", res, err)
		}
		if err := os.Rename(file+".out", file); err != nil {
			t.Fatal(err)
		}
	}
	for name, d := range map[string]*DB{"a": a, "b": b} {
		if rs, _ := d.ListRemoved(); len(rs) != 3 {
			t.Errorf("%s holds %d records, want 3", name, len(rs))
		}
	}
	if n := fileCount(t, file, `SELECT count(*) FROM removed_entries`); n != 3 {
		t.Errorf("the shared file holds %d", n)
	}
}

// A file of version 3 has no such table: merged as an empty one, and written back as version 4. A
// file of a newer version is merged for the columns both share and never written over.
func TestRemovedAcrossSchemaVersions(t *testing.T) {
	d := open(t)
	_, _, _ = d.AddRemoved("settings.json", `{"mine":1}`)
	file := filepath.Join(t.TempDir(), "shared.db")
	if _, err := d.Merge(file, file+".out"); err != nil {
		t.Fatal(err)
	}
	_ = os.Rename(file+".out", file)
	set := func(qs ...string) {
		db, err := sql.Open("sqlite", "file:"+file)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, q := range qs {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	set(`DROP TABLE removed_entries`, `PRAGMA user_version = 3`)
	if res, err := d.Merge(file, file+".out"); err != nil || res.Newer || res.Changes != "removed_entries +1" {
		t.Fatalf("a version 3 file: %+v %v", res, err)
	}
	if n := fileCount(t, file+".out", `PRAGMA user_version`); n != 4 {
		t.Errorf("user_version %d", n)
	}
	_ = os.Remove(file + ".out")
	set(`CREATE TABLE removed_entries(hash TEXT PRIMARY KEY, file TEXT NOT NULL, updated_at TEXT NOT NULL, entries TEXT NOT NULL, note TEXT)`,
		`INSERT INTO removed_entries VALUES ('h5', 'settings.json', '2030-01-01T00:00:00Z', '{"from":"v5"}', 'x')`, `PRAGMA user_version = 5`)
	res, err := d.Merge(file, file+".out")
	if err != nil || !res.Newer {
		t.Fatalf("a version 5 file: %+v %v", res, err)
	}
	if _, err := os.Stat(file + ".out"); err == nil {
		t.Error("wrote over a newer file")
	}
	if rs, _ := d.ListRemoved(); len(rs) != 2 {
		t.Errorf("the shared columns did not merge: %d records", len(rs))
	}
}

// Two machines record the same entries at different times: after a round each way they agree on one
// time, and the next round has nothing to push (no row is rewritten back and forth).
func TestRemovedSameEntriesConverge(t *testing.T) {
	file := filepath.Join(t.TempDir(), "shared.db")
	a, b := open(t), open(t)
	for d, at := range map[*DB]string{a: "2026-10-26T10:00:00Z", b: "2026-10-26T11:00:00Z"} {
		_, _, _ = d.AddRemoved("settings.json", `{"x":1}`)
		if _, err := d.db.Exec(`UPDATE removed_entries SET updated_at = ?`, at); err != nil {
			t.Fatal(err)
		}
	}
	round := func(d *DB) string {
		res, err := d.Merge(file, file+".out")
		if err != nil {
			t.Fatal(err)
		}
		if res.Changes != "" {
			_ = os.Rename(file+".out", file)
		}
		return res.Changes
	}
	round(a)
	round(b)
	round(a)
	for name, d := range map[string]*DB{"a": a, "b": b} {
		if rs, _ := d.ListRemoved(); len(rs) != 1 || rs[0].At != "2026-10-26T11:00:00Z" {
			t.Errorf("%s: %+v", name, rs)
		}
	}
	if c := round(b) + round(a) + round(b); c != "" {
		t.Errorf("still pushing after they agree: %q", c)
	}
}
