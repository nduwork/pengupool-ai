// Package agentdb is PenguPool's record: one SQLite database owned by the host, independent of
// every repository, and the same on every machine and account that shares a sync repository.
//
//   - compass: the owner's preference lines, one global set; signals and signal_effects: what the
//     owner said or chose and what each did to a line, from which a line's belief is replayed;
//   - experience: one entry per finished helper job (title, and the harness's compacted context as the body);
//   - gates: each moment an agent of an autonomous team waited on the owner, and who answered how.
//
// Search is SQLite FTS5; there are no embeddings. Writes go through the host only: triggers on the
// guarded tables call pp_host(), a function only this process registers, so the sqlite3 shell or a
// script editing agents.db directly fails.
package agentdb

import (
	"cmp"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func init() {
	// ponytail: a speed bump, not a wall: a script that registers its own pp_host() gets through.
	// Running agents as another OS user is the hard boundary.
	sqlite.MustRegisterDeterministicScalarFunction("pp_host", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		return int64(1), nil
	})
}

// tables is the schema, one table each; Open rebuilds a table whose definition differs (keeping the
// columns both share) and drops every other table.
var tables = []struct{ name, cols string }{
	{"meta", `key TEXT PRIMARY KEY, value TEXT NOT NULL`},
	// Deleted shared rows ("<table>:<key>"), so a delete reaches every machine (sync.go).
	{"tombstones", `key TEXT PRIMARY KEY, at TEXT NOT NULL`},
	// The owner's signals and what each did to a Compass line: append-only, never edited (compass.go).
	{"signals", `uid TEXT PRIMARY KEY, owner TEXT NOT NULL,
  kind TEXT NOT NULL,                     -- said | chose | correction | spot_check
  words TEXT NOT NULL DEFAULT '',         -- the owner's words, or "chose X over Y"
  options TEXT NOT NULL DEFAULT '[]', chosen TEXT NOT NULL DEFAULT '',
  team_id TEXT NOT NULL DEFAULT '',       -- provenance only
  session_id TEXT NOT NULL DEFAULT '', at TEXT NOT NULL,
  -- how a go-ahead turned out (right | wrong | missed | ''): the thresholds learn from it
  outcome TEXT NOT NULL DEFAULT ''`},
	{"signal_effects", `signal_uid TEXT NOT NULL, line_uid TEXT NOT NULL, p_sup REAL NOT NULL, p_con REAL NOT NULL, weight REAL NOT NULL,
  at TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '', -- at, session_id, kind: copied from the signal, so replay needs no join
  PRIMARY KEY(signal_uid, line_uid)`},
	{"compass", `id INTEGER PRIMARY KEY, uid TEXT NOT NULL, owner TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'preference', -- preference | restriction
  tag TEXT NOT NULL DEFAULT '',           -- a permission key: perm:<allow|deny>:<tool>:<pattern>
  body TEXT NOT NULL, condition TEXT NOT NULL DEFAULT '', -- condition: when it applies; empty = always
  origin TEXT NOT NULL DEFAULT 'stated',  -- stated | chose: picks the prior
  source TEXT NOT NULL DEFAULT '',        -- owner | triage | gate
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL`},
	// A helper's journal: an entry per finished job, its title and the compaction summary (experience.go).
	{"experience", `id INTEGER PRIMARY KEY AUTOINCREMENT, uid TEXT NOT NULL UNIQUE,
  helper TEXT NOT NULL,                   -- the helper's agent key (its skill's repository key)
  at TEXT NOT NULL,                       -- RFC 3339
  title TEXT NOT NULL,                    -- the first line of the done report
  body TEXT NOT NULL,                     -- the compaction summary, else the done report
  body_hash TEXT NOT NULL DEFAULT '',     -- hex sha256 of the body: the same helper keeps a body once
  team_id TEXT NOT NULL DEFAULT ''`}, // the team the job was done for: its entries rank first in its searches
	// A gate is a moment an agent waited on the owner: a check-in at the end of its turn or its own
	// permission prompt; what triage predicted, and who answered it how.
	{"gates", `id INTEGER PRIMARY KEY, team_id TEXT NOT NULL, session_id TEXT NOT NULL, agent TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,                     -- checkin | permission
  question TEXT NOT NULL DEFAULT '',
  floor INTEGER NOT NULL DEFAULT 0, floor_hit TEXT NOT NULL DEFAULT '',
  predicted TEXT NOT NULL DEFAULT '',     -- proceed | ask_owner | ''
  confidence REAL NOT NULL DEFAULT 0, calibrated INTEGER NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL DEFAULT 'pending', -- pending | auto_proceeded | owner | moved_on | expired
  owner_answer TEXT NOT NULL DEFAULT '',
  owner_proceed INTEGER,                  -- NULL | 0 | 1: did the owner let it proceed as proposed
  line_uid TEXT NOT NULL DEFAULT '',
  lines TEXT NOT NULL DEFAULT '[]',       -- JSON: uids of the Compass lines that guided a go-ahead
  review TEXT NOT NULL DEFAULT '',        -- spot | right | wrong: the owner's check of a go-ahead
  suggestion TEXT NOT NULL DEFAULT '',    -- the Compass's answer to the question, for the owner to confirm
  evidence TEXT NOT NULL DEFAULT '',      -- what it rests on: how often the owner chose it
  created_at TEXT NOT NULL, resolved_at TEXT NOT NULL DEFAULT ''`},
}

