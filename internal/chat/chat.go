// Package chat carries messages along the team tree, recorded.
//
// `send` appends one line to chat.jsonl and nudges a live TUI pane;
// `inbox` lists entries addressed to a session. Either endpoint may be
// `owner`: owner-bound entries wait in the owner's inbox; routing to a
// question or proposal happens in the judge, never on send.
//
// Delivery is harness-side (inbox file plus pane nudge): agent ingress
// from outside a session is closed on this machine. Mirrors
// pengu/chat.py.
package chat

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/pengusession"
	"github.com/nduwork/pengupool-ai/internal/pengustore"
)

// Keep is the chat retention window: 30 days.
const Keep = 30 * 86400.0

// Entry is one recorded turn.
type Entry struct {
	ID   string  `json:"id"`
	At   string  `json:"at"`
	Ts   float64 `json:"ts"`
	From string  `json:"from"`
	To   string  `json:"to"`
	Text string  `json:"text"`
}

// StdinIsTerminal reports a real terminal. A pty can fake this, so agents
// must never send as owner — the owner types, speaks, or taps.
var StdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func chatPath() string { return filepath.Join(store.Home(), "chat.jsonl") }

func valid(m map[string]any) bool {
	for _, k := range []string{"id", "from", "to", "text"} {
		if _, ok := m[k].(string); !ok {
			return false
		}
	}
	return true
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Send records a turn and nudges a live pane. Returns the entry.
// Owner sends need a real terminal (see SendOwner for the GUI path).
func Send(fromSID, toSID, text string) (*Entry, error) {
	return send(fromSID, toSID, text, false)
}

// SendOwner is Send for the authenticated owner API: the bearer token
// is the owner's credential there, so `from == "owner"` is allowed.
// The CLI never uses this path.
func SendOwner(fromSID, toSID, text string) (*Entry, error) {
	return send(fromSID, toSID, text, true)
}

func send(fromSID, toSID, text string, ownerAuthed bool) (*Entry, error) {
	if fromSID == "owner" && !ownerAuthed && !StdinIsTerminal() {
		return nil, errors.New("only the owner, at a real terminal, sends as owner")
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	rows, err := session.Load()
	if err != nil {
		return nil, err
	}
	byID := map[string]*session.Row{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, party := range []string{fromSID, toSID} {
		if party != "owner" {
			if _, ok := byID[party]; !ok {
				return nil, fmt.Errorf("off the tree: no session %s", party)
			}
		}
	}
	now := float64(time.Now().UnixNano()) / 1e9
	entry := &Entry{
		ID: newID(), At: time.Now().Format("2006-01-02T15:04:05-0700"),
		Ts: now, From: fromSID, To: toSID, Text: text,
	}
	err = store.Locked(func() error {
		if err := store.Ensure(); err != nil {
			return err
		}
		p := chatPath()
		var kept []string
		if f, err := os.Open(p); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			for sc.Scan() {
				raw := strings.TrimSpace(sc.Text())
				if raw == "" {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err != nil || !valid(m) {
					continue
				}
				ts, _ := m["ts"].(float64)
				if ts >= now-Keep {
					kept = append(kept, raw)
				}
			}
			f.Close()
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		kept = append(kept, string(data))
		return os.WriteFile(p, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
	})
	if err != nil {
		return nil, err
	}
	if toSID != "owner" && session.Alive(byID[toSID].Tmux) {
		// Best-effort: the line is recorded either way.
		_ = session.SendKeys(byID[toSID].Tmux,
			fmt.Sprintf("[pengu] message from %s; run: pengu inbox %s", fromSID, toSID))
	}
	return entry, nil
}

// Inbox lists entries addressed to a session (or owner-bound), oldest first.
func Inbox(sid string) ([]*Entry, error) {
	if sid != "owner" {
		rows, err := session.Load()
		if err != nil {
			return nil, err
		}
		found := false
		for _, r := range rows {
			if r.ID == sid {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("off the tree: no session %s", sid)
		}
	}
	f, err := os.Open(chatPath())
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
		if err := json.Unmarshal([]byte(raw), &m); err != nil || !valid(m) || m["to"] != sid {
			continue
		}
		data, _ := json.Marshal(m)
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		out = append(out, &e)
	}
	return out, sc.Err()
}
