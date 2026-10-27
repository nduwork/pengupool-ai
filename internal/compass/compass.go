// Package compass is the Compass: one JSON-lines file of owner-confirmed rules.
//
// A line carries its own evidence; nothing is forced and agents never write
// here — the owner confirms every addition (see ingest/confirm). Mirrors
// pengu/compass.py: same file layout, same confidence math.
package compass

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/pengustore"
)

// Evidence records one supporting or contradicting observation.
type Evidence struct {
	At      float64 `json:"at"`
	Words   string  `json:"words"`
	Session string  `json:"session"`
	Effect  string  `json:"effect"` // supports | contradicts
}

// Line is one confirmed Compass rule.
type Line struct {
	ID         string     `json:"id"`
	Rule       string     `json:"rule"`
	Source     string     `json:"source"`
	Created    string     `json:"created"`
	Evidence   []Evidence `json:"evidence"`
	Confidence float64    `json:"confidence"`
	Updated    any        `json:"updated"` // epoch float64; older files carry a date string
}

const createdLayout = "2006-01-02T15:04:05-0700"

// Path of the Compass file.
func Path() string { return filepath.Join(store.Home(), "compass.jsonl") }

// Normalize repairs old or hand-edited lines in place: missing evidence,
// out-of-range confidence, missing updated stamp.
func Normalize(l *Line) *Line {
	if l.Evidence == nil {
		l.Evidence = []Evidence{}
	}
	if l.Confidence != l.Confidence || l.Confidence < 0 || l.Confidence > 1 {
		l.Confidence = 1.0
	}
	if l.Updated == nil {
		l.Updated = l.Created
	}
	return l
}

// Load reads every line, skipping corrupt rows. Never fails on a missing file.
func Load() ([]*Line, error) {
	p := Path()
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []*Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l Line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		lines = append(lines, Normalize(&l))
	}
	return lines, sc.Err()
}

// FormatLines renders lines the one way every surface uses (MCP, task prefix).
func FormatLines(lines []*Line) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, "- "+l.Rule)
	}
	return strings.Join(out, "\n")
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Add appends one owner-confirmed rule. Callers must confirm first.
func Add(rule, source string, confidence float64) (*Line, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	l := &Line{
		ID:         newID(),
		Rule:       rule,
		Source:     source,
		Created:    time.Now().Format(createdLayout),
		Evidence:   []Evidence{},
		Confidence: confidence,
		Updated:    now,
	}
	Normalize(l)
	err := store.Locked(func() error {
		if err := store.Ensure(); err != nil {
			return err
		}
		f, err := os.OpenFile(Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := json.Marshal(l)
		if err != nil {
			return err
		}
		_, err = f.Write(append(data, '\n'))
		return err
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

func saveAll(lines []*Line) error {
	tmp := Path() + ".tmp"
	var sb strings.Builder
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			return err
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	// No silent fallback: a failed save must raise.
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// UpdateRule applies an owner-confirmed rewording. True when the line exists.
func UpdateRule(id, text string) (bool, error) {
	found := false
	err := store.Locked(func() error {
		lines, err := Load()
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l.ID == id {
				l.Rule = text
				found = true
			}
		}
		if found {
			return saveAll(lines)
		}
		return nil
	})
	return found, err
}

// UpdateEvidence folds one observation into confidence: age discounts first
// (half every 90 days), support +0.15 (cap 1), contradiction −0.30 (floor 0).
func UpdateEvidence(id, effect, words, session string, now float64) (*Line, error) {
	if effect != "supports" && effect != "contradicts" {
		return nil, fmt.Errorf("effect is supports or contradicts")
	}
	if now == 0 {
		now = float64(time.Now().UnixNano()) / 1e9
	}
	var updated *Line
	err := store.Locked(func() error {
		lines, err := Load()
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l.ID != id {
				continue
			}
			ageDays := (now - ts(l.Updated, now)) / 86400
			if ageDays < 0 {
				ageDays = 0
			}
			conf := l.Confidence * math.Pow(0.5, ageDays/90.0)
			if effect == "supports" {
				conf = min(1.0, conf+0.15)
			} else {
				conf = max(0.0, conf-0.30)
			}
			l.Confidence = conf
			l.Updated = now
			l.Evidence = append(l.Evidence, Evidence{At: now, Words: words, Session: session, Effect: effect})
			updated = l
		}
		return saveAll(lines)
	})
	return updated, err
}

// Forget drops one line. True when a line existed.
func Forget(id string) (bool, error) {
	found := false
	err := store.Locked(func() error {
		lines, err := Load()
		if err != nil {
			return err
		}
		keep := lines[:0]
		for _, l := range lines {
			if l.ID == id {
				found = true
				continue
			}
			keep = append(keep, l)
		}
		if !found {
			return nil
		}
		return saveAll(keep)
	})
	return found, err
}

func ts(updated any, now float64) float64 {
	switch v := updated.(type) {
	case float64:
		return v
	case string:
		if t, err := time.Parse(createdLayout, v); err == nil {
			return float64(t.UnixNano()) / 1e9
		}
	}
	return now
}

// UnmarshalJSON gives missing keys Python's setdefault semantics: a line
// without "confidence" loads at 1.0, while an explicit 0.0 stays 0.0.
func (l *Line) UnmarshalJSON(data []byte) error {
	type shadow Line
	var s struct {
		shadow
		Confidence *float64 `json:"confidence"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*l = Line(s.shadow)
	// The outer Confidence shadows the inner one during unmarshal, so the
	// inner value never fills: copy the present value back explicitly.
	if s.Confidence == nil {
		l.Confidence = 1.0
	} else {
		l.Confidence = *s.Confidence
	}
	return nil
}
