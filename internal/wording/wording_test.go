package wording

import (
	"testing"

	"github.com/nduwork/pengupool-ai/internal/model"
)

func stubChat(out string) func() {
	old := Chat
	Chat = func(messages []model.Message, m string) (string, error) { return out, nil }
	return func() { Chat = old }
}

func TestGeneralizeCompass(t *testing.T) {
	defer stubChat(`"Always run the tests before committing."`)()
	got, err := GeneralizeCompass("from now on run tests", "")
	if err != nil || got != "Always run the tests before committing." {
		t.Fatalf("got %q %v", got, err)
	}
	defer stubChat("none")()
	if got, _ := GeneralizeCompass("fix the bug", ""); got != "" {
		t.Fatalf("none must give empty: %q", got)
	}
	defer stubChat("No standing rule.")()
	if got, _ := GeneralizeCompass("fix the bug", ""); got != "" {
		t.Fatalf("no-rule must give empty: %q", got)
	}
}

func TestGeneralizeLessonCaps(t *testing.T) {
	defer stubChat("one\ntwo\nthree\nfour\nfive\nsix")()
	got, err := GeneralizeLesson("summary", "")
	if err != nil || got != "one\ntwo\nthree\nfour\nfive" {
		t.Fatalf("got %q %v", got, err)
	}
}