// guarded are the tables only the host may write.
var guarded = []string{"meta", "tombstones", "compass", "experience", "signals", "signal_effects", "gates"}

// extra are the indexes, full-text indexes and triggers, created after the tables.
const extra = `
CREATE INDEX IF NOT EXISTS compass_owner ON compass(owner);
CREATE INDEX IF NOT EXISTS gates_session ON gates(session_id, id);
CREATE INDEX IF NOT EXISTS gates_team ON gates(team_id, id);
CREATE INDEX IF NOT EXISTS experience_helper ON experience(helper, id);
CREATE VIRTUAL TABLE IF NOT EXISTS compass_fts USING fts5(body, condition, content='compass', content_rowid='id', tokenize='porter unicode61');
CREATE TRIGGER IF NOT EXISTS compass_ai AFTER INSERT ON compass BEGIN
  INSERT INTO compass_fts(rowid, body, condition) VALUES (new.id, new.body, new.condition); END;
CREATE TRIGGER IF NOT EXISTS compass_ad AFTER DELETE ON compass BEGIN
  INSERT INTO compass_fts(compass_fts, rowid, body, condition) VALUES ('delete', old.id, old.body, old.condition); END;
CREATE TRIGGER IF NOT EXISTS compass_au AFTER UPDATE ON compass BEGIN
  INSERT INTO compass_fts(compass_fts, rowid, body, condition) VALUES ('delete', old.id, old.body, old.condition);
  INSERT INTO compass_fts(rowid, body, condition) VALUES (new.id, new.body, new.condition); END;
CREATE VIRTUAL TABLE IF NOT EXISTS experience_fts USING fts5(title, body, content='experience', content_rowid='id', tokenize='porter unicode61');
CREATE TRIGGER IF NOT EXISTS experience_ai AFTER INSERT ON experience BEGIN
  INSERT INTO experience_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END;
CREATE TRIGGER IF NOT EXISTS experience_ad AFTER DELETE ON experience BEGIN
  INSERT INTO experience_fts(experience_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body); END;
CREATE TRIGGER IF NOT EXISTS experience_au AFTER UPDATE ON experience BEGIN
  INSERT INTO experience_fts(experience_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO experience_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END;
CREATE TRIGGER IF NOT EXISTS compass_tombstone AFTER DELETE ON compass BEGIN
  INSERT OR IGNORE INTO tombstones(key, at) VALUES ('compass:' || old.uid, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')); END;
CREATE TRIGGER IF NOT EXISTS experience_tombstone AFTER DELETE ON experience BEGIN
  INSERT OR IGNORE INTO tombstones(key, at) VALUES ('experience:' || old.uid, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')); END;
CREATE TRIGGER IF NOT EXISTS signals_tombstone AFTER DELETE ON signals BEGIN
  INSERT OR IGNORE INTO tombstones(key, at) VALUES ('signals:' || old.uid, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')); END;
`

