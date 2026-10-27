// Package experience keeps per-helper lessons in JSON-lines, separate
// from the Compass. Retrieval: FTS5 candidates, judge relevance, code
// ranking (relevance × outcome × recency), capped at 5. Mirrors
// pengu/experience.py.
package experience

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/judge"
	"github.com/nduwork/pengupool-ai/internal/pengustore"
	"github.com/nduwork/pengupool-ai/internal/wording"
	_ "modernc.org/sqlite"
)

// Threshold floors judge confidence; TopK caps retrieved lessons.
const (
	Threshold = 0.6
	TopK      = 5
)

// Seams tests stub.
var (
	GeneralizeLesson = wording.GeneralizeLesson
	Relate           = judge.Relate
	RateRelevance    = judge.RateRelevance
	Now              = time.Now
)

// Evidence on a lesson entry.
type Evidence struct {
	At      float64 `json:"at"`
	Session string  `json:"session"`
	Outcome string  `json:"outcome"`
	Effect  string  `json:"effect,omitempty"`
}

// Entry is one generalized lesson.
type Entry struct {
	ID       string     `json:"id"`
	Helper   string     `json:"helper"`
	Title    string     `json:"title"`
	Task     string     `json:"task"`
	Lesson   string     `json:"lesson"`
	Evidence []Evidence `json:"evidence"`
	Uses     int        `json:"uses"`
	LastUsed float64    `json:"last_used"`
}

var safeRe = regexp.MustCompile(`[^a-z0-9_-]`)

// Path of a helper's store.
func Path(helper string) string {
	safe := safeRe.ReplaceAllString(strings.ToLower(helper), "")
	if safe == "" {
		safe = "helper"
	}
	sum := sha1.Sum([]byte(helper))
	return filepath.Join(store.Home(), fmt.Sprintf("experience-%s-%s.jsonl", safe, hex.EncodeToString(sum[:])[:8]))
}

// Load reads a helper's entries, skipping corrupt rows and rows without
// a string id and lesson.
func Load(helper string) ([]*Entry, error) {
	f, err := os.Open(Path(helper))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			continue
		}
		id, _ := m["id"].(string)
		lesson, _ := m["lesson"].(string)
		if id == "" || lesson == "" {
			continue
		}
		data, _ := json.Marshal(m)
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		if e.Evidence == nil {
			e.Evidence = []Evidence{}
		}
		out = append(out, &e)
	}
	return out, sc.Err()
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func saveAll(helper string, entries []*Entry) error {
	tmp := Path(helper) + ".tmp"
	var sb strings.Builder
	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path(helper))
}

