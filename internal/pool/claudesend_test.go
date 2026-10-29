package pool

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// inbox stands in for a Claude session's cross-session socket and collects what is written to it.
func inbox(t *testing.T, p *Pool, id string) <-chan string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ib") // a unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got := make(chan string, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				got <- sc.Text()
			}
			_ = c.Close()
		}
	}()
	p.setInbox(id, claudeInbox{Socket: sock})
	return got
}

// Two Claude Code sessions talk through `pengupool send`: a working recipient gets the message in its
// inbox at once (Claude reads it at its next tool round) and it counts as delivered once it is in the
// recipient's turn; an ask is answered with --reply-to, which settles it.
func TestClaudeToClaudeSend(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions,
			store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: harness.Claude, State: "running", Status: "working"},
			store.Session{ID: "webaaaaaaaaa", Name: "web", Harness: harness.Claude, State: "running", Status: "idle"})
		s.Domain.Teams = append(s.Domain.Teams, protocol.Team{ID: "team1", Name: "app", State: "active", Members: []protocol.Member{
			{SessionID: "leadaaaaaaaa", Name: "lead", Role: "main", State: "active"},
			{SessionID: "webaaaaaaaaa", Name: "web", Role: "helper", State: "active", Parent: "leadaaaaaaaa"},
		}})
		return nil
	})
	leadIn := inbox(t, p, "leadaaaaaaaa")
	webIn := inbox(t, p, "webaaaaaaaaa")
	ctx := context.Background()
	send := func(in sendIn) string {
		t.Helper()
		out, err := p.messageSend(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]string)["id"]
	}
	frame := func(ch <-chan string) string {
		t.Helper()
		select {
		case s := <-ch:
			return s
		case <-time.After(2 * time.Second):
			t.Fatal("nothing reached the inbox")
			return ""
		}
	}

	// To the working LEAD: in its inbox now, queued until it is in the LEAD's turn.
	ask := send(sendIn{From: "webaaaaaaaaa", To: "lead", Text: "Ask: may I touch the loader?"})
	if f := frame(leadIn); !strings.Contains(f, "an ask waiting for your answer") || !strings.Contains(f, "--reply-to "+ask) {
		t.Fatalf("the LEAD's inbox got %s", f)
	}
	if m := message(p, ask); m.State != "queued" || m.Type != "ask" || m.Via != viaNative {
		t.Fatalf("after the inbox write: %+v", m)
	}
	p.flushQueue("leadaaaaaaaa") // the LEAD's Stop: not sent a second time
	select {
	case f := <-leadIn:
		t.Fatalf("sent twice: %s", f)
	case <-time.After(200 * time.Millisecond):
	}
	time.Sleep(1100 * time.Millisecond)
	b, _ := json.Marshal(map[string]any{"tool_name": "Bash"})
	if _, err := p.hookEvent(ctx, hookIn{SessionID: "leadaaaaaaaa", Harness: harness.Claude, Event: "PostToolUse", Payload: b}); err != nil {
		t.Fatal(err)
	}
	if m := message(p, ask); m.State != "delivered" {
		t.Fatalf("not delivered at the LEAD's tool round: %s", m.State)
	}

	// The LEAD answers with --reply-to: the idle web gets it at once, and the ask is settled.
	reply := send(sendIn{From: "leadaaaaaaaa", To: "web", Text: "yes", ReplyTo: ask})
	if f := frame(webIn); !strings.Contains(f, "yes") {
		t.Fatalf("web's inbox got %s", f)
	}
	if m := message(p, reply); m.State != "delivered" || m.ReplyTo != ask {
		t.Fatalf("the reply: %+v", m)
	}
	ms := p.st.State().Domain.Messages
	for i, m := range ms {
		if m.ID == ask && askOpen(ms, i) {
			t.Fatal("the ask is still open after its reply")
		}
	}
}

