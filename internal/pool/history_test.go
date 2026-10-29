package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// The history a phone may read never carries a credential the agent saw or typed.
func TestHistoryLeavesOutSecrets(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"use key sk-ant-abcdefghijklmnopqrstuvwxyz0123"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Set sk-ant-abcdefghijklmnopqrstuvwxyz0123 in the env."}]}}
`), 0o644)
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{ID: "histaaaaaaaa", Name: "h", State: "running", Transcript: transcript})
		return nil
	})
	out, err := call(t, p, true, "history.read", map[string]any{"session_id": "histaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	got := string(raw)
	if strings.Contains(got, "sk-ant-") || strings.Count(got, leftOut) != 2 {
		t.Fatalf("history: %s", got)
	}
}
