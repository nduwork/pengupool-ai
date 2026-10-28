package agentdb

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Shared record (the sync): one SQLite file (pengupool-agents.db) in a Git clone every machine and account
// writes holds the lasting record: the Compass with the owner's signals and their effects, helper
// experience entries, and the tombstones of those rows. No team has a record. The gates stay on
// their machine.
// Merge attaches the file to the database and merges row by row: a row is the same row everywhere
// by its key, the newer updated_at wins, and a tombstone deletes its row on every side. The file it
// writes holds exactly this schema.

// SchemaVersion is the file's PRAGMA user_version: a file with a newer one is merged only for the
// columns both share and is never written over.
// 2: a file of version 1 held each team's epics, milestones and tickets; the next push drops them.
// 3: experience entries replace the helpers' notes; a file's older tables are ignored.
const SchemaVersion = 3

type syncTable struct {
	name  string
	key   []string // the row's identity across machines
	local string   // a column that is this machine's own (a rowid), never shared
}

// syncTables are the tables of the file. The tombstones come first: what they delete goes before
// any row comes in.
var syncTables = []syncTable{
	{name: "tombstones", key: []string{"key"}},
	{name: "compass", key: []string{"uid"}, local: "id"},
	{name: "experience", key: []string{"uid"}, local: "id"},
	{name: "signals", key: []string{"uid"}},
	{name: "signal_effects", key: []string{"signal_uid", "line_uid"}},
}

type row = map[string]any

// DamagedError is a shared file SQLite can't read or whose integrity check fails.
type DamagedError struct{ Reason string }

func (e *DamagedError) Error() string { return "damaged: " + e.Reason }

// MergeResult is what a Merge found.
type MergeResult struct {
	Changes string // what the outgoing file adds to the shared file, by table ("compass +1 ~2, signals +5"); empty: nothing to push
	Newer   bool   // the shared file is from a newer PenguPool: merged in, no outgoing file
}

