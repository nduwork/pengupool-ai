package agentdb

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Gate is one moment an agent waited on the owner (see the gates table).
type Gate struct {
	ID          int64   `json:"id"`
	TeamID      string  `json:"team_id"`
	SessionID   string  `json:"session_id"`
	Agent       string  `json:"agent"`
	Harness     string  `json:"harness"`
	Kind        string  `json:"kind"` // checkin | permission
	Question    string  `json:"question"`
	Floor       bool    `json:"floor"`
	FloorHit    string  `json:"floor_hit,omitempty"`
	Predicted   string  `json:"predicted,omitempty"` // proceed | ask_owner | ""
	Confidence  float64 `json:"confidence"`
	Calibrated  bool    `json:"calibrated"`
	Outcome     string  `json:"outcome"` // pending | auto_proceeded | owner | moved_on | expired
	OwnerAnswer string  `json:"owner_answer,omitempty"`
	// OwnerProceed: did the owner let the agent proceed as it proposed; nil when unknown.
	OwnerProceed *bool  `json:"owner_proceed,omitempty"`
	LineUID      string `json:"line_uid,omitempty"`
	// Lines are the Compass lines that guided a go-ahead; Review is the owner's check of it: spot
	// (asked, unanswered), right or wrong. Local to this machine.
	Lines  []string `json:"lines,omitempty"`
	Review string   `json:"review,omitempty"`
	// Suggestion is the Compass's answer to a design or direction question the agent asked, with the
	// Evidence it rests on, for the owner to confirm; given automatically when it is confident enough.
	Suggestion string `json:"suggestion,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	CreatedAt  string `json:"created_at"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// AddGate stores a gate; a gate stored already resolved gets its resolved time.
func (d *DB) AddGate(g Gate) (int64, error) {
	t := now()
	if g.Outcome == "" {
		g.Outcome = "pending"
	}
	if g.Outcome != "pending" && g.ResolvedAt == "" {
		g.ResolvedAt = t
	}
	lines, _ := json.Marshal(append([]string{}, g.Lines...))
	r, err := d.db.Exec(`INSERT INTO gates(team_id, session_id, agent, harness, kind, question, floor, floor_hit, predicted, confidence, calibrated,
		outcome, owner_answer, owner_proceed, line_uid, lines, review, suggestion, evidence, created_at, resolved_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		g.TeamID, g.SessionID, g.Agent, g.Harness, g.Kind, g.Question, g.Floor, g.FloorHit, g.Predicted, g.Confidence, g.Calibrated,
		g.Outcome, g.OwnerAnswer, g.OwnerProceed, g.LineUID, lines, g.Review, g.Suggestion, g.Evidence, t, g.ResolvedAt)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

const gateCols = `id, team_id, session_id, agent, harness, kind, question, floor, floor_hit, predicted, confidence, calibrated,
	outcome, owner_answer, owner_proceed, line_uid, lines, review, suggestion, evidence, created_at, resolved_at`

func scanGate(row interface{ Scan(...any) error }) (Gate, error) {
	var g Gate
	var proceed sql.NullBool
	var lines string
	err := row.Scan(&g.ID, &g.TeamID, &g.SessionID, &g.Agent, &g.Harness, &g.Kind, &g.Question, &g.Floor, &g.FloorHit, &g.Predicted,
		&g.Confidence, &g.Calibrated, &g.Outcome, &g.OwnerAnswer, &proceed, &g.LineUID, &lines, &g.Review, &g.Suggestion, &g.Evidence, &g.CreatedAt, &g.ResolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if proceed.Valid {
		g.OwnerProceed = &proceed.Bool
	}
	_ = json.Unmarshal([]byte(lines), &g.Lines)
	return g, err
}

// Gate is the gate with this id.
func (d *DB) Gate(id int64) (Gate, error) {
	return scanGate(d.db.QueryRow(`SELECT `+gateCols+` FROM gates WHERE id = ?`, id))
}

// OpenGates are the gates waiting on the owner now, oldest first: pending, so neither answered nor
// expired (ExpireGates ends them when the agent moves on).
func (d *DB) OpenGates() ([]Gate, error) {
	rows, err := d.db.Query(`SELECT ` + gateCols + ` FROM gates WHERE outcome = 'pending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Gate{}
	for rows.Next() {
		g, err := scanGate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SpotChecks are the go-aheads waiting for the owner's verdict, newest first; those made before
// since (RFC3339) have expired and count as unknown.
func (d *DB) SpotChecks(since string) ([]Gate, error) {
	rows, err := d.db.Query(`SELECT `+gateCols+` FROM gates WHERE review = 'spot' AND created_at > ? ORDER BY id DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Gate{}
	for rows.Next() {
		g, err := scanGate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SpotGate answers a spot check that has not expired (made after since), once. A wrong verdict also
// marks the go-ahead overridden. It reports whether the check was open.
func (d *DB) SpotGate(id int64, right bool, since string) (bool, error) {
	r, err := d.db.Exec(`UPDATE gates SET review = CASE WHEN ? THEN 'right' ELSE 'wrong' END, owner_proceed = CASE WHEN ? THEN owner_proceed ELSE 0 END
		WHERE id = ? AND review = 'spot' AND created_at > ?`, right, right, id, since)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// WrongGate marks an auto-proceeded gate as one the owner overrode, once. It reports whether it was
// not marked wrong already.
func (d *DB) WrongGate(id int64) (bool, error) {
	r, err := d.db.Exec(`UPDATE gates SET review = 'wrong', owner_proceed = 0 WHERE id = ? AND outcome = 'auto_proceeded' AND review != 'wrong'`, id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// LastGate is the session's newest gate of a kind ("" for any).
func (d *DB) LastGate(sessionID, kind string) (Gate, error) {
	return scanGate(d.db.QueryRow(`SELECT `+gateCols+` FROM gates WHERE session_id = ? AND (? = '' OR kind = ?) ORDER BY id DESC LIMIT 1`, sessionID, kind, kind))
}

// OpenCheckin is the check-in an owner prompt answers: the session's newest pending one, else its
// newest go-ahead the owner has not spoken after yet.
func (d *DB) OpenCheckin(sessionID string) (Gate, error) {
	return scanGate(d.db.QueryRow(`SELECT `+gateCols+` FROM gates WHERE session_id = ? AND kind = 'checkin'
		AND (outcome = 'pending' OR outcome = 'auto_proceeded' AND owner_answer = '') ORDER BY outcome = 'pending' DESC, id DESC LIMIT 1`, sessionID))
}

// ClaimGate gives a gate the owner's reply: a pending one becomes the owner's, and an auto-proceeded
// one takes the owner's next words once (to see whether the owner overrode triage). It reports
// whether the gate took the reply.
func (d *DB) ClaimGate(id int64, answer string) (bool, error) {
	r, err := d.db.Exec(`UPDATE gates SET owner_answer = ?,
		resolved_at = CASE WHEN outcome = 'pending' THEN ? ELSE resolved_at END,
		outcome = CASE WHEN outcome = 'pending' THEN 'owner' ELSE outcome END
		WHERE id = ? AND (outcome = 'pending' OR outcome = 'auto_proceeded' AND owner_answer = '')`, answer, now(), id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// ResolveGate ends a pending gate with an outcome and, when known, whether the owner let it proceed.
func (d *DB) ResolveGate(id int64, outcome string, proceed *bool) error {
	_, err := d.db.Exec(`UPDATE gates SET outcome = ?, owner_proceed = ?, resolved_at = ? WHERE id = ? AND outcome = 'pending'`, outcome, proceed, now(), id)
	return err
}

// MovedOn marks an owner-claimed gate whose reply turned out not to answer it.
func (d *DB) MovedOn(id int64) error {
	_, err := d.db.Exec(`UPDATE gates SET outcome = 'moved_on', owner_proceed = NULL WHERE id = ? AND outcome = 'owner'`, id)
	return err
}

// SetGateDecision records whether the owner let the agent proceed and the Compass line the answer
// went to.
func (d *DB) SetGateDecision(id int64, proceed *bool, lineUID string) error {
	_, err := d.db.Exec(`UPDATE gates SET owner_proceed = ?, line_uid = ? WHERE id = ?`, proceed, lineUID, id)
	return err
}

// ExpireGates ends the session's pending gates of a kind ("" for any): the agent moved on without
// an answer.
func (d *DB) ExpireGates(sessionID, kind string) error {
	_, err := d.db.Exec(`UPDATE gates SET outcome = 'expired', resolved_at = ? WHERE session_id = ? AND outcome = 'pending' AND (? = '' OR kind = ?)`,
		now(), sessionID, kind, kind)
	return err
}

// GateStats measure triage against the owner for one team. A gate the owner moved on from
// (moved_on) counts in none of the answers.
type GateStats struct {
	Checkins         int `json:"checkins"`
	AutoProceeded    int `json:"auto_proceeded"`
	OwnerAnswered    int `json:"owner_answered"`    // check-ins the owner answered
	OwnerPermissions int `json:"owner_permissions"` // permission prompts the owner answered
	Floor            int `json:"floor"`
	// SureOwnerAnswered are the owner-answered check-ins whose prediction was sure (calibrated, at
	// or above sure) and whose owner_proceed is known; Agreed are those the prediction got right,
	// and ProceedAgreed those of them that predicted proceed.
	SureOwnerAnswered int `json:"sure_owner_answered"`
	Agreed            int `json:"agreed"`
	ProceedAgreed     int `json:"proceed_agreed"`
	Overrides         int `json:"overrides"` // auto-proceeded gates the owner then overrode
}

// GateStats counts a team's gates; sure is the confidence a calibrated prediction needs.
func (d *DB) GateStats(teamID string, sure float64) (GateStats, error) {
	var s GateStats
	const sureOwner = `kind = 'checkin' AND outcome = 'owner' AND predicted != '' AND calibrated = 1 AND confidence >= ? AND owner_proceed IS NOT NULL`
	err := d.db.QueryRow(`SELECT
		count(*) FILTER (WHERE kind = 'checkin'),
		count(*) FILTER (WHERE outcome = 'auto_proceeded'),
		count(*) FILTER (WHERE kind = 'checkin' AND outcome = 'owner'),
		count(*) FILTER (WHERE kind = 'permission' AND outcome = 'owner'),
		count(*) FILTER (WHERE floor = 1),
		count(*) FILTER (WHERE `+sureOwner+`),
		count(*) FILTER (WHERE `+sureOwner+` AND (predicted = 'proceed') = (owner_proceed = 1)),
		count(*) FILTER (WHERE `+sureOwner+` AND predicted = 'proceed' AND owner_proceed = 1),
		count(*) FILTER (WHERE outcome = 'auto_proceeded' AND owner_proceed = 0)
		FROM gates WHERE team_id = ?`, sure, sure, sure, teamID).
		Scan(&s.Checkins, &s.AutoProceeded, &s.OwnerAnswered, &s.OwnerPermissions, &s.Floor, &s.SureOwnerAnswered, &s.Agreed, &s.ProceedAgreed, &s.Overrides)
	return s, err
}

// DeleteTeamRecord removes everything recorded for a team: its gates. The helpers' entries stay.
func (d *DB) DeleteTeamRecord(teamID string) error {
	_, err := d.db.Exec(`DELETE FROM gates WHERE team_id = ?`, teamID)
	return err
}