// Save stores one entry verbatim (already generalized or redacted).
func Save(helper, title, task, lesson, outcome, session string) (*Entry, error) {
	if outcome != "worked" && outcome != "failed" {
		return nil, errors.New("outcome is worked or failed")
	}
	e := &Entry{
		ID:       newID(),
		Helper:   helper,
		Title:    judge.Redact(title),
		Task:     judge.Redact(task),
		Lesson:   judge.Redact(lesson),
		Evidence: []Evidence{{At: float64(Now().UnixNano()) / 1e9, Session: session, Outcome: outcome}},
	}
	err := store.Locked(func() error {
		if err := store.Ensure(); err != nil {
			return err
		}
		f, err := os.OpenFile(Path(helper), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = f.Write(append(data, '\n'))
		return err
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

func appendEvidence(e *Entry, outcome, session, effect string) {
	e.Evidence = append(e.Evidence, Evidence{
		At: float64(Now().UnixNano()) / 1e9, Session: session, Outcome: outcome, Effect: effect,
	})
}

// Result of Ingest.
type Result struct {
	Action string
	Entry  *Entry
}

// Ingest generalizes a finished task and merges it with existing lessons:
// merge adds evidence, contradict weakens, a low-confidence supports
// refines the wording, otherwise the entry is new.
func Ingest(helper, title, task, summary, outcome, session, model string) (*Result, error) {
	if outcome != "worked" && outcome != "failed" {
		return nil, errors.New("outcome is worked or failed")
	}
	lesson, err := GeneralizeLesson(summary, model)
	if err != nil {
		return nil, err
	}
	if lesson = judge.Redact(lesson); strings.TrimSpace(lesson) == "" {
		lesson = judge.Redact(summary)
	}
	if strings.TrimSpace(lesson) == "" {
		return nil, errors.New("empty lesson")
	}
	entries, err := Load(helper)
	if err != nil {
		return nil, err
	}
	var best *Entry
	bestRel := ""
	bestConf := -1.0
	for _, e := range candidates(entries, lesson, 5) {
		r, err := Relate(lesson, e.Lesson, model)
		if err != nil {
			continue
		}
		if r.Relation != "unrelated" && r.Confidence > bestConf {
			best, bestRel, bestConf = e, r.Relation, r.Confidence
		}
	}
	if best == nil {
		e, err := Save(helper, title, task, lesson, outcome, session)
		return &Result{Action: "new", Entry: e}, err
	}
	matchedID := best.ID
	var action string
	var matched *Entry
	needNew := false
	if err := store.Locked(func() error {
		fresh, err := Load(helper)
		if err != nil {
			return err
		}
		for _, e := range fresh {
			if e.ID == matchedID {
				matched = e
			}
		}
		if matched == nil {
			needNew = true
			return nil
		}
		switch {
		case bestRel == "contradicts":
			appendEvidence(matched, outcome, session, "contradicts")
			action = "contradict"
		case bestRel == "supports" && bestConf >= 0.85:
			appendEvidence(matched, outcome, session, "supports")
			action = "merge"
		default:
			matched.Lesson = lesson
			appendEvidence(matched, outcome, session, "supports")
			action = "refine"
		}
		return saveAll(helper, fresh)
	}); err != nil {
		return nil, err
	}
	if needNew {
		// Entry vanished between match and write: store fresh instead.
		e, err := Save(helper, title, task, lesson, outcome, session)
		return &Result{Action: "new", Entry: e}, err
	}
	return &Result{Action: action, Entry: matched}, nil
}

var termRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)

// candidates ranks by FTS5 over title+task+lesson on a transient table
// (the jsonl file is the store); without usable terms or FTS, newest.
func candidates(entries []*Entry, task string, limit int) []*Entry {
	if len(entries) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var terms []string
	for _, t := range termRe.FindAllString(strings.ToLower(task), -1) {
		if !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
		if len(terms) == 12 {
			break
		}
	}
	if len(terms) == 0 {
		return tail(entries, limit)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return tail(entries, limit)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE VIRTUAL TABLE e USING fts5(id UNINDEXED, text)"); err != nil {
		return tail(entries, limit)
	}
	for _, x := range entries {
		if _, err := db.Exec("INSERT INTO e VALUES (?, ?)", x.ID,
			x.Title+" "+x.Task+" "+x.Lesson); err != nil {
			return tail(entries, limit)
		}
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	rows, err := db.Query("SELECT id FROM e WHERE e MATCH ? ORDER BY rank LIMIT ?",
		strings.Join(quoted, " OR "), limit)
	if err != nil {
		return tail(entries, limit)
	}
	defer rows.Close()
	byID := map[string]*Entry{}
	for _, x := range entries {
		byID[x.ID] = x
	}
	var out []*Entry
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

func tail(entries []*Entry, limit int) []*Entry {
	if len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

func recency(e *Entry, now float64) float64 {
	last := 0.0
	for _, ev := range e.Evidence {
		if ev.At > last {
			last = ev.At
		}
	}
	days := 365.0
	if last != 0 {
		days = (now - last) / 86400
		if days < 0 {
			days = 0
		}
	}
	return 1.0 / (1.0 + days/30.0)
}

func outcomeFactor(e *Entry) float64 {
	if len(e.Evidence) == 0 {
		return 1.0
	}
	worked, failed, contradicted := 0, 0, 0
	for _, ev := range e.Evidence {
		switch ev.Outcome {
		case "worked":
			worked++
		case "failed":
			failed++
		}
		if ev.Effect == "contradicts" {
			contradicted++
		}
	}
	base := 1.0
	if worked < failed {
		base = 0.5
	}
	f := base
	for range contradicted {
		f *= 0.5
	}
	return max(0.1, f)
}

// ForTask returns up to k entries: FTS5 candidates, judge relevance,
// code ranking. Retrieved entries get their use stamped.
func ForTask(helper, task string, k int, threshold float64, model string) ([]*Entry, error) {
	if k <= 0 {
		return nil, nil
	}
	entries, err := Load(helper)
	if err != nil {
		return nil, err
	}
	cands := candidates(entries, task, 20)
	if len(cands) == 0 {
		return nil, nil
	}
	var jcands []judge.Candidate
	for _, c := range cands {
		jcands = append(jcands, judge.Candidate{ID: c.ID, Title: c.Title, Lesson: c.Lesson})
	}
	rated, err := RateRelevance(judge.Redact(task), jcands, model)
	if err != nil {
		return nil, err
	}
	byID := map[string]judge.Rating{}
	for _, r := range rated {
		byID[r.ID] = r
	}
	now := float64(Now().UnixNano()) / 1e9
	type ranked struct {
		score float64
		e     *Entry
	}
	var rs []ranked
	for _, c := range cands {
		r, ok := byID[c.ID]
		if !ok || !r.Relevant || r.Confidence < threshold {
			continue
		}
		rs = append(rs, ranked{r.Confidence * outcomeFactor(c) * recency(c, now), c})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].score > rs[j].score })
	if len(rs) > k {
		rs = rs[:k]
	}
	if len(rs) == 0 {
		return nil, nil
	}
	ids := map[string]bool{}
	for _, r := range rs {
		ids[r.e.ID] = true
	}
	if err := store.Locked(func() error {
		fresh, err := Load(helper)
		if err != nil {
			return err
		}
		for _, e := range fresh {
			if ids[e.ID] {
				e.Uses++
				e.LastUsed = now
			}
		}
		return saveAll(helper, fresh)
	}); err != nil {
		return nil, err
	}
	// Callers see the stamped rows.
	stamped := map[string]*Entry{}
	fresh, err := Load(helper)
	if err != nil {
		return nil, err
	}
	for _, e := range fresh {
		if ids[e.ID] {
			stamped[e.ID] = e
		}
	}
	out := make([]*Entry, len(rs))
	for i, r := range rs {
		out[i] = stamped[r.e.ID]
	}
	return out, nil
}
