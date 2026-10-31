package agentdb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Entry is one finished job of a helper: Title is the first line of its done report, Body the
// harness's compaction summary of the work, else the report. Snippet is set by a search only.
type Entry struct {
	ID      int64  `json:"id"`
	UID     string `json:"uid,omitempty"`
	Helper  string `json:"helper"`
	At      string `json:"at"`
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	TeamID  string `json:"team_id,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// EntryUID is an entry's identity across machines and imports: a hash of helper, time and title.
func EntryUID(helper, at, title string) string {
	h := sha256.Sum256([]byte(helper + "\x00" + at + "\x00" + title))
	return hex.EncodeToString(h[:16])
}

func bodyHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// hashBodies sets body_hash of the entries that lack it (rows of a file before it).
func hashBodies(tx *sql.Tx) error {
	rows, err := txRows(tx, `SELECT id, body FROM experience WHERE body_hash = ''`)
	for _, x := range rows {
		if _, err := tx.Exec(`UPDATE experience SET body_hash = ? WHERE id = ?`, bodyHash(str(x["body"])), x["id"]); err != nil {
			return err
		}
	}
	return err
}

// AddExperience stores an entry (At: now, UID: EntryUID when empty) and reports whether it was
// added: not when its uid is already there or was deleted (its tombstone), or the helper already
// has an entry with the same body.
func (d *DB) AddExperience(e Entry) (added bool, err error) {
	err = d.tx(func(tx *sql.Tx) error {
		added, err = addExperience(tx, e)
		return err
	})
	return added, err
}

func addExperience(tx *sql.Tx, e Entry) (bool, error) {
	if e.At == "" {
		e.At = now()
	}
	if e.UID == "" {
		e.UID = EntryUID(e.Helper, e.At, e.Title)
	}
	var n int
	if err := tx.QueryRow(`SELECT (SELECT count(*) FROM experience WHERE uid = ?1) + (SELECT count(*) FROM tombstones WHERE key = 'experience:' || ?1) + (SELECT count(*) FROM experience WHERE helper = ?2 AND body_hash = ?3)`, e.UID, e.Helper, bodyHash(e.Body)).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO experience(uid, helper, at, title, body, body_hash, team_id) VALUES (?,?,?,?,?,?,?)`, e.UID, e.Helper, e.At, e.Title, e.Body, bodyHash(e.Body), e.TeamID); err != nil {
		return false, err
	}
	return true, nil
}

