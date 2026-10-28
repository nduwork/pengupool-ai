package agentdb

import (
	"crypto/sha256"
	"encoding/hex"
)

// Removed is what `pengupool doctor --fix` took out of one of Claude's settings files: only the
// PenguPool v1 hook entries and the v1 wrapper of the status line (Entries, JSON), never the rest of
// the file, which can hold tokens, nor the owner's own commands. File is the settings file's name, not its path.
type Removed struct {
	Hash    string `json:"hash"` // the row's identity: a hash of File and Entries, so a repeated run or another machine adds nothing
	File    string `json:"file"`
	At      string `json:"at"`
	Entries string `json:"entries"`
}

// AddRemoved records what doctor removed, once per content, and reports whether it was new.
func (d *DB) AddRemoved(file, entries string) (hash string, added bool, err error) {
	h := sha256.Sum256([]byte(file + "\x00" + entries))
	hash = hex.EncodeToString(h[:])
	r, err := d.db.Exec(`INSERT OR IGNORE INTO removed_entries(hash, file, updated_at, entries) VALUES (?,?,?,?)`, hash, file, now(), entries)
	if err != nil {
		return "", false, err
	}
	n, _ := r.RowsAffected()
	return hash, n > 0, nil
}

// ListRemoved is every record, newest first.
func (d *DB) ListRemoved() ([]Removed, error) {
	rs, err := d.db.Query(`SELECT hash, file, updated_at, entries FROM removed_entries ORDER BY updated_at DESC, hash`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []Removed{}
	for rs.Next() {
		var r Removed
		if err := rs.Scan(&r.Hash, &r.File, &r.At, &r.Entries); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
