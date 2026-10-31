package agentdb

import (
	"database/sql"
	"errors"
)

// PerCompass is the last Compass decision on one waiting item (see the per_compass table).
type PerCompass struct {
	Item      string `json:"item"`
	TeamID    string `json:"team_id"`
	SessionID string `json:"session_id"`
	Lead      string `json:"lead"`
	Question  string `json:"question"`
	Outcome   string `json:"outcome"` // answered | refused
	Detail    string `json:"detail"`
	LineUID   string `json:"line_uid,omitempty"`
	Version   string `json:"version"`
	At        string `json:"at"`
}

const perCompassCols = `item, team_id, session_id, lead, question, outcome, detail, line_uid, version, at`

func scanPerCompass(r interface{ Scan(...any) error }) (PerCompass, error) {
	var x PerCompass
	err := r.Scan(&x.Item, &x.TeamID, &x.SessionID, &x.Lead, &x.Question, &x.Outcome, &x.Detail, &x.LineUID, &x.Version, &x.At)
	return x, err
}

// PerCompassFor is the item's last Compass decision; ok false when there is none.
func (d *DB) PerCompassFor(item string) (PerCompass, bool, error) {
	x, err := scanPerCompass(d.db.QueryRow(`SELECT `+perCompassCols+` FROM per_compass WHERE item = ?`, item))
	if errors.Is(err, sql.ErrNoRows) {
		return x, false, nil
	}
	return x, err == nil, err
}

// PutPerCompass records an item's Compass decision, replacing the one before.
func (d *DB) PutPerCompass(x PerCompass) error {
	if x.At == "" {
		x.At = now()
	}
	_, err := d.db.Exec(`INSERT OR REPLACE INTO per_compass (`+perCompassCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		x.Item, x.TeamID, x.SessionID, x.Lead, x.Question, x.Outcome, x.Detail, x.LineUID, x.Version, x.At)
	return err
}

// PerCompassSince are the Compass decisions since a time, newest first.
func (d *DB) PerCompassSince(since string) ([]PerCompass, error) {
	rows, err := d.db.Query(`SELECT `+perCompassCols+` FROM per_compass WHERE at >= ? ORDER BY at DESC LIMIT 200`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PerCompass
	for rows.Next() {
		x, err := scanPerCompass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