type DB struct{ db *sql.DB }

var ErrNotFound = errors.New("not found")

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=secure_delete(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // ponytail: one writer connection; split read/write pools if the UI ever waits on it
	d := &DB{db}
	// An earlier schema's file is copied whole before anything changes it, once, out of the daily
	// snapshots' rotation: Upgrade drops what this schema has no place for.
	if pre := PreUpgradePath(path); d.Legacy() {
		if _, err := os.Stat(pre); errors.Is(err, fs.ErrNotExist) {
			_ = os.MkdirAll(filepath.Dir(pre), 0o700)
			if err := d.Snapshot(pre); err != nil {
				db.Close()
				return nil, fmt.Errorf("agent db: copy before the upgrade: %w", err)
			}
			_ = os.Chmod(pre, 0o600)
		}
	}
	if err := d.reconcile(); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent db schema: %w", err)
	}
	return d, nil
}

// PreUpgradePath is where Open keeps the file as an earlier schema left it: in the store's backups
// folder (store.BackupsDir), named apart from the daily snapshots.
func PreUpgradePath(path string) string {
	return filepath.Join(filepath.Dir(path), "backups", "pre-upgrade-agents.db")
}

// ours are the full-text indexes of this schema (each with SQLite's shadow tables, named after it).
var ours = []string{"compass_fts", "experience_fts"}

