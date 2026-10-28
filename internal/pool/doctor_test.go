package pool

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
)

// doctor.record keeps only v1 hooks and a status line, from Claude's settings files; twice is once.
func TestDoctorRecord(t *testing.T) {
	p, _ := twoTeams(t)
	rec := func(file, entries string) (any, error) {
		return p.doctorRecord(nil, doctorRecordIn{File: file, Entries: json.RawMessage(entries)})
	}
	for name, c := range map[string][2]string{
		"another file":                 {"../x.json", `{"statusLine":{}}`},
		"an env":                       {"settings.json", `{"env":{"TOKEN":"x"}}`},
		"a hook and env":               {"settings.json", `{"hooks":{},"env":{}}`},
		"nothing":                      {"settings.json", `{}`},
		"not an object":                {"settings.json", `[1]`},
		"too large":                    {"settings.json", `{"hooks":"` + strings.Repeat("a", 65<<10) + `"}`},
		"a hook of the owner's":        {"settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`},
		"a v1 hook beside the owner's": {"settings.json", `{"hooks":{"Stop":[{"hooks":[{"command":"python -m pengupool.context"},{"command":"echo mine"}]}]}}`},
		"the owner's status line":      {"settings.json", `{"statusLine":{"command":"npx ccstatusline"}}`},
		"a secret":                     {"settings.json", `{"hooks":{"Stop":[{"hooks":[{"command":"python -m pengupool.context --token sk-abcdefghijklmnopqrstuvwxyz123456"}]}]}}`},
	} {
		if _, err := rec(c[0], c[1]); err == nil {
			t.Errorf("%s was recorded", name)
		}
	}
	for i, want := range []bool{true, false} {
		out, err := rec("settings.json", `{"statusLine":{"command":"python -m pengupool.statusline","wraps":true},"hooks":{"Stop":[{"hooks":[{"command":"~/bin/python -m pengupool.context"}]}]}}`)
		if err != nil || out.(map[string]any)["added"] != want {
			t.Fatalf("record %d: %v %v", i, out, err)
		}
	}
	got, err := p.doctorRemoved(nil, struct{}{})
	if err != nil || len(got.(map[string]any)["removed"].([]agentdb.Removed)) != 1 {
		t.Fatalf("list: %v %v", got, err)
	}
}
