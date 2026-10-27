package pool

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	pengustore "github.com/nduwork/pengupool-ai/internal/pengustore"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// One-time import of the separate Compass layer into agentdb, for the
// app's real store only (same gate as EnsureMusePlugin). The old JSONL
// files become agentdb rows, then are renamed to *.imported: the rename
// is the backup and the marker, so a rerun imports nothing. A partial
// failure renames nothing and logs, so the next start retries. The
// reader is deliberately tiny and private: the old compass/experience
// packages are going away, and nothing may depend on them.

// penguCompassLine is one row of the old compass.jsonl.
type penguCompassLine struct {
	Rule    string `json:"rule"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Created string `json:"created"`
}

// penguLesson is one row of an old experience-*.jsonl.
type penguLesson struct {
	Helper   string `json:"helper"`
	Title    string `json:"title"`
	Task     string `json:"task"`
	Lesson   string `json:"lesson"`
	Evidence []struct {
		At float64 `json:"at"`
	} `json:"evidence"`
}

func readPenguLines(path string) ([]penguCompassLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []penguCompassLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		if b := strings.TrimSpace(sc.Text()); b != "" {
			var l penguCompassLine
			if err := json.Unmarshal([]byte(b), &l); err != nil {
				return nil, err
			}
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

func readPenguLessons(path string) ([]penguLesson, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []penguLesson
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		if b := strings.TrimSpace(sc.Text()); b != "" {
			var e penguLesson
			if err := json.Unmarshal([]byte(b), &e); err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// checkPenguLine is the Compass bar for an imported line: text, a known
// kind, no secrets.
func checkPenguLine(body, kind string) (string, string, error) {
	body = strings.TrimSpace(body)
	if kind == "" {
		kind = "preference"
	}
	switch {
	case body == "":
		return "", "", invalid("a line needs text")
	case kind != "preference" && kind != "restriction":
		return "", "", invalid("kind is preference or restriction")
	case detectSecret(body):
		return "", "", invalid("the text looks like it contains a secret")
	}
	return body, kind, nil
}

// importPengu runs the one-time import from the pengu home. It reports
// lines and entries imported.
func (p *Pool) importPengu(home string) {
	if p.db == nil {
		return
	}
	files, err := filepath.Glob(filepath.Join(home, "experience-*.jsonl"))
	if err != nil {
		p.logf("pool: pengu import: %v", err)
		return
	}
	compassPath := filepath.Join(home, "compass.jsonl")
	if _, err := os.Stat(compassPath); err == nil {
		files = append([]string{compassPath}, files...)
	}
	if len(files) == 0 {
		return // nothing to import, not even a log line
	}
	type pending struct {
		apply func() error
	}
	var queue []pending
	var done []string
	linesIn, linesSkipped := 0, 0
	entriesAdded, entriesSkipped := 0, 0

	own := p.owner()
	existing, _ := p.db.Lines(own, "", "", 0)
	seen := map[string]bool{}
	for _, v := range existing {
		seen[v.Body] = true
	}
	d := p.st.State().Domain
	keyOf := func(h string) (string, bool) {
		h = strings.TrimSpace(h)
		for _, sk := range d.Skills {
			if strings.EqualFold(sk.Name, h) || agentOf(sk) == h {
				return agentOf(sk), true
			}
		}
		return "", false
	}

	for _, f := range files {
		if strings.HasSuffix(f, "compass.jsonl") {
			lines, err := readPenguLines(f)
			if err != nil {
				p.logf("pool: pengu import: %s: %v", f, err)
				return
			}
			for _, l := range lines {
				body, kind, err := checkPenguLine(l.Rule, l.Kind)
				if err != nil {
					p.logf("pool: pengu import: skipping a compass line: %v", err)
					linesSkipped++
					continue
				}
				if seen[body] {
					linesSkipped++
					continue
				}
				seen[body] = true
				body, kind, created := body, kind, strings.TrimSpace(l.Created)
				queue = append(queue, pending{apply: func() error {
					_, err := p.db.PutLine(agentdb.Line{Owner: own, Body: body, Kind: kind, Source: "owner", CreatedAt: created})
					if err == nil {
						linesIn++
					}
					return err
				}})
			}
			done = append(done, f)
			continue
		}
		lessons, err := readPenguLessons(f)
		if err != nil {
			p.logf("pool: pengu import: %s: %v", f, err)
			return
		}
		for _, e := range lessons {
			title := clipRunes(oneLine(redactSecrets(e.Title)), journalTitle)
			task := strings.TrimSpace(redactSecrets(e.Task))
			body := strings.TrimSpace(redactSecrets(e.Lesson))
			if task != "" {
				body = strings.TrimSpace(body + "\n\nTask: " + task)
			}
			if title == "" || body == "" {
				p.logf("pool: pengu import: skipping an entry without title or lesson")
				entriesSkipped++
				continue
			}
			helper, ok := keyOf(e.Helper)
			if !ok {
				p.logf("pool: pengu import: unknown helper %q, keeping the raw name", e.Helper)
				helper = strings.TrimSpace(e.Helper)
			}
			at := time.Now().UTC()
			for _, ev := range e.Evidence {
				if t := time.Unix(int64(ev.At), 0).UTC(); ev.At > 0 && t.Before(at) {
					at = t
				}
			}
			entry := agentdb.Entry{Helper: helper, At: at.Format(time.RFC3339), Title: title, Body: clipBytes(body, journalBodyMax)}
			queue = append(queue, pending{apply: func() error {
				added, err := p.db.AddExperience(entry)
				if err == nil {
					if added {
						entriesAdded++
					} else {
						entriesSkipped++
					}
				}
				return err
			}})
		}
		done = append(done, f)
	}
	for _, q := range queue {
		if err := q.apply(); err != nil {
			p.logf("pool: pengu import: %v; nothing renamed, will retry at the next start", err)
			return
		}
	}
	for _, f := range done {
		if err := os.Rename(f, f+".imported"); err != nil {
			p.logf("pool: pengu import: rename %s: %v", f, err)
			return
		}
	}
	p.logf("pool: pengu import: %d compass lines (%d skipped), %d journal entries (%d skipped)", linesIn, linesSkipped, entriesAdded, entriesSkipped)
}

// maybeImportPengu runs the one-time import for the app's real store only.
func (p *Pool) maybeImportPengu(storeDir string) {
	if storeDir != store.DefaultDir() {
		return
	}
	p.importPengu(pengustore.Home())
}
