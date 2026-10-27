package agentdb

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Upgrade carries what an earlier schema held into this one, once: the owner's global preferences,
// restrictions and decisions become Compass lines of owner (a decision with the choice it records,
// so its belief replays). Every other memory (facts, ideas, notes, proposals, shares, a team's, a
// helper's or a repository's, the roster rows) goes, and so do the helpers' lessons and log entries
// (a helper's journal starts empty and fills as it finishes jobs), then the earlier tables (team logs, tasks) are dropped. dropped counts the
// rows that went, by table; Open copied the file before (PreUpgradePath). Without earlier tables it
// does nothing.
func (d *DB) Upgrade(owner string) (dropped map[string]int, err error) {
	if !d.Legacy() {
		return nil, nil
	}
	dropped = map[string]int{}
	legacy := d.legacyTables()
	for _, t := range legacy {
		if t == "memories" || strings.HasPrefix(t, "memories_fts") || strings.HasPrefix(t, "experience_fts") { // counted by what became of each row, or an index
			continue
		}
		var n int
		if d.db.QueryRow(`SELECT count(*) FROM "`+strings.ReplaceAll(t, `"`, `""`)+`"`).Scan(&n) == nil && n > 0 {
			dropped[t] = n
		}
	}
	uidCol := `''`
	if cols, _ := d.strings(`SELECT name FROM pragma_table_info('memories') WHERE name = 'uid'`); len(cols) > 0 {
		uidCol = `uid`
	}
	err = d.tx(func(tx *sql.Tx) error { // one connection: no query outside tx in here
		for name, q := range map[string]string{
			"memories (not the owner's)":  `DELETE FROM memories WHERE scope != 'global'`,
			"memories (not carried over)": `DELETE FROM memories WHERE state != 'active' OR scope = 'global' AND kind NOT IN ('preference', 'restriction', 'decision')`,
		} {
			r, err := tx.Exec(q)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				dropped[name] = int(n)
			}
		}
		rest, err := txRows(tx, `SELECT scope, kind, tag, title, body, evidence, source, created_at, `+uidCol+` AS uid FROM memories ORDER BY id`)
		if err != nil {
			return err
		}
		for _, m := range rest {
			if err := putMemoryLine(tx, owner, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dropped, d.DropLegacy()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// putMemoryLine writes an owner memory of 0.10 as a Compass line (a decision with the choices it
// records as signals). Its uids come from the memory's, so every machine that carries it over makes
// the same rows and a sync unions them.
func putMemoryLine(x execer, owner string, m row) error {
	text := str(m["body"])
	if t := str(m["title"]); t != "" {
		text = t + ": " + text
	}
	l := Line{Owner: owner, Kind: str(m["kind"]), Tag: str(m["tag"]), Body: text, Origin: "stated", Source: "triage", CreatedAt: str(m["created_at"])}
	if str(m["source"]) == "owner" {
		l.Source = "owner"
	}
	if l.Kind == "decision" {
		l.Kind, l.Origin, l.Source = "preference", "chose", "gate"
	}
	id := str(m["uid"])
	if id == "" {
		id = newUID()
	}
	uid := "m-" + id
	if _, err := x.Exec(`INSERT INTO compass(uid, owner, kind, tag, body, origin, source, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		uid, owner, l.Kind, l.Tag, l.Body, l.Origin, l.Source, l.CreatedAt, l.CreatedAt); err != nil {
		return err
	}
	if l.Origin != "chose" {
		return nil
	}
	for i, c := range choices(str(m["evidence"]), l.Body, l.CreatedAt) {
		sig := fmt.Sprintf("s-%s-%d", id, i)
		if _, err := x.Exec(`INSERT INTO signals(uid, owner, kind, words, at) VALUES (?,?,?,?,?)`, sig, owner, "chose", c.words, c.at); err != nil {
			return err
		}
		if _, err := x.Exec(`INSERT INTO signal_effects(signal_uid, line_uid, p_sup, p_con, weight, at, kind) VALUES (?,?,1,0,?,?,'chose')`, sig, uid, WeightChose, c.at); err != nil {
			return err
		}
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

var entryDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

type choice struct{ words, at string }

// choices are the owner's answers behind a decision: one per entry of its evidence (entries part
// at "\n---\n", each starting with its date and holding "Owner: <answer>"), at that day's noon.
// With no evidence the decision itself is the one choice, at its creation.
func choices(evidence, body, created string) []choice {
	var out []choice
	for _, e := range strings.Split(evidence, "\n---\n") {
		if strings.TrimSpace(e) == "" {
			continue
		}
		c := choice{words: body, at: created}
		if d := entryDate.FindString(e); d != "" {
			c.at = d + "T12:00:00Z"
		}
		if _, a, ok := strings.Cut(e, "\nOwner: "); ok {
			c.words = strings.TrimSpace(a)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		out = []choice{{body, created}}
	}
	return out
}
