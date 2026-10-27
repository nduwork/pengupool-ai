package voice

import (
	"errors"
	"testing"
	"time"
)

func TestTranscribeRejects(t *testing.T) {
	if _, err := Transcribe(""); err == nil {
		t.Fatal("empty clip must fail")
	}
	old := LookPath
	LookPath = func(string) (string, error) { return "", errors.New("missing") }
	defer func() { LookPath = old }()
	if _, err := Transcribe("a.m4a"); err == nil {
		t.Fatal("missing muse must fail")
	}
}

func TestTranscribeRuns(t *testing.T) {
	oldL, oldR := LookPath, Run
	defer func() { LookPath, Run = oldL, oldR }()
	LookPath = func(string) (string, error) { return "/bin/muse", nil }
	Run = func(argv []string, timeout time.Duration) (string, string, error) {
		return "  prefer small commits  ", "", nil
	}
	got, err := Transcribe("a.m4a")
	if err != nil || got != "prefer small commits" {
		t.Fatalf("got %q %v", got, err)
	}
	Run = func([]string, time.Duration) (string, string, error) { return "", "boom", errors.New("exit 1") }
	if _, err := Transcribe("a.m4a"); err == nil {
		t.Fatal("failure must fail")
	}
	Run = func([]string, time.Duration) (string, string, error) { return "  ", "", nil }
	if _, err := Transcribe("a.m4a"); err == nil {
		t.Fatal("empty transcript must fail")
	}
}