// legacyTables are the tables in the file that this schema does not have: an earlier schema's,
// which Upgrade reads once before they go.
func (d *DB) legacyTables() []string {
	names, _ := d.strings(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	var out []string
	for _, n := range names {
		if slices.ContainsFunc(tables, func(t struct{ name, cols string }) bool { return t.name == n }) ||
			slices.ContainsFunc(ours, func(f string) bool { return n == f || strings.HasPrefix(n, f+"_") }) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// reconcile brings the file to the schema: missing tables are created, a table whose definition
// differs is rebuilt with the columns both share, and the triggers are made anew. Tables of an
// earlier schema stay until Upgrade has read them.
func (d *DB) reconcile() error {
	if _, err := d.db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`PRAGMA foreign_keys=OFF`) // a rebuild renames tables under their references
	defer d.db.Exec(`PRAGMA foreign_keys=ON`)
	// Every trigger is this schema's and is made again below; one of an earlier schema would break
	// a rebuild of the table it writes to.
	triggers, err := d.strings(`SELECT name FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		return err
	}
	for _, t := range triggers {
		if _, err := d.db.Exec(`DROP TRIGGER IF EXISTS "` + strings.ReplaceAll(t, `"`, `""`) + `"`); err != nil {
			return err
		}
	}
	// No team has a record any more (2026-10-24), and a helper's experience is a journal (2026-10-25,
	// schema 3): the Kanban tables and the helpers' notes, raw log, cursors and lesson counts go.
	// Each Open does this again; it finds nothing after the first.
	for _, t := range []string{"ticket_entries", "handoffs", "tickets", "milestones", "epics", "experience_docs", "raw_fts", "raw_log", "summary_state", "lesson_use"} {
		if _, err := d.db.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	// A file of a much earlier schema has an experience table of its own: not this one, and no data to keep.
	if cols, _ := d.strings(`SELECT name FROM pragma_table_info('experience') WHERE name = 'helper'`); len(cols) == 0 {
		for _, t := range []string{"experience_fts", "experience"} {
			if _, err := d.db.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
				return err
			}
		}
	}
	for _, t := range tables {
		have, err := d.strings(`SELECT name FROM pragma_table_info(?)`, t.name)
		if err != nil {
			return err
		}
		want := columnNames(t.cols)
		var def string
		_ = d.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, t.name).Scan(&def)
		switch {
		case len(have) == 0:
			if _, err := d.db.Exec(`CREATE TABLE ` + t.name + `(` + t.cols + `)`); err != nil {
				return fmt.Errorf("%s: %w", t.name, err)
			}
		// Other columns, or the same ones defined otherwise (a type, a default, AUTOINCREMENT).
		case !slices.Equal(slices.Sorted(slices.Values(have)), slices.Sorted(slices.Values(want))) || !strings.Contains(def, t.cols):
			var keep []string
			for _, c := range want {
				if slices.Contains(have, c) {
					keep = append(keep, `"`+c+`"`)
				}
			}
			if err := d.tx(func(tx *sql.Tx) error {
				for _, q := range []string{
					`DROP TABLE IF EXISTS ` + t.name + `_new`,
					`CREATE TABLE ` + t.name + `_new(` + t.cols + `)`,
					`INSERT INTO ` + t.name + `_new(` + strings.Join(keep, ",") + `) SELECT ` + strings.Join(keep, ",") + ` FROM ` + t.name,
					`DROP TABLE ` + t.name,
					`ALTER TABLE ` + t.name + `_new RENAME TO ` + t.name,
				} {
					if _, err := tx.Exec(q); err != nil {
						return fmt.Errorf("rebuild %s: %w", t.name, err)
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{
		`DELETE FROM tombstones WHERE key LIKE 'epics:%' OR key LIKE 'tickets:%' OR key LIKE 'milestones:%' OR key LIKE 'experience_docs:%'`,
		`DELETE FROM tombstones WHERE key IS NULL`,
	} {
		if _, err := d.db.Exec(q); err != nil {
			return err
		}
	}
	if err := d.tx(hashBodies); err != nil { // entries of a file before body_hash
		return err
	}
	// An index made before stemming is dropped, so extra makes it again with the stemmer; the rebuild
	// below refills it. Its triggers name the table, so they stay valid.
	for _, t := range []string{"compass_fts", "experience_fts"} {
		var def string
		if err := d.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = ?`, t).Scan(&def); err == nil && !strings.Contains(def, "porter") {
			if _, err := d.db.Exec("DROP TABLE " + t); err != nil {
				return err
			}
		}
	}
	// Full-text indexes follow their tables: rebuilt, so a rebuilt table's index is true.
	q := extra + `
INSERT INTO compass_fts(compass_fts) VALUES ('rebuild');
INSERT INTO experience_fts(experience_fts) VALUES ('rebuild');`
	for _, t := range guarded {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			q += fmt.Sprintf("\nCREATE TRIGGER IF NOT EXISTS host_only_%s_%s BEFORE %s ON %s WHEN NOT pp_host() BEGIN SELECT RAISE(ABORT, 'agents.db is written only by the PenguPool host'); END;",
				t, strings.ToLower(op), op, t)
		}
	}
	_, err = d.db.Exec(q)
	return err
}

// columnNames are the column names of a table definition (its constraints left out).
func columnNames(def string) []string {
	lines := strings.Split(def, "\n") // comments go first: a comma or a parenthesis in one is prose
	for j := range lines {
		lines[j], _, _ = strings.Cut(lines[j], "--")
	}
	def = strings.Join(lines, " ")
	var out []string
	depth := 0
	start := 0
	for i, r := range def + "," {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				part := def[start:min(i, len(def))]
				start = i + 1
				f := strings.Fields(part)
				if len(f) > 0 && !slices.Contains([]string{"PRIMARY", "FOREIGN", "UNIQUE", "CHECK"}, strings.ToUpper(f[0])) {
					out = append(out, f[0])
				}
			}
		}
	}
	return out
}

// Legacy reports whether tables of an earlier schema are still in the file.
func (d *DB) Legacy() bool { return len(d.legacyTables()) > 0 }

// DropLegacy drops the tables of an earlier schema (references among them do not hold it up).
func (d *DB) DropLegacy() error {
	if _, err := d.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer d.db.Exec(`PRAGMA foreign_keys=ON`)
	for _, t := range d.legacyTables() {
		if _, err := d.db.Exec(`DROP TABLE IF EXISTS "` + strings.ReplaceAll(t, `"`, `""`) + `"`); err != nil {
			return fmt.Errorf("drop %s: %w", t, err)
		}
	}
	return nil
}

// Setting reads a host setting ("" when unset).
func (d *DB) Setting(key string) string {
	var v string
	_ = d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.db.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (d *DB) Close() error { return d.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// newUID is a row's global id, the same on every machine the row is synced to.
func newUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- the Compass --------------------------------------------------------------------------------

// Line is one preference of the owner's Compass. Its belief is not stored: it is replayed from its
// signal effects (compass.go).
type Line struct {
	ID        int64  `json:"id,omitempty"`
	UID       string `json:"uid"`
	Owner     string `json:"owner"`
	Kind      string `json:"kind"`
	Tag       string `json:"tag,omitempty"`
	Body      string `json:"body"`
	Condition string `json:"condition,omitempty"`
	Origin    string `json:"origin"` // stated | chose
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const lineCols = `c.id, c.uid, c.owner, c.kind, c.tag, c.body, c.condition, c.origin, c.source, c.created_at, c.updated_at`

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func args(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ftsQuery turns free text into an FTS5 query of quoted terms joined by OR, so user input can never
// be parsed as FTS syntax.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r > 127)
	}) {
		if len(w) > 1 {
			terms = append(terms, `"`+strings.ReplaceAll(w, `"`, "")+`"`)
		}
	}
	return strings.Join(terms, " OR ")
}

// Lines are the owner's lines matching a full-text query (all when empty) and, when set, an exact
// tag; best match first, else newest first.
func (d *DB) Lines(owner, query, tag string, limit int) ([]Line, error) {
	q := `SELECT ` + lineCols + ` FROM compass c`
	where, a := []string{"c.owner = ?"}, []any{owner}
	order := "c.updated_at DESC, c.id DESC"
	if fq := ftsQuery(query); fq != "" {
		q += ` JOIN compass_fts ON compass_fts.rowid = c.id`
		where = append(where, "compass_fts MATCH ?")
		a = append(a, fq)
		order = "bm25(compass_fts), c.updated_at DESC"
	} else if strings.TrimSpace(query) != "" {
		return nil, nil
	}
	if tag != "" {
		where = append(where, "c.tag = ?")
		a = append(a, tag)
	}
	q += " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + order
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.db.Query(q, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.UID, &l.Owner, &l.Kind, &l.Tag, &l.Body, &l.Condition, &l.Origin, &l.Source, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (d *DB) Line(uid string) (Line, error) {
	var l Line
	err := d.db.QueryRow(`SELECT `+lineCols+` FROM compass c WHERE c.uid = ?`, uid).
		Scan(&l.ID, &l.UID, &l.Owner, &l.Kind, &l.Tag, &l.Body, &l.Condition, &l.Origin, &l.Source, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// PutLine inserts a line (empty UID: one is assigned) or replaces the editable fields of the one
// with that UID, and returns the UID. Origin is set at creation only, source when given.
func (d *DB) PutLine(l Line) (string, error) {
	t := now()
	if l.Kind == "" {
		l.Kind = "preference"
	}
	if l.UID != "" {
		r, err := d.db.Exec(`UPDATE compass SET kind=?, tag=?, body=?, condition=?, source=COALESCE(NULLIF(?,''),source), updated_at=? WHERE uid=?`,
			l.Kind, l.Tag, l.Body, l.Condition, l.Source, t, l.UID)
		if err != nil {
			return "", err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return "", ErrNotFound
		}
		return l.UID, nil
	}
	if l.Origin == "" {
		l.Origin = "stated"
	}
	if l.CreatedAt == "" {
		l.CreatedAt = t
	}
	l.UID = newUID()
	_, err := d.db.Exec(`INSERT INTO compass(uid, owner, kind, tag, body, condition, origin, source, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		l.UID, l.Owner, l.Kind, l.Tag, l.Body, l.Condition, l.Origin, l.Source, l.CreatedAt, t)
	return l.UID, err
}

// DeleteLine deletes a line, its effects and the signals that touched no other line (their
// tombstones sync that). A signal that still touches another line stays, and so does one that
// never touched any: it waits in the ledger.
func (d *DB) DeleteLine(uid string) error {
	return d.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM signals WHERE uid IN (SELECT signal_uid FROM signal_effects WHERE line_uid = ?1)
			AND NOT EXISTS (SELECT 1 FROM signal_effects o WHERE o.signal_uid = signals.uid AND o.line_uid != ?1)`, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM signal_effects WHERE line_uid = ?`, uid); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM compass WHERE uid = ?`, uid)
		return err
	})
}

// MergeLines folds the line drop into keep (the older, whose uid stays): drop's effects move to
// keep (on a clash for one signal the heavier stays), takes drop's
// condition when it has none and body when given; drop is deleted, and its tombstone syncs that.
func (d *DB) MergeLines(keep, drop, body string) error {
	k, err := d.Line(keep)
	if err != nil {
		return err
	}
	x, err := d.Line(drop)
	if err != nil {
		return err
	}
	return d.tx(func(tx *sql.Tx) error {
		if x.Origin == "stated" { // its creating signal weighed nothing (its prior was that signal): on the kept line it is evidence
			if _, err := tx.Exec(`UPDATE signal_effects SET weight = CASE kind WHEN 'correction' THEN ? ELSE ? END WHERE line_uid = ? AND weight = 0`,
				WeightCorrect, WeightSaid, drop); err != nil {
				return err
			}
		}
		if err := foldEffects(tx, "signal_effects", keep, drop); err != nil {
			return err
		}
		if body == "" {
			body = k.Body
		}
		if _, err := tx.Exec(`UPDATE compass SET body = ?, condition = ?, updated_at = ? WHERE uid = ?`,
			body, cmp.Or(k.Condition, x.Condition), now(), keep); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM compass WHERE uid = ?`, drop); err != nil {
			return err
		}
		// The redirect syncs with the delete: an effect another machine recorded on drop moves to keep there too.
		_, err := tx.Exec(`INSERT OR IGNORE INTO tombstones(key, at) VALUES ('merged:' || ? || ':' || ?, ?)`, drop, keep, now())
		return err
	})
}

// foldEffects moves the effects of line drop to keep in an effects table (this one's or a shared
// file's, "repo.signal_effects"): on a clash for one signal the heavier stays.
func foldEffects(tx *sql.Tx, table, keep, drop string) error {
	for _, q := range []string{
		`UPDATE ` + table + ` AS k SET p_sup = e.p_sup, p_con = e.p_con, weight = e.weight, at = e.at, session_id = e.session_id, kind = e.kind
		   FROM ` + table + ` e WHERE k.line_uid = ?1 AND e.line_uid = ?2 AND k.signal_uid = e.signal_uid AND e.weight > k.weight`,
		`DELETE FROM ` + table + ` WHERE line_uid = ?2 AND signal_uid IN (SELECT signal_uid FROM ` + table + ` WHERE line_uid = ?1)`,
		`UPDATE ` + table + ` SET line_uid = ?1 WHERE line_uid = ?2`,
	} {
		if _, err := tx.Exec(q, keep, drop); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) strings(q string, a ...any) ([]string, error) {
	rows, err := d.db.Query(q, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) tx(fn func(*sql.Tx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
