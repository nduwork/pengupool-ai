package chat

import (
	"testing"
)

func testEnv(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
	old := StdinIsTerminal
	StdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { StdinIsTerminal = old })
}

func TestSendInboxOwner(t *testing.T) {
	testEnv(t)
	e, err := Send("owner", "owner", "hello")
	if err != nil || len(e.ID) != 12 || e.Text != "hello" {
		t.Fatalf("send: %+v %v", e, err)
	}
	got, err := Inbox("owner")
	if err != nil || len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("inbox: %+v %v", got, err)
	}
}

func TestSendRejects(t *testing.T) {
	testEnv(t)
	if _, err := Send("owner", "owner", "   "); err == nil {
		t.Fatal("empty must fail")
	}
	if _, err := Send("ghost", "owner", "hi"); err == nil {
		t.Fatal("off-tree from must fail")
	}
	if _, err := Send("owner", "ghost", "hi"); err == nil {
		t.Fatal("off-tree to must fail")
	}
	if _, err := Inbox("ghost"); err == nil {
		t.Fatal("off-tree inbox must fail")
	}
}

func TestOwnerNeedsTerminal(t *testing.T) {
	h := t.TempDir()
	t.Setenv("PENGUPOOL_AI_HOME", h)
	t.Setenv("PENGUPOOL_MUSE_HOME", h+"/no-legacy")
	old := StdinIsTerminal
	StdinIsTerminal = func() bool { return false }
	defer func() { StdinIsTerminal = old }()
	if _, err := Send("owner", "owner", "hi"); err == nil {
		t.Fatal("owner without terminal must fail")
	}
}