// A Stop's flush and the inbox write race for the same message: whichever comes first sends it, the
// other leaves it, so the recipient reads it once.
func TestClaudeInboxAndFlushSendOnce(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Store: st, Exe: "pengupool"})
	_ = st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions,
			store.Session{ID: "leadaaaaaaaa", Name: "lead", Harness: harness.Claude, State: "running", Status: "idle"},
			store.Session{ID: "webaaaaaaaaa", Name: "web", Harness: harness.Claude, State: "running", Status: "idle"})
		return nil
	})
	in := inbox(t, p, "leadaaaaaaaa")
	web, _ := p.session("webaaaaaaaaa")
	count := func() int {
		n := 0
		for {
			select {
			case <-in:
				n++
			case <-time.After(300 * time.Millisecond):
				return n
			}
		}
	}
	// The flush first (the recipient's Stop ran while the sender was still deciding), then the write.
	putMessage(t, p, protocol.TeamMessage{ID: "m1", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "one", State: "queued", At: now(), Via: viaHost, Type: "tell"})
	p.flushQueue("leadaaaaaaaa")
	p.setStatus("leadaaaaaaaa", "working")
	if !p.intoInbox(message(p, "m1"), web) {
		t.Fatal("a message the flush sent was queued again")
	}
	if n := count(); n != 1 {
		t.Fatalf("flush then write: %d frames", n)
	}
	// The write first: the flush leaves it.
	putMessage(t, p, protocol.TeamMessage{ID: "m2", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "two", State: "queued", At: now(), Via: viaHost, Type: "tell"})
	if !p.intoInbox(message(p, "m2"), web) {
		t.Fatal("not written to a working session's inbox")
	}
	p.flushQueue("leadaaaaaaaa")
	if n := count(); n != 1 {
		t.Fatalf("write then flush: %d frames", n)
	}
	// A recipient no longer working is left to its Stop.
	putMessage(t, p, protocol.TeamMessage{ID: "m3", TeamID: "team1", From: "webaaaaaaaaa", To: "leadaaaaaaaa", Text: "three", State: "queued", At: now(), Via: viaHost, Type: "tell"})
	p.setStatus("leadaaaaaaaa", "idle")
	if p.intoInbox(message(p, "m3"), web) || count() != 0 || message(p, "m3").Via != viaHost {
		t.Fatalf("written to an idle session's inbox: %+v", message(p, "m3"))
	}
}

// A delivered message keeps when it was delivered; a second mark leaves the first time.
func TestDeliveredAt(t *testing.T) {
	p := testPool(t)
	putMessage(t, p, protocol.TeamMessage{ID: "m1", TeamID: "team1", From: "a", To: "b", Text: "x", State: "queued", At: "2026-01-01T00:00:00Z"})
	if m := message(p, "m1"); m.DeliveredAt != "" {
		t.Fatalf("queued with a delivery time: %+v", m)
	}
	p.markDelivered([]string{"m1"})
	first := message(p, "m1").DeliveredAt
	if first == "" || first == "2026-01-01T00:00:00Z" {
		t.Fatalf("delivered at %q", first)
	}
	p.update(func(st *store.State) error { st.Domain.Messages[0].DeliveredAt = "2026-01-02T00:00:00Z"; return nil })
	p.markDelivered([]string{"m1"})
	if got := message(p, "m1").DeliveredAt; got != "2026-01-02T00:00:00Z" {
		t.Fatalf("a second mark moved the delivery time to %q", got)
	}
	// A record delivered before 0.9.23 has no time, and a late mark gives it none: its route stays unlit.
	putMessage(t, p, protocol.TeamMessage{ID: "m2", TeamID: "team1", From: "a", To: "b", Text: "y", State: "delivered", At: "2026-01-01T00:00:00Z"})
	p.markDelivered([]string{"m2"})
	if got := message(p, "m2").DeliveredAt; got != "" {
		t.Fatalf("an old delivered record was stamped %q", got)
	}
}
