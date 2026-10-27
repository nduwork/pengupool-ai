package agentdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// syncRound merges d with the shared file and reports what changed ("": nothing).
func syncRound(t *testing.T, d *DB, file string) string {
	t.Helper()
	res, err := d.Merge(file, file+".out")
	if err != nil {
		t.Fatal(err)
	}
	if res.Changes != "" {
		if err := os.Rename(file+".out", file); err != nil {
			t.Fatal(err)
		}
	}
	return res.Changes
}

func legacyMemoryDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.db")
	raw, _ := sql.Open("sqlite", "file:"+path)
	if _, err := raw.Exec(`CREATE TABLE memories(id INTEGER PRIMARY KEY, uid TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'note', tag TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, evidence TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'active', source TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO memories(uid, scope, kind, body, source, created_at, updated_at) VALUES ('m1', 'global', 'preference', 'Keep replies short', 'owner', '2026-01-01T00:00:00Z', 'x');
INSERT INTO memories(uid, scope, kind, body, evidence, created_at, updated_at) VALUES ('m2', 'global', 'decision', 'Use tabs', '2026-01-02 first' || char(10) || 'Owner: tabs' || char(10) || '---' || char(10) || '2026-01-03 again' || char(10) || 'Owner: tabs', '2026-01-02T00:00:00Z', 'x');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Upgrade("me"); err != nil {
		t.Fatal(err)
	}
	return d
}

// Two machines that each upgrade the same 0.10 memories make the same rows: a sync unions them.
func TestUpgradeOnTwoMachinesMakesOneLine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.db")
	a, b := legacyMemoryDB(t), legacyMemoryDB(t)
	for _, d := range []*DB{a, b, a, b} {
		syncRound(t, d, file)
	}
	for _, d := range []*DB{a, b} {
		if n := count(t, d, `SELECT count(*) FROM compass`); n != 2 {
			t.Fatalf("%d lines", n)
		}
		if n := count(t, d, `SELECT count(*) FROM signals`); n != 2 {
			t.Fatalf("%d signals", n)
		}
		if n := count(t, d, `SELECT count(*) FROM signal_effects`); n != 2 {
			t.Fatalf("%d effects", n)
		}
	}
}

// A 0.10 shared/ folder's memories become the same lines an upgrade of them makes.
func TestImportSharedMemories(t *testing.T) {
	dir := t.TempDir()
	m := `{"uid":"m1","scope":"global","kind":"preference","body":"Keep replies short","source":"owner","created_at":"2026-01-01T00:00:00Z","updated_at":"x"}` + "\n" +
		`{"uid":"m9","scope":"global","kind":"fact","body":"not carried","created_at":"2026-01-01T00:00:00Z","updated_at":"x"}` + "\n"
	_ = os.WriteFile(filepath.Join(dir, "memories.jsonl"), []byte(m), 0o600)
	file := filepath.Join(t.TempDir(), "legacy.db")
	if err := ImportShared(dir, file, "me"); err != nil {
		t.Fatal(err)
	}
	d, _ := openRaw(t)
	if res, err := d.Merge(file, file+".out"); err != nil || res.Changes == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if ls, _ := d.Lines("me", "", "", 0); len(ls) != 1 || ls[0].Body != "Keep replies short" {
		t.Fatalf("lines %+v", ls)
	}
	a := legacyMemoryDB(t) // upgraded its own copy: m1 and m2
	syncRound(t, a, file)
	if n := count(t, a, `SELECT count(*) FROM compass WHERE body = 'Keep replies short'`); n != 1 {
		t.Fatalf("%d copies of the preference", n)
	}
}

// An effect another machine recorded on a line before it learned of the line's merge is kept, on
// the line it was merged into, on both machines.
func TestLineMergeKeepsEvidenceFromOtherMachine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	keep, _ := a.PutLine(Line{Owner: "me", Body: "keep", CreatedAt: "2026-01-01T00:00:00Z"})
	drop, _ := a.PutLine(Line{Owner: "me", Body: "drop", CreatedAt: "2026-01-02T00:00:00Z"})
	syncRound(t, a, file)
	syncRound(t, b, file)
	s, _ := b.AddSignal(Signal{Owner: "me", Kind: "correction", Words: "on B"})
	_ = b.AddEffects([]Effect{{SignalUID: s, LineUID: drop, PCon: 1, Weight: 5, At: now(), Kind: "correction"}})
	if err := a.MergeLines(keep, drop, ""); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DB{a, b, a, b} {
		syncRound(t, d, file)
	}
	for _, d := range []*DB{a, b} {
		if n := count(t, d, `SELECT count(*) FROM signal_effects WHERE signal_uid = ? AND line_uid = ?`, s, keep); n != 1 {
			t.Fatalf("the effect is on the kept line %d times", n)
		}
		if n := count(t, d, `SELECT count(*) FROM signal_effects WHERE line_uid = ?`, drop); n != 0 {
			t.Fatalf("%d effects stayed on the dropped line", n)
		}
	}
}

// A newer file that lacks a column this schema requires ends as Newer, not as an SQL error.
func TestNewerFileMissingColumn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "newer.db")
	raw, _ := sql.Open("sqlite", "file:"+file)
	if _, err := raw.Exec(`CREATE TABLE compass(uid TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO compass VALUES ('x', 'b', 'x', 'x'); PRAGMA user_version = 99;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, _ := openRaw(t)
	if res, err := d.Merge(file, file+".out"); err != nil || !res.Newer {
		t.Fatalf("%+v %v", res, err)
	}
}

// Merges a into b and b into c, recorded in the same second, reach a machine as one set: an effect it
// holds on a or on b ends on c.
func TestChainedLineMergesFoldToTheLastLine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	la, _ := a.PutLine(Line{Owner: "me", Body: "a", CreatedAt: "2026-01-01T00:00:00Z"})
	lb, _ := a.PutLine(Line{Owner: "me", Body: "b", CreatedAt: "2026-01-02T00:00:00Z"})
	lc, _ := a.PutLine(Line{Owner: "me", Body: "c", CreatedAt: "2026-01-03T00:00:00Z"})
	syncRound(t, a, file)
	syncRound(t, b, file)
	sa, _ := b.AddSignal(Signal{Owner: "me", Kind: "said", Words: "on a"})
	sb, _ := b.AddSignal(Signal{Owner: "me", Kind: "said", Words: "on b"})
	_ = b.AddEffects([]Effect{{SignalUID: sa, LineUID: la, PSup: 1, Weight: 1, At: now(), Kind: "said"}, {SignalUID: sb, LineUID: lb, PSup: 1, Weight: 1, At: now(), Kind: "said"}})
	if err := a.MergeLines(lb, la, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.MergeLines(lc, lb, ""); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DB{a, b, a, b} {
		syncRound(t, d, file)
	}
	for _, d := range []*DB{a, b} {
		if n := count(t, d, `SELECT count(*) FROM signal_effects WHERE line_uid = ? AND signal_uid IN (?, ?)`, lc, sa, sb); n != 2 {
			t.Fatalf("%d of the two effects reached the last line", n)
		}
	}
}

// A rekeyed helper's entries follow it to the other machine, and the old key does not come back.
func TestRekeyAgentSyncs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.db")
	a, _ := openRaw(t)
	b, _ := openRaw(t)
	_, _ = a.AddExperience(Entry{Helper: "old", At: "2026-01-01T00:00:00Z", Title: "moved", Body: "x"})
	syncRound(t, a, file)
	syncRound(t, b, file)
	if err := a.RekeyAgent("old", "new"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DB{a, b, a, b} {
		syncRound(t, d, file)
	}
	for _, d := range []*DB{a, b} {
		if l, _ := d.ListExperience("old"); len(l) != 0 {
			t.Fatalf("the old key came back: %+v", l)
		}
		if l, _ := d.ListExperience("new"); len(l) != 1 || l[0].Title != "moved" {
			t.Fatalf("new key: %+v", l)
		}
	}
}