// Merge merges the shared file's rows into the database and writes the union to out (a path this
// call makes), which holds the shared tables only. A missing file is an empty one. The change
// count is by row content, not bytes: the out file's pages need not be byte-stable.
func (d *DB) Merge(file, out string) (res MergeResult, err error) {
	ctx := context.Background()
	tmp := out + ".repo" // the file is merged from a copy, never in place
	_ = os.Remove(out)
	defer os.Remove(tmp)
	if b, err := os.ReadFile(file); err == nil {
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return res, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	defer conn.ExecContext(ctx, `DETACH out`)
	defer conn.ExecContext(ctx, `DETACH repo`)
	var ver int
	if _, err = conn.ExecContext(ctx, `ATTACH ? AS repo`, tmp); err == nil {
		ver, err = checkFile(ctx, conn, "repo")
	}
	if err != nil {
		return res, &DamagedError{err.Error()}
	}
	res.Newer = ver > SchemaVersion
	err = withTx(ctx, conn, func(tx *sql.Tx) error {
		for _, t := range syncTables { // an older file may lack a table: it is empty
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS repo.` + t.name + `(` + tableDef(t.name) + `)`); err != nil {
				return err
			}
		}
		// A tombstone with no key (an experience row deleted while its uid was NULL) names no row,
		// here or in the file, and cannot be read back.
		for _, t := range []string{"tombstones", "repo.tombstones"} {
			if _, err := tx.Exec(`DELETE FROM ` + t + ` WHERE key IS NULL`); err != nil {
				return err
			}
		}
		if err := mergeTable(tx, syncTables[0]); err != nil {
			return err
		}
		// A file of version 1 carries tombstones of epics and tickets: no table of ours holds them.
		if _, err := tx.Exec(`DELETE FROM tombstones WHERE key LIKE 'epics:%' OR key LIKE 'tickets:%' OR key LIKE 'milestones:%'`); err != nil {
			return err
		}
		if err := applyTombstones(tx); err != nil {
			return err
		}
		for _, t := range syncTables[1:] {
			if err := mergeTable(tx, t); err != nil {
				return fmt.Errorf("%s: %w", t.name, err)
			}
		}
		return hashBodies(tx) // entries of a file before body_hash
	})
	if res.Newer { // a newer file may not fit this schema: nothing of it is merged, and it is never written over
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if _, err := conn.ExecContext(ctx, `ATTACH ? AS out`, out); err != nil {
		return res, err
	}
	err = withTx(ctx, conn, func(tx *sql.Tx) error {
		var parts []string
		for _, t := range syncTables {
			def := tableDef(t.name)
			cols := quoteAll(slices.DeleteFunc(columnNames(def), func(c string) bool { return c == t.local }))
			if _, err := tx.Exec(`CREATE TABLE out.` + t.name + `(` + def + `)`); err != nil {
				return err
			}
			// In key order and without the rowid, so the same rows make the same file on every machine.
			if _, err := tx.Exec(`INSERT INTO out.` + t.name + `(` + cols + `) SELECT ` + cols + ` FROM main.` + t.name + ` ORDER BY ` + strings.Join(quoteList(t.key), ", ")); err != nil {
				return err
			}
			if c, err := countChanges(tx, t); err != nil {
				return err
			} else if c != "" {
				parts = append(parts, t.name+" "+c)
			}
		}
		res.Changes = strings.Join(parts, ", ")
		_, err := tx.Exec(fmt.Sprintf(`PRAGMA out.user_version = %d`, SchemaVersion))
		return err
	})
	if err != nil {
		return res, err
	}
	if res.Changes == "" && ver < SchemaVersion {
		res.Changes = fmt.Sprintf("schema v%d", SchemaVersion)
	}
	if _, err := checkFile(ctx, conn, "out"); err != nil {
		return res, fmt.Errorf("the file to push failed its check: %w", err)
	}
	return res, nil
}

// checkFile runs SQLite's integrity check on an attached file and reads its schema version.
func checkFile(ctx context.Context, conn *sql.Conn, schema string) (int, error) {
	var ok string
	if err := conn.QueryRowContext(ctx, `PRAGMA `+schema+`.integrity_check`).Scan(&ok); err != nil {
		return 0, err
	}
	if ok != "ok" {
		return 0, errors.New(ok)
	}
	var v int
	err := conn.QueryRowContext(ctx, `PRAGMA `+schema+`.user_version`).Scan(&v)
	return v, err
}

func withTx(ctx context.Context, conn *sql.Conn, fn func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func tableDef(name string) string {
	for _, t := range tables {
		if t.name == name {
			return t.cols
		}
	}
	return ""
}

func quoteList(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = `"` + c + `"`
	}
	return out
}

func quoteAll(cols []string) string { return strings.Join(quoteList(cols), ", ") }

// applyTombstones deletes what the tombstones name. A signal's or a line's effects go with it, with no tombstones of their own. First the effects of a merged line (its
// tombstone "merged:<drop>:<keep>") move to the line it was merged into, here and in the shared
// file, so an effect recorded on the dropped line before its merge arrived is kept.
func applyTombstones(tx *sql.Tx) error {
	merged, err := txStrings(tx, `SELECT substr(key, 8) FROM tombstones WHERE key LIKE 'merged:%'`)
	if err != nil {
		return err
	}
	into := map[string]string{}
	for _, m := range merged {
		drop, keep, _ := strings.Cut(m, ":")
		into[drop] = keep
	}
	for drop := range into {
		keep := drop
		for range into { // a chain (a into b into c) ends at its last line; a cycle ends after one lap
			next, ok := into[keep]
			if !ok {
				break
			}
			keep = next
		}
		if keep == drop {
			continue
		}
		for _, table := range []string{"signal_effects", "repo.signal_effects"} {
			if err := foldEffects(tx, table, keep, drop); err != nil {
				return err
			}
		}
	}
	dead, err := txStrings(tx, `SELECT key FROM tombstones`)
	if err != nil {
		return err
	}
	for _, k := range dead {
		table, key, _ := strings.Cut(k, ":")
		col := map[string]string{"compass": "uid", "experience": "uid", "signals": "uid"}[table]
		if col == "" {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE `+col+` = ?`, key); err != nil {
			return err
		}
		if child := map[string]string{"signals": "signal_uid", "compass": "line_uid"}[table]; child != "" {
			if _, err := tx.Exec(`DELETE FROM signal_effects WHERE `+child+` = ?`, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// shared are the columns of a in b's table too, in a's order, without the table's local column.
func (t syncTable) shared(tx *sql.Tx, a, b string) ([]string, error) {
	in, err := txStrings(tx, `SELECT name FROM pragma_table_info(?, ?)`, t.name, a)
	if err != nil {
		return nil, err
	}
	other, err := txStrings(tx, `SELECT name FROM pragma_table_info(?, ?)`, t.name, b)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(in, func(c string) bool { return c == t.local || !slices.Contains(other, c) }), nil
}

// on is the condition that rows a and b (aliases) are the same row.
func (t syncTable) on(a, b string) string {
	eq := make([]string, len(t.key))
	for i, k := range t.key {
		eq[i] = a + `."` + k + `" = ` + b + `."` + k + `"`
	}
	return strings.Join(eq, " AND ")
}

// live is the condition that row r (an alias) is not deleted: not tombstoned, and a signal's or
// line's effects go with their parent.
func (t syncTable) live(r string) string {
	parts := make([]string, len(t.key))
	for i, k := range t.key {
		parts[i] = r + `."` + k + `"`
	}
	c := `NOT EXISTS (SELECT 1 FROM main.tombstones x WHERE x.key = '` + t.name + `:' || ` + strings.Join(parts, " || char(0) || ") + `)`
	if t.name == "signal_effects" {
		c += ` AND NOT EXISTS (SELECT 1 FROM main.tombstones x WHERE x.key = 'signals:' || ` + r + `.signal_uid) AND NOT EXISTS (SELECT 1 FROM main.tombstones x WHERE x.key = 'compass:' || ` + r + `.line_uid)`
	}
	return c
}

// mergeTable brings the shared file's rows of one table into the database: rows the database
// lacks come in, and a row the file has newer (updated_at, then the larger row, so every machine
// picks the same one) replaces it. A table with no updated_at is append-only, but for the heavier of
// two effects of one key.
func mergeTable(tx *sql.Tx, t syncTable) error {
	cols, err := t.shared(tx, "repo", "main")
	if err != nil {
		return err
	}
	for _, k := range t.key {
		if !slices.Contains(cols, k) {
			return nil // a file this PenguPool can't match rows of
		}
	}
	newer := ""
	if slices.Contains(cols, "updated_at") {
		theirs, mine := []string{`r."updated_at"`}, []string{`m."updated_at"`}
		for _, c := range cols {
			theirs, mine = append(theirs, `r."`+c+`"`), append(mine, `m."`+c+`"`)
		}
		newer = `(` + strings.Join(theirs, ", ") + `) > (` + strings.Join(mine, ", ") + `)`
	} else if t.name == "signal_effects" { // only a merge of two lines makes two effects of one key: the heavier stays
		newer = `r."weight" > m."weight"`
	}
	if newer != "" {
		sets := make([]string, len(cols))
		for i, c := range cols {
			sets[i] = `"` + c + `" = r."` + c + `"`
		}
		if _, err := tx.Exec(`UPDATE ` + t.name + ` AS m SET ` + strings.Join(sets, ", ") + ` FROM repo.` + t.name + ` AS r WHERE ` + t.on("m", "r") +
			` AND ` + t.live("r") + ` AND ` + newer); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO ` + t.name + `(` + quoteAll(cols) + `) SELECT r.` + strings.Join(quoteList(cols), ", r.") + ` FROM repo.` + t.name + ` AS r WHERE ` + t.live("r") +
		` AND NOT EXISTS (SELECT 1 FROM main.` + t.name + ` m WHERE ` + t.on("m", "r") + `)`)
	return err
}

// countChanges counts what the out file's table has against the shared file's: "+added ~changed
// -removed", only the non-zero ones.
func countChanges(tx *sql.Tx, t syncTable) (string, error) {
	cols, err := t.shared(tx, "out", "repo")
	if err != nil {
		return "", err
	}
	count := func(q string) (n int) {
		if err == nil {
			err = tx.QueryRow(q).Scan(&n)
		}
		return n
	}
	missing := func(a, b string) int { // rows of a whose key b lacks
		return count(`SELECT count(*) FROM ` + a + `.` + t.name + ` x WHERE NOT EXISTS (SELECT 1 FROM ` + b + `.` + t.name + ` y WHERE ` + t.on("x", "y") + `)`)
	}
	added, removed := missing("out", "repo"), missing("repo", "out")
	differ := count(`SELECT count(*) FROM (SELECT ` + quoteAll(cols) + ` FROM out.` + t.name + ` EXCEPT SELECT ` + quoteAll(cols) + ` FROM repo.` + t.name + `)`)
	if err != nil {
		return "", err
	}
	var s []string
	for _, c := range []struct {
		sign string
		n    int
	}{{"+", added}, {"~", differ - added}, {"-", removed}} {
		if c.n > 0 {
			s = append(s, fmt.Sprintf("%s%d", c.sign, c.n))
		}
	}
	return strings.Join(s, " "), nil
}

// ImportShared reads the JSONL folder a 0.10 machine wrote (shared/) into a shared file at path, for
// Merge to bring in once. Its memories that Upgrade carries over become Compass lines of owner, with
// the same uids Upgrade makes, so a machine that upgraded its own copy of them gains no second line.
func ImportShared(dir, path, owner string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=synchronous(0)")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, t := range syncTables {
		def := tableDef(t.name)
		if _, err := db.Exec(`CREATE TABLE ` + t.name + `(` + def + `)`); err != nil {
			return err
		}
		rows, err := readRows(filepath.Join(dir, t.name+".jsonl"))
		if err != nil {
			return err
		}
		for _, r := range rows {
			var cols []string
			var vals []any
			for _, c := range columnNames(def) {
				if v, ok := r[c]; ok {
					cols, vals = append(cols, c), append(vals, v)
				}
			}
			if _, err := db.Exec(`INSERT OR IGNORE INTO `+t.name+`(`+quoteAll(cols)+`) VALUES (`+placeholders(len(vals))+`)`, vals...); err != nil {
				return fmt.Errorf("%s: %w", t.name, err)
			}
		}
	}
	mems, err := readRows(filepath.Join(dir, "memories.jsonl"))
	if err != nil {
		return err
	}
	for _, m := range mems {
		if str(m["scope"]) == "global" && slices.Contains([]string{"preference", "restriction", "decision"}, str(m["kind"])) &&
			(str(m["state"]) == "" || str(m["state"]) == "active") && !(str(m["tag"]) == "roster" && str(m["source"]) == "pengupool") {
			if err := putMemoryLine(db, owner, m); err != nil {
				return fmt.Errorf("memories: %w", err)
			}
		}
	}
	return nil
}

func readRows(path string) ([]row, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []row
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.UseNumber()
		var r row
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		for k, v := range r {
			if n, ok := v.(json.Number); ok {
				if i, err := n.Int64(); err == nil {
					r[k] = i
				} else if f, err := n.Float64(); err == nil {
					r[k] = f
				}
			}
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func txRows(tx *sql.Tx, q string, a ...any) ([]row, error) {
	rs, err := tx.Query(q, a...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	vals, ptrs := make([]any, len(cols)), make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rs.Next() {
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(row, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				r[c] = string(b)
			} else {
				r[c] = vals[i]
			}
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func txStrings(tx *sql.Tx, q string, a ...any) ([]string, error) {
	rs, err := tx.Query(q, a...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rs.Err()
}
