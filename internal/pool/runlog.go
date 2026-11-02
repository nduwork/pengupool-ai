package pool

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// Agent run logs: one plain-text file per session under logs/, written by the daemon only. Each
// line is opened, appended and closed, so an editor that saves by rename loses nothing. They are
// disposable: the owner can edit or delete them at any time.

const maxRunLogRead = 512 << 10

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "session"
	}
	return s
}

// slugCopy is slug(s) for copy n of a skill: "-n" appended for n >= 2, the base cut short so the
// suffix survives slug's 40-character limit.
func slugCopy(s string, n int) string {
	b := slug(s)
	if n < 2 {
		return b
	}
	suf := "-" + itoa(n)
	if len(b)+len(suf) > 40 {
		b = strings.Trim(b[:40-len(suf)], "-")
	}
	return b + suf
}

func (p *Pool) runLogPath(id, name string) string {
	return filepath.Join(p.st.Dir(), store.LogsDir,
		time.Now().Format("2006-01-02")+"-"+slug(name)+"-"+idTail(id)+".log")
}

// logLine appends one "HH:MM:SS text" line to a session's run log. Newlines in text are folded so
// one item stays one line.
func (p *Pool) logLine(sessionID, text string) {
	s, ok := p.session(sessionID)
	if !ok || s.RunLog == "" {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ⏎ ")
	f, err := os.OpenFile(s.RunLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		p.logf("pool: run log %s: %v", s.RunLog, err)
		return
	}
	_, _ = f.WriteString(time.Now().Format("15:04:05") + " " + text + "\n")
	_ = f.Close()
}

func (p *Pool) runLogFile(id string) (string, error) {
	s, ok := p.session(id)
	if !ok {
		return "", notFound("no session %s", id)
	}
	if s.RunLog == "" {
		return "", notFound("session %s has no run log", s.Name)
	}
	return s.RunLog, nil
}

func (p *Pool) runlogRead(_ context.Context, in sessionIn) (any, error) {
	path, err := p.runLogFile(in.SessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{"path": path, "content": "", "exists": false}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	truncated := st.Size() > maxRunLogRead
	if truncated {
		_, _ = f.Seek(-maxRunLogRead, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	return map[string]any{"path": path, "content": string(b), "exists": true, "truncated": truncated, "size": st.Size()}, nil
}

func (p *Pool) runlogDelete(_ context.Context, in sessionIn) (any, error) {
	path, err := p.runLogFile(in.SessionID)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return nil, nil
}

// idTail is the short form of an id in a file, folder or branch name. Ids are time-ordered (UUIDv7):
// their first 8 characters change only about once a minute, so two of them made close together
// shared a name; the tail is the random part. Names made before keep working: they are stored.
func idTail(id string) string { return id[max(0, len(id)-8):] }