// RebuildExperience deletes the drop entries and adds es in one transaction: all or nothing. An entry
// that replaces a dropped one (same helper, at and title, read from the dropped row) takes a uid made
// from that row's id, new on every rebuild, so the dropped uid's tombstone still deletes the old row
// on every machine and the new one syncs as new.
func (d *DB) RebuildExperience(drop []int64, es []Entry) (added, skipped int, err error) {
	err = d.tx(func(tx *sql.Tx) error {
		gone := map[[3]string]int64{} // helper, at, title of a dropped row → its id
		for _, id := range drop {
			var k [3]string
			if err := tx.QueryRow(`SELECT helper, at, title FROM experience WHERE id = ?`, id).Scan(&k[0], &k[1], &k[2]); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("no entry %d: %w", id, ErrNotFound)
				}
				return err
			}
			if _, err := tx.Exec(`DELETE FROM experience WHERE id = ?`, id); err != nil {
				return err
			}
			gone[k] = id
		}
		for _, e := range es {
			if id, ok := gone[[3]string{e.Helper, e.At, e.Title}]; ok {
				e.UID = EntryUID(e.Helper, e.At, fmt.Sprintf("%s\x00rebuilt from %d", e.Title, id))
			}
			ok, err := addExperience(tx, e)
			if err != nil {
				return err
			}
			if ok {
				added++
			} else {
				skipped++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return added, skipped, nil
}

const entryCols = `e.id, e.uid, e.helper, e.at, e.title`

// SearchExperience is a helper's entries (no bodies) best matching the query's words (those of
// team first, then bm25, then newest), at most limit, and how many matched. No words, or no match:
// the newest ones (team's first), and all of them counted.
func (d *DB) SearchExperience(helper, team, query string, limit int) (hits []Entry, total int, err error) {
	if fq := ftsQuery(query); fq != "" {
		where := `FROM experience e JOIN experience_fts ON experience_fts.rowid = e.id WHERE e.helper = ? AND experience_fts MATCH ?`
		if err = d.db.QueryRow(`SELECT count(*) `+where, helper, fq).Scan(&total); err != nil {
			return nil, 0, err
		}
		if total > 0 {
			hits, err = d.entries(`SELECT `+entryCols+`, snippet(experience_fts, 1, '', '', '...', 16) `+where+
				fmt.Sprintf(` ORDER BY e.team_id = ? DESC, bm25(experience_fts, 2.0, 1.0), e.id DESC LIMIT %d`, limit), helper, fq, team)
			return hits, total, err
		}
	}
	if err = d.db.QueryRow(`SELECT count(*) FROM experience WHERE helper = ?`, helper).Scan(&total); err != nil {
		return nil, 0, err
	}
	hits, err = d.entries(`SELECT `+entryCols+`, substr(e.body, 1, 160) FROM experience e WHERE e.helper = ? ORDER BY e.team_id = ? DESC, e.id DESC`+fmt.Sprintf(` LIMIT %d`, limit), helper, team)
	return hits, total, err
}

// AllExperience is every entry of every helper, bodies included, oldest first: the owner's export.
func (d *DB) AllExperience() ([]Entry, error) {
	rows, err := d.db.Query(`SELECT id, uid, helper, at, title, body FROM experience ORDER BY helper, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.UID, &e.Helper, &e.At, &e.Title, &e.Body); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListExperience is all of a helper's entries, newest first, without bodies.
func (d *DB) ListExperience(helper string) ([]Entry, error) {
	es, err := d.entries(`SELECT `+entryCols+`, '' FROM experience e WHERE e.helper = ? ORDER BY e.id DESC`, helper)
	if es == nil {
		es = []Entry{}
	}
	return es, err
}

func (d *DB) entries(q string, a ...any) ([]Entry, error) {
	rows, err := d.db.Query(q, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.UID, &e.Helper, &e.At, &e.Title, &e.Snippet); err != nil {
			return nil, err
		}
		e.Snippet = strings.Join(strings.Fields(e.Snippet), " ")
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExperienceEntry is one entry in full.
func (d *DB) ExperienceEntry(id int64) (Entry, error) {
	var e Entry
	err := d.db.QueryRow(`SELECT id, uid, helper, at, title, body FROM experience WHERE id = ?`, id).Scan(&e.ID, &e.UID, &e.Helper, &e.At, &e.Title, &e.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// DeleteExperience deletes an entry; its tombstone syncs that.
func (d *DB) DeleteExperience(id int64) error {
	r, err := d.db.Exec(`DELETE FROM experience WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RekeyAgent moves a helper's entries to its new key (its folder moved, or it took another helper
// over): each becomes an entry of the new key and the old rows are deleted, so their tombstones sync.
func (d *DB) RekeyAgent(from, to string) error {
	if from == "" || to == "" || from == to {
		return nil
	}
	return d.tx(func(tx *sql.Tx) error {
		rows, err := txRows(tx, `SELECT at, title, body, body_hash, team_id FROM experience WHERE helper = ? ORDER BY id`, from)
		if err != nil {
			return err
		}
		for _, x := range rows {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO experience(uid, helper, at, title, body, body_hash, team_id) VALUES (?,?,?,?,?,?,?)`,
				EntryUID(to, str(x["at"]), str(x["title"])), to, x["at"], x["title"], x["body"], x["body_hash"], x["team_id"]); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`DELETE FROM experience WHERE helper = ?`, from)
		return err
	})
}

// AgentTotals counts a helper's entries.
func (d *DB) AgentTotals(helper string) (entries int, err error) {
	err = d.db.QueryRow(`SELECT count(*) FROM experience WHERE helper = ?`, helper).Scan(&entries)
	return entries, err
}

// DeleteAgentRecord removes everything recorded for a helper (its repository key) and the given meta
// keys, in one transaction, and reports how many entries went.
func (d *DB) DeleteAgentRecord(helper string, metaKeys ...string) (entries int, err error) {
	err = d.tx(func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT count(*) FROM experience WHERE helper = ?`, helper).Scan(&entries); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM experience WHERE helper = ?`, helper); err != nil {
			return err
		}
		for _, k := range metaKeys {
			if _, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, k); err != nil {
				return err
			}
		}
		return nil
	})
	return entries, err
}
